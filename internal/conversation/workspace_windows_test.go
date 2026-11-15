//go:build windows

package conversation

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/store"
	"github.com/jackgan90/Asterhand/internal/workspace"
)

type workspaceFixtureRuntime struct {
	*fixtureRuntime
	files *workspace.Service
}

func (w *workspaceFixtureRuntime) Workspace() (*workspace.Service, error) {
	return w.files, nil
}

func (w *workspaceFixtureRuntime) SetWorkspacePolicy(_ context.Context, enabled bool, expected string) (workspace.Status, error) {
	if err := w.files.Configure(enabled, expected); err != nil {
		return workspace.Status{}, err
	}
	return w.files.Status(), nil
}

func TestActivitiesProjectionIncludesSummary(t *testing.T) {
	root := t.TempDir()
	files, err := workspace.Open(filepath.Join(root, "files"), filepath.Join(root, "journal"), "conv-ws-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = files.RuntimeStopped() })
	if err := files.Configure(true, "policy-act"); err != nil {
		t.Fatal(err)
	}

	records, err := store.Open(filepath.Join(root, "records.db"))
	if err != nil {
		t.Fatal(err)
	}
	runtime := &workspaceFixtureRuntime{fixtureRuntime: &fixtureRuntime{}, files: files}
	service := New(model.ID(), records, runtime)
	t.Cleanup(func() { service.Close(); _ = records.Close() })

	convID := createFixture(t, service)
	submitReq := Request{Action: "submit", Target: service.Target(), Conversation: convID, Operation: model.ID(), Text: "inspect docs"}
	if _, err := service.Call(context.Background(), submitReq); err != nil {
		t.Fatal(err)
	}
	var stored turnRecord
	if err := records.Get("chat-turn", submitReq.Operation, &stored); err != nil {
		t.Fatal(err)
	}
	scope := workspace.Scope{Conversation: convID, Turn: stored.RuntimeRequest, Policy: "policy-act"}
	if err := files.AcquireTurnScope(scope); err != nil {
		t.Fatal(err)
	}
	if _, err := files.Begin(workspace.Begin{
		ID:          "act-call-1",
		Scope:       scope,
		Tool:        "read_file",
		Path:        "docs/readme.md",
		Summary:     "docs/readme.md:1-20",
		Fingerprint: "fp-act-1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := files.End("act-call-1", "completed", ""); err != nil {
		t.Fatal(err)
	}

	resp, err := service.Call(context.Background(), Request{
		Action:       "activities",
		Target:       service.Target(),
		Conversation: convID,
		Operation:    submitReq.Operation,
	})
	if err != nil || resp.Activities == nil || len(resp.Activities.Items) != 1 {
		t.Fatalf("activities response = %+v %v", resp, err)
	}
	item := resp.Activities.Items[0]
	if item.ID != "act-call-1" || item.Tool != "read_file" || item.State != "completed" || item.Summary != "docs/readme.md:1-20" || item.Sequence == 0 {
		t.Fatalf("unexpected activity item: %+v", item)
	}
}
