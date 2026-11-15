package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/workspace"
)

func TestNativeConversationToolTimelinePagination(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_RUNTIME") != "1" {
		t.Skip("requires the locked native runtime and local model fixture")
	}
	node, entry := os.Getenv("ASTERHAND_CHAT_NODE"), os.Getenv("ASTERHAND_CHAT_ENTRY")
	if node == "" || entry == "" {
		t.Fatal("locked runtime paths required")
	}
	root := t.TempDir()
	if evidence := os.Getenv("ASTERHAND_CHAT_EVIDENCE"); evidence != "" {
		var err error
		root, err = os.MkdirTemp(evidence, "native-timeline-")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("isolated timeline fixture: %s", root)
	}
	const toolCount = 32
	server, calls := timelineModel(toolCount)
	defer server.Close()
	process, files := startNativeWorkspaceFixture(t, root, node, entry, server.URL)
	defer func() { process.close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	binding := conversation.Binding{Runtime: "openclaw", Generation: strings.Repeat("a", 32), Key: model.ID()}
	if err := process.call(ctx, chatCommand{Action: "create", Binding: binding}, &binding); err != nil {
		t.Fatal(err)
	}
	sendTimelineTurn(t, ctx, process, files, binding, "opening greeting")
	sendTimelineTurn(t, ctx, process, files, binding, "exercise the tools")
	before := calls.Load()
	if before != toolCount+2 {
		t.Fatalf("unexpected model calls: %d", before)
	}
	messages := readTimelinePages(t, ctx, process, files, binding)
	assertTimelineOrder(t, messages, toolCount)
	for _, message := range messages {
		for _, block := range message.Blocks {
			if block.Type != "text" || block.ID == "" {
				continue
			}
			var fragment conversation.Fragment
			if err := process.call(ctx, chatCommand{Action: "message", Binding: binding, Message: block.ID}, &fragment); err != nil {
				t.Fatal(err)
			}
			if fragment.Text != block.Text || fragment.HasMore {
				t.Fatal("mixed message full-read crossed a tool boundary")
			}
		}
	}
	if err := process.close(); err != nil {
		t.Fatal(err)
	}
	process, files = startNativeWorkspaceFixture(t, root, node, entry, server.URL)
	reopened := readTimelinePages(t, ctx, process, files, binding)
	if !reflect.DeepEqual(messages, reopened) || calls.Load() != before {
		t.Fatal("read-only restart changed the timeline or invoked the model")
	}
	t.Logf("%d native tools interleaved with text; earliest greeting reachable across pages and restart; zero additional model requests during reads", toolCount)
}

func timelineModel(count int) (*httptest.Server, *atomic.Int32) {
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input json.RawMessage
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		index := int(calls.Add(1)) - 2
		delta := map[string]any{"role": "assistant", "content": "final reply"}
		finish := "stop"
		if index < 0 {
			delta["content"] = "hello back"
		} else if index < count {
			delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0,
				"id": fmt.Sprintf("timeline-%d", index), "type": "function",
				"function": map[string]any{"name": "list_dir", "arguments": "{\"path\":\".\"}"}}}}
			if index%2 == 0 {
				delta["content"] = fmt.Sprintf("before tool %02d", index)
			}
			finish = "tool_calls"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, choice := range []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil},
			map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}} {
			data, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "model": "chat", "choices": []any{choice}})
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	return server, calls
}

func sendTimelineTurn(t *testing.T, ctx context.Context, process *chatProcess, files *workspace.Service, binding conversation.Binding, text string) {
	t.Helper()
	run := model.ID()
	if err := files.AcquireTurnScope(workspace.Scope{Conversation: binding.Key, Turn: run, Policy: "fixture-policy"}); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UnixMilli()
	var receipt conversation.Receipt
	if err := process.call(ctx, chatCommand{Action: "submit", Binding: binding, Run: run, Text: text}, &receipt); err != nil {
		t.Fatal(err)
	}
	accepted := receipt.Accepted
	for attempt := 0; attempt < 180; attempt++ {
		var observation conversation.Observation
		if err := process.call(ctx, chatCommand{Action: "observe", Binding: binding, Run: run, Started: started}, &observation); err != nil {
			t.Fatal(err)
		}
		accepted = accepted || observation.Accepted
		if observation.Stopped {
			// A long tool turn moves its user row outside the latest native page;
			// retain the durable receipt already observed for this exact request.
			if observation.Run != "completed" || !observation.Saved || !accepted {
				t.Fatalf("timeline fixture did not complete: %+v", observation)
			}
			if err := files.ReleaseTurn(run, true); err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("timeline fixture did not settle")
}

func readTimelinePages(t *testing.T, ctx context.Context, process *chatProcess, files *workspace.Service, binding conversation.Binding) []conversation.Message {
	t.Helper()
	var messages []conversation.Message
	offset, cursor := 0, ""
	for pageNumber := 0; pageNumber < 10; pageNumber++ {
		var page conversation.Page
		if err := process.call(ctx, chatCommand{Action: "history", Binding: binding, Offset: offset, Cursor: cursor}, &page); err != nil {
			t.Fatal(err)
		}
		if page.Reset {
			t.Fatal("unchanged native offset history reset")
		}
		enrichHistoryTools(&page, binding.Key, files)
		data, _ := json.Marshal(page)
		if len(data) > conversation.PageBytes {
			t.Fatal("history page exceeded the public budget")
		}
		t.Logf("page offset=%d next=%d more=%t messages=%d", offset, page.Next, page.HasMore, len(page.Messages))
		messages = append(page.Messages, messages...)
		if !page.HasMore {
			if pageNumber == 0 {
				t.Fatal("fixture did not exercise an older offset page")
			}
			return messages
		}
		if page.Next <= offset {
			t.Fatal("history offset did not advance")
		}
		offset, cursor = page.Next, page.Cursor
	}
	t.Fatal("history pagination did not reach the first message")
	return nil
}

func assertTimelineOrder(t *testing.T, messages []conversation.Message, toolCount int) {
	t.Helper()
	var actual, expected []string
	seen := map[string]bool{}
	for _, message := range messages {
		if seen[message.ID] || len(message.Blocks) == 0 {
			t.Fatalf("duplicate or empty visible message: id=%q duplicate=%t blocks=%d", message.ID, seen[message.ID], len(message.Blocks))
		}
		seen[message.ID] = true
		for _, block := range message.Blocks {
			switch block.Type {
			case "text":
				actual = append(actual, block.Text)
			case "tool":
				if block.Tool == nil || block.Tool.State != "completed" || block.Tool.Summary != "." {
					t.Fatalf("tool outcome or summary missing from timeline: %+v", block.Tool)
				}
				actual = append(actual, "tool:"+block.Tool.Tool)
			default:
				t.Fatal("unexpected public content type")
			}
		}
	}
	expected = append(expected, "opening greeting", "hello back", "exercise the tools")
	for index := 0; index < toolCount; index++ {
		if index%2 == 0 {
			expected = append(expected, fmt.Sprintf("before tool %02d", index))
		}
		expected = append(expected, "tool:list_dir")
	}
	expected = append(expected, "final reply")
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("timeline order differs\nactual: %q\nexpected: %q", actual, expected)
	}
}
