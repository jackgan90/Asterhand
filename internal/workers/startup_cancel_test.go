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
)

type spyWriteCloser struct {
	mu     sync.Mutex
	frames []string
}

func (s *spyWriteCloser) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.frames = append(s.frames, string(p))
	s.mu.Unlock()
	return len(p), nil
}

func (s *spyWriteCloser) Close() error { return nil }

func (s *spyWriteCloser) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.frames)
}

type spyWorkerProc struct {
	stdin  *spyWriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
	done   chan struct{}
}

func newSpyWorkerProc() *spyWorkerProc {
	outR, _ := io.Pipe()
	errR, _ := io.Pipe()
	return &spyWorkerProc{
		stdin:  &spyWriteCloser{},
		stdout: outR,
		stderr: errR,
		done:   make(chan struct{}),
	}
}

func (p *spyWorkerProc) Stdin() io.WriteCloser { return p.stdin }
func (p *spyWorkerProc) Stdout() io.ReadCloser { return p.stdout }
func (p *spyWorkerProc) Stderr() io.ReadCloser { return p.stderr }
func (p *spyWorkerProc) Done() <-chan struct{} { return p.done }
func (p *spyWorkerProc) Kill() error           { return nil }

type controlledPipeProc struct {
	inR, inW  *io.PipeReader
	inWriter  *io.PipeWriter
	outR      *io.PipeReader
	outW      *io.PipeWriter
	errR      *io.PipeReader
	errW      *io.PipeWriter
	done      chan struct{}
	killErr   error
	closeOnce sync.Once
}

func newControlledPipeProc(killErr error) *controlledPipeProc {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	return &controlledPipeProc{
		inR:      inR,
		inWriter: inW,
		outR:     outR,
		outW:     outW,
		errR:     errR,
		errW:     errW,
		done:     make(chan struct{}),
		killErr:  killErr,
	}
}

func (p *controlledPipeProc) Stdin() io.WriteCloser { return p.inWriter }
func (p *controlledPipeProc) Stdout() io.ReadCloser { return p.outR }
func (p *controlledPipeProc) Stderr() io.ReadCloser { return p.errR }
func (p *controlledPipeProc) Done() <-chan struct{} { return p.done }
func (p *controlledPipeProc) Kill() error {
	p.closeOnce.Do(func() {
		_ = p.inWriter.Close()
		_ = p.inR.Close()
		_ = p.outW.Close()
		_ = p.errW.Close()
		close(p.done)
	})
	return p.killErr
}

// Cancelling while OpenThread is blocked must propagate cancellation and
// NEVER dispatch StartTurn even if OpenThread ignores ctx and returns a handle.
func TestCancelDuringOpenThreadNeverDispatchesStartTurn(t *testing.T) {
	inOpen := make(chan struct{})
	releaseOpen := make(chan struct{})
	var ctxCancelled atomic.Bool
	var startCalls atomic.Int32
	var closeCalls atomic.Int32

	fa := &fakeAdapter{
		openFn: func(ctx context.Context, spec ThreadOpenSpec) (ThreadHandle, error) {
			close(inOpen)
			select {
			case <-ctx.Done():
				ctxCancelled.Store(true)
			case <-time.After(2 * time.Second):
			}
			<-releaseOpen
			// Deliberately ignore ctx and return a handle to verify Service's final dispatch gate.
			return ThreadHandle{Key: spec.WorkerID + ":thr_late", NativeSessionID: "thr_late"}, nil
		},
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			startCalls.Add(1)
			ch := make(chan TurnOutcome, 1)
			ch <- TurnOutcome{State: TurnCompleted, NativeTurnID: "nt_should_never_run"}
			return "nt_should_never_run", ch, nil
		},
		closeFn: func(handle ThreadHandle) error {
			closeCalls.Add(1)
			return nil
		},
	}

	svc, hs, ws := setupTestService(t, fa)
	enableWrite := true
	if _, err := hs.UpdatePolicy(harnesses.UpdatePolicyInput{HarnessID: harnesses.HarnessCodex, AllowWorkspaceWrite: &enableWrite}); err != nil {
		t.Fatal(err)
	}
	w, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_cancel_open_c1", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_cancel_open_s1", WorkerID: w.ID, Prompt: "do work",
	})
	if err != nil {
		t.Fatal(err)
	}
	<-inOpen

	cancelDone := make(chan Turn, 1)
	go func() {
		ct, _ := svc.Cancel(context.Background(), turn.ID, ws.ID)
		cancelDone <- ct
	}()

	// Verify OpenThread received context cancellation promptly.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !ctxCancelled.Load() {
		time.Sleep(10 * time.Millisecond)
	}
	if !ctxCancelled.Load() {
		t.Fatalf("expected OpenThread context to be cancelled on Cancel()")
	}

	close(releaseOpen)
	ct := <-cancelDone
	if ct.State != TurnCancelled {
		t.Fatalf("expected TurnCancelled, got %s", ct.State)
	}
	if startCalls.Load() != 0 {
		t.Fatalf("StartTurn MUST NOT be called after cancel during OpenThread, got %d", startCalls.Load())
	}
	if closeCalls.Load() == 0 {
		t.Fatalf("expected late OpenThread handle to be closed instead of leaked")
	}
}

// Shutdown timeout while OpenThread is still blocked must NOT close the
// SQLite store prematurely (no nil-pointer panic when the goroutine finishes).
func TestCloseDoesNotCloseStoreBeforeBlockedOpenThreadExits(t *testing.T) {
	inOpen := make(chan struct{})
	releaseOpen := make(chan struct{})
	fa := &fakeAdapter{
		openFn: func(ctx context.Context, spec ThreadOpenSpec) (ThreadHandle, error) {
			close(inOpen)
			<-releaseOpen
			return ThreadHandle{}, errors.New("late open abort")
		},
	}

	svc, hs, ws := setupTestService(t, fa)
	enableWrite := true
	if _, err := hs.UpdatePolicy(harnesses.UpdatePolicyInput{HarnessID: harnesses.HarnessCodex, AllowWorkspaceWrite: &enableWrite}); err != nil {
		t.Fatal(err)
	}
	w, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_close_open_c1", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_close_open_s1", WorkerID: w.ID, Prompt: "blocked open",
	}); err != nil {
		t.Fatal(err)
	}
	<-inOpen

	// Close with a very short timeout while OpenThread ignores ctx.
	closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	closeErr := svc.Close(closeCtx)
	if closeErr == nil {
		t.Fatalf("expected Close to report timeout while OpenThread is still blocked")
	}

	// Release the blocked OpenThread; runTurn must finish without panicking on a closed/nil DB.
	close(releaseOpen)

	// Wait for background deferred close to finish.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := svc.store.GetWorker(w.ID); errors.Is(err, ErrStoreClosed) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected store to close cleanly once blocked OpenThread exited")
}

// Cancelling while Claude or DeepSeek StartTurn is blocked waiting on the
// session lock before writing stdin must emit ZERO frames to the pipe.
func TestClaudeAndDeepSeekCancelBeforeStdinWriteWritesZeroFrames(t *testing.T) {
	t.Run("claude", func(t *testing.T) {
		proc := newSpyWorkerProc()
		sess := &claudeSession{
			openSpec: ThreadOpenSpec{WorkerID: "w_cl"},
			proc:     proc,
			pending:  map[string]chan claudeControlReply{},
		}
		adapter := &ClaudeAdapter{}
		handle := ThreadHandle{Key: "w_cl:s1", NativeSessionID: "s1", ProcessRef: sess}

		ctx, cancel := context.WithCancel(context.Background())
		// Hold sess.mu so writeJSONCtx blocks on sess.mu.Lock() after passing initial checks.
		sess.mu.Lock()
		errCh := make(chan error, 1)
		go func() {
			_, _, err := adapter.StartTurn(ctx, handle, TurnStartSpec{Prompt: "do not write"}, nil)
			errCh <- err
		}()
		// Ensure goroutine has entered StartTurn and is waiting on sess.mu.
		time.Sleep(40 * time.Millisecond)
		cancel()
		sess.mu.Unlock()

		err := <-errCh
		if !errors.Is(err, ErrPreStartFailed) {
			t.Fatalf("expected ErrPreStartFailed, got %v", err)
		}
		if got := proc.stdin.count(); got != 0 {
			t.Fatalf("Claude wrote %d frame(s) after cancel completed; expected 0", got)
		}
	})

	t.Run("deepseek", func(t *testing.T) {
		proc := newSpyWorkerProc()
		sess := &deepseekSession{
			openSpec:  ThreadOpenSpec{WorkerID: "w_ds"},
			proc:      proc,
			sessionID: "dsh_sess_1",
			pending:   map[int64]chan rpcReply{},
		}
		adapter := &DeepSeekAdapter{}
		handle := ThreadHandle{Key: "w_ds:dsh_sess_1", NativeSessionID: "dsh_sess_1", ProcessRef: sess}

		ctx, cancel := context.WithCancel(context.Background())
		sess.mu.Lock()
		errCh := make(chan error, 1)
		go func() {
			_, _, err := adapter.StartTurn(ctx, handle, TurnStartSpec{Prompt: "do not write"}, nil)
			errCh <- err
		}()
		time.Sleep(40 * time.Millisecond)
		cancel()
		sess.mu.Unlock()

		err := <-errCh
		if !errors.Is(err, ErrPreStartFailed) {
			t.Fatalf("expected ErrPreStartFailed, got %v", err)
		}
		if got := proc.stdin.count(); got != 0 {
			t.Fatalf("DeepSeek wrote %d frame(s) after cancel completed; expected 0", got)
		}
	})
}

// When DeepSeek hits a policy-blocked approval (strict_deny) and the ACP stdout
// stream hits local EOF before a native session/prompt response, LocalClose
// must propagate through the blockedCode branch so the runner verifies
// ForceKill and retains the write lock if stop is unconfirmed.
func TestDeepSeekPolicyBlockWithLocalEOFRetainsWriteLock(t *testing.T) {
	home := t.TempDir()
	wsDir := t.TempDir()
	hs, err := harnesses.Open(home, "")
	if err != nil {
		t.Fatal(err)
	}
	fakeExe := filepath.Join(home, "dsh.exe")
	if err := os.WriteFile(fakeExe, []byte("stub"), 0600); err != nil {
		t.Fatal(err)
	}
	enable := true
	if _, err := hs.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessDeepSeek,
		CollabWorkerEnabled: &enable,
		AllowWorkspaceWrite: &enable,
		ManualPath:          &fakeExe,
	}); err != nil {
		t.Fatal(err)
	}

	ds := NewDeepSeekAdapter()
	ds.spawn = func(exe string, args []string, cwd string, env []string) (workerProcess, error) {
		p := newControlledPipeProc(ErrProcessStopUnconfirmed)
		go func() {
			sc := bufio.NewScanner(p.inR)
			var promptID json.RawMessage
			for sc.Scan() {
				var msg struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
					Result json.RawMessage `json:"result"`
				}
				if json.Unmarshal(sc.Bytes(), &msg) != nil {
					continue
				}
				switch msg.Method {
				case "initialize":
					_, _ = io.WriteString(p.outW, `{"jsonrpc":"2.0","id":`+string(msg.ID)+`,"result":{"protocolVersion":1}}`+"\n")
				case "session/new":
					_, _ = io.WriteString(p.outW, `{"jsonrpc":"2.0","id":`+string(msg.ID)+`,"result":{"sessionId":"ds_sess_eof"}}`+"\n")
				case "session/prompt":
					promptID = msg.ID
					// Trigger approval request; strict_deny sets blockedCode and responds cancelled.
					_, _ = io.WriteString(p.outW, `{"jsonrpc":"2.0","id":99,"method":"session/request_permission","params":{"toolCall":{"toolCallId":"tc1","title":"Run shell","rawInput":{"command":"echo hi"}},"options":[{"optionId":"o1","kind":"allow_once"},{"optionId":"o2","kind":"reject_once"}]}}`+"\n")
				case "session/cancel":
					// Simulate stdout closing (local EOF) BEFORE any native session/prompt reply.
					_ = promptID
					_ = p.outW.Close()
					return
				}
			}
		}()
		return p, nil
	}

	svc, err := NewService(home, hs, ds)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc.Close(context.Background()) }()

	w1, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_ds_eof_w1", HarnessID: harnesses.HarnessDeepSeek,
		WorkspaceRoot: wsDir, Alias: "w1",
		AccessMode: harnesses.AccessWorkspaceWrite, ApprovalMode: ApprovalModeStrictDeny,
	})
	if err != nil {
		t.Fatal(err)
	}
	w2, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_ds_eof_w2", HarnessID: harnesses.HarnessDeepSeek,
		WorkspaceRoot: wsDir, Alias: "w2",
		AccessMode: harnesses.AccessWorkspaceWrite, ApprovalMode: ApprovalModeStrictDeny,
	})
	if err != nil {
		t.Fatal(err)
	}

	t1, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_ds_eof_s1", WorkerID: w1.ID, Prompt: "run command",
	})
	if err != nil {
		t.Fatal(err)
	}
	wt, _, _, err := svc.Wait(context.Background(), t1.ID, w1.WorkspaceID, 3*time.Second)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if wt.State != TurnReconcile || wt.ErrorCode != "process_stop_unconfirmed" || !wt.WriteLockRetained {
		t.Fatalf("expected reconciliation_required + process_stop_unconfirmed + WriteLockRetained=true on local EOF with unconfirmed Kill, got %+v", wt)
	}
	if _, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_ds_eof_s2", WorkerID: w2.ID, Prompt: "competing write",
	}); err == nil || !strings.Contains(err.Error(), "worktree_busy") {
		t.Fatalf("expected worktree_busy while DeepSeek process stop unconfirmed, got %v", err)
	}
}

// Closed sessions discovered before writing any frame (Codex StartTurn/request,
// DeepSeek StartTurn/dispatchPrompt) and pre-write JSON marshal failures
// (Claude writeJSONCtx) must wrap ErrPreStartFailed.
func TestPreWriteClosedSessionAndMarshalFailureReturnErrPreStartFailed(t *testing.T) {
	codexSess := &codexSession{closed: true, pending: map[int64]chan rpcReply{}}
	codexHandle := ThreadHandle{Key: "w:thr", NativeSessionID: "thr", ProcessRef: codexSess}
	if _, _, err := NewCodexAdapter().StartTurn(context.Background(), codexHandle, TurnStartSpec{Prompt: "hi"}, nil); !errors.Is(err, ErrPreStartFailed) {
		t.Fatalf("Codex StartTurn on closed session expected ErrPreStartFailed, got %v", err)
	}
	if _, err := codexSess.request(context.Background(), "turn/start", map[string]any{}); !errors.Is(err, ErrPreStartFailed) {
		t.Fatalf("Codex request on closed session expected ErrPreStartFailed, got %v", err)
	}

	dsAd := NewDeepSeekAdapter()
	dsAd.spawn = func(string, []string, string, []string) (workerProcess, error) {
		return nil, errors.New("respawn unavailable")
	}
	dsSess := &deepseekSession{
		adapter: dsAd,
		closed:  true,
		pending: map[int64]chan rpcReply{},
	}
	dsHandle := ThreadHandle{Key: "w:ds", NativeSessionID: "ds", ProcessRef: dsSess}
	if _, _, err := dsAd.StartTurn(context.Background(), dsHandle, TurnStartSpec{Prompt: "hi"}, nil); !errors.Is(err, ErrPreStartFailed) {
		t.Fatalf("DeepSeek StartTurn on closed session expected ErrPreStartFailed, got %v", err)
	}
	if _, err := dsSess.dispatchPrompt(context.Background(), "ds", "hi"); !errors.Is(err, ErrPreStartFailed) {
		t.Fatalf("DeepSeek dispatchPrompt on closed session expected ErrPreStartFailed, got %v", err)
	}

	clSess := &claudeSession{}
	if err := clSess.writeJSONCtx(context.Background(), map[string]any{"bad": func() {}}); !errors.Is(err, ErrPreStartFailed) {
		t.Fatalf("Claude writeJSONCtx marshal failure expected ErrPreStartFailed, got %v", err)
	}
}

// Terminal persistence in finalizeTurn must merge into the latest DB row so
// commentary sequence numbers and latest progress emitted during execution are preserved.
func TestFinalizeTurnPreservesLatestProgressAndCommentarySeq(t *testing.T) {
	fa := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			ch := make(chan TurnOutcome, 1)
			go func() {
				onEvent(AdapterEvent{
					Kind:    "commentary",
					Summary: "Checking files",
					Item:    &NativeItem{ItemID: "c1", Type: "agentMessage", Phase: "commentary", Text: "Checking files"},
				})
				onEvent(AdapterEvent{
					Kind:    "progress",
					Summary: "Running final verification step",
				})
				ch <- TurnOutcome{
					State:        TurnFailed,
					NativeTurnID: "nt_term_merge",
					ErrorCode:    "boom",
					ErrorMessage: "tool failed",
				}
			}()
			return "nt_term_merge", ch, nil
		},
	}

	svc, _, ws := setupTestService(t, fa)
	w, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_term_merge_c1", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_term_merge_s1", WorkerID: w.ID, Prompt: "check",
	})
	if err != nil {
		t.Fatal(err)
	}
	doneT, _, _, err := svc.Wait(context.Background(), turn.ID, ws.ID, 3*time.Second)
	if err != nil || doneT.State != TurnFailed {
		t.Fatalf("expected failed turn, got %+v err=%v", doneT, err)
	}

	// Read raw persisted row directly from SQLite (simulating post-restart read without in-memory cache).
	persisted, err := svc.store.GetTurn(turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.LatestCommentarySeq != 1 {
		t.Fatalf("expected persisted LatestCommentarySeq=1, got %d", persisted.LatestCommentarySeq)
	}
	if persisted.Progress != "Running final verification step" {
		t.Fatalf("expected persisted Progress preserved, got %q", persisted.Progress)
	}
	// Verify JSON serialization carries expected fields.
	raw, _ := json.Marshal(persisted)
	if !strings.Contains(string(raw), `"latest_commentary_seq":1`) {
		t.Fatalf("expected latest_commentary_seq in persisted JSON: %s", string(raw))
	}
}
