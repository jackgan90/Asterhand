package localcommands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

func maximumGraphRecord(t *testing.T) RunRecord {
	t.Helper()
	c := graphCommand(t)
	c.ID, c.Revision, c.Name = strings.Repeat("c", 64), strings.Repeat("r", 64), strings.Repeat("n", 64)
	script := c.Graph.Nodes[1].Script
	c.Graph.Nodes = []GraphNode{{ID: strings.Repeat("s", 64), Kind: "start"}, {ID: strings.Repeat("e", 64), Kind: "end"}}
	c.Graph.Edges = nil
	for i := 2; i < MaxGraphNodes; i++ {
		n := GraphNode{ID: fmt.Sprintf("n%063d", i), Kind: "script", Script: script, FailurePolicy: AbortGraph}
		c.Graph.Nodes = append(c.Graph.Nodes, n)
		c.Graph.Edges = append(c.Graph.Edges, GraphEdge{c.Graph.Nodes[0].ID, n.ID}, GraphEdge{n.ID, c.Graph.Nodes[1].ID})
	}
	for from := 2; from < MaxGraphNodes && len(c.Graph.Edges) < MaxGraphEdges; from++ {
		for to := from + 1; to < MaxGraphNodes && len(c.Graph.Edges) < MaxGraphEdges; to++ {
			c.Graph.Edges = append(c.Graph.Edges, GraphEdge{c.Graph.Nodes[from].ID, c.Graph.Nodes[to].ID})
		}
	}
	for i := range c.Graph.Nodes {
		c.Graph.Nodes[i].Name = strings.Repeat("<", 128)
	}
	if _, err := normalizeGraph(c.Graph, commandPolicy); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run := &managedRun{record: RunRecord{Version: 1, ID: model.ID(), Command: c, Phase: "finished", Result: "interrupted",
		Reason: "unexpected_shutdown", Started: now, Ended: &now, FailedNodeID: c.Graph.Nodes[2].ID}}
	initializeGraph(run)
	for i := range run.record.Nodes {
		n := &run.record.Nodes[i]
		finishStructuralNode(n, "succeeded")
		if n.Kind == "script" {
			code := ^uint32(0)
			n.RunID, n.Result, n.Reason = model.ID(), "interrupted", "unexpected_shutdown"
			n.Started, n.Elapsed, n.Exit = &now, 9223372036.854776, ExitEvidence{Kind: "exited", Code: &code}
			n.LogComplete = false
		}
	}
	return run.record
}

func TestGraphMaximumLogPagesFitIPCWithoutLosingFrames(t *testing.T) {
	r := maximumGraphRecord(t)
	store := runStore{root: t.TempDir()}
	f, err := store.prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	want := 0
	for _, data := range [][]byte{{0xe4, 0xb8}, bytes.Repeat([]byte("x"), logBlockSize)} {
		for i := 2; i < len(r.Nodes); i++ {
			for stream := byte(0); stream < 2; stream++ {
				if err = appendGraphLog(f, i, stream, data); err != nil {
					t.Fatal(err)
				}
				want += len(data)
			}
		}
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	public := publicRun(r)
	got, maximumPage := 0, 0
	for cursor := int64(0); ; {
		page, err := store.read(public, cursor)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(page)
		if err != nil || len(encoded) > 96*1024 {
			t.Fatal("oversized log page", len(encoded), err)
		}
		maximumPage = max(maximumPage, len(encoded))
		message := hostipc.Message{Version: hostipc.Version, Session: strings.Repeat("s", 64), Kind: hostipc.CommandResult,
			CommandID: strings.Repeat("r", 64), CommandReply: &wire.Response{Log: encoded}}
		var frame bytes.Buffer
		if err = hostipc.Write(&frame, message, false); err != nil {
			t.Fatal("log page cannot cross native IPC", err)
		}
		present := map[string]bool{}
		for _, chunk := range page.Chunks {
			got += len(chunk.Data)
			present[chunk.NodeID] = true
		}
		for _, chunks := range page.Context {
			for _, chunk := range chunks {
				if !present[chunk.NodeID] {
					t.Fatal("decoder context from a frame deferred to the next page")
				}
			}
		}
		if page.End {
			break
		}
		if page.Next <= cursor {
			t.Fatal("paging stalled")
		}
		cursor = page.Next
	}
	if got != want || public.Nodes[2].RunID == "" {
		t.Fatal("lost log bytes or modified execution identity", got, want)
	}
	t.Logf("read %d raw bytes; maximum page %d bytes", got, maximumPage)
}

func TestGraphStopBeforePlatformStartSkipsReservedNode(t *testing.T) {
	for _, reason := range []string{"user_stop", "node_failed", "timeout"} {
		t.Run(reason, func(t *testing.T) {
			m, b, c := graphFixture(t)
			run := &managedRun{record: RunRecord{ID: model.ID(), Command: c, Phase: "running", LogComplete: true}, busy: true, done: make(chan struct{})}
			initializeGraph(run)
			finishStructuralNode(&run.record.Nodes[0], "succeeded")
			f, err := m.store.prepare(run.record)
			if err != nil {
				t.Fatal(err)
			}
			run.file, m.runs[run.record.ID] = f, run
			run.group = &runGroup{index: runIndex{Latest: run.record.ID, IDs: []string{run.record.ID}}}
			m.groups[c.ID] = run.group
			run.mu.Lock()
			reserved := m.scheduleGraphLocked(run)
			err = m.stopLocked(run, reason)
			run.mu.Unlock()
			go m.executeGraph(run)
			if !reserved || err != nil {
				t.Fatal("reservation/stop failed", reserved, err)
			}
			result := awaitFinished(t, m, c.ID)
			noGraphStart(t, b)
			n := result.Nodes[1]
			if n.Result != "skipped" || n.Started != nil || n.RunID == "" || len(m.graphSlots) != 0 {
				t.Fatal("unstarted reservation not skipped and released", n)
			}
		})
	}
}

func TestGraphScriptErrorKeepsLocalizableReasonAndNode(t *testing.T) {
	c := graphCommand(t)
	c.Graph.Nodes[1].Script += ".txt"
	_, err := normalizeGraph(c.Graph, commandPolicy)
	if err == nil {
		t.Fatal("invalid script accepted")
	}
	public := Failure(err).Error
	if public.Code != "invalid_graph" || public.MessageKey != "err.localcmd.unsupported_ext" || public.MessageArgs["graph_node"] != "prepare" {
		t.Fatal("lost validation reason or node", public)
	}
	for _, locale := range []string{"zh-CN", "en-US"} {
		if got := i18n.LocalizeMessage(locale, public.MessageKey, public.MessageArgs, public.Message); got != i18n.T(locale, "err.localcmd.unsupported_ext") {
			t.Fatal("validation reason frozen in another language", got)
		}
	}
}

func TestGraphMaximumConfigurationRoundTripsRunMetadata(t *testing.T) {
	r := maximumGraphRecord(t)
	directory := t.TempDir()
	for len(directory) < 2038 {
		length := min(200, 2039-len(directory)-1)
		directory = filepath.Join(directory, strings.Repeat("&", length))
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(directory, "step.cmd")
	if err := os.WriteFile(script, []byte("@exit /b 0\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := range r.Command.Graph.Nodes {
		n := &r.Command.Graph.Nodes[i]
		if n.Kind == "script" {
			n.Script, n.Directory = script, directory
			n.Arguments = []string{strings.Repeat("x", 4091), strings.Repeat("y", 4091)}
		}
	}
	if _, err := normalize(r.Command); err != nil {
		t.Fatal(err)
	}
	if status := inspect(r.Command); status.state != "ready" {
		t.Fatal("maximum paths are not actually usable", status)
	}
	encoded, err := json.Marshal(r)
	if err != nil || len(encoded) <= 2*1024*1024 {
		t.Fatal("fixture does not reach the old metadata limit", len(encoded), err)
	}
	store := runStore{root: t.TempDir()}
	f, err := store.prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	loaded, err := store.loadRun(r.Command.ID, r.ID)
	if err != nil {
		t.Fatal("accepted graph cannot be read after reopening", err)
	}
	if loaded.Command.Graph.Nodes[2].Script != script || loaded.Nodes[2].RunID != r.Nodes[2].RunID {
		t.Fatal("configuration or execution identity lost")
	}
	t.Logf("round-tripped %d metadata bytes from %d-byte existing script path", len(encoded), len(script))
}

func TestRunMetadataLimitRejectsBeforeReplacingPreviousRecord(t *testing.T) {
	store := runStore{root: t.TempDir()}
	r := maximumGraphRecord(t)
	f, err := store.prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	before, err := os.ReadFile(store.recordPath(r.Command.ID, r.ID))
	if err != nil {
		t.Fatal(err)
	}
	r.Command.Description = strings.Repeat("x", maxRunRecordBytes)
	if err = store.save(r); err == nil {
		t.Fatal("published a record too large to reopen")
	}
	after, err := os.ReadFile(store.recordPath(r.Command.ID, r.ID))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("replaced previous metadata despite rejecting the new record", err)
	}
	if err = os.WriteFile(store.recordPath(r.Command.ID, r.ID), bytes.Repeat([]byte(" "), maxRunRecordBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.loadRun(r.Command.ID, r.ID); err == nil {
		t.Fatal("accepted oversized input metadata")
	}
}
