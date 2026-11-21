package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/operations"
)

func TestAutomationRemoteTaskAnswerUsesOriginalOutboxAndActor(t *testing.T) {
	a := newNode(t, &fakeExecutor{})
	b := newNode(t, &fakeExecutor{ask: true, filename: "delegated.txt"})
	pairNodes(t, a, b)
	p, token := grantTestAutomation(t, a.app, []string{"read", "execute"}, map[string][]string{"projects": {b.cfg.Get().NodeID + ":workspace"}})
	id := operations.NewID(time.Now())
	input := automation.TaskSubmit{Target: b.cfg.Get().NodeID, Project: "workspace", Goal: "ask then write"}
	reply := testAutomationCall(a.app, token, "tasks_submit", id, input)
	if reply.Error != nil {
		t.Fatal(reply)
	}
	var ref struct {
		ID string `json:"task_id"`
	}
	_ = json.Unmarshal(reply.Receipt.Result, &ref)
	var out model.Outbox
	await(t, "delegated remote question", func() bool {
		return a.app.Store.Get("outbox", ref.ID, &out) == nil && out.Job != nil && out.Job.State == "waiting_input"
	})
	question := out.Job.Interaction
	target := automation.TaskRef{Kind: "outbox", ID: out.ID, Target: b.cfg.Get().NodeID, Project: "workspace"}
	answer := automation.TaskAnswer{Task: target, Attempt: question.Attempt, Interaction: question.ID, Answer: "agent answer"}
	answerID := operations.NewID(time.Now())
	for range 2 {
		reply = testAutomationCall(a.app, token, "tasks_answer", answerID, answer)
		if reply.Error != nil {
			t.Fatal(reply)
		}
	}
	await(t, "delegated answer delivered", func() bool { return a.app.Store.Get("outbox", ref.ID, &out) == nil && out.Job.State == "succeeded" })
	content, err := os.ReadFile(filepath.Join(b.root, "delegated.txt"))
	if err != nil || string(content) != "agent answer" {
		t.Fatal(string(content), err)
	}
	var answers []pendingAnswer
	if err = a.app.Store.List("answer-outbox", &answers); err != nil || len(answers) != 1 || answers[0].Actor != "automation:"+p.ID {
		t.Fatal(answers, err)
	}
	reply = testAutomationCall(a.app, token, "tasks_events", "", automation.TaskEvents{Task: target})
	if reply.Error != nil {
		t.Fatal(reply)
	}
	var page struct {
		Event struct {
			Seq string `json:"seq"`
		} `json:"event"`
	}
	if err = json.Unmarshal(compactResult(reply.Data), &page); err != nil || page.Event.Seq == "" {
		t.Fatal(reply, err)
	}
	target.Target = a.cfg.Get().NodeID
	if reply = testAutomationCall(a.app, token, "tasks_get", "", automation.TaskGet{Task: target}); reply.Error == nil {
		t.Fatal("target scope substituted")
	}
}
