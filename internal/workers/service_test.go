package workers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/harnesses"
)

type fakeAdapter struct {
	mu             sync.Mutex
	openFn         func(ctx context.Context, spec ThreadOpenSpec) (ThreadHandle, error)
	startFn        func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error)
	interruptFn    func(ctx context.Context, handle ThreadHandle, nativeTurnID string) error
	forceKillFn    func(handle ThreadHandle) error
	inspectFn      func(ctx context.Context, handle ThreadHandle) ([]NativeTurnRecord, error)
	closeFn        func(handle ThreadHandle) error
	interruptedIDs []string
	killedKeys     []string
	startCalls     int
}

func (f *fakeAdapter) HarnessID() string { return harnesses.HarnessCodex }

func (f *fakeAdapter) OpenThread(ctx context.Context, spec ThreadOpenSpec) (ThreadHandle, error) {
	if f.openFn != nil {
		return f.openFn(ctx, spec)
	}
	sid := spec.NativeSessionID
	if sid == "" {
		sid = "thr_" + spec.WorkerID
	}
	return ThreadHandle{Key: spec.WorkerID + ":" + sid, NativeSessionID: sid}, nil
}

func (f *fakeAdapter) StartTurn(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
	f.mu.Lock()
	f.startCalls++
	n := f.startCalls
	f.mu.Unlock()
	if f.startFn != nil {
		return f.startFn(ctx, handle, spec, onEvent)
	}
	ch := make(chan TurnOutcome, 1)
	nid := fmt.Sprintf("nturn_%d", n)
	ch <- TurnOutcome{
		State:        TurnCompleted,
		NativeTurnID: nid,
		Items: []NativeItem{
			{ItemID: "u1", Type: "userMessage", ClientID: spec.ClientCorrelationID},
			{ItemID: "a1", Type: "agentMessage", Phase: "final_answer", Text: "OK: " + spec.Prompt},
		},
	}
	return nid, ch, nil
}

func (f *fakeAdapter) InterruptTurn(ctx context.Context, handle ThreadHandle, nativeTurnID string) error {
	f.mu.Lock()
	f.interruptedIDs = append(f.interruptedIDs, handle.NativeSessionID+"/"+nativeTurnID)
	f.mu.Unlock()
	if f.interruptFn != nil {
		return f.interruptFn(ctx, handle, nativeTurnID)
	}
	return nil
}

func (f *fakeAdapter) ForceKill(handle ThreadHandle) error {
	f.mu.Lock()
	f.killedKeys = append(f.killedKeys, handle.Key)
	f.mu.Unlock()
	if f.forceKillFn != nil {
		return f.forceKillFn(handle)
	}
	return nil
}

func (f *fakeAdapter) InspectThreadTurns(ctx context.Context, handle ThreadHandle) ([]NativeTurnRecord, error) {
	if f.inspectFn != nil {
		return f.inspectFn(ctx, handle)
	}
	return nil, nil
}

func (f *fakeAdapter) CloseThread(handle ThreadHandle) error {
	if f.closeFn != nil {
		return f.closeFn(handle)
	}
	return nil
}

func setupTestService(t *testing.T, fa *fakeAdapter) (*Service, *harnesses.Service, harnesses.DelegatedWorkspace) {
	t.Helper()
	home := t.TempDir()
	wsDir := t.TempDir()
	hs, err := harnesses.Open(home, `C:\Asterhand\asterhand.exe`)
	if err != nil {
		t.Fatal(err)
	}
	hs.AntigravityConfigPath = filepath.Join(home, "ag_mcp.json")
	hs.ClaudeConfigPath = filepath.Join(home, "claude.json")
	hs.DisableVersionProbe = true
	hs.CmdRunner = func(ctx context.Context, exe string, args ...string) (string, string, error) {
		return "", "No MCP server named 'asterhand' found.", errors.New("exit 1")
	}
	// Point codex manual path to a real existing file so IsWorkerEnabled finds it.
	fakeExe := filepath.Join(home, "codex.exe")
	if err := os.WriteFile(fakeExe, []byte("stub"), 0600); err != nil {
		t.Fatal(err)
	}
	ws, _, err := hs.ResolveWorkspace(wsDir, harnesses.AccessReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	tr := true
	if _, err := hs.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessCodex,
		CollabWorkerEnabled: &tr,
		ManualPath:          &fakeExe,
	}); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(home, hs, fa)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = svc.Close(ctx)
	})
	return svc, hs, ws
}

func TestSelectFinalAnswerRules(t *testing.T) {
	// 1. Commentary + tool + final_answer -> picks final_answer only.
	ans, src, avail, partial := SelectFinalAnswer(TurnCompleted, []NativeItem{
		{ItemID: "i1", Type: "agentMessage", Phase: "commentary", Text: "Checking files..."},
		{ItemID: "i2", Type: "commandExecution", Text: "git diff"},
		{ItemID: "i3", Type: "agentMessage", Phase: "final_answer", Text: "Finding 1: fix nil guard."},
		{ItemID: "i3", Type: "agentMessage", Phase: "final_answer", Text: "Finding 1: fix nil guard."}, // duplicate itemId deduped
	})
	if !avail || src != AnswerSourceFinal || ans != "Finding 1: fix nil guard." || partial != "" {
		t.Fatalf("unexpected final_answer result: ans=%q src=%q avail=%v partial=%q", ans, src, avail, partial)
	}

	// 2. Mixed phase: commentary followed by untagged final assistant reply -> picks untagged reply!
	ans, src, avail, _ = SelectFinalAnswer(TurnCompleted, []NativeItem{
		{ItemID: "i1", Type: "agentMessage", Phase: "commentary", Text: "Inspecting plan..."},
		{ItemID: "i2", Type: "agentMessage", Phase: "", Text: "Plan looks solid except step 3."},
	})
	if !avail || src != AnswerSourceFallback || ans != "Plan looks solid except step 3." {
		t.Fatalf("unexpected mixed-phase result: ans=%q src=%q avail=%v", ans, src, avail)
	}

	// 3. Only commentary -> answer_available=false.
	ans, src, avail, _ = SelectFinalAnswer(TurnCompleted, []NativeItem{
		{ItemID: "i1", Type: "agentMessage", Phase: "commentary", Text: "Still thinking"},
	})
	if avail || src != AnswerSourceNone || ans != "" {
		t.Fatalf("expected unavailable for commentary-only: ans=%q src=%q avail=%v", ans, src, avail)
	}

	// 4. partial_answer followed by interrupted -> NEVER promoted to final answer!
	ans, src, avail, partial = SelectFinalAnswer(TurnInterrupted, []NativeItem{
		{ItemID: "i1", Type: "agentMessage", Phase: "partial_answer", Text: "Halfway review text"},
	})
	if avail || src != AnswerSourceNone || ans != "" || partial != "Halfway review text" {
		t.Fatalf("interrupted partial_answer must stay partial: ans=%q src=%q avail=%v partial=%q", ans, src, avail, partial)
	}
}

func TestDualProjectAliasesBusyCapacityAndLateCancel(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan string, 4)
	fa := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			nid := "nt_" + spec.ClientCorrelationID
			ch := make(chan TurnOutcome, 1)
			started <- nid
			go func() {
				<-gate
				ch <- TurnOutcome{
					State:        TurnCompleted,
					NativeTurnID: nid,
					Items: []NativeItem{
						{ItemID: "u", Type: "userMessage", ClientID: spec.ClientCorrelationID},
						{ItemID: "a", Type: "agentMessage", Phase: "final_answer", Text: "Done " + spec.Prompt},
					},
				}
			}()
			return nid, ch, nil
		},
	}
	svc, hs, wsA := setupTestService(t, fa)
	wsBDir := t.TempDir()
	wsB, _, err := hs.ResolveWorkspace(wsBDir, harnesses.AccessReadOnly)
	if err != nil {
		t.Fatal(err)
	}

	// Same alias "codex-reviewer" in two different projects succeeds without collision!
	wA, err := svc.Create(context.Background(), CreateInput{HarnessID: harnesses.HarnessCodex, WorkspaceRoot: wsA.RootPath, Alias: "codex-reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	wB, err := svc.Create(context.Background(), CreateInput{HarnessID: harnesses.HarnessCodex, WorkspaceRoot: wsB.RootPath, Alias: "codex-reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	if wA.ID == wB.ID || wA.WorkspaceID == wB.WorkspaceID {
		t.Fatalf("expected isolated workers per workspace: %+v vs %+v", wA, wB)
	}
	// Cross-workspace mismatch guard rejects mismatched expected workspace ID.
	if _, _, err := svc.Get(wA.ID, wsB.ID); err == nil {
		t.Fatal("expected workspace_mismatch")
	}

	// Single-worker concurrency: second send while first is active returns worker_busy.
	t1, err := svc.Send(context.Background(), SendInput{OperationID: "op1", WorkerID: wA.ID, Prompt: "first"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := svc.Send(context.Background(), SendInput{OperationID: "op2", WorkerID: wA.ID, Prompt: "second"}); err == nil || !strings.Contains(err.Error(), "worker_busy") {
		t.Fatalf("expected worker_busy, got %v", err)
	}

	// Node-wide active turn capacity = 1 blocks wB while wA is active!
	svc.SetBudgets(Budgets{MaxActiveTurns: 1})
	if _, err := svc.Send(context.Background(), SendInput{OperationID: "op3", WorkerID: wB.ID, Prompt: "other"}); err == nil || !strings.Contains(err.Error(), "worker_capacity") {
		t.Fatalf("expected worker_capacity, got %v", err)
	}

	// BeginStop(false) MUST return false while active turn exists!
	if svc.BeginStop(false, nil) {
		t.Fatal("expected BeginStop(false) to report busy while active turn exists")
	}

	// Let t1 complete; then verify late cancel of t1 does NOT interrupt a new t2!
	close(gate)
	done1, preview1, _, err := svc.Wait(context.Background(), t1.ID, wsA.ID, 5*time.Second)
	if err != nil || done1.State != TurnCompleted || preview1 != "Done first" {
		t.Fatalf("unexpected t1 wait: state=%s preview=%q err=%v", done1.State, preview1, err)
	}

	// Idle resident process MUST NOT block BeginStop(false) (test via a non-stopping check: we verify activeTurns==0).
	svc.mu.Lock()
	activeCnt := len(svc.activeTurns)
	residentCnt := len(svc.residents)
	svc.mu.Unlock()
	if activeCnt != 0 || residentCnt == 0 {
		t.Fatalf("expected 0 active turns and >=1 idle resident, got active=%d resident=%d", activeCnt, residentCnt)
	}

	// Late cancel on already completed t1 returns completed cleanly without interrupting anything.
	if c1, err := svc.Cancel(context.Background(), t1.ID, wsA.ID); err != nil || c1.State != TurnCompleted {
		t.Fatalf("late cancel should be safe no-op: %+v err=%v", c1, err)
	}
}

func TestInFlightTurnStartRevocationAndLateReceiptImmunity(t *testing.T) {
	inStart := make(chan struct{})
	releaseStart := make(chan struct{})
	fa := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			close(inStart)
			<-releaseStart
			ch := make(chan TurnOutcome, 1)
			return "late_native_turn_99", ch, nil
		},
	}
	svc, hs, ws := setupTestService(t, fa)
	svc.SetBudgets(Budgets{InterruptGrace: 150 * time.Millisecond})

	w, err := svc.Create(context.Background(), CreateInput{HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "rev-test"})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := svc.Send(context.Background(), SendInput{OperationID: "op_rev", WorkerID: w.ID, Prompt: "review"})
	if err != nil {
		t.Fatal(err)
	}
	<-inStart

	// Window 2: revoke worker authorization while turn/start is awaiting response!
	fl := false
	if _, err := hs.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessCodex,
		CollabWorkerEnabled: &fl,
	}); err != nil {
		t.Fatal(err)
	}

	done, _, _, err := svc.Wait(context.Background(), turn.ID, ws.ID, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if done.State != TurnCancelledRevoked || !done.RevocationRequested || done.AnswerAvailable {
		t.Fatalf("expected cancelled_authorization_revoked, got %+v", done)
	}

	// Release late turn/start response -> may record nativeTurnID for audit, NEVER flip state back to running!
	close(releaseStart)
	time.Sleep(150 * time.Millisecond)
	after, err := svc.GetTurn(turn.ID, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != TurnCancelledRevoked || after.AnswerAvailable {
		t.Fatalf("late turn/start receipt must not revive revoked turn: %+v", after)
	}
	if after.NativeTurnID != "late_native_turn_99" {
		t.Fatalf("expected late audit native_turn_id recorded, got %q", after.NativeTurnID)
	}
}

func TestCorrelationReconciliationRepeatedPromptAndRevokedProtection(t *testing.T) {
	call := 0
	var nativeHistory []NativeTurnRecord
	var histMu sync.Mutex
	fa := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			call++
			if call == 1 {
				rec := NativeTurnRecord{
					NativeTurnID: "native_1",
					Status:       TurnCompleted,
					Items: []NativeItem{
						{ItemID: "u1", Type: "userMessage", ClientID: spec.ClientCorrelationID},
						{ItemID: "a1", Type: "agentMessage", Phase: "final_answer", Text: "First review answer"},
					},
				}
				histMu.Lock()
				nativeHistory = append(nativeHistory, rec)
				histMu.Unlock()
				ch := make(chan TurnOutcome, 1)
				ch <- TurnOutcome{State: TurnCompleted, NativeTurnID: "native_1", Items: rec.Items}
				return "native_1", ch, nil
			}
			// Second turn with IDENTICAL prompt: receipt lost before returning!
			return "", nil, errors.New("broken pipe before turn/start reply")
		},
		inspectFn: func(ctx context.Context, handle ThreadHandle) ([]NativeTurnRecord, error) {
			histMu.Lock()
			defer histMu.Unlock()
			return append([]NativeTurnRecord(nil), nativeHistory...), nil
		},
	}
	svc, _, ws := setupTestService(t, fa)
	w, err := svc.Create(context.Background(), CreateInput{HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "corr-rev"})
	if err != nil {
		t.Fatal(err)
	}

	// Turn 1 succeeds.
	t1, err := svc.Send(context.Background(), SendInput{OperationID: "op_t1", WorkerID: w.ID, Prompt: "identical prompt"})
	if err != nil {
		t.Fatal(err)
	}
	r1, _, _, _ := svc.Wait(context.Background(), t1.ID, ws.ID, 5*time.Second)
	if r1.State != TurnCompleted {
		t.Fatalf("expected t1 completed, got %s", r1.State)
	}

	// Turn 2 with identical prompt loses turn/start receipt -> enters reconciliation_required.
	t2, err := svc.Send(context.Background(), SendInput{OperationID: "op_t2", WorkerID: w.ID, Prompt: "identical prompt"})
	if err != nil {
		t.Fatal(err)
	}
	r2, _, _, _ := svc.Wait(context.Background(), t2.ID, ws.ID, 5*time.Second)
	if r2.State != TurnReconcile || r2.AnswerAvailable {
		t.Fatalf("expected t2 reconciliation_required with no answer, got %+v", r2)
	}

	// Reconcile when native history only has turn 1 -> MUST NOT match turn 1 despite identical prompt!
	rec1, err := svc.ReconcileTurn(context.Background(), t2.ID)
	if err != nil || rec1.State != TurnReconcile {
		t.Fatalf("must stay reconciliation_required without unique client_correlation_id match, got %+v err=%v", rec1, err)
	}

	// Now suppose native history DOES contain unique client_correlation_id for t2 -> recovers completed!
	histMu.Lock()
	nativeHistory = append(nativeHistory, NativeTurnRecord{
		NativeTurnID: "native_2",
		Status:       TurnCompleted,
		Items: []NativeItem{
			{ItemID: "u2", Type: "userMessage", ClientID: t2.ClientCorrelationID},
			{ItemID: "a2", Type: "agentMessage", Phase: "final_answer", Text: "Second review answer"},
		},
	})
	histMu.Unlock()
	rec2, err := svc.ReconcileTurn(context.Background(), t2.ID)
	if err != nil || rec2.State != TurnCompleted || !rec2.AnswerAvailable || rec2.NativeTurnID != "native_2" {
		t.Fatalf("expected exact correlation recovery to completed, got %+v err=%v", rec2, err)
	}
	page, err := svc.ReadResult(t2.ID, ws.ID, 0, 1024)
	if err != nil || page.Text != "Second review answer" {
		t.Fatalf("unexpected recovered text: %+v err=%v", page, err)
	}

	// Contrast case: reconciliation_required turn with revocation_requested persisted MUST NEVER promote to completed!
	t3 := Turn{
		ID:                  "turn_rev_rec",
		WorkerID:            w.ID,
		WorkspaceID:         ws.ID,
		HarnessID:           harnesses.HarnessCodex,
		SessionEpoch:        1,
		OperationID:         "op_t3",
		ClientCorrelationID: "corr_turn_rev_rec",
		State:               TurnReconcile,
		RevocationRequested: true,
		RevocationReason:    "workspace revoked",
		StartedAt:           time.Now().UTC(),
		UpdatedAt:           time.Now().UTC(),
	}
	if err := svc.store.SaveTurn(t3); err != nil {
		t.Fatal(err)
	}
	histMu.Lock()
	nativeHistory = append(nativeHistory, NativeTurnRecord{
		NativeTurnID: "native_3",
		Status:       TurnCompleted,
		Items: []NativeItem{
			{ItemID: "u3", Type: "userMessage", ClientID: t3.ClientCorrelationID},
			{ItemID: "a3", Type: "agentMessage", Phase: "final_answer", Text: "Should not be published"},
		},
	})
	histMu.Unlock()
	rec3, err := svc.ReconcileTurn(context.Background(), t3.ID)
	if err != nil || rec3.State != TurnCancelledRevoked || rec3.AnswerAvailable || rec3.NativeTurnID != "native_3" {
		t.Fatalf("revoked turn must only record audit native_turn_id and never promote to completed: %+v err=%v", rec3, err)
	}
}

func TestStallTimeoutAndResultPagination(t *testing.T) {
	fa := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			if strings.Contains(spec.Prompt, "stall") {
				// Alive process that never emits events or completes!
				return "nt_stall", make(chan TurnOutcome, 1), nil
			}
			ch := make(chan TurnOutcome, 1)
			longText := strings.Repeat("Review finding line.\n", 2000) // ~42 KiB
			ch <- TurnOutcome{
				State:        TurnCompleted,
				NativeTurnID: "nt_long",
				Items:        []NativeItem{{ItemID: "a", Type: "agentMessage", Phase: "final_answer", Text: longText}},
			}
			return "nt_long", ch, nil
		},
	}
	svc, _, ws := setupTestService(t, fa)
	svc.SetBudgets(Budgets{StallTimeout: 180 * time.Millisecond, InterruptGrace: 100 * time.Millisecond})

	w, err := svc.Create(context.Background(), CreateInput{HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "stall-test"})
	if err != nil {
		t.Fatal(err)
	}

	// Stall turn times out, interrupts & force-kills, releases lock!
	ts, err := svc.Send(context.Background(), SendInput{OperationID: "op_stall", WorkerID: w.ID, Prompt: "stall forever"})
	if err != nil {
		t.Fatal(err)
	}
	doneStall, _, _, err := svc.Wait(context.Background(), ts.ID, ws.ID, 5*time.Second)
	if err != nil || doneStall.State != TurnInterrupted || doneStall.ErrorCode != "stall_timeout" {
		t.Fatalf("expected stall_timeout interrupted, got %+v err=%v", doneStall, err)
	}

	// Worker is unlocked and immediately accepts a long review turn with cursor pagination!
	tl, err := svc.Send(context.Background(), SendInput{OperationID: "op_long", WorkerID: w.ID, Prompt: "long review"})
	if err != nil {
		t.Fatalf("worker should be unlocked after stall timeout: %v", err)
	}
	doneLong, preview, trunc, err := svc.Wait(context.Background(), tl.ID, ws.ID, 5*time.Second)
	if err != nil || doneLong.State != TurnCompleted || !trunc || len(preview) == 0 {
		t.Fatalf("expected truncated preview on long review: state=%s trunc=%v len=%d err=%v", doneLong.State, trunc, len(preview), err)
	}
	p1, err := svc.ReadResult(tl.ID, ws.ID, 0, 10000)
	if err != nil || p1.Complete || p1.NextCursor == "" {
		t.Fatalf("expected paginated first slice: %+v err=%v", p1, err)
	}
	var assembled strings.Builder
	assembled.WriteString(p1.Text)
	cur := p1.NextCursor
	for cur != "" {
		var off int
		_, _ = fmt.Sscanf(cur, "%d", &off)
		p, err := svc.ReadResult(tl.ID, ws.ID, off, 10000)
		if err != nil {
			t.Fatal(err)
		}
		assembled.WriteString(p.Text)
		cur = p.NextCursor
	}
	if assembled.Len() != doneLong.ResultBytes {
		t.Fatalf("assembled length mismatch: got %d want %d", assembled.Len(), doneLong.ResultBytes)
	}

	// Verify UTF-8 boundary enforcement on manual misaligned cursor offsets.
	utf8Text := "中文分页测试ABC中文结束"
	if _, _, err := svc.store.SaveResultText(tl.ID, utf8Text, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// Byte 1 falls inside the first 3-byte UTF-8 rune '中' -> must fail with invalid_cursor.
	if _, err := svc.ReadResult(tl.ID, ws.ID, 1, 100); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("expected invalid_cursor on mid-rune UTF-8 offset, got %v", err)
	}
	// Aligned offset 3 ('文') succeeds cleanly without replacement characters.
	aligned, err := svc.ReadResult(tl.ID, ws.ID, 3, 100)
	if err != nil || aligned.Text != "文分页测试ABC中文结束" {
		t.Fatalf("expected clean aligned UTF-8 slice, got %+v err=%v", aligned, err)
	}
}

func TestWorkspaceWriteLockingCancelRevocationAndVCS(t *testing.T) {
	gate := make(chan struct{})
	var mu sync.Mutex
	turnCount := 0
	fa := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			mu.Lock()
			turnCount++
			n := turnCount
			mu.Unlock()
			nt := fmt.Sprintf("nt_w_%d", n)
			ch := make(chan TurnOutcome, 1)
			go func() {
				if strings.Contains(spec.Prompt, "hold") {
					<-gate
				}
				ch <- TurnOutcome{
					State:        TurnCompleted,
					NativeTurnID: nt,
					Items:        []NativeItem{{ItemID: "a", Type: "agentMessage", Phase: "final_answer", Text: "write done"}},
				}
			}()
			return nt, ch, nil
		},
	}
	svc, hs, _ := setupTestService(t, fa)
	svc.SetBudgets(Budgets{InterruptGrace: 100 * time.Millisecond})

	// Create a real temp Git repository with a dirty baseline file + subdirectory.
	repoDir := t.TempDir()
	subDir := filepath.Join(repoDir, "pkg", "sub")
	if err := os.MkdirAll(subDir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(context.Background(), repoDir, "init"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	_ = runGitNoErr(repoDir, "config", "user.email", "test@example.com")
	_ = runGitNoErr(repoDir, "config", "user.name", "Test")
	_ = os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("init\n"), 0600)
	_ = runGitNoErr(repoDir, "add", "README.md")
	_ = runGitNoErr(repoDir, "commit", "-m", "init")
	_ = os.WriteFile(filepath.Join(repoDir, "dirty.txt"), []byte("untracked\n"), 0600)

	// 1. workspace_write is rejected while AllowWorkspaceWrite is false (default).
	if _, err := svc.Create(context.Background(), CreateInput{
		HarnessID:     harnesses.HarnessCodex,
		WorkspaceRoot: repoDir,
		Alias:         "writer-root",
		AccessMode:    harnesses.AccessWorkspaceWrite,
	}); err == nil {
		t.Fatal("expected workspace_write rejected before AllowWorkspaceWrite is enabled")
	}

	// Enable AllowWorkspaceWrite on Codex.
	tr, fl := true, false
	if _, err := hs.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessCodex,
		AllowWorkspaceWrite: &tr,
	}); err != nil {
		t.Fatal(err)
	}

	// Create two write workers: one at repo root, one at a subdirectory (same normalized Git worktree key!).
	wRoot, err := svc.Create(context.Background(), CreateInput{
		HarnessID:     harnesses.HarnessCodex,
		WorkspaceRoot: repoDir,
		Alias:         "writer-root",
		AccessMode:    harnesses.AccessWorkspaceWrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	wSub, err := svc.Create(context.Background(), CreateInput{
		HarnessID:     harnesses.HarnessCodex,
		WorkspaceRoot: subDir,
		Alias:         "writer-sub",
		AccessMode:    harnesses.AccessWorkspaceWrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	if wRoot.WorktreeKey != wSub.WorktreeKey {
		t.Fatalf("expected root and subdir to share Git worktree key: %q vs %q", wRoot.WorktreeKey, wSub.WorktreeKey)
	}

	// 2. Concurrent write turn on subdirectory is blocked by worktree write lock!
	t1, err := svc.Send(context.Background(), SendInput{OperationID: "op_w1", WorkerID: wRoot.ID, Prompt: "hold write 1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Send(context.Background(), SendInput{OperationID: "op_w2_busy", WorkerID: wSub.ID, Prompt: "second write"}); err == nil || !strings.Contains(err.Error(), "active write turn") {
		t.Fatalf("expected worktree write mutex rejection, got %v", err)
	}

	// 3. Cancel t1 releases the worktree write lock so wSub can now run and capture VCS snapshots!
	cancelled, err := svc.Cancel(context.Background(), t1.ID, "")
	if err != nil || cancelled.State != TurnCancelled {
		t.Fatalf("expected t1 cancelled, got %+v err=%v", cancelled, err)
	}
	t2, err := svc.Send(context.Background(), SendInput{OperationID: "op_w2_ok", WorkerID: wSub.ID, Prompt: "quick write"})
	if err != nil {
		t.Fatalf("expected worktree write lock released after cancel: %v", err)
	}
	done2, _, _, err := svc.Wait(context.Background(), t2.ID, "", 5*time.Second)
	if err != nil || done2.State != TurnCompleted {
		t.Fatalf("expected t2 completed, got %+v err=%v", done2, err)
	}
	if done2.BaselineVCS == nil || done2.BaselineVCS.VCS != "git" || !done2.BaselineVCS.DirtyBaseline || !done2.BaselineVCS.ExternalChangesPossible {
		t.Fatalf("expected git dirty baseline BaselineVCS, got %+v", done2.BaselineVCS)
	}
	if done2.AfterVCS == nil || done2.AfterVCS.UntrackedCount < 1 {
		t.Fatalf("expected AfterVCS snapshot, got %+v", done2.AfterVCS)
	}

	// 4. Disabling AllowWorkspaceWrite revokes only active workspace_write turns, leaving read_only turns intact!
	wRO, err := svc.Create(context.Background(), CreateInput{
		HarnessID:     harnesses.HarnessCodex,
		WorkspaceRoot: repoDir,
		Alias:         "reader-root",
		AccessMode:    harnesses.AccessReadOnly,
	})
	if err != nil {
		t.Fatal(err)
	}
	tWrite, err := svc.Send(context.Background(), SendInput{OperationID: "op_w_rev", WorkerID: wRoot.ID, Prompt: "hold write rev"})
	if err != nil {
		t.Fatal(err)
	}
	tRead, err := svc.Send(context.Background(), SendInput{OperationID: "op_r_keep", WorkerID: wRO.ID, Prompt: "hold read keep"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if _, err := hs.UpdatePolicy(harnesses.UpdatePolicyInput{HarnessID: harnesses.HarnessCodex, AllowWorkspaceWrite: &fl}); err != nil {
		t.Fatal(err)
	}
	revDone, _, _, err := svc.Wait(context.Background(), tWrite.ID, "", 5*time.Second)
	if err != nil || revDone.State != TurnCancelledRevoked {
		t.Fatalf("expected write turn cancelled_authorization_revoked, got %+v err=%v", revDone, err)
	}
	close(gate)
	roDone, _, _, err := svc.Wait(context.Background(), tRead.ID, "", 5*time.Second)
	if err != nil || roDone.State != TurnCompleted {
		t.Fatalf("expected read_only turn to finish normally after write revocation, got %+v err=%v", roDone, err)
	}
}

func runGitNoErr(dir string, args ...string) error {
	_, err := runGit(context.Background(), dir, args...)
	return err
}
