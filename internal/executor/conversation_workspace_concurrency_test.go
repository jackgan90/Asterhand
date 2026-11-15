package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/workspace"
)

type nativeToolBatchCall struct {
	ID     string
	Name   string
	Params any
}

func emitBatchSSE(w http.ResponseWriter, batch []nativeToolBatchCall, finalText string) {
	w.Header().Set("Content-Type", "text/event-stream")
	var choices []any
	if len(batch) == 0 {
		choices = []any{
			map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": finalText}, "finish_reason": nil},
			map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"},
		}
	} else {
		calls := make([]any, 0, len(batch))
		for idx, c := range batch {
			args, _ := json.Marshal(c.Params)
			calls = append(calls, map[string]any{
				"index": idx, "id": c.ID, "type": "function",
				"function": map[string]any{"name": c.Name, "arguments": string(args)},
			})
		}
		choices = []any{
			map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "tool_calls": calls}, "finish_reason": nil},
			map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"},
		}
	}
	for _, choice := range choices {
		data, _ := json.Marshal(map[string]any{"id": "batch-fixture", "object": "chat.completion.chunk", "model": "chat", "choices": []any{choice}})
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

type scriptedBatchMock struct {
	mu             sync.Mutex
	scripted       [][]nativeToolBatchCall
	requests       []json.RawMessage
	blockSecondReq atomic.Bool
}

func newScriptedBatchServer(mock *scriptedBatchMock) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body json.RawMessage
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		mock.mu.Lock()
		idx := len(mock.requests)
		mock.requests = append(mock.requests, body)
		var batch []nativeToolBatchCall
		if idx < len(scriptedOrNil(mock, idx)) {
			batch = mock.scripted[idx]
		}
		mock.mu.Unlock()
		if idx == 1 && mock.blockSecondReq.Load() {
			<-r.Context().Done()
			return
		}
		emitBatchSSE(w, batch, "Batch turn completed")
	}))
}

func scriptedOrNil(mock *scriptedBatchMock, idx int) []nativeToolBatchCall {
	if idx < len(mock.scripted) {
		return mock.scripted[idx]
	}
	return nil
}

func TestNativeWorkspaceConcurrency(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_RUNTIME") != "1" {
		t.Skip("requires explicit pinned Windows native runtime")
	}
	node, entry := os.Getenv("ASTERHAND_CHAT_NODE"), os.Getenv("ASTERHAND_CHAT_ENTRY")
	if node == "" || entry == "" {
		t.Fatal("locked runtime paths required")
	}
	mock := &scriptedBatchMock{}
	server := newScriptedBatchServer(mock)
	defer server.Close()

	process, files := startNativeWorkspaceFixture(t, t.TempDir(), node, entry, server.URL)
	defer process.close()
	files.EnableTrace(1024)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	runScenario := func(t *testing.T, batches [][]nativeToolBatchCall) (conversation.Binding, string, []json.RawMessage) {
		t.Helper()
		mock.mu.Lock()
		mock.scripted = batches
		mock.requests = nil
		mock.mu.Unlock()

		binding := conversation.Binding{Runtime: "openclaw", Generation: strings.Repeat("b", 32), Key: model.ID()}
		if err := process.call(ctx, chatCommand{Action: "create", Binding: binding}, &binding); err != nil {
			t.Fatal(err)
		}
		run := model.ID()
		if err := files.AcquireTurnScope(workspace.Scope{Turn: run, Conversation: binding.Key, Policy: "fixture-policy"}); err != nil {
			t.Fatal(err)
		}
		started := time.Now().UnixMilli()
		if err := process.call(ctx, chatCommand{Action: "submit", Binding: binding, Run: run, Text: "Execute batch scenario"}, nil); err != nil {
			t.Fatal(err)
		}
		if obs := waitNativeTurn(t, ctx, process, binding, run, started); obs.Run != "completed" {
			t.Fatalf("unexpected run state: %+v", obs)
		}
		if err := files.ReleaseTurn(run, true); err != nil {
			t.Fatal(err)
		}
		mock.mu.Lock()
		captured := append([]json.RawMessage(nil), mock.requests...)
		mock.mu.Unlock()
		return binding, run, captured
	}

	if _, err := files.Mutate("seed-conc", workspace.Scope{Policy: "fixture-policy"}, []workspace.Mutation{
		{Action: "write", Path: "sub/alpha.txt", Expected: workspace.MissingRevision, Content: "alpha-body"},
		{Action: "write", Path: "sub/beta.txt", Expected: workspace.MissingRevision, Content: "beta-body"},
		{Action: "write", Path: "slow-first.txt", Expected: workspace.MissingRevision, Content: "slow-body"},
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("parallel_reads_and_list_dir_overlap", func(t *testing.T) {
		verifyParallelReadsAndListDirOverlap(t, files, runScenario)
	})
	t.Run("parallel_disjoint_writes_overlap", func(t *testing.T) {
		verifyParallelDisjointWritesOverlap(t, files, runScenario)
	})
	t.Run("conflicting_write_then_read_preserves_batch_order", func(t *testing.T) {
		_, _, captured := runScenario(t, [][]nativeToolBatchCall{{
			{ID: "cw1", Name: "write_file", Params: map[string]any{"path": "ordered.txt", "content": "freshly-written-in-batch"}},
			{ID: "cr1", Name: "read_file", Params: map[string]any{"path": "ordered.txt"}},
		}})
		if !strings.Contains(string(captured[len(captured)-1]), "freshly-written-in-batch") {
			t.Fatalf("read_file did not observe preceding write_file in same batch: %s", captured[len(captured)-1])
		}
	})
	t.Run("memory_write_then_search_with_parallel_non_memory_write", func(t *testing.T) {
		verifyMemoryWriteSearchAndParallelWrite(t, files, runScenario)
	})
	t.Run("out_of_order_completion_preserves_transcript_tool_order", func(t *testing.T) {
		verifyOutOfOrderHistoryProjection(t, ctx, process, files, runScenario)
	})
	t.Run("mid_batch_cancel_retains_completed_call_and_recovers_next_turn", func(t *testing.T) {
		verifyMidBatchCancellationRecovery(t, ctx, process, files, &mock.mu, &mock.scripted, &mock.requests, &mock.blockSecondReq)
	})
	t.Run("same_batch_write_conflict_and_ordered_directory_listing", func(t *testing.T) {
		verifyBatchWriteConflictAndDirectoryListing(t, files, runScenario)
	})
}

func verifyParallelReadsAndListDirOverlap(
	t *testing.T,
	files *workspace.Service,
	runScenario func(t *testing.T, batches [][]nativeToolBatchCall) (conversation.Binding, string, []json.RawMessage),
) {
	t.Helper()
	var inFlightReads atomic.Int32
	var maxConcurrentReads atomic.Int32
	bothArrived := make(chan struct{})
	var once sync.Once
	files.SetIOHook(func(_, tool string, eff workspace.Effect, rel string) {
		if tool == "read_file" && eff == workspace.EffectRead && strings.HasPrefix(rel, "sub/") {
			cur := inFlightReads.Add(1)
			for {
				prev := maxConcurrentReads.Load()
				if cur <= prev || maxConcurrentReads.CompareAndSwap(prev, cur) {
					break
				}
			}
			if cur >= 2 {
				once.Do(func() { close(bothArrived) })
			}
			select {
			case <-bothArrived:
			case <-time.After(1500 * time.Millisecond):
			}
			inFlightReads.Add(-1)
		}
	})
	defer files.SetIOHook(nil)

	_, run, captured := runScenario(t, [][]nativeToolBatchCall{{
		{ID: "pr1", Name: "read_file", Params: map[string]any{"path": "sub/alpha.txt"}},
		{ID: "pr2", Name: "read_file", Params: map[string]any{"path": "sub/beta.txt"}},
		{ID: "pl1", Name: "list_dir", Params: map[string]any{"path": "sub"}},
	}})
	if maxConcurrentReads.Load() < 2 {
		t.Fatalf("expected simultaneous read_file overlap >= 2, got %d", maxConcurrentReads.Load())
	}
	ops, _ := files.Operations(run, 0)
	if len(ops) != 3 {
		t.Fatalf("expected 3 completed operations, got %d", len(ops))
	}
	last := string(captured[len(captured)-1])
	if !strings.Contains(last, "alpha-body") || !strings.Contains(last, "beta-body") {
		t.Fatalf("parallel read contents missing from model follow-up: %s", last)
	}
}

func verifyParallelDisjointWritesOverlap(
	t *testing.T,
	files *workspace.Service,
	runScenario func(t *testing.T, batches [][]nativeToolBatchCall) (conversation.Binding, string, []json.RawMessage),
) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(files.Root(), "par"), 0700); err != nil {
		t.Fatal(err)
	}
	var inFlightWrites atomic.Int32
	var maxWrites atomic.Int32
	bothWrites := make(chan struct{})
	var once sync.Once
	files.SetIOHook(func(_, tool string, eff workspace.Effect, rel string) {
		if tool == "write_file" && (eff == workspace.EffectWrite || eff == workspace.EffectCreate) && strings.HasPrefix(rel, "par/") {
			cur := inFlightWrites.Add(1)
			for {
				prev := maxWrites.Load()
				if cur <= prev || maxWrites.CompareAndSwap(prev, cur) {
					break
				}
			}
			if cur >= 2 {
				once.Do(func() { close(bothWrites) })
			}
			select {
			case <-bothWrites:
			case <-time.After(1500 * time.Millisecond):
			}
			inFlightWrites.Add(-1)
		}
	})
	defer files.SetIOHook(nil)

	runScenario(t, [][]nativeToolBatchCall{{
		{ID: "pw1", Name: "write_file", Params: map[string]any{"path": "par/one.txt", "content": "one-val"}},
		{ID: "pw2", Name: "write_file", Params: map[string]any{"path": "par/two.txt", "content": "two-val"}},
	}})
	if maxWrites.Load() < 2 {
		t.Fatalf("expected simultaneous write_file overlap >= 2, got %d", maxWrites.Load())
	}
	for name, want := range map[string]string{"par/one.txt": "one-val", "par/two.txt": "two-val"} {
		got, err := os.ReadFile(filepath.Join(files.Root(), filepath.FromSlash(name)))
		if err != nil || string(got) != want {
			t.Fatalf("file %s = %q (%v), want %q", name, got, err, want)
		}
	}
}

func verifyMemoryWriteSearchAndParallelWrite(
	t *testing.T,
	files *workspace.Service,
	runScenario func(t *testing.T, batches [][]nativeToolBatchCall) (conversation.Binding, string, []json.RawMessage),
) {
	t.Helper()
	_, _, captured := runScenario(t, [][]nativeToolBatchCall{{
		{ID: "mw1", Name: "write_file", Params: map[string]any{"path": "memory/astronomy.md", "content": "# Astronomy\nJupiter aurora observation logged.\n"}},
		{ID: "ms1", Name: "memory_search", Params: map[string]any{"query": "Jupiter"}},
		{ID: "mn1", Name: "write_file", Params: map[string]any{"path": "notes/unrelated.txt", "content": "unrelated-ok"}},
	}})
	last := string(captured[len(captured)-1])
	if !strings.Contains(last, "memory/astronomy.md") || !strings.Contains(last, "Jupiter") {
		t.Fatalf("memory_search did not retrieve preceding memory write in same batch: %s", last)
	}
	if got, err := os.ReadFile(filepath.Join(files.Root(), "notes", "unrelated.txt")); err != nil || string(got) != "unrelated-ok" {
		t.Fatalf("non-memory write failed: %q %v", got, err)
	}
}

func verifyOutOfOrderHistoryProjection(
	t *testing.T,
	ctx context.Context,
	process *chatProcess,
	files *workspace.Service,
	runScenario func(t *testing.T, batches [][]nativeToolBatchCall) (conversation.Binding, string, []json.RawMessage),
) {
	t.Helper()
	files.SetIOHook(func(_, tool string, eff workspace.Effect, rel string) {
		if tool == "read_file" && eff == workspace.EffectRead && rel == "slow-first.txt" {
			time.Sleep(200 * time.Millisecond)
		}
	})
	defer files.SetIOHook(nil)

	binding, _, _ := runScenario(t, [][]nativeToolBatchCall{{
		{ID: "oo1", Name: "read_file", Params: map[string]any{"path": "slow-first.txt"}},
		{ID: "oo2", Name: "write_file", Params: map[string]any{"path": "fast-second.txt", "content": "fast"}},
		{ID: "oo3", Name: "progress_card", Params: map[string]any{"plan": []any{map[string]any{"step": "Verify order", "status": "completed"}}}},
	}})

	var page conversation.Page
	if err := process.call(ctx, chatCommand{Action: "history", Binding: binding}, &page); err != nil {
		t.Fatal(err)
	}
	enrichHistoryTools(&page, binding.Key, files)

	var gotTools []string
	for _, msg := range page.Messages {
		for _, block := range msg.Blocks {
			if block.Type == "tool" && block.Tool != nil {
				gotTools = append(gotTools, block.Tool.Tool)
				if block.Tool.State != "completed" {
					t.Fatalf("expected completed tool in history, got %+v", block.Tool)
				}
			}
		}
	}
	wantTools := []string{"read_file", "write_file", "progress_card"}
	if !reflect.DeepEqual(gotTools, wantTools) {
		t.Fatalf("history tool order = %v, want %v", gotTools, wantTools)
	}
}

func verifyMidBatchCancellationRecovery(
	t *testing.T,
	ctx context.Context,
	process *chatProcess,
	files *workspace.Service,
	mu *sync.Mutex,
	scripted *[][]nativeToolBatchCall,
	requests *[]json.RawMessage,
	blockSecondReq *atomic.Bool,
) {
	t.Helper()
	slowEntered := make(chan struct{}, 1)
	releaseSlow := make(chan struct{})
	files.SetIOHook(func(_, tool string, eff workspace.Effect, rel string) {
		if tool == "read_file" && eff == workspace.EffectRead && rel == "slow-first.txt" {
			select {
			case slowEntered <- struct{}{}:
			default:
			}
			<-releaseSlow
		}
	})

	mu.Lock()
	*scripted = [][]nativeToolBatchCall{{
		{ID: "mc1", Name: "write_file", Params: map[string]any{"path": "kept-in-batch.txt", "content": "retained"}},
		{ID: "mc2", Name: "read_file", Params: map[string]any{"path": "slow-first.txt"}},
	}}
	*requests = nil
	mu.Unlock()

	binding := conversation.Binding{Runtime: "openclaw", Generation: strings.Repeat("b", 32), Key: model.ID()}
	if err := process.call(ctx, chatCommand{Action: "create", Binding: binding}, &binding); err != nil {
		t.Fatal(err)
	}
	run := model.ID()
	if err := files.AcquireTurnScope(workspace.Scope{Turn: run, Conversation: binding.Key, Policy: "fixture-policy"}); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UnixMilli()
	if err := process.call(ctx, chatCommand{Action: "submit", Binding: binding, Run: run, Text: "Cancel mid-batch"}, nil); err != nil {
		t.Fatal(err)
	}

	<-slowEntered
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(files.Root(), "kept-in-batch.txt")); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	files.CancelTurn(run)
	if err := process.call(ctx, chatCommand{Action: "cancel", Binding: binding, Run: run}, nil); err != nil {
		close(releaseSlow)
		t.Fatal(err)
	}
	close(releaseSlow)
	files.SetIOHook(nil)

	obs := waitNativeTurn(t, ctx, process, binding, run, started)
	if obs.Run != "cancelled" {
		t.Fatalf("expected cancelled run, got %+v", obs)
	}
	if err := files.ReleaseTurn(run, true); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(files.Root(), "kept-in-batch.txt"))
	if err != nil || string(got) != "retained" {
		t.Fatalf("completed sibling write lost on batch cancel: %q %v", got, err)
	}

	blockSecondReq.Store(false)
	mu.Lock()
	*scripted = nil
	*requests = nil
	mu.Unlock()
	nextRun := model.ID()
	if err := files.AcquireTurnScope(workspace.Scope{Turn: nextRun, Conversation: binding.Key, Policy: "fixture-policy"}); err != nil {
		t.Fatal(err)
	}
	nextStart := time.Now().UnixMilli()
	if err := process.call(ctx, chatCommand{Action: "submit", Binding: binding, Run: nextRun, Text: "Follow up after cancel"}, nil); err != nil {
		t.Fatal(err)
	}
	nextObs := waitNativeTurn(t, ctx, process, binding, nextRun, nextStart)
	if nextObs.Run != "completed" {
		t.Fatalf("next turn after batch cancel failed: %+v", nextObs)
	}
	if err := files.ReleaseTurn(nextRun, true); err != nil {
		t.Fatal(err)
	}
}

func verifyBatchWriteConflictAndDirectoryListing(
	t *testing.T,
	files *workspace.Service,
	runScenario func(t *testing.T, batches [][]nativeToolBatchCall) (conversation.Binding, string, []json.RawMessage),
) {
	t.Helper()
	if _, err := files.Mutate("seed-dir-order", workspace.Scope{Policy: "fixture-policy"}, []workspace.Mutation{
		{Action: "write", Path: "sub/nested/child.txt", Expected: workspace.MissingRevision, Content: "nested-body"},
		{Action: "write", Path: "sub/gone.txt", Expected: workspace.MissingRevision, Content: "to-be-deleted"},
	}); err != nil {
		t.Fatal(err)
	}

	// Batch 1: two blind write_file calls to race.txt -> bw0 wins, bw1 fails with conflicting_batch_write.
	_, _, capturedWrite := runScenario(t, [][]nativeToolBatchCall{{
		{ID: "bw0", Name: "write_file", Params: map[string]any{"path": "race.txt", "content": "WRITER_A"}},
		{ID: "bw1", Name: "write_file", Params: map[string]any{"path": "race.txt", "content": "WRITER_B"}},
	}})
	gotRace, err := os.ReadFile(filepath.Join(files.Root(), "race.txt"))
	if err != nil || string(gotRace) != "WRITER_A" {
		t.Fatalf("race.txt = %q (%v), want %q", gotRace, err, "WRITER_A")
	}
	lastWriteMsg := string(capturedWrite[len(capturedWrite)-1])
	if !strings.Contains(lastWriteMsg, "conflicting_batch_write") {
		t.Fatalf("expected conflicting_batch_write in tool result sent to model: %s", lastWriteMsg)
	}

	// Next turn/batch: write_file on race.txt remains allowed across batches, and delete -> list_dir runs in order with '/' directory marker.
	_, _, capturedList := runScenario(t, [][]nativeToolBatchCall{{
		{ID: "bw2", Name: "write_file", Params: map[string]any{"path": "race.txt", "content": "NEXT_TURN_OK"}},
		{ID: "dl0", Name: "apply_file_patch", Params: map[string]any{"input": "*** Begin Patch\n*** Delete File: sub/gone.txt\n*** End Patch"}},
		{ID: "dl1", Name: "list_dir", Params: map[string]any{"path": "sub"}},
	}})
	gotRace2, err := os.ReadFile(filepath.Join(files.Root(), "race.txt"))
	if err != nil || string(gotRace2) != "NEXT_TURN_OK" {
		t.Fatalf("cross-batch race.txt = %q (%v), want %q", gotRace2, err, "NEXT_TURN_OK")
	}
	var followUp struct {
		Messages []struct {
			Role    string `json:"role"`
			CallID  string `json:"tool_call_id"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(capturedList[len(capturedList)-1], &followUp); err != nil {
		t.Fatal(err)
	}
	var listOutput string
	for _, msg := range followUp.Messages {
		if msg.CallID == "dl1" {
			raw, _ := json.Marshal(msg.Content)
			listOutput = string(raw)
		}
	}
	if listOutput == "" {
		t.Fatalf("missing dl1 list_dir result in follow-up: %s", capturedList[len(capturedList)-1])
	}
	if strings.Contains(listOutput, "gone.txt") {
		t.Fatalf("list_dir observed deleted file despite following apply_file_patch in batch order: %s", listOutput)
	}
	if !strings.Contains(listOutput, "nested/") {
		t.Fatalf("list_dir did not mark subdirectory with trailing slash: %s", listOutput)
	}
}
