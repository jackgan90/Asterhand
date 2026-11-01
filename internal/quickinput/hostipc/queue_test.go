package hostipc

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jackgan90/Asterhand/internal/conversation/wire"
)

func TestQueueRecoveryFrameFitsMaximumEscapedInput(t *testing.T) {
	id := strings.Repeat("a", 128)
	state := &wire.QueueState{Limit: 8}
	for i := 0; i < 16; i++ {
		item := wire.QueueItem{Operation: id, Conversation: id, Kind: "create", State: "queued"}
		if i < 8 {
			item.Kind = "send"
			item.Turn = &wire.Turn{Operation: id, Conversation: id, ID: id, Delivery: "queued", Run: "queued"}
		}
		state.Entries = append(state.Entries, item)
	}
	item := state.Entries[0]
	for range 8 {
		failed := item
		failed.State = "failed"
		state.Failures = append(state.Failures, failed)
	}
	item.Text = strings.Repeat("\x00", wire.MaxInputBytes)
	message := Message{Version: Version, Session: "session-123456789", Kind: ChatResult, ChatID: "request-123456789",
		ChatReply: &ChatReply{Response: wire.Response{Queue: state, QueueItem: &item}}}
	var frame bytes.Buffer
	if err := Write(&frame, message, false); err != nil {
		t.Fatal(err)
	}
	if frame.Len() > MaxFrameBytes+4 {
		t.Fatal("queue body recovery exceeded IPC budget")
	}
	decoded, err := Read(&frame, false)
	if err != nil || decoded.ChatReply.Response.QueueItem.Text != item.Text {
		t.Fatalf("queue recovery failed: %v", err)
	}
}
