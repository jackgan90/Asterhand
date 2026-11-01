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

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/store"
)

// Only the provider selection is a fixture. Queue, startup ownership, private
// bridge, native sessions, receipts and history use production code.
type queueNativeRuntime struct{ *ConversationRuntime }

func (r queueNativeRuntime) Describe() (conversation.Descriptor, error) {
	return conversation.Descriptor{ID: "openclaw", Binding: chatBindingID(), CanSend: true, Provider: "fixture", Model: "fixture/chat",
		Capabilities: conversation.Capabilities{History: true, Streaming: true, Cancellation: true, DurableReceipts: true}}, nil
}

func TestNativeConversationQueuedStartupAndHistory(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_RUNTIME") != "1" {
		t.Skip("explicit locked native runtime required")
	}
	node, entry := os.Getenv("ASTERHAND_CHAT_NODE"), os.Getenv("ASTERHAND_CHAT_ENTRY")
	if node == "" || entry == "" {
		t.Fatal("locked paths required")
	}
	var calls atomic.Int32
	var contextPreserved atomic.Bool
	server := queuedModelFixture(t, &calls, &contextPreserved)
	defer server.Close()
	manager, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewConversationRuntime(manager)
	ready := make(chan *chatProcess, 1)
	runtime.start = func(ctx context.Context, _ config.Config, _ openClawOptions, _ providerBridge, _, _ string) (*chatProcess, error) {
		select {
		case process := <-ready:
			return process, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	records, err := store.Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer records.Close()
	service := conversation.New(model.ID(), records, queueNativeRuntime{runtime})
	defer service.Close()
	call := func(request conversation.Request) conversation.Response {
		request.Target = service.Target()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		response, err := service.Call(ctx, request)
		if err != nil {
			t.Fatalf("%s: %v", request.Action, err)
		}
		return response
	}
	a := call(conversation.Request{Action: "prepare", Operation: model.ID()}).Conversation.ID
	ops := []string{model.ID(), model.ID(), model.ID()}
	for i := 0; i < 2; i++ {
		response := call(conversation.Request{Action: "enqueue", Conversation: a, Operation: ops[i], Text: fmt.Sprintf("queued-text-%d", i), DraftRevision: uint64(i + 1)})
		if response.Turn.Delivery != "queued" {
			t.Fatal("startup did not acknowledge node custody")
		}
	}
	b := call(conversation.Request{Action: "prepare", Operation: model.ID()}).Conversation.ID
	call(conversation.Request{Action: "enqueue", Conversation: b, Operation: ops[2], Text: "other conversation", DraftRevision: 3})
	if calls.Load() != 0 {
		t.Fatal("model ran before startup")
	}
	process := startNativeChatFixture(t, t.TempDir(), node, entry, server.URL, true)
	defer process.close()
	ready <- process
	deadline := time.Now().Add(90 * time.Second)
	for {
		state := call(conversation.Request{Action: "queue"}).Queue
		if state.Paused {
			t.Fatalf("native queue paused: %+v", state.Entries)
		}
		if len(state.Entries) == 0 && state.Active == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native queue did not drain")
		}
		time.Sleep(100 * time.Millisecond)
	}
	for i, operation := range ops {
		id := a
		if i == 2 {
			id = b
		}
		result := call(conversation.Request{Action: "queue_item", Conversation: id, Operation: operation})
		if result.QueueItem.Turn.Delivery != "accepted" || result.QueueItem.Turn.Run != "completed" || result.QueueItem.Turn.Persistence != "saved" {
			t.Fatalf("unsettled native receipt: %+v", result.QueueItem)
		}
	}
	page := call(conversation.Request{Action: "history", Conversation: a}).Page
	if len(page.Messages) != 4 || calls.Load() != 3 || !contextPreserved.Load() {
		t.Fatalf("history/context mismatch: rows=%d calls=%d context=%v", len(page.Messages), calls.Load(), contextPreserved.Load())
	}
	other := call(conversation.Request{Action: "history", Conversation: b}).Page
	if len(other.Messages) != 2 {
		t.Fatal("cross-conversation history was mixed")
	}
	t.Log("three queued sends, one shared startup, two native conversations; receipts and history preserved")
}

func queuedModelFixture(t *testing.T, calls *atomic.Int32, contextPreserved *atomic.Bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		index := calls.Add(1)
		if index == 2 {
			var user, assistant bool
			for _, message := range input.Messages {
				user = user || message.Role == "user" && strings.Contains(string(message.Content), "queued-text-0")
				assistant = assistant || message.Role == "assistant" && strings.Contains(string(message.Content), "queue-answer-1")
			}
			contextPreserved.Store(user && assistant)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"id\":\"queue\",\"object\":\"chat.completion.chunk\",\"model\":\"chat\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"queue-answer-%d\"},\"finish_reason\":null}]}\n\n", index)
		fmt.Fprint(w, "data: {\"id\":\"queue\",\"object\":\"chat.completion.chunk\",\"model\":\"chat\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
}
