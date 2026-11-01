package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/model"
)

func TestNativeConversationConnectionWithoutModelProbe(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_RUNTIME") != "1" {
		t.Skip("requires locked native chat runtime")
	}
	node, entry := os.Getenv("ASTERHAND_CHAT_NODE"), os.Getenv("ASTERHAND_CHAT_ENTRY")
	if node == "" || entry == "" {
		t.Fatal("locked runtime paths required")
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid API key","type":"authentication_error","code":"invalid_api_key"}}`))
	}))
	defer server.Close()
	process := startNativeChatFixture(t, t.TempDir(), node, entry, server.URL, true)
	defer process.close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	snapshot := func(action string) string {
		t.Helper()
		var value struct {
			State string `json:"state"`
		}
		if err := process.call(ctx, chatCommand{Action: action}, &value); err != nil {
			t.Fatal(err)
		}
		return value.State
	}
	for _, action := range []string{"connection", "connect", "connection"} {
		if got := snapshot(action); got != "connected" {
			t.Fatal("authenticated agent not connected", got)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("connection status called model", calls.Load())
	}
	binding := conversation.Binding{Runtime: "openclaw", Generation: strings.Repeat("a", 32), Key: model.ID()}
	if err := process.call(ctx, chatCommand{Action: "create", Binding: binding}, &binding); err != nil {
		t.Fatal(err)
	}
	request, started := model.ID(), time.Now().UnixMilli()
	if err := process.call(ctx, chatCommand{Action: "submit", Binding: binding, Run: request, Text: "connection failure fixture"}, nil); err != nil {
		t.Fatal(err)
	}
	observed := waitNativeTurn(t, ctx, process, binding, request, started)
	if observed.Run != "failed" || calls.Load() == 0 {
		t.Fatal("provider rejection not observed", observed.Run, calls.Load())
	}
	count := calls.Load()
	for _, action := range []string{"connection", "connect", "connection"} {
		if got := snapshot(action); got != "unavailable" {
			t.Fatal("provider rejection hidden by local connection", got)
		}
	}
	if calls.Load() != count {
		t.Fatal("status retried rejected model", calls.Load(), count)
	}
}
