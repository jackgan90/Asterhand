//go:build windows

package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/platform/sandbox"
)

type countingEscalationBackend struct {
	inner  sandbox.Backend
	starts atomic.Int32
}

func (b *countingEscalationBackend) Supported() bool { return b.inner.Supported() }
func (b *countingEscalationBackend) Prepare(ws, tmp string) error {
	return b.inner.Prepare(ws, tmp)
}
func (b *countingEscalationBackend) CleanupTemp(tmp string) error {
	return b.inner.CleanupTemp(tmp)
}
func (b *countingEscalationBackend) Start(ctx context.Context, spec sandbox.SpawnSpec) (sandbox.SessionProcess, error) {
	b.starts.Add(1)
	return b.inner.Start(ctx, spec)
}

func TestExecEscalationAuditMultiDirAndUndeclaredWorkdir(t *testing.T) {
	s, _, _, scope := openEscalationTestService(t)
	dirA := t.TempDir()
	dirB := t.TempDir()
	undeclared := t.TempDir()

	var persisted []config.TrustRule
	s.SetTrustCallbacks(func(rules []config.TrustRule) error {
		persisted = append(persisted, rules...)
		return nil
	}, nil)

	// Two declared external folders must both appear in Approval.Targets and both become trusted.
	testBeginTurnCall(t, s, scope, "c-multi", "b-multi", 0, "exec_command", nil, "multi", 0)
	reqMulti := ExecStartRequest{
		CallID:           "c-multi",
		Command:          `Get-ChildItem "` + dirA + `"; Get-ChildItem "` + dirB + `"`,
		Escalated:        true,
		EscalationReason: "Read two export folders",
		ExternalPaths:    []string{dirA, dirB},
	}
	res, err := s.StartExec(context.Background(), reqMulti)
	if err != nil || res.Status != "approval_required" {
		t.Fatalf("StartExec(c-multi): res=%+v err=%v", res, err)
	}
	op, err := s.Operation("c-multi")
	if err != nil || op.Approval == nil || len(op.Approval.Targets) != 2 {
		t.Fatalf("expected 2 targets in ApprovalView, got %+v err=%v", op.Approval, err)
	}
	if err := s.ApproveTool(scope.Conversation, res.ApprovalID, "allow_always"); err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	waitMs := 2500
	finalRes, err := s.WaitApproval(context.Background(), ExecApprovalWaitRequest{
		CallID: "c-multi", ApprovalID: res.ApprovalID, WaitMs: &waitMs,
	})
	if err != nil || finalRes.Status != "completed" {
		t.Fatalf("WaitApproval(c-multi): res=%+v err=%v", finalRes, err)
	}
	_, _ = s.End("c-multi", "completed", "")
	if len(persisted) != 2 {
		t.Fatalf("expected 2 persisted folder rules, got %+v", persisted)
	}

	// Undeclared external workdir must be rejected before creating any ticket.
	testBeginTurnCall(t, s, scope, "c-undec-wd", "b-undec-wd", 0, "exec_command", nil, "wd", 0)
	_, err = s.StartExec(context.Background(), ExecStartRequest{
		CallID:           "c-undec-wd",
		Command:          `Get-Location`,
		Workdir:          undeclared,
		Escalated:        true,
		EscalationReason: "Check undeclared folder",
		ExternalPaths:    []string{dirA},
	})
	if Code(err) != "undeclared_external_workdir" {
		t.Fatalf("expected undeclared_external_workdir, got %v", err)
	}
	if pending := s.PendingApprovals(scope.Conversation, scope.Turn); len(pending) != 0 {
		t.Fatalf("expected no ticket created for undeclared external workdir, got %+v", pending)
	}
	_, _ = s.End("c-undec-wd", "failed", Code(err))
}

func TestExecEscalationAuditCommandBindingNoCrossHit(t *testing.T) {
	s, _, _, scope := openEscalationTestService(t)
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	tool1 := filepath.Join(dir1, "mytool.cmd")
	tool2 := filepath.Join(dir2, "mytool.cmd")
	if err := os.WriteFile(tool1, []byte("@echo off\r\necho from-dir1\r\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tool2, []byte("@echo off\r\necho from-dir2\r\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	var persisted []config.TrustRule
	s.SetTrustCallbacks(func(rules []config.TrustRule) error {
		persisted = append(persisted, rules...)
		return nil
	}, nil)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", dir1+string(os.PathListSeparator)+origPath)

	// Approve bare "mytool" (resolved from dir1\mytool.cmd) with allow_always.
	res1 := runApprovedFlow(t, s, scope, "c-cmd-1", "allow_always", ExecStartRequest{
		CallID:           "c-cmd-1",
		Command:          "mytool",
		Escalated:        true,
		EscalationReason: "Run harmless test tool",
	})
	if res1.Status != "completed" || !strings.Contains(res1.Output, "from-dir1") {
		t.Fatalf("unexpected tool1 result: %+v", res1)
	}
	if len(persisted) != 1 || persisted[0].Kind != "command" || !pathsEqualOS(persisted[0].Pattern, tool1) {
		t.Fatalf("expected rule bound to canonical path %q, got %+v", tool1, persisted)
	}
	s.ConfigureTrust(config.ExecutionModeStandard, "rev-cmd-exact", persisted)

	// Explicit path to same-named binary in dir2 must NOT match dir1\mytool.cmd.
	testBeginTurnCall(t, s, scope, "c-cmd-2", "b-cmd-2", 0, "exec_command", nil, "cmd2", 0)
	res2, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID:           "c-cmd-2",
		Command:          `"` + tool2 + `"`,
		Escalated:        true,
		EscalationReason: "Run second tool",
	})
	if err != nil || res2.Status != "approval_required" {
		t.Fatalf("expected approval_required for %q, got %+v err=%v", tool2, res2, err)
	}
	_ = s.CancelCall("c-cmd-2", "cancelled")
	_, _ = s.End("c-cmd-2", "cancelled", "cancelled")

	// Bare "mytool" when PATH now resolves to dir2 must ALSO require approval.
	t.Setenv("PATH", dir2+string(os.PathListSeparator)+origPath)
	testBeginTurnCall(t, s, scope, "c-cmd-3", "b-cmd-3", 0, "exec_command", nil, "cmd3", 0)
	res3, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID:           "c-cmd-3",
		Command:          "mytool",
		Escalated:        true,
		EscalationReason: "Run shadowed tool",
	})
	if err != nil || res3.Status != "approval_required" {
		t.Fatalf("expected approval_required when PATH resolves to dir2, got %+v err=%v", res3, err)
	}
	_ = s.CancelCall("c-cmd-3", "cancelled")
	_, _ = s.End("c-cmd-3", "cancelled", "cancelled")
}

func TestExecEscalationAuditLargeScriptJunctionTOCTOUAndPreflight(t *testing.T) {
	s, home, ws, scope := openEscalationTestService(t)
	extDir := t.TempDir()

	var persisted []config.TrustRule
	s.SetTrustCallbacks(func(rules []config.TrustRule) error {
		persisted = append(persisted, rules...)
		return nil
	}, nil)

	testAuditLargeScriptTOCTOU(t, s, ws, extDir, scope)
	testAuditJunctionRepointTOCTOU(t, s, scope, &persisted)
	testAuditReferencedScriptPreflight(t, s, home, ws, scope)
}

func testAuditLargeScriptTOCTOU(t *testing.T, s *Service, ws, extDir string, scope Scope) {
	t.Helper()
	// 1. > 4 MiB .ps1 script must be hashed and equal-size modification caught as approval_tampered.
	bigPath := filepath.Join(ws, "large.ps1")
	header := []byte("# large script header\r\nWrite-Output 'ok'\r\n")
	pad := make([]byte, 4*1024*1024+128*1024)
	for i := range pad {
		pad[i] = '#'
	}
	pad[len(pad)-1] = '\n'
	content := append(append([]byte{}, header...), pad...)
	if err := os.WriteFile(bigPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	testBeginTurnCall(t, s, scope, "c-big-ps1", "b-big-ps1", 0, "exec_command", nil, "big", 0)
	resBig, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID:           "c-big-ps1",
		Command:          `powershell -File "` + bigPath + `"`,
		Escalated:        true,
		EscalationReason: "Run large script",
		ExternalPaths:    []string{extDir},
	})
	if err != nil || resBig.Status != "approval_required" {
		t.Fatalf("StartExec(c-big-ps1): res=%+v err=%v", resBig, err)
	}
	edited := append([]byte{}, content...)
	edited[len(edited)-10] = 'X'
	if err := os.WriteFile(bigPath, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.ApproveTool(scope.Conversation, resBig.ApprovalID, "allow_once"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WaitApproval(context.Background(), ExecApprovalWaitRequest{
		CallID: "c-big-ps1", ApprovalID: resBig.ApprovalID,
	}); Code(err) != "approval_tampered" {
		t.Fatalf("expected approval_tampered on >4MiB equal-size edit, got %v", err)
	}
	_, _ = s.End("c-big-ps1", "failed", "approval_tampered")
}

func testAuditJunctionRepointTOCTOU(t *testing.T, s *Service, scope Scope, persisted *[]config.TrustRule) {
	t.Helper()
	// 2. External junction repointed during wait must return approval_tampered and leave zero trust rules.
	junctionBase := t.TempDir()
	targetA := filepath.Join(junctionBase, "targetA")
	targetB := filepath.Join(junctionBase, "targetB")
	linkPath := filepath.Join(junctionBase, "link")
	if err := os.MkdirAll(targetA, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(targetB, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", linkPath, targetA).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J targetA failed: %v %s", err, out)
	}
	testBeginTurnCall(t, s, scope, "c-junc", "b-junc", 0, "exec_command", nil, "junc", 0)
	resJunc, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID:           "c-junc",
		Command:          `Get-ChildItem "` + linkPath + `"`,
		Escalated:        true,
		EscalationReason: "Inspect linked directory",
		ExternalPaths:    []string{linkPath},
	})
	if err != nil || resJunc.Status != "approval_required" {
		t.Fatalf("StartExec(c-junc): res=%+v err=%v", resJunc, err)
	}
	if err := os.Remove(linkPath); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", linkPath, targetB).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J targetB failed: %v %s", err, out)
	}
	if err := s.ApproveTool(scope.Conversation, resJunc.ApprovalID, "allow_always"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WaitApproval(context.Background(), ExecApprovalWaitRequest{
		CallID: "c-junc", ApprovalID: resJunc.ApprovalID,
	}); Code(err) != "approval_tampered" {
		t.Fatalf("expected approval_tampered on repointed junction, got %v", err)
	}
	_, _ = s.End("c-junc", "failed", "approval_tampered")
	if len(*persisted) != 0 || len(s.Status().AllowedItems) != 0 {
		t.Fatalf("tampered approval must leave zero trust rules, got persisted=%+v status=%+v", *persisted, s.Status().AllowedItems)
	}
}

func testAuditReferencedScriptPreflight(t *testing.T, s *Service, home, ws string, scope Scope) {
	t.Helper()
	// 3. Referenced workspace scripts containing elevation verbs or protected paths fail preflight.
	verbScript := filepath.Join(ws, "verb.ps1")
	if err := os.WriteFile(verbScript, []byte("Start-Process cmd -Verb RunAs\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testBeginTurnCall(t, s, scope, "c-ws-verb", "b-ws-verb", 0, "exec_command", nil, "verb", 0)
	if _, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "c-ws-verb", Command: `powershell -File "` + verbScript + `"`,
	}); Code(err) != "elevation_verb_denied" {
		t.Fatalf("expected elevation_verb_denied for referenced script, got %v", err)
	}
	_, _ = s.End("c-ws-verb", "failed", "elevation_verb_denied")

	homeScript := filepath.Join(ws, "home.ps1")
	if err := os.WriteFile(homeScript, []byte(`Get-Content "`+filepath.Join(home, "config.json")+`"`+"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testBeginTurnCall(t, s, scope, "c-ws-home", "b-ws-home", 0, "exec_command", nil, "home", 0)
	if _, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "c-ws-home", Command: `powershell -File "` + homeScript + `"`,
	}); Code(err) != "sensitive_path_denied" {
		t.Fatalf("expected sensitive_path_denied for referenced script, got %v", err)
	}
	_, _ = s.End("c-ws-home", "failed", "sensitive_path_denied")
}

func TestExecEscalationAuditSingleFlightWaitAndEndCleanup(t *testing.T) {
	s, _, _, scope := openEscalationTestService(t)
	counter := &countingEscalationBackend{inner: s.exec.backend}
	s.exec.backend = counter
	extDir := t.TempDir()

	// 1. Concurrent WaitApproval on same ticket must call Backend.Start only once.
	testBeginTurnCall(t, s, scope, "c-dup-wait", "b-dup-wait", 0, "exec_command", nil, "dup", 0)
	res, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID:           "c-dup-wait",
		Command:          `Write-Output "single-launch"`,
		Escalated:        true,
		EscalationReason: "Test single launch",
		ExternalPaths:    []string{extDir},
	})
	if err != nil || res.Status != "approval_required" {
		t.Fatalf("StartExec(c-dup-wait): res=%+v err=%v", res, err)
	}

	var wg sync.WaitGroup
	results := make([]ExecResult, 2)
	errs := make([]error, 2)
	waitMs := 2500
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = s.WaitApproval(context.Background(), ExecApprovalWaitRequest{
				CallID: "c-dup-wait", ApprovalID: res.ApprovalID, WaitMs: &waitMs,
			})
		}(i)
	}
	time.Sleep(60 * time.Millisecond)
	if err := s.ApproveTool(scope.Conversation, res.ApprovalID, "allow_once"); err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	wg.Wait()
	for i := 0; i < 2; i++ {
		if errs[i] != nil || results[i].Status != "completed" {
			t.Fatalf("waiter %d unexpected: res=%+v err=%v", i, results[i], errs[i])
		}
	}
	if got := counter.starts.Load(); got != 1 {
		t.Fatalf("expected exactly 1 Backend.Start call for concurrent WaitApproval, got %d", got)
	}
	_, _ = s.End("c-dup-wait", "completed", "")

	// 2. Ending a call with no WaitApproval must release awaitingApproval barrier and settle turn.
	testBeginTurnCall(t, s, scope, "c-abandon", "b-abandon", 0, "exec_command", nil, "abandon", 0)
	resAbandon, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID:           "c-abandon",
		Command:          `Get-ChildItem "` + extDir + `"`,
		Escalated:        true,
		EscalationReason: "Abandoned wait",
		ExternalPaths:    []string{extDir},
	})
	if err != nil || resAbandon.Status != "approval_required" {
		t.Fatalf("StartExec(c-abandon): res=%+v err=%v", resAbandon, err)
	}
	if got := s.sched.awaitingApproval.Load(); got != 1 {
		t.Fatalf("expected awaitingApproval=1, got %d", got)
	}
	if _, err := s.End("c-abandon", "cancelled", "cancelled"); err != nil {
		t.Fatalf("End(c-abandon): %v", err)
	}
	if got := s.sched.awaitingApproval.Load(); got != 0 {
		t.Fatalf("expected awaitingApproval=0 after End cleanup, got %d", got)
	}
	settled, err := s.exec.TurnSettled(scope.Turn)
	if err != nil || !settled {
		t.Fatalf("expected TurnSettled=true after End cleanup, got settled=%v err=%v", settled, err)
	}
}

func TestExecEscalationAuditConcurrentApproveCancelAndSanitizeSpeed(t *testing.T) {
	s, _, _, scope := openEscalationTestService(t)
	extDir := t.TempDir()

	// Concurrent ApproveTool vs CancelCall must never panic or leak awaitingApproval.
	testBeginTurnCall(t, s, scope, "c-race", "b-race", 0, "exec_command", nil, "race", 0)
	res, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID:           "c-race",
		Command:          `Get-ChildItem "` + extDir + `"`,
		Escalated:        true,
		EscalationReason: "Race test",
		ExternalPaths:    []string{extDir},
	})
	if err != nil || res.Status != "approval_required" {
		t.Fatalf("StartExec(c-race): res=%+v err=%v", res, err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = s.ApproveTool(scope.Conversation, res.ApprovalID, "allow_once")
	}()
	go func() {
		defer wg.Done()
		_ = s.CancelCall("c-race", "cancelled")
	}()
	wg.Wait()
	_, _ = s.End("c-race", "cancelled", "cancelled")
	if got := s.sched.awaitingApproval.Load(); got != 0 {
		t.Fatalf("expected awaitingApproval=0 after approve/cancel race, got %d", got)
	}

	// sanitizeApprovalReason on 64 KiB input must stay bounded and linear-time.
	huge := strings.Repeat("   ", 2000) + strings.Repeat("说明", 16000)
	start := time.Now()
	trimmed := sanitizeApprovalReason(huge, "", "", "folder", extDir)
	elapsed := time.Since(start)
	if len(trimmed) == 0 || len(trimmed) > MaxApprovalReasonBytes || !utf8.ValidString(trimmed) {
		t.Fatalf("invalid sanitized reason len=%d valid=%v", len(trimmed), utf8.ValidString(trimmed))
	}
	if elapsed > 50*time.Millisecond {
		t.Fatalf("sanitizeApprovalReason took too long for 64KiB input: %v", elapsed)
	}
}
