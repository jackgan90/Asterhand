package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/drafts"
	"github.com/jackgan90/Asterhand/internal/editors"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/operations"
)

func TestEditorSaveReceiptSurvivesLostWindowReply(t *testing.T) {
	a := newNode(t, &fakeExecutor{}).app
	p, token := grantTestAutomation(t, a, []string{"read", "edit"}, map[string][]string{"node": {"*"}})
	const page = "reviewer-settings-page-00001"
	ctx, closeWindow := context.WithCancel(context.Background())
	defer closeWindow()
	line := a.Editors.Commands(ctx, "web", page)
	cfg := a.Config.Get()
	target := drafts.Target{Kind: "settings", Settings: "node", Node: cfg.NodeID, Baseline: nodeSettingsRevision(cfg)}
	done := make(chan string, 1)
	go func() {
		for q := range line {
			switch q.Action {
			case "get":
				reply := liveSnapshot{ID: "editor", Kind: "settings", Target: target, Revision: "r", Content: compactResult(map[string]any{"name": "actual saved name", "model": cfg.LLM.Model})}
				_ = a.Editors.Respond("web", editors.Poll{Page: page, Reply: &editors.Reply{ID: q.ID, Data: compactResult(reply)}})
			case "commit":
				// Invoke the same ordinary-only business call as web/node-settings.mjs.
				body := compactResult(map[string]any{"operation": "settings.patch", "operation_id": q.Operation, "arguments": map[string]string{"settings": "node", "expected_revision": target.Baseline, "name": "actual saved name"}})
				w := httptest.NewRecorder()
				a.automationHumanCall(w, httptest.NewRequest("POST", "/api/automation/human-call", bytes.NewReader(body)))
				var result automation.Response
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Error != nil || w.Code != 200 {
					t.Error(w.Code, w.Body.String())
				}
				closeWindow()
				done <- q.Operation // lost window -> node acknowledgement, after durable domain success
			}
		}
	}()
	id := operations.NewID(time.Now())
	draftID := page + ":editor"
	out := testAutomationCall(a, token, "drafts.commit", id, liveWrite{ID: draftID, Revision: "r"})
	if out.Receipt == nil || out.Receipt.State != "unknown" {
		t.Fatal(out)
	}
	childID := <-done
	if a.Config.Get().Name != "actual saved name" {
		t.Fatal("domain did not publish")
	}
	inner, e := a.Store.Lookup("human:web", cfg.NodeID, childID)
	if e != nil || inner.State != "completed" {
		t.Fatal(inner, e)
	}
	original, e := a.Store.Lookup(p.ID, cfg.NodeID, id)
	if e != nil {
		t.Fatal(e)
	}
	recovered, e := a.reconcileAutomationOperation(original)
	if e != nil || recovered.State != "completed" {
		t.Fatal(recovered, e)
	}
	viaQuery := testAutomationCall(a, token, "operations.get", "", map[string]string{"operation_id": id})
	b := compactResult(viaQuery.Data)
	if viaQuery.Error != nil || !bytes.Contains(b, []byte(`"state":"completed"`)) {
		t.Fatal(viaQuery)
	}
	if bytes.Contains(b, []byte("actual saved name")) {
		t.Fatal("editor receipt copied domain content")
	}
}

type rejectEditorChat struct{ automationChatRuntime }

func (r *rejectEditorChat) Submit(_ context.Context, _ conversation.Binding, _ conversation.Submission) (conversation.Receipt, error) {
	r.sends.Add(1)
	return conversation.Receipt{}, conversation.Fail("rejected", "controlled refusal before admission")
}

func TestEditorReceiptRecoversDefiniteChatRejection(t *testing.T) {
	a := newNode(t, &fakeExecutor{}).app
	a.Chat.Close()
	runtime := &rejectEditorChat{}
	a.Chat = conversation.New(a.Config.Get().NodeID, a.Store, runtime)
	a.Chat.Start()
	c, err := a.Chat.Call(context.Background(), conversation.Request{Action: "create", Target: a.Chat.Target(), Operation: model.ID()})
	if err != nil {
		t.Fatal(err)
	}
	p, token := grantTestAutomation(t, a, []string{"read", "edit", "execute"}, map[string][]string{"conversations": {"*"}})
	id := operations.NewID(time.Now())
	identity := operations.Identity{Actor: p.ID, Node: a.Config.Get().NodeID, ID: id, Name: "drafts.commit", Digest: "fixture", Target: "native:foo"}
	if _, _, err := a.Store.Begin(identity, time.Now()); err != nil {
		t.Fatal(err)
	}
	ref, err := a.prepareEditorCommit(identity, liveSnapshot{ID: "native:foo", Kind: "chat", Revision: "r"})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := a.Store.Lookup(identity.Actor, identity.Node, identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	pending.State = "unknown"
	if err = a.Store.Finish(pending, time.Now()); err != nil {
		t.Fatal(err)
	}
	unconfirmed, err := a.reconcileAutomationOperation(pending)
	if err != nil || unconfirmed.State != "unknown" || runtime.sends.Load() != 0 {
		t.Fatal("no delivery evidence", unconfirmed, err)
	}
	_, err = a.Chat.Call(context.Background(), conversation.Request{Action: "submit", Target: a.Chat.Target(), Conversation: c.Conversation.ID, Operation: ref.ID, Text: "ordinary draft"})
	if err == nil {
		t.Fatal("expected the negative domain receipt")
	}
	_, turn, err := a.Chat.OperationStatus(ref.ID)
	if err != nil || turn == nil || turn.Delivery != "rejected" {
		t.Fatal(turn, err)
	}
	lost, err := a.Store.Lookup(identity.Actor, identity.Node, identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	lost.State = "unknown"
	if err := a.Store.Finish(lost, time.Now()); err != nil {
		t.Fatal(err)
	}
	query := testAutomationCall(a, token, "operations.get", "", map[string]string{"operation_id": id})
	if query.Error != nil {
		t.Fatal(query.Error)
	}
	current, err := a.Store.Lookup(identity.Actor, identity.Node, id)
	if err != nil || current.State != "rejected" || current.Accepted || current.Code != "rejected" || runtime.sends.Load() != 1 {
		t.Fatal(current, err, runtime.sends.Load())
	}
}
