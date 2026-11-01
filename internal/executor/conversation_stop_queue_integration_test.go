package executor

import (
	"context"
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

func TestNativeConversationStopReplyContinuesQueue(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_RUNTIME") != "1" {
		t.Skip("explicit locked native runtime required")
	}
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if index == 1 {
			fmt.Fprint(w, "data: {\"id\":\"stop-queue\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n")
			w.(http.Flusher).Flush()
			close(entered)
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		fmt.Fprint(w, "data: {\"id\":\"stop-queue\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"reply after stop\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"stop-queue\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	defer close(release)
	service := nativeStopQueueService(t, server.URL)
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
	id := call(conversation.Request{Action: "prepare", Operation: model.ID()}).Conversation.ID
	first, second := model.ID(), model.ID()
	for index, operation := range []string{first, second} {
		call(conversation.Request{Action: "enqueue", Conversation: id, Operation: operation, Text: fmt.Sprintf("stop-queue-user-%d", index), DraftRevision: uint64(index + 1)})
	}
	select {
	case <-entered:
	case <-time.After(90 * time.Second):
		t.Fatal("native model never started")
	}
	deadline := time.Now().Add(90 * time.Second)
	for state := call(conversation.Request{Action: "queue"}).Queue; state.Active == nil || state.Active.Delivery != "accepted"; state = call(conversation.Request{Action: "queue"}).Queue {
		if time.Now().After(deadline) {
			t.Fatal("native receipt was not confirmed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	stopped := call(conversation.Request{Action: "cancel", Conversation: id, Operation: first})
	t.Logf("stop receipt: %+v", stopped.Turn)
	for {
		state := call(conversation.Request{Action: "queue"}).Queue
		if len(state.Failures) != 0 {
			t.Fatalf("unexpected failed send: %+v", state.Failures)
		}
		if len(state.Entries) == 0 && state.Active == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stop reply blocked the next send: active=%+v entries=%+v calls=%d", state.Active, state.Entries, calls.Load())
		}
		time.Sleep(100 * time.Millisecond)
	}
	a := call(conversation.Request{Action: "queue_item", Conversation: id, Operation: first}).QueueItem.Turn
	b := call(conversation.Request{Action: "queue_item", Conversation: id, Operation: second}).QueueItem.Turn
	if a.Delivery != "accepted" || a.Run != "cancelled" || b.Delivery != "accepted" || b.Run != "completed" || calls.Load() != 2 {
		t.Fatalf("wrong terminal receipts: first=%+v second=%+v calls=%d", a, b, calls.Load())
	}
	page := call(conversation.Request{Action: "history", Conversation: id}).Page
	checkStopQueueHistory(t, page)
	t.Log("native stop kept both user messages; next queued send completed without resume or replay")
}

func checkStopQueueHistory(t *testing.T, page *conversation.Page) {
	t.Helper()
	var users, replies int
	for _, message := range page.Messages {
		for _, block := range message.Blocks {
			if message.Role == "user" && strings.Contains(block.Text, "stop-queue-user-") {
				users++
			}
			if message.Role == "assistant" && strings.Contains(block.Text, "reply after stop") {
				replies++
			}
		}
	}
	if users != 2 || replies != 1 {
		t.Fatalf("stop lost history: users=%d replies=%d", users, replies)
	}
}

func nativeStopQueueService(t *testing.T, endpoint string) *conversation.Service {
	t.Helper()
	manager, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewConversationRuntime(manager)
	process := startNativeChatFixture(t, t.TempDir(), os.Getenv("ASTERHAND_CHAT_NODE"), os.Getenv("ASTERHAND_CHAT_ENTRY"), endpoint, true)
	t.Cleanup(func() { process.close() })
	runtime.start = func(context.Context, config.Config, openClawOptions, providerBridge, string, string) (*chatProcess, error) {
		return process, nil
	}
	records, err := store.Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { records.Close() })
	service := conversation.New(model.ID(), records, queueNativeRuntime{runtime})
	t.Cleanup(func() { service.Close() })
	return service
}
