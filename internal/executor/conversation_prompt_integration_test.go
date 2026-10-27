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
	"github.com/jackgan90/Asterhand/internal/prompt"
)

func TestNativeConversationPromptModes(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_RUNTIME") != "1" {
		t.Skip("explicit locked native runtime required")
	}
	node, entry := os.Getenv("ASTERHAND_CHAT_NODE"), os.Getenv("ASTERHAND_CHAT_ENTRY")
	if node == "" || entry == "" {
		t.Fatal("locked paths required")
	}
	for _, scenario := range []struct {
		name    string
		mode    prompt.Mode
		invalid bool
	}{
		{"unsupported", prompt.Unsupported, false}, {"invalid-policy", prompt.Replace, true}, {"append", prompt.Append, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var calls, tools atomic.Int32
			var sawPolicy, sawNativePersona atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input map[string]json.RawMessage
				if json.NewDecoder(r.Body).Decode(&input) != nil {
					http.Error(w, "bad request", 400)
					return
				}
				var exposed []json.RawMessage
				_ = json.Unmarshal(input["tools"], &exposed)
				if len(exposed) > 0 {
					tools.Add(1)
				}
				var messages []map[string]json.RawMessage
				_ = json.Unmarshal(input["messages"], &messages)
				for _, message := range messages {
					var role, content string
					_ = json.Unmarshal(message["role"], &role)
					_ = json.Unmarshal(message["content"], &content)
					if role == "system" && strings.Contains(content, "# Asterhand 当前聊天入口适配") {
						sawPolicy.Store(true)
					}
					if role == "system" && strings.Contains(content, "You are a personal assistant running inside OpenClaw.") && strings.Contains(content, "Be genuinely helpful, not performatively helpful.") {
						sawNativePersona.Store(true)
					}
				}
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				chunk, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "model": "chat",
					"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "reasoning_content": strings.Repeat("synthetic internal field ", 100), "content": "short answer"}, "finish_reason": nil}}})
				fmt.Fprintf(w, "data: %s\n\n", chunk)
				final, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "model": "chat",
					"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
				fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", final)
			}))
			defer server.Close()
			root := t.TempDir()
			process := startNativeChatFixture(t, root, node, entry, server.URL, true, scenario.mode)
			defer process.close()
			if scenario.invalid {
				if err := os.WriteFile(filepath.Join(root, "prompt-policy.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			binding := conversation.Binding{Runtime: "openclaw", Generation: strings.Repeat("a", 32), Key: model.ID()}
			if err := process.call(ctx, chatCommand{Action: "create", Binding: binding}, &binding); err != nil {
				t.Fatal(err)
			}
			run := model.ID()
			if err := process.call(ctx, chatCommand{Action: "submit", Binding: binding, Run: run, Text: "ordinary text chat"}, nil); err != nil {
				t.Fatal(err)
			}
			result := waitNativeTurn(t, ctx, process, binding, run, time.Now().UnixMilli())
			if !result.Accepted || !result.Saved || result.Run != "completed" || result.Prompt == nil || calls.Load() != 1 || tools.Load() != 0 || !sawNativePersona.Load() {
				t.Fatalf("optional prompt blocked chat: %+v calls=%d tools=%d", result, calls.Load(), tools.Load())
			}
			expected := prompt.Degraded
			if scenario.mode == prompt.Append {
				expected = prompt.Applied
			}
			if result.Prompt.Mode != scenario.mode || result.Prompt.State != expected || sawPolicy.Load() != (scenario.mode == prompt.Append) {
				t.Fatalf("incorrect application evidence: %+v observed=%t", result.Prompt, sawPolicy.Load())
			}
			var page conversation.Page
			if err := process.call(ctx, chatCommand{Action: "history", Binding: binding}, &page); err != nil {
				t.Fatal(err)
			}
			for _, message := range page.Messages {
				if message.ID == result.AssistantMessage && message.Truncated {
					t.Fatal("complete short answer marked truncated")
				}
			}
		})
	}
}
