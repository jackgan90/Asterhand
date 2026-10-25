package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
)

func TestRemoteQuestionAnswerOutboxDeliversExactlyOnce(t *testing.T) {
	a := newNode(t, &fakeExecutor{})
	b := newNode(t, &fakeExecutor{ask: true, filename: "answer.txt"})
	pairNodes(t, a, b)
	route, err := a.app.Engine.Accept(a.cfg.Get().NodeID, model.Request{RequestID: model.ID(), ProjectID: "workspace", Goal: "ask B then write", Mode: "route", Target: b.cfg.Get().NodeID, StartBefore: time.Now().Add(time.Hour).Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	var out model.Outbox
	await(t, "remote question visible", func() bool {
		return a.app.Store.Get("outbox", route.ID, &out) == nil && out.Job != nil && out.Job.State == "waiting_input"
	})
	if _, err = os.Stat(filepath.Join(b.root, "answer.txt")); !os.IsNotExist(err) {
		t.Fatal("file appeared before answer")
	}
	base := "http://" + a.app.Address()
	credentials, _ := json.Marshal(map[string]string{"token": a.app.loginToken})
	request, _ := http.NewRequest("POST", base+"/api/login", bytes.NewReader(credentials))
	request.Header.Set("Origin", base)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var login map[string]string
	json.NewDecoder(response.Body).Decode(&login)
	response.Body.Close()
	cookie := response.Cookies()[0]
	i := out.Job.Interaction
	body, _ := json.Marshal(map[string]string{"interaction_id": i.ID, "attempt": i.Attempt, "answer_id": "answer-retry-123", "answer": "hello world"})
	for n := 0; n < 2; n++ {
		request, _ = http.NewRequest("POST", base+"/api/remote/"+route.ID+"/answer", bytes.NewReader(body))
		request.AddCookie(cookie)
		request.Header.Set("Origin", base)
		request.Header.Set("X-Asterhand-CSRF", login["csrf"])
		response, err = http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 200 && response.StatusCode != 202 {
			t.Fatalf("answer failed: %d", response.StatusCode)
		}
	}
	await(t, "B created file after answer", func() bool { return a.app.Store.Get("outbox", route.ID, &out) == nil && out.Job.State == "succeeded" })
	content, err := os.ReadFile(filepath.Join(b.root, "answer.txt"))
	if err != nil || string(content) != "hello world" {
		t.Fatal("incorrect answer file", err)
	}
	var answers []pendingAnswer
	if err = a.app.Store.List("answer-outbox", &answers); err != nil || len(answers) != 1 || answers[0].State != "delivered" {
		t.Fatal("answer not durably deduplicated", err)
	}
}
