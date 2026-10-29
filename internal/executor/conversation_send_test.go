package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/model"
)

// Keep the provider silent so receipt and history checks cover the interval
// before any assistant text exists, using the locked runtime and real bridge.
func TestNativeConversationSendBeforeReply(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_RUNTIME") != "1" {
		t.Skip("explicit locked native runtime required")
	}
	release := make(chan struct{})
	releaseReply := sync.OnceFunc(func() { close(release) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"chat\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"fixture reply\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"chat\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	root := t.TempDir()
	if evidence := os.Getenv("ASTERHAND_CHAT_EVIDENCE"); evidence != "" {
		var err error
		root, err = os.MkdirTemp(evidence, "send-")
		if err != nil {
			t.Fatal(err)
		}
	}
	process := startNativeChatFixture(t, root, os.Getenv("ASTERHAND_CHAT_NODE"), os.Getenv("ASTERHAND_CHAT_ENTRY"), server.URL, true)
	defer process.close()
	defer releaseReply()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	binding := conversation.Binding{Runtime: "openclaw", Generation: strings.Repeat("a", 32), Key: model.ID()}
	if err := process.call(ctx, chatCommand{Action: "create", Binding: binding}, &binding); err != nil {
		t.Fatal(err)
	}
	run, started := model.ID(), time.Now().UnixMilli()
	var receipt conversation.Receipt
	if err := process.call(ctx, chatCommand{Action: "submit", Binding: binding, Run: run, Text: "fixture send before reply"}, &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.Accepted {
		t.Fatal("successful native send did not establish durable receipt")
	}
	for i := 0; i < 4; i++ {
		var observed conversation.Observation
		if err := process.call(ctx, chatCommand{Action: "observe", Binding: binding, Run: run, Started: started}, &observed); err != nil {
			t.Fatal(err)
		}
		var page conversation.Page
		if err := process.call(ctx, chatCommand{Action: "history", Binding: binding}, &page); err != nil {
			t.Fatal(err)
		}
		if !observed.Accepted || observed.Stopped || observed.Run != "running" || observed.Live != nil {
			t.Fatalf("unexpected state before provider output: accepted=%v stopped=%v run=%s live=%v", observed.Accepted, observed.Stopped, observed.Run, observed.Live != nil)
		}
		for _, message := range page.Messages {
			if message.Role != "user" || message.Turn != run {
				t.Fatal("history exposed an unexpected row before provider output")
			}
		}
		t.Logf("before reply: receipt accepted=%v observation accepted=%v run=%s visible_rows=%d", receipt.Accepted, observed.Accepted, observed.Run, len(page.Messages))
		time.Sleep(100 * time.Millisecond)
	}
	releaseReply()
	completed := waitNativeTurn(t, ctx, process, binding, run, started)
	if !completed.Accepted || !completed.Saved || completed.Run != "completed" {
		t.Fatal("confirmed input did not complete durably")
	}
}
