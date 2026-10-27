package quickinput

import (
	"context"
	"testing"

	"github.com/jackgan90/Asterhand/internal/conversation/wire"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

type delayedChatClient struct{ entered, release chan struct{} }

func (f *delayedChatClient) Call(_ context.Context, request wire.Request) (wire.Response, error) {
	close(f.entered)
	<-f.release
	return wire.Response{Turn: &wire.Turn{Operation: request.Operation, Conversation: request.Conversation, Delivery: "accepted", Run: "running"}}, nil
}

func TestChatReceiptSurvivesHostReplacementAndPreservesNewEdits(t *testing.T) {
	client := &delayedChatClient{make(chan struct{}), make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	c := &chatController{client: client, ctx: ctx, cancel: cancel, pending: map[string]*chatSubmission{}}
	defer c.close()
	draft := &Draft{text: "original", revision: 5}
	old := &remoteSurface{session: model.ID(), draft: draft, out: make(chan hostipc.Message, 4), done: make(chan struct{})}
	request := wire.Request{Action: "submit", Operation: model.ID(), Conversation: model.ID(), Text: "original", DraftRevision: 9}
	c.dispatch(old, hostipc.Message{ChatID: model.ID(), Chat: &request, Sequence: 9, Revision: 5})
	<-client.entered
	if pending := c.latestPending(); pending == nil || pending.identity.Operation != request.Operation {
		t.Fatal("operation was not retained before first response")
	}
	close(old.done)
	close(client.release)
	c.wg.Wait()
	replacement := &remoteSurface{session: model.ID(), draft: draft}
	reply := c.decorate(replacement, wire.Request{Action: "hello"}, wire.Response{}, "hello")
	if reply.ClearSequence == nil || *reply.ClearSequence != 5 || reply.Pending.Operation != request.Operation {
		t.Fatalf("accepted snapshot not recovered: %+v", reply)
	}
	if err := draft.Update("new edit", 6); err != nil {
		t.Fatal(err)
	}
	reply = c.decorate(replacement, wire.Request{Action: "hello"}, wire.Response{}, "hello")
	if reply.ClearSequence != nil {
		t.Fatal("late receipt cleared newer draft")
	}
	// Returning to identical text after another edit is still a new revision.
	if err := draft.Update("original", 7); err != nil {
		t.Fatal(err)
	}
	reply = c.decorate(replacement, wire.Request{Action: "hello"}, wire.Response{}, "hello")
	if reply.ClearSequence != nil {
		t.Fatal("digest alone cleared a newer revision")
	}
}

func TestChatOutOfOrderHistoryAndCoalescedDraft(t *testing.T) {
	c := &chatController{pending: map[string]*chatSubmission{}, selectionRequest: "new-selection", selected: "new"}
	surface := &remoteSurface{session: "replacement", draft: &Draft{text: "snapshot", revision: 3}}
	c.decorate(surface, wire.Request{Action: "history"}, wire.Response{Conversation: &wire.Conversation{ID: "old"}}, "old-selection")
	if c.selected != "new" {
		t.Fatal("old response replaced current selection")
	}
	op := model.ID()
	c.pending[op] = &chatSubmission{identity: hostipc.PendingSubmission{Operation: op}, digest: model.Digest([]byte("snapshot")), host: "old", sequence: 2, parentRevision: 0, accepted: true}
	result := c.decorate(surface, wire.Request{Action: "hello"}, wire.Response{}, "hello")
	if result.ClearSequence != nil {
		t.Fatal("coalesced edit incorrectly cleared recovered draft")
	}
	c.edited("replacement", 4, 4, "new")
	if len(c.pending) != 0 {
		t.Fatal("settled stale intent was retained")
	}
}
