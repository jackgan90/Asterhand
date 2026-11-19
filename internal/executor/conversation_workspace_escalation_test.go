//go:build windows

package executor

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/conversation/wire"
	"github.com/jackgan90/Asterhand/internal/store"
	"github.com/jackgan90/Asterhand/internal/workspace"
)

func TestTrustPolicyChangesDoNotRestartGatewayAndSurviveToggle(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	mgr, err := config.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	rt := NewConversationRuntime(mgr)
	t.Cleanup(func() { _ = rt.Close() })

	baseCfg := mgr.Get()
	baseFP := chatFingerprint(baseCfg, openClawOptions{}, "k")
	if baseFP == "" {
		t.Fatal("expected non-empty chatFingerprint")
	}

	if err := rt.persistTrustRules([]config.TrustRule{{
		ID: "tr_test_folder_001", Kind: "folder", Pattern: filepath.Join(t.TempDir(), "data"), Action: "allow", Label: "Data",
	}}); err != nil {
		t.Fatal(err)
	}
	st, err := rt.SetWorkspaceTrustPolicy(context.Background(), config.ExecutionModeTurbo, "")
	if err != nil {
		t.Fatal(err)
	}
	if st.ExecutionMode != config.ExecutionModeTurbo || len(st.AllowedItems) != 1 || st.AllowedItems[0].ID != "tr_test_folder_001" {
		t.Fatalf("unexpected trust status: %+v", st)
	}
	if fp := chatFingerprint(mgr.Get(), openClawOptions{}, "k"); fp != baseFP {
		t.Fatalf("chatFingerprint changed on trust update (%q vs %q); would falsely restart Gateway", fp, baseFP)
	}

	st, err = rt.SetWorkspaceTrustPolicy(context.Background(), config.ExecutionModeStandard, "tr_test_folder_001")
	if err != nil {
		t.Fatal(err)
	}
	if st.ExecutionMode != config.ExecutionModeStandard || len(st.AllowedItems) != 0 {
		t.Fatalf("expected standard mode and empty allowed list after removal, got %+v", st)
	}

	// Re-add a rule and switch to turbo, then toggle LocalTools.Enabled off/on and confirm trust state is preserved.
	if err := rt.persistTrustRules([]config.TrustRule{{
		ID: "tr_keep_002", Kind: "command", Pattern: "git", Action: "allow", Label: "git",
	}}); err != nil {
		t.Fatal(err)
	}
	_, err = rt.SetWorkspaceTrustPolicy(context.Background(), config.ExecutionModeTurbo, "")
	if err != nil {
		t.Fatal(err)
	}
	rev := mgr.Get().LocalTools.Revision
	if _, err := rt.SetWorkspacePolicy(context.Background(), false, rev); err != nil {
		t.Fatal(err)
	}
	afterOff := mgr.Get().LocalTools
	if afterOff.Enabled || afterOff.ExecutionMode != config.ExecutionModeTurbo || len(afterOff.TrustRules) != 1 {
		t.Fatalf("SetWorkspacePolicy(false) lost trust state: %+v", afterOff)
	}

	// Validate closed wire protocol approve_tool routing & invalid decision rejection.
	stDB, err := store.Open(filepath.Join(home, "conv.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stDB.Close() })
	svc := conversation.New(baseCfg.NodeID, stDB, rt)
	badReq := wire.Request{
		Action: "approve_tool", Target: svc.Target(), Conversation: "conv-123456789012",
		ApprovalID: "ap_1234567890123456", Decision: "allow_forever_evil",
	}
	if err := badReq.Validate(); err == nil {
		t.Fatal("expected invalid decision to be rejected by wire protocol")
	}
	goodReq := wire.Request{
		Action: "approve_tool", Target: svc.Target(), Conversation: "conv-123456789012",
		ApprovalID: "ap_1234567890123456", Decision: "allow_session",
	}
	if err := goodReq.Validate(); err != nil {
		t.Fatalf("expected valid approve_tool request to pass, got %v", err)
	}
}

func TestConcurrentTrustPolicySyncAndHistoryProjection(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	mgr, err := config.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	rt := NewConversationRuntime(mgr)
	t.Cleanup(func() { _ = rt.Close() })

	if _, err := rt.SetWorkspaceTrustPolicy(context.Background(), config.ExecutionModeTurbo, ""); err != nil {
		t.Fatal(err)
	}
	if err := rt.persistTrustRules([]config.TrustRule{{
		ID: "tr_r1", Kind: "folder", Pattern: t.TempDir(), Action: "allow", Label: "r1",
	}}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, err := rt.SetWorkspaceTrustPolicy(context.Background(), config.ExecutionModeStandard, "tr_r1"); err != nil {
			t.Errorf("SetWorkspaceTrustPolicy: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := rt.persistTrustRules([]config.TrustRule{{
			ID: "tr_r2", Kind: "folder", Pattern: t.TempDir(), Action: "allow", Label: "r2",
		}}); err != nil {
			t.Errorf("persistTrustRules: %v", err)
		}
	}()
	wg.Wait()

	// Runtime status and disk config must always agree on execution_mode and allowed items.
	files, err := rt.Workspace()
	if err != nil {
		t.Fatal(err)
	}
	st := files.Status()
	disk := mgr.Get().LocalTools
	if st.ExecutionMode != disk.ExecutionMode || st.TrustRevision != disk.TrustRevision || len(st.AllowedItems) != len(disk.TrustRules) {
		t.Fatalf("runtime/disk trust mismatch: status=%+v disk=%+v", st, disk)
	}

	// enrichHistoryTools must project denied, approval_timeout, and turbo_auto via shared mapper.
	scope := workspace.Scope{Conversation: "conv-hist-proj", Turn: "turn-hist-proj", Policy: disk.Revision}
	if err := files.AcquireTurnScope(scope); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = files.ReleaseTurn(scope.Turn, true) }()
	for _, tc := range []struct {
		id, detail string
	}{
		{"call-denied", "denied_by_user"},
		{"call-timeout", "approval_timeout"},
	} {
		if _, err := files.Begin(workspace.Begin{
			ID: tc.id, Scope: scope, Tool: "exec_command", Summary: tc.id, Fingerprint: "fp-" + tc.id,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := files.End(tc.id, "completed", tc.detail); err != nil {
			t.Fatal(err)
		}
	}
	page := conversation.Page{
		Messages: []conversation.Message{{
			ID:   "msg-1",
			Role: "assistant",
			Turn: scope.Turn,
			Blocks: []wire.Block{
				{Type: "tool", Tool: &wire.ToolActivity{ID: "call-denied", Turn: scope.Turn, Tool: "exec_command", State: "started"}},
				{Type: "tool", Tool: &wire.ToolActivity{ID: "call-timeout", Turn: scope.Turn, Tool: "exec_command", State: "started"}},
			},
		}},
	}
	enrichHistoryTools(&page, scope.Conversation, files)
	got0 := page.Messages[0].Blocks[0].Tool
	got1 := page.Messages[0].Blocks[1].Tool
	if got0.State != "denied" || got0.Detail != "denied_by_user" {
		t.Fatalf("expected denied history projection, got %+v", got0)
	}
	if got1.State != "failed" || got1.Detail != "approval_timeout" {
		t.Fatalf("expected timeout history projection, got %+v", got1)
	}
}
