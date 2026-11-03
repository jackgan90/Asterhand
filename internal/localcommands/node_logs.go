package localcommands

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"time"
)

// A sparse node can be far from the cursor in a busy merged log. Bound the
// work performed while holding the run lock; empty pages still advance it.
const nodeLogScanBytes = 1024 * 1024
const nodeLogScanFrames = 4096

type nodeLogFrame struct {
	start, end int64
	header     [logHeaderSize]byte
	chunk      LogChunk
}

func (s runStore) readNode(record RunRecord, nodeID string, cursor int64, before bool) (LogPage, error) {
	index := slices.IndexFunc(record.Nodes, func(n NodeRecord) bool { return n.NodeID == nodeID && n.Kind == "script" })
	if record.Command.Kind != "graph" || index < 0 {
		return LogPage{}, problem("invalid", "err.localcmd.invalid_log_node")
	}
	record = logRecord(record)
	f, err := os.Open(s.logPath(record.Command.ID, record.ID))
	if err != nil {
		return LogPage{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return LogPage{}, err
	}
	tail := cursor == -1
	if tail {
		cursor, before = info.Size(), true
	}
	if cursor < 0 || cursor > info.Size() {
		return LogPage{}, problem("invalid", "err.localcmd.invalid_log_cursor")
	}
	page, err := readNodeLogPage(f, record, nodeID, cursor, info.Size(), before)
	if err != nil && tail {
		// As for merged tail reads, a partial crash tail falls back to a
		// bounded first page so intact earlier output remains readable.
		return readNodeLogPage(f, record, nodeID, 0, info.Size(), false)
	}
	return page, err
}

func readNodeLogPage(f *os.File, record RunRecord, nodeID string, cursor, end int64, before bool) (LogPage, error) {
	page := LogPage{Run: &record, Chunks: []LogChunk{}, Start: cursor, Next: cursor}
	metadata, err := json.Marshal(record)
	if err != nil {
		return page, err
	}
	// One node's four encoding contexts plus the response envelope fit in
	// 2 KiB, even with a maximum-length node ID and both pending streams.
	wireBytes, dataBytes, scanned := len(metadata)+2048, 0, int64(0)
	position := cursor
	for frames := 0; frames < nodeLogScanFrames && scanned < nodeLogScanBytes; frames++ {
		if before && position == 0 || !before && position == end {
			break
		}
		frame, err := readNodeLogFrame(f, record, nodeID, position, end, before)
		if !before && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
			page.End = true
			break
		}
		if err != nil {
			return page, err
		}
		if frames > 0 && scanned+frame.end-frame.start > nodeLogScanBytes {
			break
		}
		if frame.chunk.NodeID == nodeID {
			encoded, _ := json.Marshal(frame.chunk)
			if len(page.Chunks) > 0 && (dataBytes+len(frame.chunk.Data) > logPageBytes || wireBytes+len(encoded)+1 > 96*1024) {
				break
			}
			if wireBytes+len(encoded)+1 > 96*1024 {
				return page, problem("invalid", "err.localcmd.limit_exceeded")
			}
			if before || len(page.Chunks) == 0 {
				page.Context, err = nodeLogContext(frame.header[13:], nodeID)
				if err != nil {
					return page, err
				}
			}
			page.Chunks = append(page.Chunks, frame.chunk)
			wireBytes += len(encoded) + 1
			dataBytes += len(frame.chunk.Data)
		}
		scanned += frame.end - frame.start
		if before {
			position, page.Start = frame.start, frame.start
		} else {
			position, page.Next = frame.end, frame.end
		}
		if len(page.Chunks) == logPageFrames || dataBytes >= logPageBytes {
			break
		}
	}
	if before {
		slices.Reverse(page.Chunks)
	}
	page.Previous, page.End = page.Start, page.End || page.Next == end
	return page, nil
}

func readNodeLogFrame(f *os.File, record RunRecord, nodeID string, cursor, end int64, before bool) (nodeLogFrame, error) {
	frame := nodeLogFrame{start: cursor}
	var footer [4]byte
	if before {
		if cursor < logFrameOverhead {
			return frame, problem("invalid", "err.localcmd.invalid_log_cursor")
		}
		if _, err := f.ReadAt(footer[:], cursor-4); err != nil {
			return frame, err
		}
		n := int64(binary.LittleEndian.Uint32(footer[:]))
		if n < 1 || n > logBlockSize || cursor < n+logFrameOverhead {
			return frame, problem("invalid", "err.localcmd.invalid_log_cursor")
		}
		frame.start = cursor - n - logFrameOverhead
	}
	if _, err := f.ReadAt(frame.header[:], frame.start); err != nil {
		return frame, err
	}
	n := int64(binary.LittleEndian.Uint32(frame.header[9:13]))
	source, stream, err := logFrameSource(record, frame.header[0])
	if err != nil || n < 1 || n > logBlockSize {
		return frame, problem("invalid", "err.localcmd.invalid_log_cursor")
	}
	frame.end = frame.start + n + logFrameOverhead
	if frame.end > end {
		return frame, io.ErrUnexpectedEOF
	}
	if before && frame.end != cursor {
		return frame, problem("invalid", "err.localcmd.invalid_log_cursor")
	}
	if _, err = f.ReadAt(footer[:], frame.end-4); err != nil {
		return frame, err
	}
	if int64(binary.LittleEndian.Uint32(footer[:])) != n {
		return frame, problem("invalid", "err.localcmd.invalid_log_cursor")
	}
	if source == nodeID {
		data := make([]byte, n)
		if _, err = f.ReadAt(data, frame.start+logHeaderSize); err != nil {
			return frame, err
		}
		streamName := "stdout"
		if stream == 1 {
			streamName = "stderr"
		}
		frame.chunk = LogChunk{NodeID: source, Stream: streamName, Data: data, Time: time.Unix(0, int64(binary.LittleEndian.Uint64(frame.header[1:9]))).UTC()}
	}
	return frame, nil
}

func nodeLogContext(header []byte, nodeID string) (map[string][]LogChunk, error) {
	context, err := decodeLogContext(header)
	for _, chunks := range context {
		for i := range chunks {
			chunks[i].NodeID = nodeID
		}
	}
	return context, err
}
