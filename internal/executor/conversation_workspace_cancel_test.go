package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/workspace"
)

func TestNativeWorkspaceCancellationRetainsCompletedFiles(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_RUNTIME") != "1" {
		t.Skip("requires explicit pinned Windows native runtime")
	}
	root := t.TempDir()
	if evidence := os.Getenv("ASTERHAND_CHAT_EVIDENCE"); evidence != "" {
		var err error
		root, err = os.MkdirTemp(evidence, "native-cancel-")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("fixture: %s", root)
	}
	var calls atomic.Int32
	stalled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request json.RawMessage
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "invalid", 400)
			return
		}
		index := calls.Add(1)
		if index == 2 {
			close(stalled)
			<-r.Context().Done()
			return
		}
		delta := map[string]any{"role": "assistant", "content": "Next turn completed"}
		finish := "stop"
		if index == 1 {
			args, _ := json.Marshal(map[string]any{"path": "kept.txt", "content": "Written before stop"})
			delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "cancel-write", "type": "function", "function": map[string]any{"name": "write", "arguments": string(args)}}}}
			finish = "tool_calls"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, choice := range []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}, map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}} {
			data, _ := json.Marshal(map[string]any{"id": "cancel-fixture", "object": "chat.completion.chunk", "model": "chat", "choices": []any{choice}})
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	process, files := startNativeWorkspaceFixture(t, root, os.Getenv("ASTERHAND_CHAT_NODE"), os.Getenv("ASTERHAND_CHAT_ENTRY"), server.URL)
	defer process.close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	binding := conversation.Binding{Runtime: "openclaw", Generation: strings.Repeat("a", 32), Key: model.ID()}
	if err := process.call(ctx, chatCommand{Action: "create", Binding: binding}, &binding); err != nil {
		t.Fatal(err)
	}
	run := model.ID()
	if err := files.AcquireTurnScope(workspace.Scope{Turn: run, Conversation: binding.Key, Policy: "fixture-policy"}); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UnixMilli()
	if err := process.call(ctx, chatCommand{Action: "submit", Binding: binding, Run: run, Text: "Write then wait"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stalled:
	case <-ctx.Done():
		t.Fatal("model did not reach pending second request")
	}
	if err := process.call(ctx, chatCommand{Action: "cancel", Binding: binding, Run: run}, nil); err != nil {
		t.Fatal(err)
	}
	observed := waitNativeTurn(t, ctx, process, binding, run, started)
	if observed.Run != "cancelled" {
		t.Fatalf("cancel outcome: %+v", observed)
	}
	if err := files.ReleaseTurn(run, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(files.Root(), "kept.txt"))
	if err != nil || string(data) != "Written before stop" {
		t.Fatal("completed file lost", err)
	}
	ops, _ := files.Operations(run, 0)
	if len(ops) != 1 || ops[0].State != "completed" {
		t.Fatalf("lost completed tool: %+v", ops)
	}
	next := model.ID()
	if err := files.AcquireTurnScope(workspace.Scope{Turn: next, Conversation: binding.Key, Policy: "fixture-policy"}); err != nil {
		t.Fatal(err)
	}
	started = time.Now().UnixMilli()
	if err := process.call(ctx, chatCommand{Action: "submit", Binding: binding, Run: next, Text: "Continue"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := process.call(ctx, chatCommand{Action: "cancel", Binding: binding, Run: run}, nil); err != nil {
		t.Fatal(err)
	}
	observed = waitNativeTurn(t, ctx, process, binding, next, started)
	if observed.Run != "completed" {
		t.Fatalf("late cancel affected next turn: %+v", observed)
	}
	if err := files.ReleaseTurn(next, true); err != nil {
		t.Fatal(err)
	}
}
