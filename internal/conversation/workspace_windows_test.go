//go:build windows

package conversation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
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

func openLongTurnWorkspaceFixture(t *testing.T) (*Service, *workspace.Service, string, Request, workspace.Scope) {
	t.Helper()
	root := t.TempDir()
	wsDir := filepath.Join(root, "files")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := workspace.Open(wsDir, filepath.Join(root, "journal"), "conv-ws-long")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = files.RuntimeStopped() })
	if err := files.Configure(true, "policy-long"); err != nil {
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
	submitReq := Request{Action: "submit", Target: service.Target(), Conversation: convID, Operation: model.ID(), Text: "long turn"}
	if _, err := service.Call(context.Background(), submitReq); err != nil {
		t.Fatal(err)
	}
	var stored turnRecord
	if err := records.Get("chat-turn", submitReq.Operation, &stored); err != nil {
		t.Fatal(err)
	}
	scope := workspace.Scope{Conversation: convID, Turn: stored.RuntimeRequest, Policy: "policy-long"}
	if err := files.AcquireTurnScope(scope); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for {
			if err := files.ReleaseTurn(scope.Turn, true); err == nil {
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
	})
	return service, files, convID, submitReq, scope
}

func TestLongTurnPendingApprovalVisibleAndDeniedProjection(t *testing.T) {
	service, files, convID, submitReq, scope := openLongTurnWorkspaceFixture(t)

	for i := 0; i < 22; i++ {
		cid := fmt.Sprintf("act-done-%02d", i)
		if _, err := files.Begin(workspace.Begin{
			ID: cid, Scope: scope, Tool: "read_file", Path: "notes.txt", Summary: "notes.txt", Fingerprint: "fp-" + cid,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := files.End(cid, "completed", ""); err != nil {
			t.Fatal(err)
		}
	}

	extDir := t.TempDir()
	if _, err := files.Begin(workspace.Begin{
		ID: "act-approve-23", Scope: scope, Tool: "exec_command", Summary: "check ext", Fingerprint: "fp-23",
	}); err != nil {
		t.Fatal(err)
	}
	startRes, err := files.StartExec(context.Background(), workspace.ExecStartRequest{
		CallID: "act-approve-23", Command: `Get-ChildItem "` + extDir + `"`, Escalated: true, EscalationReason: "Inspect folder", ExternalPaths: []string{extDir},
	})
	if err != nil || startRes.Status != "approval_required" {
		t.Fatalf("StartExec(23): res=%+v err=%v", startRes, err)
	}

	// Reconcile at offset=0 must still surface the 23rd pending approval item.
	recResp, err := service.Call(context.Background(), Request{
		Action: "reconcile", Target: service.Target(), Conversation: convID, Operation: submitReq.Operation,
	})
	if err != nil || recResp.Activities == nil {
		t.Fatalf("reconcile failed: resp=%+v err=%v", recResp, err)
	}
	foundPending := false
	for _, it := range recResp.Activities.Items {
		if it.ID == "act-approve-23" && it.State == "awaiting_approval" && it.Approval != nil && it.Approval.Status == "pending" {
			foundPending = true
		}
	}
	if !foundPending {
		t.Fatalf("expected 23rd awaiting_approval item in reconcile page, got %+v", recResp.Activities.Items)
	}

	// Denying via approve_tool returns updated 23rd item and collapses into state="denied".
	appResp, err := service.Call(context.Background(), Request{
		Action: "approve_tool", Target: service.Target(), Conversation: convID, Operation: submitReq.Operation,
		ApprovalID: startRes.ApprovalID, Decision: "deny",
	})
	if err != nil || appResp.Activities == nil {
		t.Fatalf("approve_tool(deny) failed: resp=%+v err=%v", appResp, err)
	}
	if _, err := files.End("act-approve-23", "completed", "denied_by_user"); err != nil {
		t.Fatal(err)
	}

	recAfter, err := service.Call(context.Background(), Request{
		Action: "reconcile", Target: service.Target(), Conversation: convID, Operation: submitReq.Operation, Offset: int(recResp.Activities.Next),
	})
	if err != nil || recAfter.Activities == nil {
		t.Fatalf("reconcile with offset failed: resp=%+v err=%v", recAfter, err)
	}
	var deniedFound bool
	for _, it := range recAfter.Activities.Items {
		if it.ID == "act-approve-23" {
			if it.State != "denied" || it.Detail != "denied_by_user" || it.ApprovalMode != "denied" {
				t.Fatalf("expected denied projection, got %+v", it)
			}
			deniedFound = true
		}
	}
	if !deniedFound {
		t.Fatalf("expected denied 23rd item via incremental offset, got %+v", recAfter.Activities.Items)
	}

	// Verify shared projection maps approval_timeout and turbo_auto consistently.
	timeoutProj := ProjectToolActivity(workspace.Operation{
		ID: "op-to", Tool: "exec_command", State: "completed", Detail: "approval_timeout", ApprovalMode: "timeout",
		Approval: &workspace.ApprovalView{ID: "ap_to", Status: "expired", Kind: "folder", Reason: "r"},
	})
	if timeoutProj.State != "failed" || timeoutProj.Detail != "approval_timeout" {
		t.Fatalf("unexpected timeout projection: %+v", timeoutProj)
	}
	files.ConfigureTrust(config.ExecutionModeTurbo, "rev-turbo", nil)
	turboProj := ProjectToolActivity(workspace.Operation{
		ID: "op-tb", Tool: "exec_command", State: "completed", ApprovalMode: "turbo_auto",
	})
	if turboProj.State != "completed" || turboProj.ApprovalMode != "turbo_auto" {
		t.Fatalf("unexpected turbo projection: %+v", turboProj)
	}
}
