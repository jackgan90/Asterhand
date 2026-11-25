package workers

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/harnesses"
)

func runGitInit(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s failed: %v (%s)", dir, err, string(out))
	}
}

// When ForceKill fails (stop unconfirmed), the turn must enter
// reconciliation_required and retain the worktree write lock until resolved.
func TestForceKillFailureEntersReconciliationAndRetainsWriteLock(t *testing.T) {
	started := make(chan struct{})
	var failKill atomic.Bool
	failKill.Store(true)

	fa := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			select {
			case <-started:
			default:
				close(started)
			}
			// Never completes on InterruptTurn so waitStopOrKill falls through to ForceKill.
			return "nt_unkillable", make(chan TurnOutcome, 1), nil
		},
		forceKillFn: func(handle ThreadHandle) error {
			if failKill.Load() {
				return ErrProcessStopUnconfirmed
			}
			return nil
		},
	}

	svc, hs, ws := setupTestService(t, fa)
	enableWrite := true
	if _, err := hs.UpdatePolicy(harnesses.UpdatePolicyInput{HarnessID: harnesses.HarnessCodex, AllowWorkspaceWrite: &enableWrite}); err != nil {
		t.Fatal(err)
	}
	svc.SetBudgets(Budgets{InterruptGrace: 40 * time.Millisecond})

	w1, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_kill_lock_c1", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath,
		AccessMode: harnesses.AccessWorkspaceWrite, Alias: "writer-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	w2, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_kill_lock_c2", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath,
		AccessMode: harnesses.AccessWorkspaceWrite, Alias: "writer-2",
	})
	if err != nil {
		t.Fatal(err)
	}

	t1, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_kill_lock_s1", WorkerID: w1.ID, Prompt: "edit files",
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started

	cancelledTurn, err := svc.Cancel(context.Background(), t1.ID, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelledTurn.State != TurnReconcile || cancelledTurn.ErrorCode != "process_stop_unconfirmed" {
		t.Fatalf("expected reconciliation_required/process_stop_unconfirmed, got state=%s code=%s", cancelledTurn.State, cancelledTurn.ErrorCode)
	}

	// Closing a worker in reconciliation_required must be rejected.
	if _, err := svc.CloseWorker(context.Background(), w1.ID, ws.ID); err == nil || !strings.Contains(err.Error(), "reconciliation_required") {
		t.Fatalf("expected CloseWorker rejected while reconciliation_required, got %v", err)
	}

	// Second writer on the same worktree MUST still be blocked by retained write lock!
	if _, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_kill_lock_s2_blocked", WorkerID: w2.ID, Prompt: "conflicting write",
	}); err == nil || !strings.Contains(err.Error(), "worktree_busy") {
		t.Fatalf("expected worktree_busy while unconfirmed write turn is retained, got %v", err)
	}

	// After explicit human/operator ResolveTurn, the retained write lock is released.
	if _, err := svc.ResolveTurn(t1.ID, "human:test", "Verified process exited manually"); err != nil {
		t.Fatalf("ResolveTurn failed: %v", err)
	}

	failKill.Store(false)
	t2, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_kill_lock_s2_ok", WorkerID: w2.ID, Prompt: "safe write after resolve",
	})
	if err != nil {
		t.Fatalf("expected second write worker admitted after ResolveTurn, got %v", err)
	}
	_, _ = svc.Cancel(context.Background(), t2.ID, ws.ID)
}

// Write locks must re-evaluate current worktree identity and path overlap
// at Send time, blocking concurrent writes after parent git init or on nested
// non-Git directories.
func TestWriteLockRevalidatesWorktreeAfterParentGitInitAndPathOverlap(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{}, 2)
	fa := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			started <- struct{}{}
			ch := make(chan TurnOutcome, 1)
			go func() {
				<-gate
				ch <- TurnOutcome{
					State:        TurnCompleted,
					NativeTurnID: "nt_w",
					Items:        []NativeItem{{ItemID: "a", Type: "agentMessage", Phase: "final_answer", Text: "ok"}},
				}
			}()
			return "nt_w", ch, nil
		},
	}

	svc, hs, _ := setupTestService(t, fa)
	enableWrite := true
	if _, err := hs.UpdatePolicy(harnesses.UpdatePolicyInput{HarnessID: harnesses.HarnessCodex, AllowWorkspaceWrite: &enableWrite}); err != nil {
		t.Fatal(err)
	}
	parentDir := t.TempDir()
	subDir := filepath.Join(parentDir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// 1) Create sub-dir write worker BEFORE parent git init.
	wSub, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_wt_sub", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: subDir,
		AccessMode: harnesses.AccessWorkspaceWrite, Alias: "sub-writer",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 2) Case A: Non-Git parent/child overlap while wSub is running.
	wParent, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_wt_parent", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: parentDir,
		AccessMode: harnesses.AccessWorkspaceWrite, Alias: "parent-writer",
	})
	if err != nil {
		t.Fatal(err)
	}
	t1, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_wt_s1", WorkerID: wSub.ID, Prompt: "write sub",
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started

	if _, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_wt_s2_overlap", WorkerID: wParent.ID, Prompt: "write parent",
	}); err == nil || !strings.Contains(err.Error(), "worktree_busy") {
		t.Fatalf("expected worktree_busy for overlapping non-Git parent/child dirs, got %v", err)
	}

	// 3) Case B: Parent directory runs `git init` while wSub is already running
	// (and create another sibling dir worker inside the new Git repo).
	if err := os.MkdirAll(filepath.Join(parentDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	siblingDir := filepath.Join(parentDir, "sibling")
	if err := os.MkdirAll(siblingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wSibling, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_wt_sib", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: siblingDir,
		AccessMode: harnesses.AccessWorkspaceWrite, Alias: "sib-writer",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Sibling and sub are NOT path-overlapping, so ONLY live WorktreeKey re-evaluation catches this!
	if _, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_wt_s3_git", WorkerID: wSibling.ID, Prompt: "write sibling in same new git repo",
	}); err == nil || !strings.Contains(err.Error(), "worktree_busy") {
		t.Fatalf("expected worktree_busy for sibling worker after parent git init, got %v", err)
	}

	close(gate)
	if _, _, _, err := svc.Wait(context.Background(), t1.ID, "", 3*time.Second); err != nil {
		t.Fatal(err)
	}
}

// A retained write lock from process_stop_unconfirmed must survive Service
// restart and continue blocking conflicting writes until ResolveTurn.
func TestRetainedWriteLockSurvivesServiceRestart(t *testing.T) {
	home := t.TempDir()
	wsDir := t.TempDir()
	hs, err := harnesses.Open(home, "asterhand.exe")
	if err != nil {
		t.Fatal(err)
	}
	tr := true
	if _, err := hs.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessCodex,
		CollabWorkerEnabled: &tr,
		AllowWorkspaceWrite: &tr,
	}); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	fa1 := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			close(started)
			return "nt_restart_lock", make(chan TurnOutcome, 1), nil
		},
		forceKillFn: func(handle ThreadHandle) error {
			return ErrProcessStopUnconfirmed
		},
	}
	svc1, err := NewService(home, hs, fa1)
	if err != nil {
		t.Fatal(err)
	}
	svc1.SetBudgets(Budgets{InterruptGrace: 30 * time.Millisecond})

	w1, err := svc1.Create(context.Background(), CreateInput{
		OperationID: "op_restart_lock_w1", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: wsDir,
		AccessMode: harnesses.AccessWorkspaceWrite, Alias: "w1",
	})
	if err != nil {
		t.Fatal(err)
	}
	w2, err := svc1.Create(context.Background(), CreateInput{
		OperationID: "op_restart_lock_w2", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: wsDir,
		AccessMode: harnesses.AccessWorkspaceWrite, Alias: "w2",
	})
	if err != nil {
		t.Fatal(err)
	}
	t1, err := svc1.Send(context.Background(), SendInput{
		OperationID: "op_restart_lock_s1", WorkerID: w1.ID, Prompt: "write",
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	ct, err := svc1.Cancel(context.Background(), t1.ID, "")
	if err != nil || ct.State != TurnReconcile || !ct.WriteLockRetained {
		t.Fatalf("expected reconciliation_required with WriteLockRetained=true, got %+v err=%v", ct, err)
	}
	if err := svc1.Close(context.Background()); err != nil {
		t.Fatalf("svc1.Close: %v", err)
	}

	// Recreate Service against the SAME home/DB.
	fa2 := &fakeAdapter{}
	svc2, err := NewService(home, hs, fa2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc2.Close(context.Background()) }()

	// Conflicting write worker MUST still be blocked after restart!
	if _, err := svc2.Send(context.Background(), SendInput{
		OperationID: "op_restart_lock_s2_blocked", WorkerID: w2.ID, Prompt: "write after restart",
	}); err == nil || !strings.Contains(err.Error(), "worktree_busy") {
		t.Fatalf("expected worktree_busy after restart while retained lock is unresolved, got %v", err)
	}

	// Resolving the turn clears persisted WriteLockRetained and unlocks the worktree.
	resolved, err := svc2.ResolveTurn(t1.ID, "human:test", "Confirmed exited")
	if err != nil || resolved.WriteLockRetained {
		t.Fatalf("ResolveTurn failed or kept WriteLockRetained: %+v err=%v", resolved, err)
	}
	t2, err := svc2.Send(context.Background(), SendInput{
		OperationID: "op_restart_lock_s2_ok", WorkerID: w2.ID, Prompt: "write after resolve",
	})
	if err != nil {
		t.Fatalf("expected write admitted after ResolveTurn, got %v", err)
	}
	if _, _, _, err := svc2.Wait(context.Background(), t2.ID, "", 3*time.Second); err != nil {
		t.Fatal(err)
	}
}

// A retained write lock must keep its dispatch-time WorktreeKey across
// service restarts even if a nested git repo is initialized inside the locked
// directory before restart.
func TestRestartPreservesOriginalWorktreeLockAcrossTopologyChange(t *testing.T) {
	home := t.TempDir()
	repoRoot := t.TempDir()
	runGitInit(t, repoRoot)

	dirA := filepath.Join(repoRoot, "dirA")
	dirB := filepath.Join(repoRoot, "dirB")
	if err := os.MkdirAll(dirA, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dirB, 0755); err != nil {
		t.Fatal(err)
	}

	hs, err := harnesses.Open(home, "")
	if err != nil {
		t.Fatal(err)
	}
	fakeExe := filepath.Join(home, "codex.exe")
	if err := os.WriteFile(fakeExe, []byte("stub"), 0600); err != nil {
		t.Fatal(err)
	}
	enable := true
	if _, err := hs.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessCodex,
		CollabWorkerEnabled: &enable,
		AllowWorkspaceWrite: &enable,
		ManualPath:          &fakeExe,
	}); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	fa1 := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			close(started)
			return "nt_topo_a", make(chan TurnOutcome, 1), nil
		},
		forceKillFn: func(handle ThreadHandle) error {
			return ErrProcessStopUnconfirmed
		},
	}

	svc1, err := NewService(home, hs, fa1)
	if err != nil {
		t.Fatal(err)
	}
	svc1.SetBudgets(Budgets{InterruptGrace: 20 * time.Millisecond})

	wA, err := svc1.Create(context.Background(), CreateInput{
		OperationID: "op_topo_wa", HarnessID: harnesses.HarnessCodex,
		WorkspaceRoot: dirA, Alias: "wa", AccessMode: harnesses.AccessWorkspaceWrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	wB, err := svc1.Create(context.Background(), CreateInput{
		OperationID: "op_topo_wb", HarnessID: harnesses.HarnessCodex,
		WorkspaceRoot: dirB, Alias: "wb", AccessMode: harnesses.AccessWorkspaceWrite,
	})
	if err != nil {
		t.Fatal(err)
	}

	tA, err := svc1.Send(context.Background(), SendInput{
		OperationID: "op_topo_sa", WorkerID: wA.ID, Prompt: "write in A",
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started

	ct, err := svc1.Cancel(context.Background(), tA.ID, wA.WorkspaceID)
	if err != nil || ct.State != TurnReconcile || !ct.WriteLockRetained {
		t.Fatalf("expected reconciliation_required with WriteLockRetained=true, got %+v err=%v", ct, err)
	}
	origLockKey := ct.WriteLockKey
	if origLockKey == "" {
		t.Fatal("expected non-empty persisted WriteLockKey")
	}

	// Mutate topology: initialize an independent git repo inside dirA.
	runGitInit(t, dirA)

	// Before restart: sibling dirB must still be blocked by dirA's original repo lock.
	if _, err := svc1.Send(context.Background(), SendInput{
		OperationID: "op_topo_sb_before", WorkerID: wB.ID, Prompt: "write in B before restart",
	}); err == nil || !strings.Contains(err.Error(), "worktree_busy") {
		t.Fatalf("expected worktree_busy before restart, got %v", err)
	}

	if err := svc1.Close(context.Background()); err != nil {
		t.Fatalf("svc1.Close: %v", err)
	}

	// Restart service on same DB: lock scope must NOT shrink to dirA's new git root.
	svc2, err := NewService(home, hs, &fakeAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc2.Close(context.Background()) }()

	reloadedA, err := svc2.GetTurn(tA.ID, wA.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if reloadedA.WriteLockKey != origLockKey {
		t.Fatalf("expected persisted WriteLockKey %q preserved across restart, got %q", origLockKey, reloadedA.WriteLockKey)
	}
	if _, err := svc2.Send(context.Background(), SendInput{
		OperationID: "op_topo_sb_after", WorkerID: wB.ID, Prompt: "write in B after restart",
	}); err == nil || !strings.Contains(err.Error(), "worktree_busy") {
		t.Fatalf("expected worktree_busy after restart despite nested git init in dirA, got %v", err)
	}
}
