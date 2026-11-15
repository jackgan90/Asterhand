//go:build windows

package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testBeginTurnCall(t *testing.T, s *Service, scope Scope, id, batch string, order int, tool string, targets []Target, path string, newBytes int64) Operation {
	t.Helper()
	fp := "fp-" + id
	if _, err := s.Register(Register{
		ID: id, Batch: batch, Order: order, Scope: scope, Tool: tool,
		Targets: targets, Path: path, NewBytes: newBytes, InputFingerprint: fp,
	}); err != nil {
		t.Fatalf("Register(%s): %v", id, err)
	}
	if err := s.SealBatch(SealBatch{
		Batch: batch, Scope: scope,
		Items: []SealItem{{ID: id, Tool: tool, Targets: targets, Path: path, NewBytes: newBytes, Fingerprint: fp}},
	}); err != nil {
		t.Fatalf("SealBatch(%s): %v", id, err)
	}
	op, err := s.Acquire(context.Background(), id, fp)
	if err != nil {
		t.Fatalf("Acquire(%s): %v", id, err)
	}
	return op
}

func waitForReleaseTurnTrue(t *testing.T, s *Service, turn string) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if err := s.ReleaseTurn(turn, true); err == nil {
			return
		} else if Code(err) != "cleanup_pending" {
			t.Fatalf("unexpected ReleaseTurn error: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for ReleaseTurn(%s, true)", turn)
}

func startDetachedScriptForTest(t *testing.T, s *Service, scope Scope, id, cmd string, yieldMs int) string {
	t.Helper()
	testBeginTurnCall(t, s, scope, id, "b-"+id, 0, "exec_command", nil, "script", 0)
	y := yieldMs
	res, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: id, Command: cmd, YieldMs: &y,
	})
	if err != nil || res.Status != "running" || res.SessionID == "" {
		t.Fatalf("StartExec detached(%s): %+v err=%v", id, res, err)
	}
	if op, err := s.Operation(res.SessionID); err != nil || op.State != "started" {
		t.Fatalf("expected persisted started op %s, got %+v err=%v", res.SessionID, op, err)
	}
	if _, err := s.End(id, "completed", ""); err != nil {
		t.Fatal(err)
	}
	return res.SessionID
}

func TestExecRoundTripAndPublishGenStaleRestore(t *testing.T) {
	s, scope := testTurnService(t)
	testBeginTurnCall(t, s, scope, "w-seed", "b-seed", 0, "write_file", []Target{{Path: "note.txt"}}, "", 64)
	if _, err := s.Write("w-seed", "note.txt", []byte("line1\n"), false); err != nil {
		t.Fatal(err)
	}
	wOp, err := s.End("w-seed", "completed", "")
	if err != nil || wOp.State != "completed" {
		t.Fatalf("End(w-seed): %+v err=%v", wOp, err)
	}

	s.mu.Lock()
	genBefore := s.publishGen
	s.mu.Unlock()

	testBeginTurnCall(t, s, scope, "ex-1", "b-ex1", 0, "exec_command", nil, "script", 0)
	cmd := `$b=[System.Text.Encoding]::GetEncoding(54936).GetBytes("中文测试-GBK` + "`n" + `"); ` +
		`[System.Console]::OpenStandardOutput().Write($b,0,$b.Length); ` +
		`Add-Content -LiteralPath 'note.txt' -Value 'line2-from-script' -Encoding UTF8; ` +
		`[System.IO.File]::WriteAllText((Join-Path $PWD 'created.txt'), 'hello-created', [System.Text.UTF8Encoding]::new($false))`
	res, err := s.StartExec(context.Background(), ExecStartRequest{CallID: "ex-1", Command: cmd})
	if err != nil {
		t.Fatalf("StartExec: %v", err)
	}
	if res.Status != "completed" || res.ExitCode != 0 || !strings.Contains(res.Output, "中文测试-GBK") {
		t.Fatalf("unexpected exec result: %+v", res)
	}
	if _, err := s.End("ex-1", "completed", ""); err != nil {
		t.Fatal(err)
	}

	s.mu.Lock()
	genAfter := s.publishGen
	s.mu.Unlock()
	if genAfter <= genBefore {
		t.Fatalf("expected publishGen to advance (%d -> %d)", genBefore, genAfter)
	}
	if frag, err := s.Read("note.txt", "", 0); err != nil || !strings.Contains(string(frag.Data), "line2-from-script") {
		t.Fatalf("expected script append in note.txt, got %q err=%v", string(frag.Data), err)
	}
	if frag, err := s.Read("created.txt", "", 0); err != nil || string(frag.Data) != "hello-created" {
		t.Fatalf("expected created.txt, got %q err=%v", string(frag.Data), err)
	}
	waitForReleaseTurnTrue(t, s, scope.Turn)
	if _, err := s.Restore("rest-stale", Scope{Policy: scope.Policy}, wOp.ID, "note.txt", revision([]byte("line1\n"))); Code(err) != "revision_conflict" {
		t.Fatalf("expected revision_conflict on stale Restore after exec edit, got %v", err)
	}
}

func TestExecDACLFailureBlocksAndResolveRecovers(t *testing.T) {
	s, scope := testTurnService(t)
	origDACL := reinheritHandleDACL
	reinheritHandleDACL = func(*os.File) error { return errors.New("injected acl error") }
	defer func() { reinheritHandleDACL = origDACL }()

	testBeginTurnCall(t, s, scope, "w-acl", "b-acl", 0, "write_file", []Target{{Path: "acl.txt"}}, "", 32)
	if _, err := s.Write("w-acl", "acl.txt", []byte("blocked"), false); Code(err) != "acl_reinherit_failed" {
		t.Fatalf("expected acl_reinherit_failed, got %v", err)
	}
	opACL, endErr := s.End("w-acl", "failed", "acl_reinherit_failed")
	if endErr != nil || opACL.State != "unknown" {
		t.Fatalf("expected op state unknown on DACL failure, got %+v err=%v", opACL, endErr)
	}
	if st := s.Status(); st.Ready || st.Reason != "reconciliation_required" {
		t.Fatalf("expected blocked status after DACL failure, got %+v", st)
	}
	waitForReleaseTurnTrue(t, s, scope.Turn)
	if _, err := s.ResolveRequest("req-acl-1", "w-acl", "verified on disk"); err != nil {
		t.Fatalf("ResolveRequest failed: %v", err)
	}
	if st := s.Status(); !st.Ready {
		t.Fatalf("expected Ready after ResolveRequest, got %+v", st)
	}
}

func TestExecForegroundBatchSerialization(t *testing.T) {
	s, scope := testTurnService(t)
	if _, err := s.Register(Register{
		ID: "fb-write", Batch: "b-fg", Order: 0, Scope: scope, Tool: "write_file",
		Targets: []Target{{Path: "fg.txt"}}, NewBytes: 32, InputFingerprint: "fp-fb-write",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register(Register{
		ID: "fb-exec", Batch: "b-fg", Order: 1, Scope: scope, Tool: "exec_command",
		Path: "cmd", InputFingerprint: "fp-fb-exec",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SealBatch(SealBatch{
		Batch: "b-fg", Scope: scope,
		Items: []SealItem{
			{ID: "fb-write", Tool: "write_file", Targets: []Target{{Path: "fg.txt"}}, NewBytes: 32, Fingerprint: "fp-fb-write"},
			{ID: "fb-exec", Tool: "exec_command", Path: "cmd", Fingerprint: "fp-fb-exec"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire(context.Background(), "fb-write", "fp-fb-write"); err != nil {
		t.Fatal(err)
	}
	execGranted := make(chan error, 1)
	go func() {
		_, err := s.Acquire(context.Background(), "fb-exec", "fp-fb-exec")
		execGranted <- err
	}()
	select {
	case err := <-execGranted:
		t.Fatalf("fb-exec granted prematurely while fb-write held lock: %v", err)
	case <-time.After(60 * time.Millisecond):
	}
	if _, err := s.Write("fb-write", "fg.txt", []byte("written"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.End("fb-write", "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := <-execGranted; err != nil {
		t.Fatalf("fb-exec failed: %v", err)
	}
	if _, err := s.End("fb-exec", "completed", ""); err != nil {
		t.Fatal(err)
	}
	waitForReleaseTurnTrue(t, s, scope.Turn)
}

func assertConcurrentToolsDuringDetached(t *testing.T, s *Service, scope Scope, sessionID string) {
	t.Helper()
	testBeginTurnCall(t, s, scope, "bg-read", "b-ro", 0, "read_file", nil, "seed.txt", 0)
	if data, err := s.NativeRead("bg-read", "seed.txt"); err != nil || string(data) != "seed-body" {
		t.Fatalf("concurrent NativeRead failed: %q err=%v", string(data), err)
	}
	if _, err := s.End("bg-read", "completed", ""); err != nil {
		t.Fatal(err)
	}

	testBeginTurnCall(t, s, scope, "bg-search", "b-srch", 0, "search_files", nil, "", 0)
	if matches, err := s.SearchWithCallContext(context.Background(), "bg-search", "", "seed-body", "", true, true); err != nil || len(matches.Items) == 0 {
		t.Fatalf("concurrent SearchWithCallContext failed: %+v err=%v", matches, err)
	}
	if _, err := s.End("bg-search", "completed", ""); err != nil {
		t.Fatal(err)
	}

	testBeginTurnCall(t, s, scope, "bg-ctl", "b-ctl", 0, "process_control", nil, "list", 0)
	listed, err := s.ControlExec(ExecControlRequest{CallID: "bg-ctl", Action: "list"})
	if err != nil || len(listed.Sessions) != 1 || listed.Sessions[0].SessionID != sessionID {
		t.Fatalf("ControlExec(list): %+v err=%v", listed, err)
	}
	if stRes, err := s.ControlExec(ExecControlRequest{CallID: "bg-ctl", Action: "status", SessionID: sessionID}); err != nil || stRes.SessionID != sessionID {
		t.Fatalf("ControlExec(status): %+v err=%v", stRes, err)
	}
	if logRes, err := s.ControlExec(ExecControlRequest{CallID: "bg-ctl", Action: "log", SessionID: sessionID}); err != nil || logRes.SessionID != sessionID {
		t.Fatalf("ControlExec(log): %+v err=%v", logRes, err)
	}
	waitZero := 0
	if _, err := s.PollExec(context.Background(), ExecPollRequest{CallID: "bg-ctl", SessionID: sessionID, WaitMs: &waitZero}); err != nil {
		t.Fatalf("PollExec: %v", err)
	}
	if _, err := s.End("bg-ctl", "completed", ""); err != nil {
		t.Fatal(err)
	}

	testBeginTurnCall(t, s, scope, "ex-2", "b-ex2", 0, "exec_command", nil, "echo", 0)
	res2, err := s.StartExec(context.Background(), ExecStartRequest{CallID: "ex-2", Command: "cmd /c echo concurrent-exec"})
	if err != nil || res2.Status != "completed" || !strings.Contains(res2.Output, "concurrent-exec") {
		t.Fatalf("2nd concurrent exec_command failed: %+v err=%v", res2, err)
	}
	if _, err := s.End("ex-2", "completed", ""); err != nil {
		t.Fatal(err)
	}
}

func TestExecDetachedConcurrencyAndExecActiveBarrier(t *testing.T) {
	s, scope := testTurnService(t, Mutation{Action: "write", Path: "seed.txt", Expected: MissingRevision, Content: "seed-body"})
	sessionID := startDetachedScriptForTest(t, s, scope, "ex-bg", `Write-Output 'bg-started'; Start-Sleep -Milliseconds 1200; Write-Output 'bg-done'`, 80)

	assertConcurrentToolsDuringDetached(t, s, scope, sessionID)

	if _, err := s.Register(Register{
		ID: "w-barrier", Batch: "b-wb", Order: 0, Scope: scope, Tool: "write_file",
		Targets: []Target{{Path: "after-bg.txt"}}, NewBytes: 16, InputFingerprint: "fp-wb",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SealBatch(SealBatch{
		Batch: "b-wb", Scope: scope,
		Items: []SealItem{{ID: "w-barrier", Tool: "write_file", Targets: []Target{{Path: "after-bg.txt"}}, NewBytes: 16, Fingerprint: "fp-wb"}},
	}); err != nil {
		t.Fatal(err)
	}
	wbDone := make(chan error, 1)
	go func() {
		_, err := s.Acquire(context.Background(), "w-barrier", "fp-wb")
		wbDone <- err
	}()
	deadline := time.Now().Add(1000 * time.Millisecond)
	sawExecActive := false
	for time.Now().Before(deadline) {
		if op, err := s.Operation("w-barrier"); err == nil && op.WaitReason == "exec_active" {
			sawExecActive = true
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !sawExecActive {
		t.Fatal("expected w-barrier waitReason=exec_active while detached exec running")
	}
	if err := <-wbDone; err != nil {
		t.Fatalf("w-barrier Begin failed after bg exec exited: %v", err)
	}
	if _, err := s.Write("w-barrier", "after-bg.txt", []byte("unblocked"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.End("w-barrier", "completed", ""); err != nil {
		t.Fatal(err)
	}
	waitForReleaseTurnTrue(t, s, scope.Turn)
}

func TestExecDetachedUnpolledCrashAndRuntimeStopped(t *testing.T) {
	s, scope := testTurnService(t)
	sessionID := startDetachedScriptForTest(t, s, scope, "ex-u", `powershell -NoProfile -Command "Start-Sleep -Milliseconds 250; Write-Output 'done-unpolled'"`, 50)

	var termOp Operation
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if op, err := s.Operation(sessionID); err == nil && op.Phase == PhaseTerminal {
			termOp = op
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if termOp.State != "completed" {
		t.Fatalf("expected unpolled detached terminal state=completed, got %+v", termOp)
	}
	acts, _ := s.Activities("", scope.Turn, 0)
	foundAct := false
	for _, a := range acts {
		if a.ID == sessionID && a.State == "completed" {
			foundAct = true
		}
	}
	if !foundAct {
		t.Fatalf("expected detached session %s in Activities, got %+v", sessionID, acts)
	}
	waitForReleaseTurnTrue(t, s, scope.Turn)
	s.mu.Lock()
	resBytes := s.reservedActivityBytes
	s.mu.Unlock()
	if resBytes != 0 {
		t.Fatalf("expected reservedActivityBytes == 0 after detached exit, got %d", resBytes)
	}

	// Open() recovery from persisted "started" es_*.json -> "unknown" + s.blocked = true.
	crashOp := Operation{
		ID: "es_crashtest01", Scope: Scope{Conversation: "c-crash", Turn: "t-crash", Policy: scope.Policy},
		Tool: "exec_command", Summary: "crashed background script", Fingerprint: "exec:es_crashtest01",
		Sequence: 999, Started: time.Now().UTC(), State: "started", Phase: PhaseRunning, Changes: []Change{},
	}
	raw, _ := json.Marshal(crashOp)
	if err := os.WriteFile(filepath.Join(s.journal, crashOp.ID+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.root, s.journal, s.id)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Configure(true, scope.Policy); err != nil {
		t.Fatal(err)
	}
	if op, err := reopened.Operation(crashOp.ID); err != nil || op.State != "unknown" {
		t.Fatalf("expected Open to mark started es_* unknown, got %+v err=%v", op, err)
	}
	if st := reopened.Status(); st.Ready || st.Reason != "reconciliation_required" {
		t.Fatalf("expected blocked after crash Open, got %+v", st)
	}
	if _, err := reopened.ResolveRequest("req-crash-1", crashOp.ID, "verified no side effects"); err != nil {
		t.Fatalf("ResolveRequest: %v", err)
	}
	if !reopened.Status().Ready {
		t.Fatalf("expected Ready after ResolveRequest, got %+v", reopened.Status())
	}

	// RuntimeStopped() with empty callsByID while detached es_* runs -> "unknown" + s.blocked = true.
	s2, scope2 := testTurnService(t)
	rtID := startDetachedScriptForTest(t, s2, scope2, "ex-rt", `powershell -NoProfile -Command "Start-Sleep -Seconds 30"`, 50)
	s2.mu.Lock()
	activeCalls := len(s2.callsByID)
	s2.mu.Unlock()
	if activeCalls != 0 {
		t.Fatalf("expected callsByID empty before RuntimeStopped, got %d", activeCalls)
	}
	if err := s2.RuntimeStopped(); err != nil {
		t.Fatalf("RuntimeStopped: %v", err)
	}
	if op, err := s2.Operation(rtID); err != nil || op.State != "unknown" {
		t.Fatalf("expected detached session marked unknown after RuntimeStopped, got %+v err=%v", op, err)
	}
	if st := s2.Status(); st.Ready || st.Reason != "reconciliation_required" {
		t.Fatalf("expected blocked after RuntimeStopped on detached session, got %+v", st)
	}
}

func TestExecSandboxTempQuotaWatchdog(t *testing.T) {
	s, scope := testTurnService(t)
	s.exec.mu.Lock()
	s.exec.testTempBytes = 512
	s.exec.mu.Unlock()

	bgID := startDetachedScriptForTest(t, s, scope, "qt-1",
		`[System.IO.File]::WriteAllBytes((Join-Path $env:TEMP 'bloat.bin'), (New-Object byte[] 2048)); Start-Sleep -Seconds 30`, 50)

	testBeginTurnCall(t, s, scope, "qt-2", "b-qt2", 0, "exec_command", nil, "innocent", 0)
	start := time.Now()
	_, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "qt-2", Command: `Start-Sleep -Seconds 30`,
	})
	if Code(err) != "sandbox_temp_limit" || time.Since(start) > 5*time.Second {
		t.Fatalf("expected qt-2 killed by sandbox_temp_limit within ~2-3s, got err=%v in %v", err, time.Since(start))
	}
	if _, err := s.End("qt-2", "failed", Code(err)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if op, err := s.Operation(bgID); err == nil && op.Phase == PhaseTerminal {
			if op.State != "failed" || op.Detail != "sandbox_temp_limit" {
				t.Fatalf("expected bg session killed with sandbox_temp_limit, got %+v", op)
			}
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	waitForReleaseTurnTrue(t, s, scope.Turn)
}

func TestExecWorkspaceQuotaOverQuotaLatchAndRecovery(t *testing.T) {
	s, scope := testTurnService(t, Mutation{Action: "write", Path: "shrink.txt", Expected: MissingRevision, Content: "0123456789"})
	testBeginTurnCall(t, s, scope, "qw-1", "b-qw1", 0, "exec_command", nil, "ws-bloat", 0)
	start := time.Now()
	cmd := `$b=[byte[]]::new(2097100); [Array]::Fill($b, [byte]97); ` +
		`0..127 | ForEach-Object { [System.IO.File]::WriteAllBytes((Join-Path $PWD "big_$_.txt"), $b) }; ` +
		`$e=[byte[]]::new(120000); [Array]::Fill($e, [byte]97); ` +
		`[System.IO.File]::WriteAllBytes((Join-Path $PWD 'big_extra.txt'), $e); Start-Sleep -Seconds 30`
	_, err := s.StartExec(context.Background(), ExecStartRequest{CallID: "qw-1", Command: cmd})
	if Code(err) != "workspace_limit" || time.Since(start) > 5*time.Second {
		t.Fatalf("expected qw-1 killed with workspace_limit, got %v in %v", err, time.Since(start))
	}
	if _, err := s.End("qw-1", "failed", "workspace_limit"); err != nil {
		t.Fatal(err)
	}
	assertOverQuotaRecovery(t, s, scope)
}

func assertOverQuotaRecovery(t *testing.T, s *Service, scope Scope) {
	t.Helper()
	testBeginTurnCall(t, s, scope, "qw-exec", "b-qwe", 0, "exec_command", nil, "blocked-exec", 0)
	if _, err := s.StartExec(context.Background(), ExecStartRequest{CallID: "qw-exec", Command: "cmd /c echo no"}); Code(err) != "workspace_limit" {
		t.Fatalf("expected overQuota to block new exec_command, got %v", err)
	}
	if _, err := s.End("qw-exec", "failed", "workspace_limit"); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Register(Register{
		ID: "qw-grow", Batch: "b-qwg", Order: 0, Scope: scope, Tool: "write_file",
		Targets: []Target{{Path: "shrink.txt"}}, NewBytes: 64, InputFingerprint: "fp-qwg",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SealBatch(SealBatch{
		Batch: "b-qwg", Scope: scope,
		Items: []SealItem{{ID: "qw-grow", Tool: "write_file", Targets: []Target{{Path: "shrink.txt"}}, NewBytes: 64, Fingerprint: "fp-qwg"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire(context.Background(), "qw-grow", "fp-qwg"); Code(err) != "workspace_limit" {
		t.Fatalf("expected overQuota to block positive-growth write Acquire, got %v", err)
	}

	// Shrinking overwrite (NewBytes == 0) is allowed even while overQuota is latched.
	testBeginTurnCall(t, s, scope, "qw-sh", "b-qwsh", 0, "write_file", []Target{{Path: "shrink.txt"}}, "", 0)
	if _, err := s.Write("qw-sh", "shrink.txt", []byte(""), false); err != nil {
		t.Fatalf("expected shrinking overwrite allowed during overQuota, got %v", err)
	}
	if _, err := s.End("qw-sh", "completed", ""); err != nil {
		t.Fatal(err)
	}

	testBeginTurnCall(t, s, scope, "qw-rm", "b-qwrm", 0, "apply_file_patch", []Target{{Path: "big_extra.txt", Remove: true}}, "", 0)
	if err := s.Remove("qw-rm", "big_extra.txt"); err != nil {
		t.Fatalf("expected Remove(big_extra.txt) allowed during overQuota, got %v", err)
	}
	if _, err := s.End("qw-rm", "completed", ""); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	stillOver := s.overQuota
	s.mu.Unlock()
	if stillOver {
		t.Fatal("expected overQuota cleared after removing big_extra.txt")
	}
	testBeginTurnCall(t, s, scope, "qw-w2", "b-qww2", 0, "write_file", []Target{{Path: "recovered.txt"}}, "", 16)
	if _, err := s.Write("qw-w2", "recovered.txt", []byte("after-clear"), false); err != nil {
		t.Fatalf("expected positive-growth write allowed after clearing quota, got %v", err)
	}
	if _, err := s.End("qw-w2", "completed", ""); err != nil {
		t.Fatal(err)
	}
	waitForReleaseTurnTrue(t, s, scope.Turn)

	scope2 := Scope{Conversation: scope.Conversation, Turn: "turn-fixture-0002", Policy: scope.Policy}
	if err := s.AcquireTurnScope(scope2); err != nil {
		t.Fatal(err)
	}
	testBeginTurnCall(t, s, scope2, "qw-ok", "b-qwok", 0, "exec_command", nil, "after-clear", 0)
	if res, err := s.StartExec(context.Background(), ExecStartRequest{CallID: "qw-ok", Command: "cmd /c echo ok"}); err != nil || res.Status != "completed" {
		t.Fatalf("expected exec_command allowed in next turn after clearing quota, got %+v err=%v", res, err)
	}
	if _, err := s.End("qw-ok", "completed", ""); err != nil {
		t.Fatal(err)
	}
	waitForReleaseTurnTrue(t, s, scope2.Turn)
}

func TestExecUnsafeLinksDetectionAndSafeTraversal(t *testing.T) {
	s, scope := testTurnService(t, Mutation{Action: "write", Path: "sub/real.txt", Expected: MissingRevision, Content: "inside"})
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret-outside.txt"), []byte("SECRET-OUTSIDE"), 0600); err != nil {
		t.Fatal(err)
	}

	testBeginTurnCall(t, s, scope, "ex-link", "b-link", 0, "exec_command", nil, "mklink", 0)
	cmd := fmt.Sprintf(`cmd /c mklink /J junc "%s"`, outside)
	res, err := s.StartExec(context.Background(), ExecStartRequest{CallID: "ex-link", Command: cmd})
	if err != nil {
		t.Fatalf("StartExec mklink: %v", err)
	}
	if res.ExitCode != 0 || res.Warning != "unsafe_links_detected" {
		t.Fatalf("expected mklink junction + Warning=unsafe_links_detected, got %+v", res)
	}
	if _, err := s.End("ex-link", "completed", ""); err != nil {
		t.Fatal(err)
	}

	entries, err := s.ReadDirectory("")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Path == "junc" {
			t.Fatalf("ReadDirectory exposed reparse junction %+v", entry)
		}
	}
	testBeginTurnCall(t, s, scope, "srch-safe", "b-srch", 0, "search_files", nil, "", 0)
	srch, err := s.SearchWithCallContext(context.Background(), "srch-safe", "", "SECRET-OUTSIDE", "", true, true)
	if err != nil || len(srch.Items) != 0 {
		t.Fatalf("Search traversed outside via junction! items=%+v err=%v", srch.Items, err)
	}
	if _, err := s.End("srch-safe", "completed", ""); err != nil {
		t.Fatal(err)
	}
	waitForReleaseTurnTrue(t, s, scope.Turn)
	_ = os.Remove(filepath.Join(s.root, "junc"))
}

func TestExecFourTierStopAndReleaseCleanupFailure(t *testing.T) {
	s, scope := testTurnService(t)
	for i := 0; i < 80; i++ {
		if err := os.WriteFile(filepath.Join(s.root, fmt.Sprintf("f%03d.txt", i)), []byte("needle line\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	testBeginTurnCall(t, s, scope, "srch-cancel", "b-sc", 0, "search_files", nil, "", 0)
	ctxCanceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	t0 := time.Now()
	if _, err := s.SearchWithCallContext(ctxCanceled, "srch-cancel", "", "needle", "", true, true); Code(err) != "cancelled" || time.Since(t0) > 200*time.Millisecond {
		t.Fatalf("expected immediate cancelled on SearchWithCallContext, got err=%v in %v", err, time.Since(t0))
	}
	if _, err := s.End("srch-cancel", "cancelled", "cancelled"); err != nil {
		t.Fatal(err)
	}

	testBeginTurnCall(t, s, scope, "fg-cancel", "b-fgc", 0, "exec_command", nil, "sleep", 0)
	fgErr := make(chan error, 1)
	go func() {
		_, err := s.StartExec(context.Background(), ExecStartRequest{
			CallID: "fg-cancel", Command: `powershell -NoProfile -Command "Start-Sleep -Seconds 20"`,
		})
		fgErr <- err
	}()
	time.Sleep(150 * time.Millisecond)
	t0 = time.Now()
	if err := s.CancelCall("fg-cancel", "cancelled"); err != nil {
		t.Fatal(err)
	}
	if err := <-fgErr; Code(err) != "cancelled" || time.Since(t0) > 2*time.Second {
		t.Fatalf("expected fg-cancel in <2s, got %v in %v", err, time.Since(t0))
	}
	if _, err := s.End("fg-cancel", "cancelled", "cancelled"); err != nil {
		t.Fatal(err)
	}

	markerFile := filepath.Join(s.root, "grandchild-alive.txt")
	bgCmd := `powershell -NoProfile -Command "$p = Start-Process -FilePath 'powershell.exe' -ArgumentList '-NoProfile','-Command','Start-Sleep -Seconds 30; Set-Content grandchild-alive.txt yes' -PassThru; Start-Sleep -Seconds 30"`
	bgID := startDetachedScriptForTest(t, s, scope, "tree-bg", bgCmd, 50)

	if err := s.ReleaseTurn(scope.Turn, false); Code(err) != "cleanup_pending" {
		t.Fatalf("expected cleanup_pending on ReleaseTurn(turn, false), got %v", err)
	}
	if op, err := s.Operation(bgID); err != nil || op.State != "started" {
		t.Fatalf("ReleaseTurn(false) should not kill detached session, got %+v err=%v", op, err)
	}

	t0 = time.Now()
	s.CancelTurn(scope.Turn)
	waitForReleaseTurnTrue(t, s, scope.Turn)
	if time.Since(t0) > 2500*time.Millisecond {
		t.Fatalf("expected CancelTurn+ReleaseTurn in <2.5s, took %v", time.Since(t0))
	}
	if _, err := os.Stat(markerFile); !os.IsNotExist(err) {
		t.Fatal("grandchild survived CancelTurn!")
	}

	// Cleanup failure during ReleaseTurn(turn, true) releases s.turn AND sets s.blocked = true.
	s2, scope2 := testTurnService(t)
	s2.exec.mu.Lock()
	lockPath := filepath.Join(s2.exec.tempRoot, "locked.tmp")
	s2.exec.mu.Unlock()
	if err := os.WriteFile(lockPath, []byte("lock"), 0600); err != nil {
		t.Fatal(err)
	}
	lockedHandle, err := os.Open(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer lockedHandle.Close()

	if err := s2.ReleaseTurn(scope2.Turn, true); err != nil {
		t.Fatalf("ReleaseTurn(true) with cleanup failure should still release turn, got %v", err)
	}
	_ = lockedHandle.Close()
	if s2.busyLocked() {
		t.Fatal("expected turn released even on cleanup failure")
	}
	if st := s2.Status(); st.Ready || st.Reason != "reconciliation_required" {
		t.Fatalf("expected blocked status after ReleaseTurn cleanup failure, got %+v", st)
	}
}

func TestDetachedExitCleansSandboxTempBeforeTurnRelease(t *testing.T) {
	s, scope := testTurnService(t)
	defer func() {
		s.CancelTurn(scope.Turn)
		waitForReleaseTurnTrue(t, s, scope.Turn)
	}()

	cmd := `powershell -NoProfile -Command "Set-Content -Path (Join-Path $env:TEMP 'bg-junk.tmp') -Value 'temp-data'; Start-Sleep -Milliseconds 150; Write-Output 'bg-done'"`
	callID := startDetachedScriptForTest(t, s, scope, "bg-temp-clean", cmd, 10)

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		op, err := s.Operation(callID)
		if err == nil && op.State == "completed" {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	op, err := s.Operation(callID)
	if err != nil || op.State != "completed" {
		t.Fatalf("expected detached session completed, got op=%+v err=%v", op, err)
	}
	s.exec.mu.Lock()
	tmpDir := s.exec.tempRoot
	s.exec.mu.Unlock()
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("ReadDir(tempRoot): %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected sandbox-temp cleaned when sole detached session exited, found %d entries", len(entries))
	}
}

func TestKillExecSessionIDRequiresActiveCallAndBlockedGate(t *testing.T) {
	s, scope := testTurnService(t)
	defer func() {
		s.CancelTurn(scope.Turn)
		waitForReleaseTurnTrue(t, s, scope.Turn)
	}()

	bgCmd := `powershell -NoProfile -Command "Start-Sleep -Seconds 20"`
	sessionID := startDetachedScriptForTest(t, s, scope, "kill-auth-bg", bgCmd, 20)

	if _, err := s.KillExec(ExecKillRequest{CallID: "", SessionID: sessionID}); Code(err) != "invalid_call" {
		t.Fatalf("empty CallID KillExec = %v, want invalid_call", err)
	}
	testBeginTurnCall(t, s, scope, "wrong-read", "b-wr", 0, "read_file", []Target{{Path: "ok.txt"}}, "", 0)
	if _, err := s.KillExec(ExecKillRequest{CallID: "wrong-read", SessionID: sessionID}); Code(err) != "invalid_call" {
		t.Fatalf("mismatched tool KillExec = %v, want invalid_call", err)
	}
	_, _ = s.End("wrong-read", "completed", "")

	testBeginTurnCall(t, s, scope, "canceled-ctl", "b-cc", 0, "process_control", nil, "kill", 0)
	_ = s.CancelCall("canceled-ctl", "cancelled")
	if _, err := s.KillExec(ExecKillRequest{CallID: "canceled-ctl", SessionID: sessionID}); Code(err) != "cancelled" {
		t.Fatalf("cancelled CallID KillExec = %v, want cancelled", err)
	}
	_, _ = s.End("canceled-ctl", "cancelled", "cancelled")

	testBeginTurnCall(t, s, scope, "valid-ctl", "b-vc", 0, "process_control", nil, "kill", 0)
	s.mu.Lock()
	s.blocked = true
	s.mu.Unlock()
	if _, err := s.KillExec(ExecKillRequest{CallID: "valid-ctl", SessionID: sessionID}); Code(err) != "reconciliation_required" {
		t.Fatalf("blocked service KillExec = %v, want reconciliation_required", err)
	}
	s.mu.Lock()
	s.blocked = false
	s.mu.Unlock()

	if res, err := s.KillExec(ExecKillRequest{CallID: "valid-ctl", SessionID: sessionID}); err != nil || (res.Status != "cancelled" && res.Status != "failed") {
		t.Fatalf("authorized KillExec failed: res=%+v err=%v", res, err)
	}
	if _, err := s.End("valid-ctl", "completed", ""); err != nil {
		t.Fatal(err)
	}
}

func TestGoExecFallbackRedactsInlineSecrets(t *testing.T) {
	s, scope := testTurnService(t)
	defer func() {
		s.CancelTurn(scope.Turn)
		waitForReleaseTurnTrue(t, s, scope.Turn)
	}()

	cmd := `Start-Sleep -Milliseconds 150; Write-Output "--token tok123 --api-key=sk-live -u alice:secret99 https://bob:pw88@example.com/hook"`
	testBeginTurnCall(t, s, scope, "redact-cmd", "b-redact", 0, "exec_command", nil, "", 0)
	y := 10
	res, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "redact-cmd", Command: cmd, YieldMs: &y,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.End("redact-cmd", "completed", ""); err != nil {
		t.Fatal(err)
	}
	if res.Status != "running" || res.SessionID == "" {
		t.Fatalf("expected detached running session, got %+v", res)
	}
	var op Operation
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		op, err = s.Operation(res.SessionID)
		if err == nil && op.State == "completed" {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if err != nil || op.State != "completed" {
		t.Fatalf("expected detached operation completed, got op=%+v err=%v", op, err)
	}
	for _, secret := range []string{"tok123", "sk-live", "secret99", "pw88"} {
		if strings.Contains(op.Summary, secret) {
			t.Fatalf("secret %q leaked into Operation.Summary: %q", secret, op.Summary)
		}
	}
	for _, want := range []string{"--token [REDACTED]", "--api-key=[REDACTED]", "-u alice:[REDACTED]", "https://bob:[REDACTED]@example.com/hook"} {
		if !strings.Contains(op.Summary, want) {
			t.Fatalf("expected %q in redacted summary %q", want, op.Summary)
		}
	}
}

func TestExecSessionLimitCountsActiveNotCumulative(t *testing.T) {
	s, scope := testTurnService(t)
	defer func() {
		s.CancelTurn(scope.Turn)
		waitForReleaseTurnTrue(t, s, scope.Turn)
	}()

	// Part A: More than MaxExecSessionsPerTurn sequential short commands in one turn must all succeed.
	for i := 0; i < MaxExecSessionsPerTurn+2; i++ {
		callID := fmt.Sprintf("seq-%d", i)
		testBeginTurnCall(t, s, scope, callID, "b-"+callID, 0, "exec_command", nil, "seq", 0)
		res, err := s.StartExec(context.Background(), ExecStartRequest{
			CallID: callID, Command: fmt.Sprintf(`Write-Output "seq-%d"`, i),
		})
		if err != nil {
			t.Fatalf("sequential StartExec #%d failed unexpectedly: %v", i, err)
		}
		if res.Status != "completed" || !strings.Contains(res.Output, fmt.Sprintf("seq-%d", i)) {
			t.Fatalf("sequential StartExec #%d unexpected result: %+v", i, res)
		}
		if _, err := s.End(callID, "completed", ""); err != nil {
			t.Fatal(err)
		}
	}

	// Part B: MaxExecSessionsPerTurn (8) concurrent active sessions must succeed; 9th must hit exec_session_limit.
	bgIDs := make([]string, 0, MaxExecSessionsPerTurn)
	for i := 0; i < MaxExecSessionsPerTurn; i++ {
		id := fmt.Sprintf("bg-slot-%d", i)
		bgIDs = append(bgIDs, startDetachedScriptForTest(t, s, scope, id, `Start-Sleep -Seconds 30`, 10))
	}
	testBeginTurnCall(t, s, scope, "bg-over", "b-bg-over", 0, "exec_command", nil, "overflow", 0)
	if _, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "bg-over", Command: `Write-Output "should-fail"`,
	}); Code(err) != "exec_session_limit" {
		t.Fatalf("9th concurrent StartExec = %v, want exec_session_limit", err)
	}
	if _, err := s.End("bg-over", "failed", "exec_session_limit"); err != nil {
		t.Fatal(err)
	}

	// Part C: Killing one background session must immediately free its slot for the next exec_command.
	testBeginTurnCall(t, s, scope, "kill-one", "b-kill-one", 0, "process_control", nil, "kill", 0)
	if res, err := s.KillExec(ExecKillRequest{CallID: "kill-one", SessionID: bgIDs[0]}); err != nil || (res.Status != "cancelled" && res.Status != "failed") {
		t.Fatalf("KillExec slot[0] failed: res=%+v err=%v", res, err)
	}
	if _, err := s.End("kill-one", "completed", ""); err != nil {
		t.Fatal(err)
	}

	testBeginTurnCall(t, s, scope, "reuse-slot", "b-reuse", 0, "exec_command", nil, "reuse", 0)
	res, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "reuse-slot", Command: `Write-Output "slot-reused"`,
	})
	if err != nil {
		t.Fatalf("StartExec after KillExec slot release failed: %v", err)
	}
	if res.Status != "completed" || !strings.Contains(res.Output, "slot-reused") {
		t.Fatalf("unexpected reuse-slot result: %+v", res)
	}
	if _, err := s.End("reuse-slot", "completed", ""); err != nil {
		t.Fatal(err)
	}
}

func TestExecSensitivePathDeniedPropagatesErrorCode(t *testing.T) {
	homeRoot := t.TempDir()
	rtDir := filepath.Join(homeRoot, "conversations", "rt-1")
	wsDir := filepath.Join(rtDir, "workspace")
	s, err := Open(wsDir, filepath.Join(rtDir, "file-versions"), "workspace-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Configure(true, "policy-one"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.RuntimeStopped() })
	s.SetHomeRoot(homeRoot)
	scope := Scope{Conversation: "conv-exec-err", Turn: "turn-exec-err", Policy: "policy-one"}
	if err := s.AcquireTurnScope(scope); err != nil {
		t.Fatal(err)
	}
	defer waitForReleaseTurnTrue(t, s, scope.Turn)

	denyCases := []struct {
		id   string
		req  ExecStartRequest
		want string
	}{
		{"deny-home", ExecStartRequest{CallID: "deny-home", Command: `Get-ChildItem "` + homeRoot + `"`}, "sensitive_path_denied"},
		{"deny-convs", ExecStartRequest{CallID: "deny-convs", Command: `Get-ChildItem "` + filepath.Join(homeRoot, "conversations") + `"`}, "sensitive_path_denied"},
		{"deny-env", ExecStartRequest{CallID: "deny-env", Command: `Write-Output "hi"`, Env: map[string]string{"NODE_OPTIONS": "--inspect"}}, "dangerous_env_denied"},
	}
	for _, tc := range denyCases {
		testBeginTurnCall(t, s, scope, tc.id, "b-"+tc.id, 0, "exec_command", nil, "script", 0)
		_, err := s.StartExec(context.Background(), tc.req)
		if got := Code(err); got != tc.want {
			t.Fatalf("%s: Code(err) = %q (%v), want %q", tc.id, got, err, tc.want)
		}
		_, _ = s.End(tc.id, "failed", tc.want)
	}

	okFile := filepath.Join(wsDir, "ok.txt")
	if err := os.WriteFile(okFile, []byte("ok-in-ws\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testBeginTurnCall(t, s, scope, "allow-ws-file", "b-allow", 0, "exec_command", nil, "script", 0)
	res, err := s.StartExec(context.Background(), ExecStartRequest{CallID: "allow-ws-file", Command: `Get-Content "` + okFile + `"`})
	if err != nil || res.Status != "completed" || !strings.Contains(res.Output, "ok-in-ws") {
		t.Fatalf("allow-ws-file: res=%+v err=%v", res, err)
	}
	if _, err := s.End("allow-ws-file", "completed", ""); err != nil {
		t.Fatal(err)
	}

	tSrv, err := Serve(s)
	if err != nil {
		t.Fatal(err)
	}
	defer tSrv.Close()
	testBeginTurnCall(t, s, scope, "http-deny", "b-http", 0, "exec_command", nil, "script", 0)
	body, _ := json.Marshal(map[string]any{"action": "exec/start", "id": "http-deny", "command": `Get-ChildItem "` + homeRoot + `"`})
	req, err := http.NewRequest(http.MethodPost, tSrv.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tSrv.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || payload["error"] != "sensitive_path_denied" {
		t.Fatalf("HTTP exec/start: status=%d payload=%+v", resp.StatusCode, payload)
	}
	_, _ = s.End("http-deny", "failed", payload["error"])
}

func TestExecDetachedSurvivesHTTPStartRequestEnd(t *testing.T) {
	s, scope := testTurnService(t)
	defer waitForReleaseTurnTrue(t, s, scope.Turn)
	tSrv, err := Serve(s)
	if err != nil {
		t.Fatal(err)
	}
	defer tSrv.Close()

	postExecJSON := func(payload map[string]any) ExecResult {
		t.Helper()
		raw, _ := json.Marshal(payload)
		req, err := http.NewRequest(http.MethodPost, tSrv.URL, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tSrv.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct {
			Value ExecResult `json:"value"`
			Error string     `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK || out.Error != "" {
			t.Fatalf("HTTP %v: status=%d err=%q", payload["action"], resp.StatusCode, out.Error)
		}
		return out.Value
	}

	verifyDetach := func(tag string, yieldMs int, cmd, wantOut string) {
		startID := "http-bg-" + tag
		testBeginTurnCall(t, s, scope, startID, "b-"+startID, 0, "exec_command", nil, "script", 0)
		started := postExecJSON(map[string]any{"action": "exec/start", "id": startID, "yield_ms": yieldMs, "command": cmd})
		if started.Status != "running" || started.SessionID == "" {
			t.Fatalf("%s start: %+v", tag, started)
		}
		if _, err := s.End(startID, "completed", ""); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
		var polled ExecResult
		var outBuf strings.Builder
		for attempt := 0; attempt < 5; attempt++ {
			pollID := fmt.Sprintf("http-poll-%s-%d", tag, attempt)
			testBeginTurnCall(t, s, scope, pollID, "b-"+pollID, 0, "process_control", nil, "process", 0)
			polled = postExecJSON(map[string]any{"action": "exec/poll", "id": pollID, "session_id": started.SessionID, "wait_ms": 5000})
			outBuf.WriteString(polled.Output)
			if _, err := s.End(pollID, "completed", ""); err != nil {
				t.Fatal(err)
			}
			if polled.Status != "running" {
				break
			}
		}
		if polled.Status != "completed" || !strings.Contains(outBuf.String(), wantOut) {
			t.Fatalf("%s poll: %+v out=%q", tag, polled, outBuf.String())
		}
	}

	verifyDetach("zero", 0, "Start-Sleep -Milliseconds 300; Set-Content -LiteralPath bg0.txt -Value 'bg0-ok'; Write-Output 'bg0-done'", "bg0-done")
	if data, err := os.ReadFile(filepath.Join(s.Root(), "bg0.txt")); err != nil || !strings.Contains(string(data), "bg0-ok") {
		t.Fatalf("Case A bg0.txt: %q err=%v", data, err)
	}
	verifyDetach("yield", 80, "Start-Sleep -Milliseconds 350; Write-Output 'yield-done'", "yield-done")

	testBeginTurnCall(t, s, scope, "fg-ctx-cancel", "b-fg-cancel", 0, "exec_command", nil, "script", 0)
	fgCtx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err = s.StartExec(fgCtx, ExecStartRequest{CallID: "fg-ctx-cancel", Command: "Start-Sleep -Seconds 10"}); Code(err) != "cancelled" {
		t.Fatalf("Case C cancel: got err=%v (code=%q), want cancelled", err, Code(err))
	}
	_, _ = s.End("fg-ctx-cancel", "cancelled", "cancelled")
}
