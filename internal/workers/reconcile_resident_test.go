package workers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/harnesses"
)

type mockResidentInstance struct {
	id     string
	closed atomic.Bool
}

type dynamicPayloadRef struct {
	Tag     string
	Payload any
}

// ReconcileTurn on a revoked turn whose native turn is STILL running must
// NOT mark it cancelled_authorization_revoked or release the retained write lock.
func TestReconcileRevokedTurnWhileNativeStillRunningKeepsLock(t *testing.T) {
	started := make(chan struct{})
	var nativeStatus atomic.Value
	nativeStatus.Store(TurnRunning)
	var corrID atomic.Value

	fa := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			corrID.Store(spec.ClientCorrelationID)
			close(started)
			return "nt_rev_run", make(chan TurnOutcome, 1), nil
		},
		forceKillFn: func(handle ThreadHandle) error {
			return ErrProcessStopUnconfirmed
		},
		inspectFn: func(ctx context.Context, handle ThreadHandle) ([]NativeTurnRecord, error) {
			cid, _ := corrID.Load().(string)
			st, _ := nativeStatus.Load().(string)
			return []NativeTurnRecord{{
				NativeTurnID: "nt_rev_run",
				Status:       st,
				Items:        []NativeItem{{ItemID: "u1", Type: "userMessage", ClientID: cid}},
			}}, nil
		},
	}

	svc, hs, ws := setupTestService(t, fa)
	tr := true
	if _, err := hs.UpdatePolicy(harnesses.UpdatePolicyInput{HarnessID: harnesses.HarnessCodex, AllowWorkspaceWrite: &tr}); err != nil {
		t.Fatal(err)
	}
	svc.SetBudgets(Budgets{InterruptGrace: 30 * time.Millisecond})

	w1, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_rev_run_w1", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath,
		AccessMode: harnesses.AccessWorkspaceWrite, Alias: "rev-w1",
	})
	if err != nil {
		t.Fatal(err)
	}
	w2, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_rev_run_w2", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath,
		AccessMode: harnesses.AccessWorkspaceWrite, Alias: "rev-w2",
	})
	if err != nil {
		t.Fatal(err)
	}
	t1, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_rev_run_s1", WorkerID: w1.ID, Prompt: "write",
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started

	// Revoke write authorization -> ForceKill fails -> enters reconciliation_required + retained lock.
	svc.RevokeWorkerWriteHarness(harnesses.HarnessCodex)
	mid, _, _, err := svc.Wait(context.Background(), t1.ID, ws.ID, 3*time.Second)
	if err != nil || mid.State != TurnReconcile || !mid.RevocationRequested {
		t.Fatalf("expected reconciliation_required with revocation_requested=true, got %+v err=%v", mid, err)
	}

	// Reconcile while native status is still "running": MUST stay reconciliation_required and KEEP lock!
	rec1, err := svc.ReconcileTurn(context.Background(), t1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec1.State != TurnReconcile {
		t.Fatalf("ReconcileTurn finalized still-running revoked turn as %s!", rec1.State)
	}
	if _, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_rev_run_s2_blocked", WorkerID: w2.ID, Prompt: "conflicting write",
	}); err == nil || !strings.Contains(err.Error(), "worktree_busy") {
		t.Fatalf("expected worktree_busy while native revoked turn is still running, got %v", err)
	}

	// Once native turn actually reaches terminal state, ReconcileTurn marks cancelled_authorization_revoked and unlocks.
	nativeStatus.Store(TurnCompleted)
	rec2, err := svc.ReconcileTurn(context.Background(), t1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec2.State != TurnCancelledRevoked || rec2.AnswerAvailable || rec2.WriteLockRetained {
		t.Fatalf("expected cancelled_authorization_revoked without answer/lock, got %+v", rec2)
	}
}

// A late ReconcileTurn completing after a human already resolved the old
// turn and a new turn started on the same worker must never overwrite the
// manual resolution, mark the worker idle, or evict the new turn's resident.
func TestLateReconcileDoesNotOverwriteManualResolveOrNewTurn(t *testing.T) {
	inspectEntered := make(chan struct{})
	releaseInspect := make(chan struct{})
	t2Started := make(chan struct{})
	releaseT2 := make(chan struct{})
	var closeCalls atomic.Int32
	var startCount atomic.Int32
	var corrT1 string
	var corrMu sync.Mutex

	fa := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			n := startCount.Add(1)
			if n == 1 {
				corrMu.Lock()
				corrT1 = spec.ClientCorrelationID
				corrMu.Unlock()
				return "", nil, errors.New("network disconnect during turn/start")
			}
			ch := make(chan TurnOutcome, 1)
			close(t2Started)
			go func() {
				<-releaseT2
				ch <- TurnOutcome{
					State:        TurnCompleted,
					NativeTurnID: "nt_new_2",
					Items:        []NativeItem{{ItemID: "f2", Type: "agentMessage", Phase: "final_answer", Text: "turn 2 ok"}},
				}
			}()
			return "nt_new_2", ch, nil
		},
		inspectFn: func(ctx context.Context, handle ThreadHandle) ([]NativeTurnRecord, error) {
			close(inspectEntered)
			<-releaseInspect
			corrMu.Lock()
			c1 := corrT1
			corrMu.Unlock()
			return []NativeTurnRecord{{
				NativeTurnID: "nt_old_1",
				Status:       TurnCompleted,
				Items: []NativeItem{
					{ItemID: "u1", Type: "userMessage", ClientID: c1},
					{ItemID: "f1", Type: "agentMessage", Phase: "final_answer", Text: "stale old answer"},
				},
			}}, nil
		},
		closeFn: func(handle ThreadHandle) error {
			closeCalls.Add(1)
			return nil
		},
	}

	svc, _, ws := setupTestService(t, fa)
	defer func() { _ = svc.Close(context.Background()) }()

	w, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_late_w", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	t1, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_late_t1", WorkerID: w.ID, Prompt: "old turn",
	})
	if err != nil {
		t.Fatal(err)
	}
	wt1, _, _, err := svc.Wait(context.Background(), t1.ID, ws.ID, 3*time.Second)
	if err != nil || wt1.State != TurnReconcile {
		t.Fatalf("expected T1 in reconciliation_required, got %+v err=%v", wt1, err)
	}

	// Start ReconcileTurn for T1 and block inside native InspectThreadTurns.
	reconcileDone := make(chan struct {
		turn Turn
		err  error
	}, 1)
	go func() {
		rt, rErr := svc.ReconcileTurn(context.Background(), t1.ID)
		reconcileDone <- struct {
			turn Turn
			err  error
		}{turn: rt, err: rErr}
	}()
	<-inspectEntered

	// Human manually resolves T1 while native inspection is blocked.
	resolved, err := svc.ResolveTurn(t1.ID, "operator", "verified manually")
	if err != nil || resolved.State != TurnFailed || resolved.ErrorCode != "reconciled_manually_closed" {
		t.Fatalf("expected manual resolve, got %+v err=%v", resolved, err)
	}

	// Start a new turn T2 on the same worker.
	t2, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_late_t2", WorkerID: w.ID, Prompt: "new turn",
	})
	if err != nil {
		t.Fatalf("Send T2 failed: %v", err)
	}
	<-t2Started

	// Release stale InspectThreadTurns for T1.
	close(releaseInspect)
	res := <-reconcileDone
	if res.err != nil {
		t.Fatalf("late ReconcileTurn returned unexpected error: %v", res.err)
	}

	// T1 must retain the human manual resolution and audit reason.
	t1After, err := svc.GetTurn(t1.ID, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if t1After.State != TurnFailed || t1After.ErrorCode != "reconciled_manually_closed" || !strings.Contains(t1After.ErrorMessage, "verified manually") {
		t.Fatalf("late ReconcileTurn overwrote manual resolution: %+v", t1After)
	}

	// Worker must still be running T2 (not idle, not cleared active_turn_id).
	wAfter, _, err := svc.Get(w.ID, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wAfter.State != WorkerRunning || wAfter.ActiveTurnID != t2.ID {
		t.Fatalf("late ReconcileTurn corrupted active worker state: %+v (want running %s)", wAfter, t2.ID)
	}

	// T2 finishes cleanly.
	close(releaseT2)
	wt2, _, _, err := svc.Wait(context.Background(), t2.ID, ws.ID, 3*time.Second)
	if err != nil || wt2.State != TurnCompleted {
		t.Fatalf("expected T2 to complete cleanly, got %+v err=%v", wt2, err)
	}
}

// A late ReconcileTurn blocked inside OpenThread must NEVER overwrite
// the resident process registered by a newer turn (even when both handles share
// the exact same Key and NativeSessionID), and must close its own stale handle.
func TestLateReconcileOpenThreadDoesNotOverwriteNewTurnResident(t *testing.T) {
	reconcileOpenEntered := make(chan struct{})
	releaseReconcileOpen := make(chan struct{})
	var openCall atomic.Int32
	var startRefs []string
	var startMu sync.Mutex

	p1 := &mockResidentInstance{id: "initial"}
	pReconcile := &mockResidentInstance{id: "stale_reconcile"}
	pNew := &mockResidentInstance{id: "new_turn"}

	fa := &fakeAdapter{
		openFn: func(ctx context.Context, spec ThreadOpenSpec) (ThreadHandle, error) {
			n := openCall.Add(1)
			sharedKey := spec.WorkerID + ":thr_shared"
			switch n {
			case 1:
				return ThreadHandle{Key: sharedKey, NativeSessionID: "thr_shared", ProcessRef: p1}, nil
			case 2:
				// ReconcileTurn's OpenThread blocks while manual resolve + T2 run.
				close(reconcileOpenEntered)
				<-releaseReconcileOpen
				return ThreadHandle{Key: sharedKey, NativeSessionID: "thr_shared", ProcessRef: pReconcile}, nil
			default:
				return ThreadHandle{Key: sharedKey, NativeSessionID: "thr_shared", ProcessRef: pNew}, nil
			}
		},
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			inst := handle.ProcessRef.(*mockResidentInstance)
			startMu.Lock()
			startRefs = append(startRefs, inst.id)
			startMu.Unlock()
			if inst.id == "initial" {
				// Fail start -> evict p1 and put T1 into reconciliation_required.
				return "", nil, errors.New("start ack lost")
			}
			ch := make(chan TurnOutcome, 1)
			ch <- TurnOutcome{
				State:        TurnCompleted,
				NativeTurnID: "nt_" + inst.id,
				Items:        []NativeItem{{ItemID: "f", Type: "agentMessage", Phase: "final_answer", Text: "ok"}},
			}
			return "nt_" + inst.id, ch, nil
		},
		closeFn: func(handle ThreadHandle) error {
			if inst, ok := handle.ProcessRef.(*mockResidentInstance); ok {
				inst.closed.Store(true)
			}
			return nil
		},
	}

	svc, _, ws := setupTestService(t, fa)
	defer func() { _ = svc.Close(context.Background()) }()

	w, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_late_open_w", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	t1, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_late_open_t1", WorkerID: w.ID, Prompt: "first turn",
	})
	if err != nil {
		t.Fatal(err)
	}
	wt1, _, _, err := svc.Wait(context.Background(), t1.ID, ws.ID, 3*time.Second)
	if err != nil || wt1.State != TurnReconcile {
		t.Fatalf("expected T1 in reconciliation_required, got %+v err=%v", wt1, err)
	}

	// Start ReconcileTurn(T1) and block inside OpenThread.
	recDone := make(chan error, 1)
	go func() {
		_, rErr := svc.ReconcileTurn(context.Background(), t1.ID)
		recDone <- rErr
	}()
	<-reconcileOpenEntered

	// Manually resolve T1 and run T2 (which opens and registers pNew).
	if _, err := svc.ResolveTurn(t1.ID, "operator", "manual close"); err != nil {
		t.Fatal(err)
	}
	t2, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_late_open_t2", WorkerID: w.ID, Prompt: "second turn",
	})
	if err != nil {
		t.Fatal(err)
	}
	wt2, _, _, err := svc.Wait(context.Background(), t2.ID, ws.ID, 3*time.Second)
	if err != nil || wt2.State != TurnCompleted {
		t.Fatalf("expected T2 completed, got %+v err=%v", wt2, err)
	}

	// Unblock stale ReconcileTurn's OpenThread returning pReconcile.
	close(releaseReconcileOpen)
	if rErr := <-recDone; rErr != nil {
		t.Fatalf("stale ReconcileTurn returned error: %v", rErr)
	}

	// Stale pReconcile must be closed immediately; pNew must remain registered.
	if !pReconcile.closed.Load() {
		t.Fatal("expected stale reconcile process to be closed immediately")
	}
	if pNew.closed.Load() {
		t.Fatal("new turn's resident process must NOT be closed by stale reconcile")
	}

	// Next Send (T3) must reuse pNew, NOT pReconcile!
	t3, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_late_open_t3", WorkerID: w.ID, Prompt: "third turn",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.Wait(context.Background(), t3.ID, ws.ID, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	startMu.Lock()
	refs := append([]string(nil), startRefs...)
	startMu.Unlock()
	if len(refs) != 3 || refs[1] != "new_turn" || refs[2] != "new_turn" {
		t.Fatalf("expected T2 and T3 to reuse new_turn resident, got startRefs=%v", refs)
	}

	// CloseWorker must close pNew.
	if _, err := svc.CloseWorker(context.Background(), w.ID, ws.ID); err != nil {
		t.Fatalf("CloseWorker: %v", err)
	}
	if !pNew.closed.Load() {
		t.Fatal("expected CloseWorker to close new_turn resident process")
	}
}

// Reconciling a failed/revoked turn must detach the old resident atomically
// in the same s.mu critical section that marks the worker idle, so a concurrent
// Send cannot reuse the old resident and get interrupted by the old reconcile's
// CloseThread.
func TestReconcileEvictsResidentAtomicallyBeforeNewSend(t *testing.T) {
	inOldClose := make(chan struct{})
	releaseOldClose := make(chan struct{})
	var openCount atomic.Int32
	var startRefs []string
	var startMu sync.Mutex
	var corrT1 string
	var corrMu sync.Mutex

	p1 := &mockResidentInstance{id: "old_proc"}
	p2 := &mockResidentInstance{id: "new_proc"}
	p2Done := make(chan TurnOutcome, 1)

	fa := &fakeAdapter{
		openFn: func(ctx context.Context, spec ThreadOpenSpec) (ThreadHandle, error) {
			n := openCount.Add(1)
			key := spec.WorkerID + ":thr_evict"
			if n == 1 {
				return ThreadHandle{Key: key, NativeSessionID: "thr_evict", ProcessRef: p1}, nil
			}
			return ThreadHandle{Key: key, NativeSessionID: "thr_evict", ProcessRef: p2}, nil
		},
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			inst := handle.ProcessRef.(*mockResidentInstance)
			startMu.Lock()
			startRefs = append(startRefs, inst.id)
			startMu.Unlock()
			if inst.id == "old_proc" && len(startRefs) == 1 {
				corrMu.Lock()
				corrT1 = spec.ClientCorrelationID
				corrMu.Unlock()
				return "nt_evict_1", make(chan TurnOutcome, 1), nil
			}
			return "nt_evict_2", p2Done, nil
		},
		forceKillFn: func(handle ThreadHandle) error {
			return ErrProcessStopUnconfirmed
		},
		inspectFn: func(ctx context.Context, handle ThreadHandle) ([]NativeTurnRecord, error) {
			corrMu.Lock()
			c1 := corrT1
			corrMu.Unlock()
			return []NativeTurnRecord{{
				NativeTurnID: "nt_evict_1",
				Status:       TurnFailed,
				Items:        []NativeItem{{ItemID: "u1", Type: "userMessage", ClientID: c1}},
			}}, nil
		},
		closeFn: func(handle ThreadHandle) error {
			inst := handle.ProcessRef.(*mockResidentInstance)
			if inst.id == "old_proc" {
				select {
				case <-inOldClose:
				default:
					close(inOldClose)
				}
				<-releaseOldClose
			}
			inst.closed.Store(true)
			return nil
		},
	}

	svc, hs, ws := setupTestService(t, fa)
	defer func() { _ = svc.Close(context.Background()) }()
	svc.SetBudgets(Budgets{InterruptGrace: 15 * time.Millisecond})
	enable := true
	if _, err := hs.UpdatePolicy(harnesses.UpdatePolicyInput{HarnessID: harnesses.HarnessCodex, AllowWorkspaceWrite: &enable}); err != nil {
		t.Fatal(err)
	}

	w, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_evict_w", HarnessID: harnesses.HarnessCodex,
		WorkspaceRoot: ws.RootPath, AccessMode: harnesses.AccessWorkspaceWrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	t1, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_evict_s1", WorkerID: w.ID, Prompt: "first turn",
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cur, _ := svc.GetTurn(t1.ID, ws.ID); cur.State == TurnRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := svc.Cancel(context.Background(), t1.ID, ws.ID); err != nil {
		t.Fatal(err)
	}

	// Re-seed p1 as resident (simulating ReconcileTurn opening/reusing a resident before evicting on TurnFailed).
	svc.mu.Lock()
	svc.nextResidentID++
	svc.residents[w.ID] = &residentProc{
		workerID: w.ID, harnessID: w.HarnessID,
		handle:     ThreadHandle{Key: w.ID + ":thr_evict", NativeSessionID: "thr_evict", ProcessRef: p1, instanceID: svc.nextResidentID},
		lastUsedAt: time.Now().UTC(),
	}
	svc.mu.Unlock()

	recDone := make(chan error, 1)
	go func() {
		_, rErr := svc.ReconcileTurn(context.Background(), t1.ID)
		recDone <- rErr
	}()
	<-inOldClose

	// While old reconcile is still inside CloseThread(p1), p1 must ALREADY be
	// detached so a new Send gets p2 and is never killed by old CloseThread(p1).
	t2, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_evict_s2", WorkerID: w.ID, Prompt: "second turn",
	})
	if err != nil {
		close(releaseOldClose)
		t.Fatalf("Send T2 failed: %v", err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cur, _ := svc.GetTurn(t2.ID, ws.ID); cur.State == TurnRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(releaseOldClose)
	if rErr := <-recDone; rErr != nil {
		t.Fatalf("ReconcileTurn: %v", rErr)
	}
	if !p1.closed.Load() {
		t.Fatal("expected old_proc closed by reconcile cleanup")
	}
	if p2.closed.Load() {
		t.Fatal("new_proc must NOT be closed by old reconcile cleanup")
	}
	p2Done <- TurnOutcome{
		State: TurnCompleted, NativeTurnID: "nt_evict_2",
		Items: []NativeItem{{ItemID: "f2", Type: "agentMessage", Phase: "final_answer", Text: "t2 ok"}},
	}
	wt2, _, _, err := svc.Wait(context.Background(), t2.ID, ws.ID, 3*time.Second)
	if err != nil || wt2.State != TurnCompleted {
		t.Fatalf("expected T2 completed without interruption, got %+v err=%v", wt2, err)
	}
	startMu.Lock()
	refs := append([]string(nil), startRefs...)
	startMu.Unlock()
	if len(refs) != 2 || refs[1] != "new_proc" {
		t.Fatalf("expected T2 to use fresh new_proc (not reused old_proc), got %v", refs)
	}
}

// A concurrent flushPendingReconcile losing the race to ResolveTurn
// must never re-attach the already-closed turn back onto the idle Worker.
func TestConcurrentFlushPendingReconcileDoesNotReattachClosedTurn(t *testing.T) {
	home := t.TempDir()
	wsDir := t.TempDir()
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
	fa := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			close(started)
			return "nt_flush_race", make(chan TurnOutcome, 1), nil
		},
		forceKillFn: func(handle ThreadHandle) error { return ErrProcessStopUnconfirmed },
	}
	svc1, err := NewService(home, hs, fa)
	if err != nil {
		t.Fatal(err)
	}
	svc1.SetBudgets(Budgets{InterruptGrace: 15 * time.Millisecond})

	w, err := svc1.Create(context.Background(), CreateInput{
		OperationID: "op_flush_race_w", HarnessID: harnesses.HarnessCodex,
		WorkspaceRoot: wsDir, AccessMode: harnesses.AccessWorkspaceWrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	t1, err := svc1.Send(context.Background(), SendInput{
		OperationID: "op_flush_race_s1", WorkerID: w.ID, Prompt: "write",
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started

	if _, err := svc1.store.db.Exec(`CREATE TRIGGER fail_flush_race BEFORE UPDATE ON turns WHEN NEW.state = 'reconciliation_required' BEGIN SELECT RAISE(ABORT, 'inject_flush_race_fail'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc1.Cancel(context.Background(), t1.ID, w.WorkspaceID); err == nil {
		t.Fatal("expected injected storage failure")
	}
	if _, err := svc1.store.db.Exec(`DROP TRIGGER fail_flush_race;`); err != nil {
		t.Fatal(err)
	}

	// Pause the first ReconcileTurn right inside s.now() at the start of flushPendingReconcile,
	// let ResolveTurn complete, then release ReconcileTurn.
	inFirstNow := make(chan struct{})
	releaseFirstNow := make(chan struct{})
	var hookCalls atomic.Int32
	svc1.now = func() time.Time {
		if hookCalls.Add(1) == 1 {
			close(inFirstNow)
			<-releaseFirstNow
		}
		return time.Now()
	}

	recDone := make(chan error, 1)
	go func() {
		_, rErr := svc1.ReconcileTurn(context.Background(), t1.ID)
		recDone <- rErr
	}()
	<-inFirstNow

	resolved, err := svc1.ResolveTurn(t1.ID, "operator", "closed manually")
	if err != nil || resolved.State != TurnFailed || resolved.ErrorCode != "reconciled_manually_closed" {
		close(releaseFirstNow)
		t.Fatalf("ResolveTurn failed: %+v err=%v", resolved, err)
	}

	close(releaseFirstNow)
	if rErr := <-recDone; rErr != nil {
		t.Fatalf("concurrent ReconcileTurn returned error: %v", rErr)
	}

	wAfter, _, err := svc1.Get(w.ID, w.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if wAfter.State != WorkerIdle || wAfter.ActiveTurnID != "" {
		t.Fatalf("late flushPendingReconcile re-attached closed turn to worker: %+v", wAfter)
	}
	t1After, err := svc1.GetTurn(t1.ID, w.WorkspaceID)
	if err != nil || t1After.State != TurnFailed || t1After.ErrorCode != "reconciled_manually_closed" {
		t.Fatalf("turn state corrupted after concurrent flush: %+v err=%v", t1After, err)
	}

	// Verify clean behavior survives service restart too.
	if err := svc1.Close(context.Background()); err != nil {
		t.Fatalf("svc1.Close: %v", err)
	}
	svc2, err := NewService(home, hs, &fakeAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc2.Close(context.Background()) }()
	if _, err := svc2.Send(context.Background(), SendInput{
		OperationID: "op_flush_race_s2_after_restart", WorkerID: w.ID, Prompt: "new work after restart",
	}); err != nil {
		t.Fatalf("expected worker usable after restart, got %v", err)
	}
}

// CloseWorker must atomically verify state/locks and persist WorkerClosed
// under s.mu BEFORE releasing s.mu to close the resident thread, so no
// concurrent Send can slip in and acquire a retained write lock while
// CloseWorker is closing the old resident.
func TestCloseWorkerCommitsClosedBeforeClosingResident(t *testing.T) {
	inClose := make(chan struct{})
	releaseClose := make(chan struct{})
	var closeCalls atomic.Int32

	fa := &fakeAdapter{
		closeFn: func(handle ThreadHandle) error {
			closeCalls.Add(1)
			select {
			case <-inClose:
			default:
				close(inClose)
			}
			<-releaseClose
			return nil
		},
	}

	svc, hs, ws := setupTestService(t, fa)
	defer func() { _ = svc.Close(context.Background()) }()
	enable := true
	if _, err := hs.UpdatePolicy(harnesses.UpdatePolicyInput{HarnessID: harnesses.HarnessCodex, AllowWorkspaceWrite: &enable}); err != nil {
		t.Fatal(err)
	}

	w, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_close_atomic_w", HarnessID: harnesses.HarnessCodex,
		WorkspaceRoot: ws.RootPath, AccessMode: harnesses.AccessWorkspaceWrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Run one completed turn so an idle resident is attached to w.
	t1, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_close_atomic_s1", WorkerID: w.ID, Prompt: "warmup",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.Wait(context.Background(), t1.ID, ws.ID, 3*time.Second); err != nil {
		t.Fatal(err)
	}

	closeDone := make(chan error, 1)
	go func() {
		_, cErr := svc.CloseWorker(context.Background(), w.ID, ws.ID)
		closeDone <- cErr
	}()
	<-inClose

	// While CloseWorker is blocked in CloseThread outside s.mu, WorkerClosed is
	// ALREADY committed so Send MUST be rejected immediately.
	if _, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_close_atomic_race_send", WorkerID: w.ID, Prompt: "must be rejected",
	}); err == nil || !strings.Contains(err.Error(), "closed") {
		close(releaseClose)
		t.Fatalf("expected Send rejected as closed while CloseWorker closes resident, got %v", err)
	}
	close(releaseClose)
	if cErr := <-closeDone; cErr != nil {
		t.Fatalf("CloseWorker: %v", cErr)
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("expected resident closed once, got %d", closeCalls.Load())
	}
}

// sameHandle must never panic on structs with interface fields holding
// dynamically uncomparable values (e.g. slices), and service-managed handles
// must compare reliably by assigned instanceID even with uncomparable ProcessRef.
func TestSameHandleNeverPanicsOnUncomparableDynamicProcessRef(t *testing.T) {
	rawA := ThreadHandle{Key: "w:thr", ProcessRef: dynamicPayloadRef{Tag: "a", Payload: []byte{1, 2, 3}}}
	rawB := ThreadHandle{Key: "w:thr", ProcessRef: dynamicPayloadRef{Tag: "a", Payload: []byte{1, 2, 3}}}
	if sameHandle(rawA, rawB) {
		t.Fatal("uncomparable raw ProcessRef values without instanceID must safely return false")
	}

	var openSeq atomic.Int32
	fa := &fakeAdapter{
		openFn: func(ctx context.Context, spec ThreadOpenSpec) (ThreadHandle, error) {
			n := openSeq.Add(1)
			return ThreadHandle{
				Key:             spec.WorkerID + ":thr_dyn",
				NativeSessionID: "thr_dyn",
				ProcessRef:      dynamicPayloadRef{Tag: "inst", Payload: []byte{byte(n)}},
			}, nil
		},
	}
	svc, _, ws := setupTestService(t, fa)
	defer func() { _ = svc.Close(context.Background()) }()
	w, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_handle_id_w", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	svc.workerTurn[w.ID] = "turn_fake_1"
	svc.mu.Unlock()

	h1, err := svc.acquireHandle(context.Background(), fa, w, "codex.exe", "turn_fake_1", false)
	if err != nil {
		t.Fatalf("acquireHandle: %v", err)
	}
	if h1.instanceID == 0 {
		t.Fatal("expected non-zero service-assigned instanceID")
	}
	if !sameHandle(h1, h1) {
		t.Fatal("expected sameHandle true for identical service-managed handle instance")
	}
	other := h1
	other.instanceID = h1.instanceID + 1
	if sameHandle(h1, other) {
		t.Fatal("expected sameHandle false for distinct instanceID")
	}

	// Evicting with mismatched instanceID must NOT evict h1; evicting with h1 MUST evict it without panicking.
	svc.evictResidentByHandle(other)
	svc.mu.Lock()
	_, stillPresent := svc.residents[w.ID]
	svc.mu.Unlock()
	if !stillPresent {
		t.Fatal("mismatched instanceID must not evict registered resident")
	}
	svc.evictResidentByHandle(h1)
	svc.mu.Lock()
	_, afterEvict := svc.residents[w.ID]
	delete(svc.workerTurn, w.ID)
	svc.mu.Unlock()
	if afterEvict {
		t.Fatal("evictResidentByHandle(h1) failed to evict matching resident")
	}
}
