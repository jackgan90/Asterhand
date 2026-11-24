package workers

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/harnesses"
)

func TestWaitAnyMultiByteTruncationReturnsAlignedNextCursor(t *testing.T) {
	longText := strings.Repeat("审", 2000) // 6000 UTF-8 bytes (> 4 KiB WaitAny preview cap)
	fa := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			ch := make(chan TurnOutcome, 1)
			ch <- TurnOutcome{
				State:        TurnCompleted,
				NativeTurnID: "nt_mb",
				Items:        []NativeItem{{ItemID: "f", Type: "agentMessage", Phase: "final_answer", Text: longText}},
			}
			return "nt_mb", ch, nil
		},
	}
	svc, _, ws := setupTestService(t, fa)
	defer func() { _ = svc.Close(context.Background()) }()

	w, err := svc.Create(context.Background(), CreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "mb", AccessMode: harnesses.AccessReadOnly,
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := svc.Send(context.Background(), SendInput{OperationID: "op_mb", WorkerID: w.ID, Prompt: "long utf8"})
	if err != nil {
		t.Fatal(err)
	}
	items, trig, err := svc.WaitAny(context.Background(), []string{turn.ID}, ws.ID, 3*time.Second)
	if err != nil || trig != turn.ID || len(items) != 1 {
		t.Fatalf("unexpected WaitAny result: trig=%q items=%+v err=%v", trig, items, err)
	}
	it := items[0]
	if !it.Truncated || it.NextCursor == "" {
		t.Fatalf("expected truncated=true and non-empty next_cursor, got %+v", it)
	}
	offset, err := strconv.Atoi(it.NextCursor)
	if err != nil || offset != len(it.AnswerPreview) {
		t.Fatalf("expected next_cursor %d, got %q (err=%v)", len(it.AnswerPreview), it.NextCursor, err)
	}
	page, err := svc.ReadResult(turn.ID, ws.ID, offset, 24<<10)
	if err != nil || !page.Complete {
		t.Fatalf("expected clean ReadResult continuation from WaitAny next_cursor, got page=%+v err=%v", page, err)
	}
	if it.AnswerPreview+page.Text != longText {
		t.Fatalf("reconstructed UTF-8 text mismatch: got %d bytes, want %d bytes", len(it.AnswerPreview+page.Text), len(longText))
	}
}

func TestStartupRecoveryReconcilesWaitingApprovalAndWaitingInput(t *testing.T) {
	home := t.TempDir()
	st, err := OpenStore(home)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	oldTime := now.Add(-10 * 24 * time.Hour)

	if err := st.SaveWorker(Worker{
		ID: "wkr_wait_1", WorkspaceID: "ws_1", HarnessID: harnesses.HarnessCodex,
		AccessMode: harnesses.AccessWorkspaceWrite, State: WorkerRunning, ActiveTurnID: "turn_wait_app",
		UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveWorker(Worker{
		ID: "wkr_wait_2", WorkspaceID: "ws_1", HarnessID: harnesses.HarnessClaudeCode,
		AccessMode: harnesses.AccessReadOnly, State: WorkerRunning, ActiveTurnID: "turn_wait_inp",
		UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTurn(Turn{
		ID: "turn_wait_app", WorkerID: "wkr_wait_1", WorkspaceID: "ws_1", HarnessID: harnesses.HarnessCodex,
		OperationID: "op_app", ClientCorrelationID: "corr_app", State: TurnWaitingApproval,
		PendingInteraction: &InteractionView{RequestID: "req_app", Kind: InteractionApproval, TurnID: "turn_wait_app"},
		StartedAt: oldTime, UpdatedAt: oldTime,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTurn(Turn{
		ID: "turn_wait_inp", WorkerID: "wkr_wait_2", WorkspaceID: "ws_1", HarnessID: harnesses.HarnessClaudeCode,
		OperationID: "op_inp", ClientCorrelationID: "corr_inp", State: TurnWaitingInput,
		PendingInteraction: &InteractionView{RequestID: "req_inp", Kind: InteractionInput, TurnID: "turn_wait_inp"},
		StartedAt: oldTime, UpdatedAt: oldTime,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SaveResultText("turn_wait_app", "keep-me", oldTime); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendLog("turn_wait_inp", LogEntry{Timestamp: oldTime, Kind: "tool", Summary: "keep-log"}); err != nil {
		t.Fatal(err)
	}

	nonTerminal, err := st.ListNonTerminalTurns()
	if err != nil || len(nonTerminal) != 2 {
		t.Fatalf("expected 2 non-terminal waiting turns, got %d (err=%v)", len(nonTerminal), err)
	}
	st.PruneExpiredDetails(now)
	if _, ok, _ := st.ReadResultText("turn_wait_app"); !ok {
		t.Fatal("expected waiting_approval turn result protected from expiration pruning")
	}
	if logs, _, _ := st.ReadLogs("turn_wait_inp", 0, 10); len(logs) != 1 {
		t.Fatal("expected waiting_input turn logs protected from expiration pruning")
	}
	_ = st.Close()

	// Reopen Service: waiting_approval and waiting_input must move to reconciliation_required and clear stale PendingInteraction.
	hs, err := harnesses.Open(home, "")
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(home, hs, &fakeAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc.Close(context.Background()) }()

	for _, pair := range []struct {
		turnID   string
		workerID string
	}{
		{"turn_wait_app", "wkr_wait_1"},
		{"turn_wait_inp", "wkr_wait_2"},
	} {
		turn, err := svc.GetTurn(pair.turnID, "")
		if err != nil {
			t.Fatalf("GetTurn(%s): %v", pair.turnID, err)
		}
		if turn.State != TurnReconcile || turn.PendingInteraction != nil {
			t.Fatalf("expected %s reconciled with nil PendingInteraction, got state=%s pi=%+v", pair.turnID, turn.State, turn.PendingInteraction)
		}
		w, _, err := svc.Get(pair.workerID, "")
		if err != nil || w.State != WorkerReconcile {
			t.Fatalf("expected worker %s in WorkerReconcile, got %+v err=%v", pair.workerID, w, err)
		}
	}
}
