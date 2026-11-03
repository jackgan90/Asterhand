package localcommands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

func nodeLogFixture(t *testing.T) (runStore, RunRecord, *logFile) {
	t.Helper()
	run := &managedRun{record: RunRecord{ID: model.ID(), Command: graphCommand(t), Phase: "finished", Started: time.Now().UTC(), LogComplete: true}}
	initializeGraph(run)
	for i := range run.record.Nodes {
		finishStructuralNode(&run.record.Nodes[i], "succeeded")
	}
	store := runStore{root: t.TempDir()}
	f, err := store.prepare(run.record)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return store, run.record, f
}

func writeNodeLog(t *testing.T, f *logFile, index int, stream byte, data []byte) {
	t.Helper()
	if err := appendGraphLog(f, index, stream, data); err != nil {
		t.Fatal(err)
	}
}

func nodeLogText(t *testing.T, page LogPage, nodeID string) string {
	t.Helper()
	var result strings.Builder
	for _, chunk := range page.Chunks {
		if chunk.NodeID != nodeID {
			t.Fatalf("another node leaked into page: %s", chunk.NodeID)
		}
		result.Write(chunk.Data)
	}
	return result.String()
}

func TestNodeLogsPageForwardAndBackwardWithoutOtherNodes(t *testing.T) {
	store, record, f := nodeLogFixture(t)
	node := record.Nodes[1].NodeID
	var expected strings.Builder
	for i := range 151 {
		text := fmt.Sprintf("A-%03d\n", i)
		writeNodeLog(t, f, 1, byte(i%2), []byte(text))
		writeNodeLog(t, f, 2, 0, []byte("other node\n"))
		expected.WriteString(text)
	}
	var forward, backward string
	for cursor := int64(0); ; {
		page, err := store.readNode(record, node, cursor, false)
		if err != nil {
			t.Fatal(err)
		}
		forward += nodeLogText(t, page, node)
		if page.End {
			break
		}
		if page.Next <= cursor || len(page.Chunks) > logPageFrames {
			t.Fatal("forward page failed to advance within its limit", page.Next)
		}
		cursor = page.Next
	}
	for cursor := int64(-1); ; {
		page, err := store.readNode(record, node, cursor, true)
		if err != nil {
			t.Fatal(err)
		}
		backward = nodeLogText(t, page, node) + backward
		if page.Start == 0 {
			break
		}
		if cursor != -1 && page.Start >= cursor {
			t.Fatal("backward page failed to advance")
		}
		cursor = page.Start
	}
	if forward != expected.String() || backward != forward {
		t.Fatal("node history lost, duplicated or reordered frames")
	}
	merged, err := store.read(record, 0)
	if err != nil || len(merged.Chunks) < 2 || merged.Chunks[1].NodeID != record.Nodes[2].NodeID {
		t.Fatal("merged reads changed", err)
	}
}

func TestNodeLogsSparseHistoryHasBoundedScanAndProgress(t *testing.T) {
	store, record, f := nodeLogFixture(t)
	node := record.Nodes[1].NodeID
	writeNodeLog(t, f, 1, 0, []byte("early completed node"))
	for range 150 {
		writeNodeLog(t, f, 2, 0, bytes.Repeat([]byte("x"), logBlockSize))
	}
	empty := 0
	var actual string
	for cursor := int64(-1); ; {
		page, err := store.readNode(record, node, cursor, true)
		if err != nil {
			t.Fatal(err)
		}
		if page.Next-page.Start > nodeLogScanBytes {
			t.Fatal("one sparse read exceeded the scan limit")
		}
		if len(page.Chunks) == 0 {
			empty++
		}
		actual = nodeLogText(t, page, node) + actual
		if page.Start == 0 {
			break
		}
		if cursor != -1 && page.Start >= cursor {
			t.Fatal("empty scan did not advance")
		}
		cursor = page.Start
	}
	if empty == 0 || actual != "early completed node" {
		t.Fatalf("sparse history: %d empty pages, %q", empty, actual)
	}
	// Tiny unrelated frames are limited by count as well as file bytes.
	for range nodeLogScanFrames + 10 {
		writeNodeLog(t, f, 2, 0, []byte("x"))
	}
	page, err := store.readNode(record, node, -1, true)
	if err != nil || len(page.Chunks) != 0 || page.Next-page.Start != nodeLogScanFrames*(logFrameOverhead+1) {
		t.Fatal("tiny-frame scan was not bounded", page.Next-page.Start, err)
	}
}

func TestNodeLogsSeedOnlySelectedNodeAndFitMaximumIPC(t *testing.T) {
	store, record, f := nodeLogFixture(t)
	node := record.Nodes[1].NodeID
	writeNodeLog(t, f, 1, 0, []byte{0xe4, 0xb8})
	writeNodeLog(t, f, 2, 0, []byte("unrelated"))
	info, _ := f.Stat()
	writeNodeLog(t, f, 1, 0, []byte{0xad})
	page, err := store.readNode(record, node, info.Size(), false)
	if err != nil || len(page.Chunks) != 1 {
		t.Fatal(page, err)
	}
	seed := page.Context["utf-8"]
	if len(seed) != 1 || seed[0].NodeID != node || !bytes.Equal(seed[0].Data, []byte{0xe4, 0xb8}) {
		t.Fatal("node decoder context lost", seed)
	}
	maximum := maximumGraphRecord(t)
	large := runStore{root: t.TempDir()}
	file, err := large.prepare(maximum)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for stream := byte(0); stream < 2; stream++ {
		writeNodeLog(t, file, 2, stream, []byte{0xe4, 0xb8})
	}
	info, _ = file.Stat()
	writeNodeLog(t, file, 2, 0, bytes.Repeat([]byte("x"), logBlockSize))
	page, err = large.readNode(maximum, maximum.Nodes[2].NodeID, info.Size(), false)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(page)
	if len(encoded) > 96*1024 || len(page.Chunks) != 1 {
		t.Fatal("maximum node page exceeded IPC budget", len(encoded))
	}
	var ipc bytes.Buffer
	message := hostipc.Message{Version: hostipc.Version, Session: model.ID(), Kind: hostipc.CommandResult, CommandID: model.ID(), CommandReply: &wire.Response{Log: encoded}}
	if err = hostipc.Write(&ipc, message, false); err != nil {
		t.Fatal(err)
	}
}

func TestNodeLogsRejectWrongIdentityAndRecoverPartialTail(t *testing.T) {
	store, record, f := nodeLogFixture(t)
	for _, node := range []string{record.Nodes[0].NodeID, model.ID(), ""} {
		if _, err := store.readNode(record, node, 0, false); err == nil {
			t.Fatal("non-script or foreign node accepted")
		}
	}
	manager := &RunManager{store: store, runs: map[string]*managedRun{record.Command.ID: {record: record}}, clock: platformClock{}}
	if _, err := manager.ReadNode(record.Command.ID, model.ID(), record.Nodes[1].NodeID, 0, false); err == nil {
		t.Fatal("old run accepted")
	}
	writeNodeLog(t, f, 1, 0, []byte("intact history"))
	if _, err := f.Write([]byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	page, err := store.readNode(record, record.Nodes[1].NodeID, -1, true)
	if err != nil || nodeLogText(t, page, record.Nodes[1].NodeID) != "intact history" {
		t.Fatal("partial tail hid intact history", err)
	}
}
