//go:build windows

package workspace

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
)

func openEscalationTestService(t *testing.T) (*Service, string, string, Scope) {
	t.Helper()
	home := t.TempDir()
	ws := filepath.Join(home, "conversations", "rt-1", "workspace")
	ver := filepath.Join(home, "conversations", "rt-1", "file-versions")
	s, err := Open(ws, ver, "policy-esc")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Configure(true, "policy-esc"); err != nil {
		t.Fatal(err)
	}
	s.SetHomeRoot(home)
	t.Cleanup(func() { _ = s.RuntimeStopped() })
	scope := Scope{Conversation: "conv-esc-main", Turn: "turn-esc-0001", Policy: "policy-esc"}
	if err := s.AcquireTurnScope(scope); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { waitForReleaseTurnTrue(t, s, scope.Turn) })
	return s, home, ws, scope
}

func runApprovedFlow(t *testing.T, s *Service, scope Scope, callID, decision string, req ExecStartRequest) ExecResult {
	t.Helper()
	testBeginTurnCall(t, s, scope, callID, "b-"+callID, 0, "exec_command", nil, "script", 0)
	defer func() { _, _ = s.End(callID, "completed", "") }()

	res, err := s.StartExec(context.Background(), req)
	if err != nil {
		t.Fatalf("StartExec(%s): %v", callID, err)
	}
	if res.Status != "approval_required" || res.ApprovalID == "" {
		t.Fatalf("expected approval_required, got %+v", res)
	}
	op, err := s.Operation(callID)
	if err != nil || op.Phase != PhaseAwaitingApproval || op.Approval == nil || op.Approval.Status != "pending" {
		t.Fatalf("expected awaiting_approval operation view, got %+v err=%v", op, err)
	}
	go func() {
		time.Sleep(60 * time.Millisecond)
		if appErr := s.ApproveTool(scope.Conversation, res.ApprovalID, decision); appErr != nil {
			t.Errorf("ApproveTool(%s): %v", decision, appErr)
		}
	}()
	waitMs := 2500
	finalRes, err := s.WaitApproval(context.Background(), ExecApprovalWaitRequest{
		CallID: callID, ApprovalID: res.ApprovalID, WaitMs: &waitMs,
	})
	if err != nil {
		t.Fatalf("WaitApproval(%s): %v", callID, err)
	}
	return finalRes
}

func TestExecEscalationApprovalChoicesAndSessionScope(t *testing.T) {
	s, _, _, scope := openEscalationTestService(t)
	extDir := t.TempDir()
	outFile := filepath.Join(extDir, "approved.txt")

	var persisted []config.TrustRule
	var notifyCount atomic.Int32
	s.SetTrustCallbacks(func(rules []config.TrustRule) error {
		persisted = append(persisted, rules...)
		return nil
	}, func() {
		notifyCount.Add(1)
	})

	req := ExecStartRequest{
		CallID:           "c-once",
		Command:          `Set-Content -LiteralPath "` + outFile + `" -Value "once-ok"`,
		Escalated:        true,
		EscalationReason: "Need to save generated CSV into your export folder",
		ExternalPaths:    []string{outFile},
	}
	resOnce := runApprovedFlow(t, s, scope, "c-once", "allow_once", req)
	if resOnce.Status != "completed" || len(persisted) != 0 || notifyCount.Load() != 1 {
		t.Fatalf("allow_once unexpected: res=%+v persisted=%d notify=%d", resOnce, len(persisted), notifyCount.Load())
	}

	// Second call to same folder must still ask after allow_once, then allow_session covers conversation.
	req2 := req
	req2.CallID = "c-sess-1"
	resSess := runApprovedFlow(t, s, scope, "c-sess-1", "allow_session", req2)
	if resSess.Status != "completed" || len(persisted) != 0 {
		t.Fatalf("allow_session unexpected: res=%+v persisted=%d", resSess, len(persisted))
	}

	testBeginTurnCall(t, s, scope, "c-sess-2", "b-sess-2", 0, "exec_command", nil, "script", 0)
	req3 := req
	req3.CallID = "c-sess-2"
	resAuto, err := s.StartExec(context.Background(), req3)
	if err != nil || resAuto.Status != "completed" {
		t.Fatalf("expected session auto-allow, got %+v err=%v", resAuto, err)
	}
	_, _ = s.End("c-sess-2", "completed", "")

	// Deny returns graceful non-blocking denied_by_user result.
	extDeny := t.TempDir()
	reqDeny := ExecStartRequest{
		CallID:           "c-deny",
		Command:          `Get-ChildItem "` + extDeny + `"`,
		Escalated:        true,
		EscalationReason: "List folder",
		ExternalPaths:    []string{extDeny},
	}
	resDeny := runApprovedFlow(t, s, scope, "c-deny", "deny", reqDeny)
	if resDeny.Status != "denied_by_user" || !resDeny.EscalationDenied {
		t.Fatalf("expected denied_by_user, got %+v", resDeny)
	}
	endOp, err := s.End("c-deny", "completed", "")
	if err != nil || endOp.Detail != "denied_by_user" || endOp.Approval == nil || endOp.Approval.Status != "denied" {
		t.Fatalf("unexpected ended deny op: %+v err=%v", endOp, err)
	}
	if s.Status().Reason == "reconciliation_required" {
		t.Fatal("denied_by_user must never trigger s.blocked")
	}

	// allow_always persists rule, appears in Status().AllowedItems, and ConfigureTrust pruning clears session rules.
	extAlways := t.TempDir()
	reqAlways := ExecStartRequest{
		CallID:           "c-always",
		Command:          `Get-ChildItem "` + extAlways + `"`,
		Escalated:        true,
		EscalationReason: "Check report folder",
		ExternalPaths:    []string{extAlways},
	}
	resAlways := runApprovedFlow(t, s, scope, "c-always", "allow_always", reqAlways)
	if resAlways.Status != "completed" || len(persisted) != 1 || persisted[0].Kind != "folder" {
		t.Fatalf("allow_always failed: res=%+v persisted=%+v", resAlways, persisted)
	}
	s.ConfigureTrust(config.ExecutionModeStandard, "rev-1", persisted)
	if st := s.Status(); len(st.AllowedItems) != 1 || st.ExecutionMode != config.ExecutionModeStandard {
		t.Fatalf("Status().AllowedItems unexpected: %+v", st)
	}
	s.ConfigureTrust(config.ExecutionModeStandard, "rev-2", nil)
	if st := s.Status(); len(st.AllowedItems) != 0 {
		t.Fatalf("expected AllowedItems empty after removal, got %+v", st.AllowedItems)
	}
}

func TestExecEscalationPolicyPrecedenceAndAntiBypass(t *testing.T) {
	s, home, _, scope := openEscalationTestService(t)
	extDir := t.TempDir()

	// Broad scope and elevation verbs are always denied before asking.
	broadCases := []struct {
		id  string
		req ExecStartRequest
	}{
		{"c-drive", ExecStartRequest{CallID: "c-drive", Command: `Get-ChildItem C:\`, Escalated: true, EscalationReason: "list root", ExternalPaths: []string{`C:\`}}},
		{"c-runas", ExecStartRequest{CallID: "c-runas", Command: `Start-Process cmd -Verb RunAs`, Escalated: true, EscalationReason: "admin", ExternalPaths: []string{extDir}}},
		{"c-home", ExecStartRequest{CallID: "c-home", Command: `Get-Content "` + filepath.Join(home, "config.json") + `"`, Escalated: true, EscalationReason: "read cfg"}},
	}
	for _, tc := range broadCases {
		testBeginTurnCall(t, s, scope, tc.id, "b-"+tc.id, 0, "exec_command", nil, "script", 0)
		_, err := s.StartExec(context.Background(), tc.req)
		if err == nil {
			t.Fatalf("%s expected hard deny error", tc.id)
		}
		_, _ = s.End(tc.id, "failed", Code(err))
	}

	// Missing escalation_reason rejects with escalation_reason_required.
	testBeginTurnCall(t, s, scope, "c-no-reason", "b-no-reason", 0, "exec_command", nil, "script", 0)
	if _, err := s.StartExec(context.Background(), ExecStartRequest{CallID: "c-no-reason", Command: `Get-ChildItem "` + extDir + `"`, Escalated: true}); Code(err) != "escalation_reason_required" {
		t.Fatalf("expected escalation_reason_required, got %v", err)
	}
	_, _ = s.End("c-no-reason", "failed", "escalation_reason_required")

	// User Deny beats Turbo and Allow; User Ask beats Allow and Turbo.
	s.ConfigureTrust(config.ExecutionModeTurbo, "rev-rules", []config.TrustRule{
		{ID: "r-allow", Kind: "folder", Pattern: extDir, Action: "allow"},
		{ID: "r-deny", Kind: "folder", Pattern: extDir, Action: "deny"},
	})
	testBeginTurnCall(t, s, scope, "c-pol-deny", "b-pol-deny", 0, "exec_command", nil, "script", 0)
	resDeny, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "c-pol-deny", Command: `Get-ChildItem "` + extDir + `"`, Escalated: true, EscalationReason: "read", ExternalPaths: []string{extDir},
	})
	if err != nil || resDeny.Status != "policy_denied" || !resDeny.EscalationDenied {
		t.Fatalf("expected policy_denied, got %+v err=%v", resDeny, err)
	}
	_, _ = s.End("c-pol-deny", "completed", "")

	s.ConfigureTrust(config.ExecutionModeTurbo, "rev-ask", []config.TrustRule{
		{ID: "r-allow", Kind: "folder", Pattern: extDir, Action: "allow"},
		{ID: "r-ask", Kind: "folder", Pattern: extDir, Action: "ask"},
	})
	testBeginTurnCall(t, s, scope, "c-pol-ask", "b-pol-ask", 0, "exec_command", nil, "script", 0)
	resAsk, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "c-pol-ask", Command: `Get-ChildItem "` + extDir + `"`, Escalated: true, EscalationReason: "read", ExternalPaths: []string{extDir},
	})
	if err != nil || resAsk.Status != "approval_required" {
		t.Fatalf("expected User Ask to beat Allow and Turbo, got %+v err=%v", resAsk, err)
	}
	_ = s.CancelCall("c-pol-ask", "cancelled")
	_, _ = s.End("c-pol-ask", "cancelled", "cancelled")

	// Generic bare interpreter inside workspace must set CanAlways=false.
	s.ConfigureTrust(config.ExecutionModeStandard, "rev-std", nil)
	testBeginTurnCall(t, s, scope, "c-bare-ps", "b-bare-ps", 0, "exec_command", nil, "script", 0)
	resShell, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "c-bare-ps", Command: `powershell -Command "Write-Output ok"`, Escalated: true, EscalationReason: "Run check",
	})
	if err != nil || resShell.Status != "approval_required" {
		t.Fatalf("expected approval_required for escalated powershell, got %+v err=%v", resShell, err)
	}
	op, _ := s.Operation("c-bare-ps")
	if op.Approval == nil || op.Approval.CanAlways || !op.Approval.CanSession {
		t.Fatalf("generic interpreter must have CanAlways=false CanSession=true, got %+v", op.Approval)
	}
	if err := s.ApproveTool(scope.Conversation, resShell.ApprovalID, "allow_always"); Code(err) != "broad_scope_denied" {
		t.Fatalf("expected broad_scope_denied on allow_always for generic shell, got %v", err)
	}
	_ = s.CancelCall("c-bare-ps", "cancelled")
	_, _ = s.End("c-bare-ps", "cancelled", "cancelled")
}

func TestExecEscalationWriteBarrierTOCTOUAndTurboEphemeral(t *testing.T) {
	s, _, ws, scope := openEscalationTestService(t)
	scriptPath := filepath.Join(ws, "job.ps1")
	if err := os.WriteFile(scriptPath, []byte("Write-Output 'v1'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	extDir := t.TempDir()

	// Start approval-pending call -> verify write_file is held by approval_pending barrier -> test TOCTOU tamper.
	testBeginTurnCall(t, s, scope, "c-toctou", "b-toctou", 0, "exec_command", nil, "script", 0)
	resWait, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "c-toctou", Command: `powershell -File "` + scriptPath + `"`, Escalated: true, EscalationReason: "Run job.ps1", ExternalPaths: []string{extDir},
	})
	if err != nil || resWait.Status != "approval_required" {
		t.Fatalf("expected approval_required, got %+v err=%v", resWait, err)
	}

	if _, err := s.Register(Register{
		ID: "w-barrier", Batch: "b-barrier", Order: 0, Scope: scope, Tool: "write_file",
		Targets: []Target{{Path: "other.txt", Expected: MissingRevision}}, NewBytes: 10, InputFingerprint: "fp-wb",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SealBatch(SealBatch{
		Batch: "b-barrier", Scope: scope, Items: []SealItem{{ID: "w-barrier", Tool: "write_file", Targets: []Target{{Path: "other.txt", Expected: MissingRevision}}, NewBytes: 10, Fingerprint: "fp-wb"}},
	}); err != nil {
		t.Fatal(err)
	}
	acqErrCh := make(chan error, 1)
	go func() {
		_, err := s.Acquire(context.Background(), "w-barrier", "fp-wb")
		acqErrCh <- err
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		wbOp, err := s.Operation("w-barrier")
		if err == nil && wbOp.Phase == PhaseQueued && wbOp.WaitReason == "approval_pending" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	wbOp, err := s.Operation("w-barrier")
	if err != nil || wbOp.Phase != PhaseQueued || wbOp.WaitReason != "approval_pending" {
		t.Fatalf("expected write queued on approval_pending barrier, got %+v err=%v", wbOp, err)
	}

	// Simulate external file modification between approval request and execution -> approval_tampered.
	if err := os.WriteFile(scriptPath, []byte("Write-Output 'tampered'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.ApproveTool(scope.Conversation, resWait.ApprovalID, "allow_once"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WaitApproval(context.Background(), ExecApprovalWaitRequest{CallID: "c-toctou", ApprovalID: resWait.ApprovalID}); Code(err) != "approval_tampered" {
		t.Fatalf("expected approval_tampered, got %v", err)
	}
	_, _ = s.End("c-toctou", "failed", "approval_tampered")

	// Barrier lifts once approval resolves; complete w-barrier cleanly.
	select {
	case err := <-acqErrCh:
		if err != nil {
			t.Fatalf("Acquire after barrier lift failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("w-barrier remained blocked after approval settled")
	}
	_, _ = s.End("w-barrier", "completed", "")
}

func TestExecEscalationTurboEphemeralAndRevert(t *testing.T) {
	s, _, _, scope := openEscalationTestService(t)
	extDir := t.TempDir()

	// Turbo mode: auto-approves without Escalated flag, runs Tier 2, zero rule pollution, reverts cleanly.
	s.ConfigureTrust(config.ExecutionModeTurbo, "rev-turbo", nil)
	turboFile := filepath.Join(extDir, "turbo.txt")
	testBeginTurnCall(t, s, scope, "c-turbo", "b-turbo", 0, "exec_command", nil, "script", 0)
	resTurbo, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID:  "c-turbo",
		Command: `Set-Content -LiteralPath "` + turboFile + `" -Value "turbo-ok"`,
		Workdir: extDir,
	})
	if err != nil || resTurbo.Status != "completed" {
		t.Fatalf("expected direct Tier 2 completion in turbo mode, got %+v err=%v", resTurbo, err)
	}
	turboOp, _ := s.End("c-turbo", "completed", "")
	if turboOp.ApprovalMode != "turbo_auto" {
		t.Fatalf("expected ApprovalMode=turbo_auto, got %q", turboOp.ApprovalMode)
	}
	if len(s.Status().AllowedItems) != 0 {
		t.Fatalf("turbo mode must never pollute AllowedItems: %+v", s.Status().AllowedItems)
	}

	s.ConfigureTrust(config.ExecutionModeStandard, "rev-back", nil)
	testBeginTurnCall(t, s, scope, "c-back", "b-back", 0, "exec_command", nil, "script", 0)
	resBack, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "c-back", Command: `Get-ChildItem "` + extDir + `"`, Escalated: true, EscalationReason: "Check after standard switch", ExternalPaths: []string{extDir},
	})
	if err != nil || resBack.Status != "approval_required" {
		t.Fatalf("expected fresh confirmation after switching back to standard, got %+v err=%v", resBack, err)
	}
	_ = s.CancelCall("c-back", "cancelled")
	_, _ = s.End("c-back", "cancelled", "cancelled")

	// Verify EscalationAvailable signal when Tier 1 hits external workdir.
	testBeginTurnCall(t, s, scope, "c-hint", "b-hint", 0, "exec_command", nil, "script", 0)
	resHint, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "c-hint", Command: `Get-Location`, Workdir: extDir,
	})
	if err != nil || resHint.Status != "failed" || resHint.Reason != "outside_workspace" || !resHint.EscalationAvailable {
		t.Fatalf("expected outside_workspace with EscalationAvailable=true, got %+v err=%v", resHint, err)
	}
	_, _ = s.End("c-hint", "completed", "")
}
