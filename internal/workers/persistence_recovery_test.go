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

// Storage failures while persisting stop-unconfirmed lock retention or
// reconciling a retained lock must return errors, never release the lock
// prematurely, and conservatively keep the lock across restart.
func TestStorageFailureOnStopPersistAndReconcileUnlock(t *testing.T) {
	t.Run("stop_persist_failure_propagates_and_retains_lock_across_restart", func(t *testing.T) {
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
				return "nt_sf1", make(chan TurnOutcome, 1), nil
			},
			forceKillFn: func(handle ThreadHandle) error {
				return ErrProcessStopUnconfirmed
			},
		}
		svc1, err := NewService(home, hs, fa)
		if err != nil {
			t.Fatal(err)
		}
		svc1.SetBudgets(Budgets{InterruptGrace: 20 * time.Millisecond})

		w1, err := svc1.Create(context.Background(), CreateInput{
			OperationID: "op_sf1_w1", HarnessID: harnesses.HarnessCodex,
			WorkspaceRoot: wsDir, Alias: "w1", AccessMode: harnesses.AccessWorkspaceWrite,
		})
		if err != nil {
			t.Fatal(err)
		}
		w2, err := svc1.Create(context.Background(), CreateInput{
			OperationID: "op_sf1_w2", HarnessID: harnesses.HarnessCodex,
			WorkspaceRoot: wsDir, Alias: "w2", AccessMode: harnesses.AccessWorkspaceWrite,
		})
		if err != nil {
			t.Fatal(err)
		}
		t1, err := svc1.Send(context.Background(), SendInput{
			OperationID: "op_sf1_s1", WorkerID: w1.ID, Prompt: "write work",
		})
		if err != nil {
			t.Fatal(err)
		}
		<-started

		// Inject SQLite write failure specifically when transitioning turn to reconciliation_required.
		if _, err := svc1.store.db.Exec(`CREATE TRIGGER fail_reconcile_write BEFORE UPDATE ON turns WHEN NEW.state = 'reconciliation_required' BEGIN SELECT RAISE(ABORT, 'inject_reconcile_write_fail'); END;`); err != nil {
			t.Fatal(err)
		}

		if _, err := svc1.Cancel(context.Background(), t1.ID, w1.WorkspaceID); err == nil || !strings.Contains(err.Error(), "inject_reconcile_write_fail") {
			t.Fatalf("expected Cancel to propagate storage failure, got err=%v", err)
		}
		// In-memory lock must still block w2.
		if _, err := svc1.Send(context.Background(), SendInput{
			OperationID: "op_sf1_s2_mem", WorkerID: w2.ID, Prompt: "competing write",
		}); err == nil || !strings.Contains(err.Error(), "worktree_busy") {
			t.Fatalf("expected worktree_busy while in-memory lock retained, got %v", err)
		}

		// Clear fault trigger and restart service on the same DB.
		if _, err := svc1.store.db.Exec(`DROP TRIGGER fail_reconcile_write;`); err != nil {
			t.Fatal(err)
		}
		_ = svc1.Close(context.Background())

		svc2, err := NewService(home, hs, &fakeAdapter{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = svc2.Close(context.Background()) }()

		t1After, err := svc2.GetTurn(t1.ID, w1.WorkspaceID)
		if err != nil {
			t.Fatal(err)
		}
		if t1After.State != TurnReconcile || !t1After.WriteLockRetained {
			t.Fatalf("expected startup recovery to conservatively retain write lock, got %+v", t1After)
		}
		if _, err := svc2.Send(context.Background(), SendInput{
			OperationID: "op_sf1_s2_restart", WorkerID: w2.ID, Prompt: "competing write after restart",
		}); err == nil || !strings.Contains(err.Error(), "worktree_busy") {
			t.Fatalf("expected worktree_busy after restart when stop persist had failed, got %v", err)
		}
	})

	t.Run("reconcile_unlock_persist_failure_does_not_release_lock", func(t *testing.T) {
		var corrID string
		var corrMu sync.Mutex
		started := make(chan struct{})
		fa := &fakeAdapter{
			startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
				corrMu.Lock()
				corrID = spec.ClientCorrelationID
				corrMu.Unlock()
				close(started)
				return "nt_sf2", make(chan TurnOutcome, 1), nil
			},
			forceKillFn: func(handle ThreadHandle) error {
				return ErrProcessStopUnconfirmed
			},
			inspectFn: func(ctx context.Context, handle ThreadHandle) ([]NativeTurnRecord, error) {
				corrMu.Lock()
				c := corrID
				corrMu.Unlock()
				return []NativeTurnRecord{{
					NativeTurnID: "nt_sf2",
					Status:       TurnCompleted,
					Items: []NativeItem{
						{ItemID: "u1", Type: "userMessage", ClientID: c},
						{ItemID: "f1", Type: "agentMessage", Phase: "final_answer", Text: "finished"},
					},
				}}, nil
			},
		}

		svc, hs, ws := setupTestService(t, fa)
		defer func() { _ = svc.Close(context.Background()) }()
		svc.SetBudgets(Budgets{InterruptGrace: 20 * time.Millisecond})
		enable := true
		if _, err := hs.UpdatePolicy(harnesses.UpdatePolicyInput{HarnessID: harnesses.HarnessCodex, AllowWorkspaceWrite: &enable}); err != nil {
			t.Fatal(err)
		}

		w1, err := svc.Create(context.Background(), CreateInput{
			OperationID: "op_sf2_w1", HarnessID: harnesses.HarnessCodex,
			WorkspaceRoot: ws.RootPath, Alias: "w1", AccessMode: harnesses.AccessWorkspaceWrite,
		})
		if err != nil {
			t.Fatal(err)
		}
		w2, err := svc.Create(context.Background(), CreateInput{
			OperationID: "op_sf2_w2", HarnessID: harnesses.HarnessCodex,
			WorkspaceRoot: ws.RootPath, Alias: "w2", AccessMode: harnesses.AccessWorkspaceWrite,
		})
		if err != nil {
			t.Fatal(err)
		}
		t1, err := svc.Send(context.Background(), SendInput{
			OperationID: "op_sf2_s1", WorkerID: w1.ID, Prompt: "write work",
		})
		if err != nil {
			t.Fatal(err)
		}
		<-started
		if _, err := svc.Cancel(context.Background(), t1.ID, ws.ID); err != nil {
			t.Fatal(err)
		}

		// Inject SQLite failure when saving completed state during ReconcileTurn.
		if _, err := svc.store.db.Exec(`CREATE TRIGGER fail_complete_write BEFORE UPDATE ON turns WHEN NEW.state = 'completed' BEGIN SELECT RAISE(ABORT, 'inject_complete_write_fail'); END;`); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ReconcileTurn(context.Background(), t1.ID); err == nil || !strings.Contains(err.Error(), "inject_complete_write_fail") {
			t.Fatalf("expected ReconcileTurn to propagate storage error, got err=%v", err)
		}

		// DB must still be reconciliation_required with retained lock, and w2 must still be blocked.
		stillRec, err := svc.GetTurn(t1.ID, ws.ID)
		if err != nil || stillRec.State != TurnReconcile || !stillRec.WriteLockRetained {
			t.Fatalf("expected turn to remain reconciliation_required + WriteLockRetained=true, got %+v err=%v", stillRec, err)
		}
		if _, err := svc.Send(context.Background(), SendInput{
			OperationID: "op_sf2_s2_blocked", WorkerID: w2.ID, Prompt: "should be blocked",
		}); err == nil || !strings.Contains(err.Error(), "worktree_busy") {
			t.Fatalf("expected worktree_busy after failed reconcile persist, got %v", err)
		}

		// Once storage recovers, ReconcileTurn succeeds and unlocks the worktree.
		if _, err := svc.store.db.Exec(`DROP TRIGGER fail_complete_write;`); err != nil {
			t.Fatal(err)
		}
		doneTurn, err := svc.ReconcileTurn(context.Background(), t1.ID)
		if err != nil || doneTurn.State != TurnCompleted || doneTurn.WriteLockRetained {
			t.Fatalf("expected ReconcileTurn to succeed after fault cleared, got %+v err=%v", doneTurn, err)
		}
		if _, err := svc.Send(context.Background(), SendInput{
			OperationID: "op_sf2_s2_ok", WorkerID: w2.ID, Prompt: "now allowed",
		}); err != nil {
			t.Fatalf("expected w2 allowed after successful reconcile commit, got %v", err)
		}
	})
}

// After a transient SQLite failure prevents persisting reconciliation_required
// during unconfirmed ForceKill, once storage recovers the operator must be able
// to flush, reconcile/resolve, and unlock ONLINE without restarting, and
// CloseWorker must reject closing while the lock is held.
func TestOnlineRecoveryAfterTransientStopPersistFailure(t *testing.T) {
	var corrID string
	var corrMu sync.Mutex
	started := make(chan struct{})
	fa := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			corrMu.Lock()
			corrID = spec.ClientCorrelationID
			corrMu.Unlock()
			close(started)
			return "nt_online_rec", make(chan TurnOutcome, 1), nil
		},
		forceKillFn: func(handle ThreadHandle) error {
			return ErrProcessStopUnconfirmed
		},
		inspectFn: func(ctx context.Context, handle ThreadHandle) ([]NativeTurnRecord, error) {
			corrMu.Lock()
			c := corrID
			corrMu.Unlock()
			return []NativeTurnRecord{{
				NativeTurnID: "nt_online_rec",
				Status:       TurnCompleted,
				Items: []NativeItem{
					{ItemID: "u1", Type: "userMessage", ClientID: c},
					{ItemID: "f1", Type: "agentMessage", Phase: "final_answer", Text: "recovered online"},
				},
			}}, nil
		},
	}

	svc, hs, ws := setupTestService(t, fa)
	defer func() { _ = svc.Close(context.Background()) }()
	svc.SetBudgets(Budgets{InterruptGrace: 20 * time.Millisecond})
	enable := true
	if _, err := hs.UpdatePolicy(harnesses.UpdatePolicyInput{HarnessID: harnesses.HarnessCodex, AllowWorkspaceWrite: &enable}); err != nil {
		t.Fatal(err)
	}

	w1, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_online_rec_w1", HarnessID: harnesses.HarnessCodex,
		WorkspaceRoot: ws.RootPath, Alias: "w1", AccessMode: harnesses.AccessWorkspaceWrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	w2, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_online_rec_w2", HarnessID: harnesses.HarnessCodex,
		WorkspaceRoot: ws.RootPath, Alias: "w2", AccessMode: harnesses.AccessWorkspaceWrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	t1, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_online_rec_s1", WorkerID: w1.ID, Prompt: "write work",
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started

	// Inject transient SQLite write failure on transitioning to reconciliation_required.
	if _, err := svc.store.db.Exec(`CREATE TRIGGER fail_online_rec BEFORE UPDATE ON turns WHEN NEW.state = 'reconciliation_required' BEGIN SELECT RAISE(ABORT, 'transient_online_rec_fail'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Cancel(context.Background(), t1.ID, ws.ID); err == nil || !strings.Contains(err.Error(), "transient_online_rec_fail") {
		t.Fatalf("expected Cancel to return transient SQLite error, got %v", err)
	}

	// While unpersisted reconciliation + retained lock exist:
	// 1. CloseWorker(w1) MUST be rejected with reconciliation_required.
	if _, err := svc.CloseWorker(context.Background(), w1.ID, ws.ID); err == nil || !strings.Contains(err.Error(), "reconciliation_required") {
		t.Fatalf("CloseWorker must reject closing worker with retained write lock / pending reconciliation, got %v", err)
	}
	// 2. Competing write on w2 MUST be rejected with worktree_busy.
	if _, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_online_rec_s2_blocked", WorkerID: w2.ID, Prompt: "blocked write",
	}); err == nil || !strings.Contains(err.Error(), "worktree_busy") {
		t.Fatalf("expected worktree_busy on w2, got %v", err)
	}

	// Clear transient SQLite fault WITHOUT restarting the service.
	if _, err := svc.store.db.Exec(`DROP TRIGGER fail_online_rec;`); err != nil {
		t.Fatal(err)
	}

	// Retrying Cancel online flushes pending reconciliation_required + WriteLockRetained=true to DB.
	flushed, err := svc.Cancel(context.Background(), t1.ID, ws.ID)
	if err != nil || flushed.State != TurnReconcile || flushed.ErrorCode != "process_stop_unconfirmed" || !flushed.WriteLockRetained {
		t.Fatalf("expected retry Cancel to flush reconciliation_required + WriteLockRetained=true, got %+v err=%v", flushed, err)
	}

	// Online ReconcileTurn now reconciles cleanly and releases the write lock.
	reconciled, err := svc.ReconcileTurn(context.Background(), t1.ID)
	if err != nil || reconciled.State != TurnCompleted || reconciled.WriteLockRetained {
		t.Fatalf("expected online ReconcileTurn to complete and unlock, got %+v err=%v", reconciled, err)
	}
	if _, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_online_rec_s2_ok", WorkerID: w2.ID, Prompt: "allowed after online reconcile",
	}); err != nil {
		t.Fatalf("expected w2 allowed after online reconciliation, got %v", err)
	}
	if _, err := svc.CloseWorker(context.Background(), w1.ID, ws.ID); err != nil {
		t.Fatalf("expected CloseWorker(w1) allowed after online reconciliation, got %v", err)
	}
}

// When turn_start_unacknowledged occurs (ForceKill confirmed) AND persisting
// reconciliation_required fails transiently, pendingReconcile must still be
// recorded so the SAME worker cannot accept a second Send until reconciled or
// resolved online, while another worker in the worktree is not blocked because
// process termination was confirmed.
func TestTurnStartUnacknowledgedPersistFailureBlocksSameWorkerAndRecoversOnline(t *testing.T) {
	var startCalls atomic.Int32
	fa := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			if startCalls.Add(1) == 1 {
				return "", nil, errors.New("stream reset waiting for turn/start ack")
			}
			ch := make(chan TurnOutcome, 1)
			ch <- TurnOutcome{
				State: TurnCompleted, NativeTurnID: "nt_other",
				Items: []NativeItem{{ItemID: "f", Type: "agentMessage", Phase: "final_answer", Text: "ok"}},
			}
			return "nt_other", ch, nil
		},
	}

	svc, hs, ws := setupTestService(t, fa)
	defer func() { _ = svc.Close(context.Background()) }()
	enable := true
	if _, err := hs.UpdatePolicy(harnesses.UpdatePolicyInput{HarnessID: harnesses.HarnessCodex, AllowWorkspaceWrite: &enable}); err != nil {
		t.Fatal(err)
	}

	w1, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_start_unack_w1", HarnessID: harnesses.HarnessCodex,
		WorkspaceRoot: ws.RootPath, Alias: "w1", AccessMode: harnesses.AccessWorkspaceWrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	w2, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_start_unack_w2", HarnessID: harnesses.HarnessCodex,
		WorkspaceRoot: ws.RootPath, Alias: "w2", AccessMode: harnesses.AccessWorkspaceWrite,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.store.db.Exec(`CREATE TRIGGER fail_start_ack_rec BEFORE UPDATE ON turns WHEN NEW.state = 'reconciliation_required' BEGIN SELECT RAISE(ABORT, 'inject_start_ack_fail'); END;`); err != nil {
		t.Fatal(err)
	}

	t1, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_start_unack_s1", WorkerID: w1.ID, Prompt: "unacked start",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Wait for t1's runner goroutine to exit after failed persistence.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		svc.mu.Lock()
		_, stillActive := svc.activeTurns[t1.ID]
		svc.mu.Unlock()
		if !stillActive {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Same worker w1 MUST be blocked by reconciliation_required (both Send and CloseWorker).
	if _, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_start_unack_s1_dup", WorkerID: w1.ID, Prompt: "should be blocked",
	}); err == nil || !strings.Contains(err.Error(), "reconciliation_required") {
		t.Fatalf("expected same worker Send blocked by reconciliation_required, got %v", err)
	}
	if _, err := svc.CloseWorker(context.Background(), w1.ID, ws.ID); err == nil || !strings.Contains(err.Error(), "reconciliation_required") {
		t.Fatalf("expected CloseWorker blocked by reconciliation_required, got %v", err)
	}

	// Because ForceKill succeeded on t1, sibling worker w2 is NOT falsely blocked by a retained worktree lock.
	t2, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_start_unack_s2", WorkerID: w2.ID, Prompt: "sibling write allowed",
	})
	if err != nil {
		t.Fatalf("expected sibling worker w2 allowed when ForceKill succeeded, got %v", err)
	}
	if _, _, _, err := svc.Wait(context.Background(), t2.ID, ws.ID, 3*time.Second); err != nil {
		t.Fatal(err)
	}

	// Drop transient fault trigger: online ReconcileTurn flushes turn_start_unacknowledged,
	// then ResolveTurn resolves it and unblocks w1.
	if _, err := svc.store.db.Exec(`DROP TRIGGER fail_start_ack_rec;`); err != nil {
		t.Fatal(err)
	}
	flushed, err := svc.ReconcileTurn(context.Background(), t1.ID)
	if err != nil || flushed.State != TurnReconcile || flushed.ErrorCode != "turn_start_unacknowledged" || flushed.WriteLockRetained {
		t.Fatalf("expected online flush to reconciliation_required/turn_start_unacknowledged with WriteLockRetained=false, got %+v err=%v", flushed, err)
	}
	if _, err := svc.ResolveTurn(t1.ID, "operator", "verified no side effects"); err != nil {
		t.Fatalf("ResolveTurn: %v", err)
	}
	if _, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_start_unack_s1_after", WorkerID: w1.ID, Prompt: "allowed after resolve",
	}); err != nil {
		t.Fatalf("expected w1 unblocked after ResolveTurn, got %v", err)
	}
}

// When a turn is ALREADY persisted in reconciliation_required with
// WriteLockRetained=false (e.g. turn_start_unacknowledged after confirmed
// ForceKill), a transient storage failure during ReconcileTurn's terminal
// commit must preserve the existing reconciliation record and never synthesize
// a retainLock=true pendingReconcile that would falsely lock the worktree
// across restart.
func TestReconcilePersistFailurePreservesConfirmedStopUnlockedState(t *testing.T) {
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

	var corrID string
	var corrMu sync.Mutex
	var startCalls atomic.Int32
	var inspectCalls atomic.Int32

	fa := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			if startCalls.Add(1) == 1 {
				corrMu.Lock()
				corrID = spec.ClientCorrelationID
				corrMu.Unlock()
				return "", nil, errors.New("disconnect before turn/start ack")
			}
			ch := make(chan TurnOutcome, 1)
			ch <- TurnOutcome{
				State: TurnCompleted, NativeTurnID: "nt_peer",
				Items: []NativeItem{{ItemID: "f", Type: "agentMessage", Phase: "final_answer", Text: "peer ok"}},
			}
			return "nt_peer", ch, nil
		},
		inspectFn: func(ctx context.Context, handle ThreadHandle) ([]NativeTurnRecord, error) {
			if inspectCalls.Add(1) == 1 {
				corrMu.Lock()
				c := corrID
				corrMu.Unlock()
				return []NativeTurnRecord{{
					NativeTurnID: "nt_rec_unlocked",
					Status:       TurnCompleted,
					Items: []NativeItem{
						{ItemID: "u1", Type: "userMessage", ClientID: c},
						{ItemID: "f1", Type: "agentMessage", Phase: "final_answer", Text: "done"},
					},
				}}, nil
			}
			// Second inspect simulates native history temporarily unreadable.
			return nil, errors.New("native history temporarily unavailable")
		},
	}

	svc1, err := NewService(home, hs, fa)
	if err != nil {
		t.Fatal(err)
	}
	w1, err := svc1.Create(context.Background(), CreateInput{
		OperationID: "op_rec_unlocked_w1", HarnessID: harnesses.HarnessCodex,
		WorkspaceRoot: wsDir, Alias: "w1", AccessMode: harnesses.AccessWorkspaceWrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	w2, err := svc1.Create(context.Background(), CreateInput{
		OperationID: "op_rec_unlocked_w2", HarnessID: harnesses.HarnessCodex,
		WorkspaceRoot: wsDir, Alias: "w2", AccessMode: harnesses.AccessWorkspaceWrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	t1, err := svc1.Send(context.Background(), SendInput{
		OperationID: "op_rec_unlocked_s1", WorkerID: w1.ID, Prompt: "unacked start",
	})
	if err != nil {
		t.Fatal(err)
	}
	wt1, _, _, err := svc1.Wait(context.Background(), t1.ID, w1.WorkspaceID, 3*time.Second)
	if err != nil || wt1.State != TurnReconcile || wt1.ErrorCode != "turn_start_unacknowledged" || wt1.WriteLockRetained {
		t.Fatalf("expected turn_start_unacknowledged with WriteLockRetained=false, got %+v err=%v", wt1, err)
	}

	// Inject transient failure on terminal commit during ReconcileTurn.
	if _, err := svc1.store.db.Exec(`CREATE TRIGGER fail_rec_unlocked_done BEFORE UPDATE ON turns WHEN NEW.state = 'completed' BEGIN SELECT RAISE(ABORT, 'transient_reconcile_done_fail'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc1.ReconcileTurn(context.Background(), t1.ID); err == nil || !strings.Contains(err.Error(), "transient_reconcile_done_fail") {
		t.Fatalf("expected ReconcileTurn to propagate terminal write failure, got %v", err)
	}
	if _, err := svc1.store.db.Exec(`DROP TRIGGER fail_rec_unlocked_done;`); err != nil {
		t.Fatal(err)
	}

	// Second ReconcileTurn (when native history is temporarily unavailable) must
	// NOT overwrite the original turn_start_unacknowledged record with WriteLockRetained=true.
	rec2, err := svc1.ReconcileTurn(context.Background(), t1.ID)
	if err != nil {
		t.Fatalf("second ReconcileTurn: %v", err)
	}
	if rec2.State != TurnReconcile || rec2.ErrorCode != "turn_start_unacknowledged" || rec2.WriteLockRetained {
		t.Fatalf("original reconciliation record corrupted after failed terminal commit: %+v", rec2)
	}

	// Peer worker in the same worktree works before restart...
	tpBefore, err := svc1.Send(context.Background(), SendInput{
		OperationID: "op_rec_unlocked_peer_before", WorkerID: w2.ID, Prompt: "peer before restart",
	})
	if err != nil {
		t.Fatalf("peer Send before restart failed: %v", err)
	}
	if _, _, _, err := svc1.Wait(context.Background(), tpBefore.ID, w2.WorkspaceID, 3*time.Second); err != nil {
		t.Fatal(err)
	}

	if err := svc1.Close(context.Background()); err != nil {
		t.Fatalf("svc1.Close: %v", err)
	}

	// ...and MUST STILL work after restart without false worktree_busy.
	svc2, err := NewService(home, hs, &fakeAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc2.Close(context.Background()) }()

	reloaded, err := svc2.GetTurn(t1.ID, w1.WorkspaceID)
	if err != nil || reloaded.State != TurnReconcile || reloaded.ErrorCode != "turn_start_unacknowledged" || reloaded.WriteLockRetained {
		t.Fatalf("expected turn_start_unacknowledged + WriteLockRetained=false after restart, got %+v err=%v", reloaded, err)
	}
	tpAfter, err := svc2.Send(context.Background(), SendInput{
		OperationID: "op_rec_unlocked_peer_after", WorkerID: w2.ID, Prompt: "peer after restart",
	})
	if err != nil {
		t.Fatalf("peer Send after restart falsely blocked: %v", err)
	}
	if _, _, _, err := svc2.Wait(context.Background(), tpAfter.ID, w2.WorkspaceID, 3*time.Second); err != nil {
		t.Fatal(err)
	}
}
