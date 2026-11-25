//go:build windows

package workers

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/harnesses"
	"golang.org/x/sys/windows"
)

// Root process exit (closed done channel) must NEVER mask a failed
// TerminateJobObject when the job cannot be independently confirmed empty.
func TestWindowsKillDoesNotMaskJobTerminationError(t *testing.T) {
	// Use a valid non-job handle (Windows event) owned by this test so
	// TerminateJobObject and QueryInformationJobObject fail deterministically
	// without touching any external process.
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	done := make(chan struct{})
	close(done) // Simulate root process already exited.

	mp := &managedProc{
		job:  ev,
		done: done,
	}

	// Concurrent and repeated Kill() calls must all observe the same ErrProcessStopUnconfirmed.
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = mp.Kill()
		}(i)
	}
	wg.Wait()

	for i, kErr := range errs {
		if !errors.Is(kErr, ErrProcessStopUnconfirmed) {
			t.Fatalf("call %d: expected ErrProcessStopUnconfirmed when TerminateJobObject fails even with closed done, got %v", i, kErr)
		}
	}
	if repeatErr := mp.Kill(); !errors.Is(repeatErr, ErrProcessStopUnconfirmed) {
		t.Fatalf("repeat Kill() must share final ErrProcessStopUnconfirmed, got %v", repeatErr)
	}
}

// During Service.Close timeout on a real CodexAdapter turn, if Windows
// ForceKill returns ErrProcessStopUnconfirmed, Close must NOT lose the failure
// behind Codex's local close notification: Close must return ErrProcessStopUnconfirmed,
// persist reconciliation_required + WriteLockRetained=true, and block subsequent
// workspace_write turns after reopening the database.
func TestCodexShutdownTerminationUnconfirmed(t *testing.T) {
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

	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe in: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe out: %v", err)
	}
	t.Cleanup(func() {
		_ = inR.Close()
		_ = inW.Close()
		_ = outR.Close()
		_ = outW.Close()
	})
	doneCh := make(chan struct{})
	close(doneCh) // Simulate root process exited while Job termination fails.

	turnStarted := make(chan struct{})
	go func() {
		defer outW.Close()
		sc := bufio.NewScanner(inR)
		for sc.Scan() {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(sc.Bytes(), &req) != nil {
				continue
			}
			switch req.Method {
			case "turn/start":
				_, _ = io.WriteString(outW, `{"id":`+string(req.ID)+`,"result":{"turn":{"id":"nt_win_shutdown","status":"inProgress"}}}`+"\n")
				close(turnStarted)
			case "turn/interrupt":
				// Deliberately ignore turn/interrupt so shutdown must escalate to ForceKill.
			}
		}
	}()

	mp := &managedProc{
		stdin:  inW,
		stdout: outR,
		job:    ev,
		done:   doneCh,
	}
	codexAd := &codexControlledProcAdapter{
		CodexAdapter: NewCodexAdapter(),
		proc:         mp,
	}

	svc1, err := NewService(home, hs, codexAd)
	if err != nil {
		t.Fatal(err)
	}
	svc1.SetBudgets(Budgets{InterruptGrace: 2 * time.Second})

	w1, err := svc1.Create(context.Background(), CreateInput{
		OperationID: "op_win_shutdown_w1", HarnessID: harnesses.HarnessCodex,
		WorkspaceRoot: wsDir, Alias: "w1", AccessMode: harnesses.AccessWorkspaceWrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	w2, err := svc1.Create(context.Background(), CreateInput{
		OperationID: "op_win_shutdown_w2", HarnessID: harnesses.HarnessCodex,
		WorkspaceRoot: wsDir, Alias: "w2", AccessMode: harnesses.AccessWorkspaceWrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	t1, err := svc1.Send(context.Background(), SendInput{
		OperationID: "op_win_shutdown_s1", WorkerID: w1.ID, Prompt: "long running write",
	})
	if err != nil {
		t.Fatal(err)
	}
	<-turnStarted

	// Close with timeout shorter than InterruptGrace so shutdown triggers ForceKill.
	closeCtx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	closeErr := svc1.Close(closeCtx)
	_ = inR.Close()
	if !errors.Is(closeErr, ErrProcessStopUnconfirmed) {
		t.Fatalf("expected Close to return ErrProcessStopUnconfirmed, got %v", closeErr)
	}

	// Reopen database and service: turn must be reconciliation_required with retained write lock.
	svc2, err := NewService(home, hs, &fakeAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc2.Close(context.Background()) }()

	reloaded, err := svc2.GetTurn(t1.ID, w1.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State != TurnReconcile || reloaded.ErrorCode != "process_stop_unconfirmed" || !reloaded.WriteLockRetained {
		t.Fatalf("expected persisted reconciliation_required + process_stop_unconfirmed + WriteLockRetained=true, got %+v", reloaded)
	}
	if _, err := svc2.Send(context.Background(), SendInput{
		OperationID: "op_win_shutdown_s2", WorkerID: w2.ID, Prompt: "competing write after shutdown",
	}); err == nil || !strings.Contains(err.Error(), "worktree_busy") {
		t.Fatalf("expected worktree_busy after reopening DB, got %v", err)
	}
}

type codexControlledProcAdapter struct {
	*CodexAdapter
	proc *managedProc
}

func (a *codexControlledProcAdapter) OpenThread(ctx context.Context, spec ThreadOpenSpec) (ThreadHandle, error) {
	sess := &codexSession{
		proc:     a.proc,
		threadID: "thr_win_shutdown",
		pending:  map[int64]chan rpcReply{},
	}
	go sess.readLoop()
	return ThreadHandle{
		Key:             spec.WorkerID + ":thr_win_shutdown",
		NativeSessionID: "thr_win_shutdown",
		ProcessRef:      sess,
	}, nil
}

// When Codex readLoop hits local EOF and simultaneously delivers LocalClose=true
// and StopErr=ErrProcessStopUnconfirmed, the runner must evict the dead resident
// handle while retaining the old turn's write lock, so subsequent ReconcileTurn
// or a new Send after ResolveTurn opens a fresh Codex session instead of reusing
// the closed one.
func TestCodexLocalEOFWithUnconfirmedJobEvictsDeadResident(t *testing.T) {
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

	var openCalls atomic.Int32
	eofTriggered := make(chan struct{})
	ad := &codexMultiOpenAdapter{
		CodexAdapter: NewCodexAdapter(),
		openFn: func(ctx context.Context, spec ThreadOpenSpec) (ThreadHandle, error) {
			n := openCalls.Add(1)
			inR, inW, err := os.Pipe()
			if err != nil {
				return ThreadHandle{}, err
			}
			outR, outW, err := os.Pipe()
			if err != nil {
				_ = inR.Close()
				_ = inW.Close()
				return ThreadHandle{}, err
			}
			t.Cleanup(func() {
				_ = inR.Close()
				_ = inW.Close()
				_ = outR.Close()
				_ = outW.Close()
			})

			doneCh := make(chan struct{})
			close(doneCh)
			var jobHandle windows.Handle
			if n == 1 {
				// First session: non-Job Event handle forces ErrProcessStopUnconfirmed on Kill().
				ev, evErr := windows.CreateEvent(nil, 1, 0, nil)
				if evErr != nil {
					return ThreadHandle{}, evErr
				}
				jobHandle = ev
			}

			go func(sessionNum int32) {
				defer outW.Close()
				sc := bufio.NewScanner(inR)
				for sc.Scan() {
					var req struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
					}
					if json.Unmarshal(sc.Bytes(), &req) != nil {
						continue
					}
					switch req.Method {
					case "turn/start":
						if sessionNum == 1 {
							_, _ = io.WriteString(outW, `{"id":`+string(req.ID)+`,"result":{"turn":{"id":"nt_eof_1","status":"inProgress"}}}`+"\n")
							// Close stdout immediately (local EOF) while Job fails Kill().
							close(eofTriggered)
							return
						}
						_, _ = io.WriteString(outW, `{"id":`+string(req.ID)+`,"result":{"turn":{"id":"nt_fresh_2","status":"inProgress"}}}`+"\n")
						_, _ = io.WriteString(outW, `{"method":"item/completed","params":{"turnId":"nt_fresh_2","item":{"id":"f2","type":"agentMessage","phase":"final_answer","text":"fresh session ok"}}}`+"\n")
						_, _ = io.WriteString(outW, `{"method":"turn/completed","params":{"turn":{"id":"nt_fresh_2","status":"completed"}}}`+"\n")
					}
				}
			}(n)

			mp := &managedProc{stdin: inW, stdout: outR, job: jobHandle, done: doneCh}
			sess := &codexSession{
				proc:     mp,
				threadID: "thr_eof_evict",
				pending:  map[int64]chan rpcReply{},
			}
			go sess.readLoop()
			return ThreadHandle{
				Key:             spec.WorkerID + ":thr_eof_evict",
				NativeSessionID: "thr_eof_evict",
				ProcessRef:      sess,
			}, nil
		},
	}

	svc, err := NewService(home, hs, ad)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc.Close(context.Background()) }()

	w, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_eof_evict_w", HarnessID: harnesses.HarnessCodex,
		WorkspaceRoot: wsDir, AccessMode: harnesses.AccessWorkspaceWrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	t1, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_eof_evict_s1", WorkerID: w.ID, Prompt: "trigger eof",
	})
	if err != nil {
		t.Fatal(err)
	}
	<-eofTriggered
	wt1, _, _, err := svc.Wait(context.Background(), t1.ID, w.WorkspaceID, 3*time.Second)
	if err != nil || wt1.State != TurnReconcile || wt1.ErrorCode != "process_stop_unconfirmed" || !wt1.WriteLockRetained {
		t.Fatalf("expected reconciliation_required/process_stop_unconfirmed with retained lock, got %+v err=%v", wt1, err)
	}

	// Dead session MUST have been evicted from s.residents even though StopErr != nil.
	svc.mu.Lock()
	_, stillCached := svc.residents[w.ID]
	svc.mu.Unlock()
	if stillCached {
		t.Fatal("dead closed Codex session must be evicted from residents on LocalClose + StopErr")
	}

	// Manually resolve t1, then send t2: MUST open a fresh second session and succeed!
	if _, err := svc.ResolveTurn(t1.ID, "operator", "confirmed stopped"); err != nil {
		t.Fatalf("ResolveTurn: %v", err)
	}
	t2, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_eof_evict_s2", WorkerID: w.ID, Prompt: "fresh turn",
	})
	if err != nil {
		t.Fatalf("Send T2: %v", err)
	}
	wt2, ans2, _, err := svc.Wait(context.Background(), t2.ID, w.WorkspaceID, 3*time.Second)
	if err != nil || wt2.State != TurnCompleted || ans2 != "fresh session ok" {
		t.Fatalf("expected T2 completed on fresh Codex session, got state=%s ans=%q err=%v", wt2.State, ans2, err)
	}
	if openCalls.Load() < 2 {
		t.Fatalf("expected OpenThread called at least twice (fresh session), got %d", openCalls.Load())
	}
}

type codexMultiOpenAdapter struct {
	*CodexAdapter
	openFn func(ctx context.Context, spec ThreadOpenSpec) (ThreadHandle, error)
}

func (a *codexMultiOpenAdapter) OpenThread(ctx context.Context, spec ThreadOpenSpec) (ThreadHandle, error) {
	return a.openFn(ctx, spec)
}

// When an idle Codex resident exits (EOF) between Turn 1 and Turn 2, Turn 2
// discovers the closed session before writing any frame. With confirmed process
// termination, Turn 2 must fail cleanly as adapter_start_failed (NOT
// turn_start_unacknowledged), evict the closed resident, and allow Turn 3 on the
// same worker to reopen a fresh session and complete without manual unlock.
func TestCodexIdleEOFBetweenTurnsFailsCleanlyAndNextTurnReopens(t *testing.T) {
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

	var openCalls atomic.Int32
	var turnStartFrames atomic.Int32
	closeFirstOut := make(chan struct{})
	firstClosed := make(chan struct{})

	ad := &codexMultiOpenAdapter{
		CodexAdapter: NewCodexAdapter(),
		openFn: func(ctx context.Context, spec ThreadOpenSpec) (ThreadHandle, error) {
			n := openCalls.Add(1)
			inR, inW, err := os.Pipe()
			if err != nil {
				return ThreadHandle{}, err
			}
			outR, outW, err := os.Pipe()
			if err != nil {
				_ = inR.Close()
				_ = inW.Close()
				return ThreadHandle{}, err
			}
			t.Cleanup(func() {
				_ = inR.Close()
				_ = inW.Close()
				_ = outR.Close()
				_ = outW.Close()
			})
			doneCh := make(chan struct{})
			close(doneCh) // Confirmed stopped root process + empty Job (0).

			go func(sessionNum int32) {
				defer func() {
					_ = outW.Close()
					if sessionNum == 1 {
						close(firstClosed)
					}
				}()
				sc := bufio.NewScanner(inR)
				for sc.Scan() {
					var req struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
					}
					if json.Unmarshal(sc.Bytes(), &req) != nil {
						continue
					}
					if req.Method == "turn/start" {
						turnStartFrames.Add(1)
						tid := "nt_idle_eof_1"
						ans := "turn 1 ok"
						if sessionNum > 1 {
							tid = "nt_idle_eof_3"
							ans = "turn 3 ok"
						}
						_, _ = io.WriteString(outW, `{"id":`+string(req.ID)+`,"result":{"turn":{"id":"`+tid+`","status":"inProgress"}}}`+"\n")
						_, _ = io.WriteString(outW, `{"method":"item/completed","params":{"turnId":"`+tid+`","item":{"id":"f","type":"agentMessage","phase":"final_answer","text":"`+ans+`"}}}`+"\n")
						_, _ = io.WriteString(outW, `{"method":"turn/completed","params":{"turn":{"id":"`+tid+`","status":"completed"}}}`+"\n")
						if sessionNum == 1 {
							<-closeFirstOut
							return
						}
					}
				}
			}(n)

			mp := &managedProc{stdin: inW, stdout: outR, job: 0, done: doneCh}
			sess := &codexSession{
				proc:     mp,
				threadID: "thr_idle_eof",
				pending:  map[int64]chan rpcReply{},
			}
			go sess.readLoop()
			return ThreadHandle{
				Key:             spec.WorkerID + ":thr_idle_eof",
				NativeSessionID: "thr_idle_eof",
				ProcessRef:      sess,
			}, nil
		},
	}

	svc, err := NewService(home, hs, ad)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc.Close(context.Background()) }()

	w, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_idle_eof_w", HarnessID: harnesses.HarnessCodex,
		WorkspaceRoot: wsDir, AccessMode: harnesses.AccessWorkspaceWrite,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Turn 1 completes cleanly and leaves resident cached while worker is idle.
	t1, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_idle_eof_s1", WorkerID: w.ID, Prompt: "first turn",
	})
	if err != nil {
		t.Fatal(err)
	}
	wt1, ans1, _, err := svc.Wait(context.Background(), t1.ID, w.WorkspaceID, 3*time.Second)
	if err != nil || wt1.State != TurnCompleted || ans1 != "turn 1 ok" {
		t.Fatalf("expected turn 1 completed, got state=%s ans=%q err=%v", wt1.State, ans1, err)
	}

	// Simulate idle Codex process dying (EOF) between Turn 1 and Turn 2.
	close(closeFirstOut)
	<-firstClosed
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		svc.mu.Lock()
		rp := svc.residents[w.ID]
		svc.mu.Unlock()
		if rp != nil {
			sess := rp.handle.ProcessRef.(*codexSession)
			sess.mu.Lock()
			closed := sess.closed
			sess.mu.Unlock()
			if closed {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Turn 2 hits closed session before writing any frame: must fail cleanly (NOT turn_start_unacknowledged).
	t2, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_idle_eof_s2", WorkerID: w.ID, Prompt: "second turn on dead idle proc",
	})
	if err != nil {
		t.Fatal(err)
	}
	wt2, _, _, err := svc.Wait(context.Background(), t2.ID, w.WorkspaceID, 3*time.Second)
	if err != nil || wt2.State != TurnFailed || wt2.ErrorCode != "adapter_start_failed" {
		t.Fatalf("expected Turn 2 failed/adapter_start_failed (not reconciliation_required), got %+v err=%v", wt2, err)
	}
	if gotFrames := turnStartFrames.Load(); gotFrames != 1 {
		t.Fatalf("expected zero turn/start frames written for Turn 2 (total 1), got %d", gotFrames)
	}

	// Turn 3 on the SAME worker must reopen a fresh Codex session and complete without manual unlock.
	t3, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_idle_eof_s3", WorkerID: w.ID, Prompt: "third turn reopens session",
	})
	if err != nil {
		t.Fatalf("Turn 3 Send should succeed without manual unlock, got %v", err)
	}
	wt3, ans3, _, err := svc.Wait(context.Background(), t3.ID, w.WorkspaceID, 3*time.Second)
	if err != nil || wt3.State != TurnCompleted || ans3 != "turn 3 ok" {
		t.Fatalf("expected Turn 3 completed on reopened session, got state=%s ans=%q err=%v", wt3.State, ans3, err)
	}
	if openCalls.Load() < 2 {
		t.Fatalf("expected OpenThread called at least twice, got %d", openCalls.Load())
	}
}

// If Cancel arrives while StartTurn is in-flight and StartTurn then discovers
// a closed Codex session (ErrPreStartFailed) whose Windows Job termination
// fails (ErrProcessStopUnconfirmed), handleCancelDuringStart must NOT drop the
// stop error: it must persist reconciliation_required with WriteLockRetained=true
// and block competing writes on the same worktree.
func TestCancelDuringStartWithPreStartClosedSessionRetainsLockOnUnconfirmedKill(t *testing.T) {
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

	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = inR.Close()
		_ = inW.Close()
		_ = outR.Close()
		_ = outW.Close()
	})
	doneCh := make(chan struct{})
	close(doneCh) // Root exited; non-Job Event handle forces ErrProcessStopUnconfirmed on Kill().

	inStart := make(chan struct{})
	releaseStart := make(chan struct{})
	var writtenFrames atomic.Int32
	go func() {
		sc := bufio.NewScanner(inR)
		for sc.Scan() {
			writtenFrames.Add(1)
		}
	}()

	mp := &managedProc{stdin: inW, stdout: outR, job: ev, done: doneCh}
	ad := &codexCancelStartAdapter{
		CodexAdapter: NewCodexAdapter(),
		proc:         mp,
		inStart:      inStart,
		releaseStart: releaseStart,
	}

	svc, err := NewService(home, hs, ad)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc.Close(context.Background()) }()

	w1, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_cancel_start_w1", HarnessID: harnesses.HarnessCodex,
		WorkspaceRoot: wsDir, Alias: "w1", AccessMode: harnesses.AccessWorkspaceWrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	w2, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_cancel_start_w2", HarnessID: harnesses.HarnessCodex,
		WorkspaceRoot: wsDir, Alias: "w2", AccessMode: harnesses.AccessWorkspaceWrite,
	})
	if err != nil {
		t.Fatal(err)
	}

	t1, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_cancel_start_s1", WorkerID: w1.ID, Prompt: "cancel during pre-start closed session",
	})
	if err != nil {
		t.Fatal(err)
	}
	<-inStart

	// Trigger Cancel first so runner enters handleCancelDuringStart; then close
	// the Codex session and release StartTurn so StartTurn returns ErrPreStartFailed.
	cancelDone := make(chan struct {
		turn Turn
		err  error
	}, 1)
	go func() {
		ct, cErr := svc.Cancel(context.Background(), t1.ID, w1.WorkspaceID)
		cancelDone <- struct {
			turn Turn
			err  error
		}{turn: ct, err: cErr}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		svc.mu.Lock()
		rt := svc.activeTurns[t1.ID]
		cancelled := rt != nil && rt.cancelled
		svc.mu.Unlock()
		if cancelled {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = outW.Close()
	close(releaseStart)

	res := <-cancelDone
	if res.err != nil {
		t.Fatalf("Cancel: %v", res.err)
	}
	if res.turn.State != TurnReconcile || res.turn.ErrorCode != "process_stop_unconfirmed" || !res.turn.WriteLockRetained {
		t.Fatalf("expected reconciliation_required + process_stop_unconfirmed + WriteLockRetained=true, got %+v", res.turn)
	}
	if got := writtenFrames.Load(); got != 0 {
		t.Fatalf("expected 0 frames written, got %d", got)
	}
	if _, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_cancel_start_s2", WorkerID: w2.ID, Prompt: "must be blocked",
	}); err == nil || !strings.Contains(err.Error(), "worktree_busy") {
		t.Fatalf("expected worktree_busy on competing writer w2, got %v", err)
	}
}

type codexCancelStartAdapter struct {
	*CodexAdapter
	proc         *managedProc
	inStart      chan struct{}
	releaseStart chan struct{}
}

func (a *codexCancelStartAdapter) OpenThread(ctx context.Context, spec ThreadOpenSpec) (ThreadHandle, error) {
	sess := &codexSession{
		proc:     a.proc,
		threadID: "thr_cancel_start",
		pending:  map[int64]chan rpcReply{},
	}
	go sess.readLoop()
	return ThreadHandle{
		Key:             spec.WorkerID + ":thr_cancel_start",
		NativeSessionID: "thr_cancel_start",
		ProcessRef:      sess,
	}, nil
}

func (a *codexCancelStartAdapter) StartTurn(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
	close(a.inStart)
	<-a.releaseStart
	// Wait briefly until readLoop marks session closed so CodexAdapter.StartTurn hits the pre-write closed check.
	if sess, ok := handle.ProcessRef.(*codexSession); ok {
		deadline := time.Now().Add(1 * time.Second)
		for time.Now().Before(deadline) {
			sess.mu.Lock()
			closed := sess.closed
			sess.mu.Unlock()
			if closed {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	// Pass a non-cancelled ctx so CodexAdapter.StartTurn exercises the sess.closed ErrPreStartFailed path.
	return a.CodexAdapter.StartTurn(context.Background(), handle, spec, onEvent)
}
