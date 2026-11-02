package localcommands

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
)

const logBlockSize = 16 * 1024

// A full 64-node graph can exceed 2 MiB after JSON escapes paths and names.
// Use one bounded limit for both publishing and reopening its private record.
const maxRunRecordBytes = 4 * 1024 * 1024

type RunRecord struct {
	Nodes          []NodeRecord `json:"nodes,omitempty"`
	FailedNodeID   string       `json:"failed_node_id,omitempty"`
	LogFormat      int          `json:"log_format,omitempty"`
	ID             string       `json:"run_id"`
	Command        Command      `json:"command"`
	Phase          string       `json:"phase"`
	Result         string       `json:"result,omitempty"`
	Reason         string       `json:"reason,omitempty"`
	Started        time.Time    `json:"started_at"`
	Ended          *time.Time   `json:"ended_at,omitempty"`
	Elapsed        float64      `json:"elapsed_seconds"`
	Exit           ExitEvidence `json:"exit"`
	LogComplete    bool         `json:"log_complete"`
	StorageError   bool         `json:"storage_error,omitempty"`
	CleanupPending bool         `json:"cleanup_pending,omitempty"`
}

type LogChunk struct {
	NodeID    string    `json:"node_id,omitempty"`
	IgnoreBOM bool      `json:"ignore_bom,omitempty"`
	Stream    string    `json:"stream"`
	Data      []byte    `json:"data"`
	Time      time.Time `json:"time"`
}

type LogPage struct {
	Context  map[string][]LogChunk `json:"context,omitempty"`
	Previous int64                 `json:"previous"`
	Start    int64                 `json:"start"`
	Run      *RunRecord            `json:"run,omitempty"`
	Chunks   []LogChunk            `json:"chunks"`
	Next     int64                 `json:"next"`
	End      bool                  `json:"end"`
}

type runStore struct{ root string }

func (s runStore) directory(id string) string    { return filepath.Join(s.root, id) }
func (s runStore) metadata(id string) string     { return filepath.Join(s.directory(id), "current.json") }
func (s runStore) logPath(id, run string) string { return filepath.Join(s.directory(id), run+".log") }

func (s runStore) save(r RunRecord) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(b) > maxRunRecordBytes {
		return problem("configuration", "err.localcmd.invalid_run")
	}
	return config.AtomicFile(s.metadata(r.Command.ID), b, 0600)
}

// Prepare does not touch the old generation until its replacement is durable.
func (s runStore) prepare(r RunRecord) (*logFile, error) {
	if _, err := os.Stat(filepath.Join(s.directory(r.Command.ID), "deleting")); err == nil {
		if err = s.clear(r.Command.ID); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(s.directory(r.Command.ID), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.logPath(r.Command.ID, r.ID), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Sync(); err == nil {
		err = s.save(r)
	}
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	return &logFile{File: f}, nil
}

// Each binary frame contains stream, UTC nanoseconds, byte length, then raw
// bytes. A partial crash tail is visible as incomplete, never silently skipped.
func appendLog(f *logFile, stream byte, data []byte) error {
	return appendLogWithContext(f, &f.context, stream, data)
}

func appendLogWithContext(f *logFile, context *logContext, stream byte, data []byte) error {
	var header [logHeaderSize]byte
	header[0] = stream
	binary.LittleEndian.PutUint64(header[1:9], uint64(time.Now().UTC().UnixNano()))
	binary.LittleEndian.PutUint32(header[9:13], uint32(len(data)))
	context.marshal(header[13:])
	if _, err := f.Write(header[:]); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	var footer [4]byte
	binary.LittleEndian.PutUint32(footer[:], uint32(len(data)))
	_, err := f.Write(footer[:])
	if err == nil {
		context.advance(stream%2, data)
	}
	return err
}

func (s runStore) read(r RunRecord, cursor int64) (LogPage, error) {
	if r.Command.Kind == "graph" {
		// Log rendering needs graph-node IDs, not platform execution IDs. Keep
		// those in status/durable records and reserve page space for a full frame.
		r.Nodes = cloneNodeRecords(r.Nodes)
		for i := range r.Nodes {
			r.Nodes[i].RunID = ""
		}
	}
	page := LogPage{Run: &r, Chunks: []LogChunk{}, Next: cursor, Start: cursor}
	f, err := os.Open(s.logPath(r.Command.ID, r.ID))
	if err != nil {
		return page, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return page, err
	}
	if cursor > info.Size() {
		return page, problem("invalid", "err.localcmd.invalid_log_cursor")
	}
	page.Previous, err = previousLogPage(f, cursor)
	if err != nil {
		return page, err
	}
	if _, err = f.Seek(cursor, io.SeekStart); err != nil {
		return page, err
	}
	// Include metadata, base64 expansion and per-frame JSON overhead. Reserve
	// space for decoder context, cursors and the outer response envelope.
	metadata, err := json.Marshal(r)
	if err != nil {
		return page, err
	}
	wireBytes := len(metadata) + 2048
	seenNodes := map[string]bool{}
	for size := 0; size < logPageBytes && len(page.Chunks) < logPageFrames; {
		var header [logHeaderSize]byte
		_, err = io.ReadFull(f, header[:])
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			page.End = true
			return page, nil
		}
		if err != nil {
			return page, err
		}
		n := binary.LittleEndian.Uint32(header[9:13])
		nodeID, streamByte, frameErr := logFrameSource(r, header[0])
		if n > logBlockSize || n == 0 || frameErr != nil {
			return page, problem("invalid", "err.localcmd.invalid_log_cursor")
		}
		if len(page.Chunks) > 0 && size+int(n) > logPageBytes {
			break
		}
		var context map[string][]LogChunk
		contextBytes := 0
		if !seenNodes[nodeID] {
			context, err = decodeLogContext(header[13:])
			if err != nil {
				return page, err
			}
			for _, chunks := range context {
				for i := range chunks {
					chunks[i].NodeID = nodeID
					encoded, _ := json.Marshal(chunks[i])
					contextBytes += len(encoded) + 1
				}
			}
		}
		b := make([]byte, n)
		if _, err = io.ReadFull(f, b); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				page.End = true
				return page, nil
			}
			return page, err
		}
		stream := "stdout"
		var footer [4]byte
		if _, err = io.ReadFull(f, footer[:]); err != nil {
			page.End = true
			return page, nil
		}
		if binary.LittleEndian.Uint32(footer[:]) != n {
			return page, problem("invalid", "err.localcmd.invalid_log_cursor")
		}
		if streamByte == 1 {
			stream = "stderr"
		}
		chunk := LogChunk{NodeID: nodeID, Stream: stream, Data: b, Time: time.Unix(0, int64(binary.LittleEndian.Uint64(header[1:9]))).UTC()}
		encoded, err := json.Marshal(chunk)
		if err != nil {
			return page, err
		}
		if wireBytes+contextBytes+len(encoded)+1 > 96*1024 {
			if len(page.Chunks) == 0 {
				return page, problem("invalid", "err.localcmd.limit_exceeded")
			}
			break
		}
		wireBytes += contextBytes + len(encoded) + 1
		if len(context) > 0 {
			if page.Context == nil {
				page.Context = map[string][]LogChunk{}
			}
			for encoding, chunks := range context {
				page.Context[encoding] = append(page.Context[encoding], chunks...)
			}
		}
		seenNodes[nodeID] = true
		page.Chunks = append(page.Chunks, chunk)
		page.Next += int64(logFrameOverhead) + int64(n)
		size += int(n)
	}
	page.End = page.Next == info.Size()
	return page, nil
}

func previousLogPage(f *os.File, end int64) (int64, error) {
	position := end
	for frames, bytes := 0, int64(0); position > 0 && frames < logPageFrames; frames++ {
		if position < logFrameOverhead {
			return 0, problem("invalid", "err.localcmd.invalid_log_cursor")
		}
		var size [4]byte
		if _, err := f.ReadAt(size[:], position-4); err != nil {
			return 0, err
		}
		n := int64(binary.LittleEndian.Uint32(size[:]))
		if n <= 0 || n > logBlockSize || position < n+logFrameOverhead {
			return 0, problem("invalid", "err.localcmd.invalid_log_cursor")
		}
		if bytes+n > logPageBytes {
			break
		}
		position -= n + logFrameOverhead
		bytes += n
	}
	return position, nil
}

func (s runStore) tailCursor(r RunRecord) (int64, error) {
	f, err := os.Open(s.logPath(r.Command.ID, r.ID))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return previousLogPage(f, info.Size())
}

func (s runStore) load(id string) (*RunRecord, error) {
	f, err := os.Open(s.metadata(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxRunRecordBytes+1))
	if err != nil {
		return nil, err
	}
	var r RunRecord
	if len(b) > maxRunRecordBytes || json.Unmarshal(b, &r) != nil || r.Command.ID != id || !wire.ValidID(r.ID) {
		return nil, problem("configuration", "err.localcmd.invalid_run")
	}
	if err := validateGraphRecord(r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s runStore) removeOtherLogs(id, current string) error {
	files, err := os.ReadDir(s.directory(id))
	if err != nil {
		return err
	}
	for _, f := range files {
		if filepath.Ext(f.Name()) == ".log" && f.Name() != current+".log" {
			if err := os.Remove(filepath.Join(s.directory(id), f.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s runStore) clear(id string) error {
	if !wire.ValidID(id) {
		return problem("invalid", "err.localcmd.invalid_wire_req")
	}
	// A durable marker hides the old record even when deletion is interrupted.
	dir := s.directory(id)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	marker := filepath.Join(dir, "deleting")
	if err := config.AtomicFile(marker, []byte("1"), 0600); err != nil {
		return err
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.Name() != "deleting" {
			if err = os.Remove(filepath.Join(dir, f.Name())); err != nil {
				return err
			}
		}
	}
	if err = os.Remove(marker); err != nil {
		return err
	}
	return os.Remove(dir)
}
