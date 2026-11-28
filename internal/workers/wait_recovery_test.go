package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackgan90/Asterhand/internal/automation"
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

func makeLongWaitAnyAnswers(count int) []string {
	answers := make([]string, count)
	for i := 0; i < count; i++ {
		switch i % 3 {
		case 0:
			answers[i] = strings.TrimSpace(fmt.Sprintf("report-%02d: ", i) + strings.Repeat("plain review finding line\n", 360))
		case 1:
			answers[i] = strings.TrimSpace(fmt.Sprintf("中文报告-%02d：", i) + strings.Repeat("审阅结论片段，包含多字节字符。", 260))
		default:
			answers[i] = strings.TrimSpace(fmt.Sprintf("escaped-%02d: ", i) + strings.Repeat("<tag attr=\"v\">&amp;\n", 450))
		}
	}
	return answers
}

func readFullResultFromPreview(t *testing.T, svc *Service, wsID string, it WaitAnyItem) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString(it.AnswerPreview)
	cursor := it.NextCursor
	for cursor != "" {
		off, err := strconv.Atoi(cursor)
		if err != nil {
			t.Fatalf("turn %s invalid cursor %q: %v", it.TurnID, cursor, err)
		}
		page, err := svc.ReadResult(it.TurnID, wsID, off, 24<<10)
		if err != nil {
			t.Fatalf("turn %s ReadResult(offset=%d): %v", it.TurnID, off, err)
		}
		sb.WriteString(page.Text)
		if page.Complete {
			cursor = ""
		} else {
			cursor = page.NextCursor
		}
	}
	return sb.String()
}

func TestWaitAnyBudget16LongAnswersAndSeamlessReadResult(t *testing.T) {
	answers := makeLongWaitAnyAnswers(16)
	idx := 0
	fa := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			cur := idx
			idx++
			ch := make(chan TurnOutcome, 1)
			go func() {
				if onEvent != nil {
					txt := fmt.Sprintf("Intermediate commentary %02d: %s", cur, strings.Repeat("detail ", 240))
					it := NativeItem{ItemID: fmt.Sprintf("c_%d", cur), Type: "agentMessage", Phase: "commentary", Text: txt}
					onEvent(AdapterEvent{Kind: "commentary", ItemID: it.ItemID, Phase: "commentary", Summary: truncateSummary(txt, 160), Item: &it})
				}
				ch <- TurnOutcome{
					State:        TurnCompleted,
					NativeTurnID: fmt.Sprintf("nt_b16_%d", cur),
					Items:        []NativeItem{{ItemID: fmt.Sprintf("f_%d", cur), Type: "agentMessage", Phase: "final_answer", Text: answers[cur]}},
				}
			}()
			return fmt.Sprintf("nt_b16_%d", cur), ch, nil
		},
	}
	svc, _, ws := setupTestService(t, fa)
	defer func() { _ = svc.Close(context.Background()) }()

	w, err := svc.Create(context.Background(), CreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "b16", AccessMode: harnesses.AccessReadOnly,
	})
	if err != nil {
		t.Fatal(err)
	}
	turnIDs := make([]string, 0, 16)
	for i := 0; i < 16; i++ {
		turn, err := svc.Send(context.Background(), SendInput{
			OperationID: fmt.Sprintf("op_b16_%02d", i), WorkerID: w.ID, Prompt: fmt.Sprintf("review module %d", i),
		})
		if err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
		if done, _, _, err := svc.Wait(context.Background(), turn.ID, ws.ID, 5*time.Second); err != nil || done.State != TurnCompleted {
			t.Fatalf("Wait %d: state=%s err=%v", i, done.State, err)
		}
		turnIDs = append(turnIDs, turn.ID)
	}

	items, trig, err := svc.WaitAny(context.Background(), turnIDs, ws.ID, 3*time.Second)
	if err != nil || trig != turnIDs[0] || len(items) != 16 {
		t.Fatalf("WaitAny(16 turns): trig=%q len=%d err=%v", trig, len(items), err)
	}
	envBytes, err := json.Marshal(waitAnyEnvelope{TriggeredTurnID: trig, Items: items})
	if err != nil || len(envBytes) > waitAnyMaxPayloadJSONBytes || len(envBytes) >= automation.MaxResponse {
		t.Fatalf("serialized WaitAny payload %d bytes exceeds budget %d (err=%v)", len(envBytes), waitAnyMaxPayloadJSONBytes, err)
	}
	for i, it := range items {
		if it.TurnID != turnIDs[i] || it.State != TurnCompleted || !it.AnswerAvailable || it.ResultBytes != len(answers[i]) {
			t.Fatalf("item[%d] unexpected status: %+v", i, it)
		}
		if !utf8.ValidString(it.AnswerPreview) || !it.Truncated || len(it.AnswerPreview) == 0 || it.NextCursor != strconv.Itoa(len(it.AnswerPreview)) {
			t.Fatalf("item[%d] invalid preview/cursor: trunc=%v cursor=%q previewLen=%d", i, it.Truncated, it.NextCursor, len(it.AnswerPreview))
		}
		if got := readFullResultFromPreview(t, svc, ws.ID, it); got != answers[i] {
			t.Fatalf("item[%d] reconstructed text mismatch: got %d bytes, want %d bytes", i, len(got), len(answers[i]))
		}
	}
}

func TestWaitAnyBudgetRunningCommentaries(t *testing.T) {
	items := make([]WaitAnyItem, 16)
	candidates := make([]waitAnyCandidate, 16)
	now := time.Now().UTC()
	for i := 0; i < 16; i++ {
		items[i] = WaitAnyItem{
			TurnID: fmt.Sprintf("turn_run_%02d", i), WorkerID: "wkr_1", WorkspaceID: "ws_1",
			HarnessID: harnesses.HarnessCodex, State: TurnRunning, Progress: "Working...", LatestCommentarySeq: 2,
		}
		candidates[i] = waitAnyCandidate{
			comments: []CommentaryEntry{
				{Seq: 1, Timestamp: now, Text: strings.Repeat("旧旁白片段", 250)},
				{Seq: 2, Timestamp: now, Text: strings.Repeat("新旁白<>&\"内容", 150)},
			},
		}
	}
	fitWaitAnyBudget("", items, candidates, waitAnyMaxPayloadJSONBytes)
	raw, err := json.Marshal(waitAnyEnvelope{Items: items})
	if err != nil || len(raw) > waitAnyMaxPayloadJSONBytes {
		t.Fatalf("running commentaries WaitAny payload %d > %d (err=%v)", len(raw), waitAnyMaxPayloadJSONBytes, err)
	}
	for i, it := range items {
		if it.TurnID != fmt.Sprintf("turn_run_%02d", i) || it.State != TurnRunning || it.LatestCommentarySeq != 2 {
			t.Fatalf("item[%d] lost metadata: %+v", i, it)
		}
		if len(it.Commentaries) == 0 || !utf8.ValidString(it.Commentaries[len(it.Commentaries)-1].Text) {
			t.Fatalf("item[%d] expected valid UTF-8 commentary preserved, got %+v", i, it.Commentaries)
		}
		if !it.CommentariesTruncated || it.DeliveredCommentarySeq == nil || *it.DeliveredCommentarySeq != 0 {
			t.Fatalf("item[%d] expected commentaries_truncated=true and delivered_commentary_seq=0, got trunc=%v delivered=%v", i, it.CommentariesTruncated, it.DeliveredCommentarySeq)
		}
	}
}

func TestWaitAnyBudgetBaseCompactionAndWaterFillingFairness(t *testing.T) {
	heavyItems := make([]WaitAnyItem, 16)
	heavyCands := make([]waitAnyCandidate, 16)
	longFiles := make([]string, 32)
	for j := range longFiles {
		longFiles[j] = fmt.Sprintf("very/deep/module/path/segment_%02d/extremely_long_filename_for_budget_test_%02d.go", j, j)
	}
	for i := 0; i < 16; i++ {
		heavyItems[i] = WaitAnyItem{
			TurnID: fmt.Sprintf("turn_heavy_%02d", i), WorkerID: "wkr_heavy", WorkspaceID: "ws_heavy",
			HarnessID: harnesses.HarnessDeepSeek, State: TurnCompleted, Progress: strings.Repeat("p", 300),
			AnswerAvailable: true, AnswerSource: AnswerSourceFinal, Truncated: true, ResultBytes: 8192, NextCursor: "0",
			ErrorMessage: strings.Repeat("err detail ", 250),
			AfterVCS:     &VCSSnapshot{VCS: "git", Branch: "main", HeadCommit: "8547297", ChangedFiles: append([]string(nil), longFiles...)},
		}
		heavyCands[i] = waitAnyCandidate{previewChunk: strings.Repeat("Z", waitAnyPerItemPreviewBytes+4), totalBytes: 8192}
	}
	fitWaitAnyBudget("turn_heavy_05", heavyItems, heavyCands, waitAnyMaxPayloadJSONBytes)
	if heavyRaw, err := json.Marshal(waitAnyEnvelope{TriggeredTurnID: "turn_heavy_05", Items: heavyItems}); err != nil || len(heavyRaw) > waitAnyMaxPayloadJSONBytes {
		t.Fatalf("heavy base WaitAny payload %d > %d (err=%v)", len(heavyRaw), waitAnyMaxPayloadJSONBytes, err)
	}
	for i, it := range heavyItems {
		if it.TurnID != fmt.Sprintf("turn_heavy_%02d", i) || it.State != TurnCompleted || !it.Truncated || it.NextCursor != strconv.Itoa(len(it.AnswerPreview)) {
			t.Fatalf("heavy item[%d] lost required cursor/state fields: %+v", i, it)
		}
	}

	// Water-filling fairness: 10 short answers + 6 long answers must give short answers
	// untruncated full text and let the 6 long answers each claim the full 4 KiB per-item cap.
	mixItems := make([]WaitAnyItem, 16)
	mixCands := make([]waitAnyCandidate, 16)
	for i := 0; i < 16; i++ {
		body := fmt.Sprintf("short-%02d", i)
		if i >= 10 {
			body = fmt.Sprintf("long-%02d-", i) + strings.Repeat("A", 8192)
		}
		mixItems[i] = WaitAnyItem{
			TurnID: fmt.Sprintf("turn_mix_%02d", i), WorkerID: "wkr_mix", WorkspaceID: "ws_mix",
			HarnessID: harnesses.HarnessCodex, State: TurnCompleted, AnswerAvailable: true,
			AnswerSource: AnswerSourceFinal, Truncated: true, ResultBytes: len(body), NextCursor: "0",
		}
		mixCands[i] = waitAnyCandidate{previewChunk: body, totalBytes: len(body)}
	}
	fitWaitAnyBudget("turn_mix_00", mixItems, mixCands, waitAnyMaxPayloadJSONBytes)
	if mixRaw, err := json.Marshal(waitAnyEnvelope{TriggeredTurnID: "turn_mix_00", Items: mixItems}); err != nil || len(mixRaw) > waitAnyMaxPayloadJSONBytes {
		t.Fatalf("mixed WaitAny payload %d > %d (err=%v)", len(mixRaw), waitAnyMaxPayloadJSONBytes, err)
	}
	for i, it := range mixItems {
		if i < 10 {
			if it.Truncated || it.NextCursor != "" || it.AnswerPreview != mixCands[i].previewChunk {
				t.Fatalf("short item[%d] expected full untruncated answer, got trunc=%v cursor=%q preview=%q", i, it.Truncated, it.NextCursor, it.AnswerPreview)
			}
		} else if !it.Truncated || len(it.AnswerPreview) != waitAnyPerItemPreviewBytes || it.NextCursor != strconv.Itoa(waitAnyPerItemPreviewBytes) {
			t.Fatalf("long item[%d] expected full 4 KiB cap via water-filling, got trunc=%v len=%d cursor=%q", i, it.Truncated, len(it.AnswerPreview), it.NextCursor)
		}
	}
}

func makeOversizedGofmtCommand() string {
	var b strings.Builder
	b.WriteString("gofmt -w")
	for i := 0; i < 584; i++ {
		fmt.Fprintf(&b, " internal/module_%02d/handler_file_%03d.go", i%20, i)
	}
	return b.String()
}

func TestWaitAnyBudgetOversizedPendingInteractionsPreservesHumanFullCommand(t *testing.T) {
	bigCmd := makeOversizedGofmtCommand()
	if len(bigCmd) < 20000 {
		t.Fatalf("expected >=20KB command, got %d", len(bigCmd))
	}
	release := make(chan struct{})
	fa := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			ch := make(chan TurnOutcome, 1)
			go func() {
				_, _ = spec.OnInteraction(context.Background(), InteractionRequest{
					Kind:     InteractionApproval,
					ToolName: "bash",
					Summary:  "Run gofmt across 584 files",
					Command:  bigCmd,
					Paths:    []string{"internal/a.go", "internal/b.go"},
				})
				<-release
				ch <- TurnOutcome{State: TurnCancelled, NativeTurnID: "nt_big_app"}
			}()
			return "nt_big_app", ch, nil
		},
	}
	svc, hs, ws := setupTestService(t, fa)
	defer func() {
		close(release)
		_ = svc.Close(context.Background())
	}()
	tr := true
	if _, err := hs.UpdatePolicy(harnesses.UpdatePolicyInput{HarnessID: harnesses.HarnessCodex, AllowWorkspaceWrite: &tr}); err != nil {
		t.Fatal(err)
	}

	turnIDs := make([]string, 0, 3)
	workerIDs := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		dir := t.TempDir()
		w, err := svc.Create(context.Background(), CreateInput{
			HarnessID: harnesses.HarnessCodex, WorkspaceRoot: dir, Alias: fmt.Sprintf("fmt-%d", i),
			AccessMode: harnesses.AccessWorkspaceWrite, ApprovalMode: ApprovalModeAskHuman,
		})
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
		turn, err := svc.Send(context.Background(), SendInput{OperationID: fmt.Sprintf("op_fmt_%d", i), WorkerID: w.ID, Prompt: "format"})
		if err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
		workerIDs = append(workerIDs, w.ID)
		turnIDs = append(turnIDs, turn.ID)
	}

	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if len(svc.ListHumanInteractions()) == 3 {
			break
		}
	}
	if len(svc.ListHumanInteractions()) != 3 {
		t.Fatalf("expected 3 pending human approvals, got %d", len(svc.ListHumanInteractions()))
	}

	items, trig, err := svc.WaitAny(context.Background(), turnIDs, "", 50*time.Millisecond)
	if err != nil || trig != "" || len(items) != 3 {
		t.Fatalf("WaitAny(3 oversized approvals): trig=%q len=%d err=%v", trig, len(items), err)
	}
	raw, err := json.Marshal(waitAnyEnvelope{TriggeredTurnID: trig, Items: items})
	if err != nil || len(raw) > waitAnyMaxPayloadJSONBytes {
		t.Fatalf("oversized approvals WaitAny payload %d > %d (err=%v)", len(raw), waitAnyMaxPayloadJSONBytes, err)
	}
	for i, it := range items {
		pi := it.PendingInteraction
		if it.State != TurnWaitingApproval || pi == nil || pi.RequestID == "" || pi.Kind != InteractionApproval || !pi.VisibleToHuman {
			t.Fatalf("item[%d] lost required approval metadata: %+v", i, it)
		}
		if !pi.DetailsTruncated || pi.Summary == "" || !strings.Contains(pi.InspectHint, workerIDs[i]) {
			t.Fatalf("item[%d] expected details_truncated with summary and inspect_hint, got %+v", i, pi)
		}
		if _, turns, gErr := svc.Get(workerIDs[i], ""); gErr != nil || len(turns) == 0 || turns[0].PendingInteraction == nil || turns[0].PendingInteraction.Command != bigCmd {
			t.Fatalf("worker %s single-turn Get must keep full command", workerIDs[i])
		}
	}
	for _, h := range svc.ListHumanInteractions() {
		if h.Command != bigCmd {
			t.Fatalf("human interaction %s lost full command: got %d bytes, want %d", h.RequestID, len(h.Command), len(bigCmd))
		}
	}
	_ = ws
}

func TestWaitAnyBudgetTruncatedCommentaryExposesResumableSeq(t *testing.T) {
	const tailSentence = "LAST_REVIEW_SENTENCE_MUST_SURVIVE"
	htmlCommentary := strings.Repeat(`<div class="finding">&lt;check&gt;</div>`, 48) + " " + tailSentence
	answers := makeLongWaitAnyAnswers(15)
	releaseRunning := make(chan struct{})
	commentarySent := make(chan struct{})
	idx := 0
	fa := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			if strings.HasPrefix(spec.Prompt, "running-html") {
				ch := make(chan TurnOutcome, 1)
				go func() {
					if onEvent != nil {
						it := NativeItem{ItemID: "c_html_1", Type: "agentMessage", Phase: "commentary", Text: htmlCommentary}
						onEvent(AdapterEvent{Kind: "commentary", ItemID: it.ItemID, Phase: "commentary", Summary: truncateSummary(htmlCommentary, 160), Item: &it})
					}
					close(commentarySent)
					<-releaseRunning
					ch <- TurnOutcome{State: TurnCancelled, NativeTurnID: "nt_run_html"}
				}()
				return "nt_run_html", ch, nil
			}
			cur := idx
			idx++
			ch := make(chan TurnOutcome, 1)
			ch <- TurnOutcome{
				State:        TurnCompleted,
				NativeTurnID: fmt.Sprintf("nt_done_%d", cur),
				Items:        []NativeItem{{ItemID: fmt.Sprintf("f_%d", cur), Type: "agentMessage", Phase: "final_answer", Text: answers[cur]}},
			}
			return fmt.Sprintf("nt_done_%d", cur), ch, nil
		},
	}
	svc, _, ws := setupTestService(t, fa)
	defer func() {
		close(releaseRunning)
		_ = svc.Close(context.Background())
	}()

	wDone, err := svc.Create(context.Background(), CreateInput{HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "done15", AccessMode: harnesses.AccessReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	wRun, err := svc.Create(context.Background(), CreateInput{HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "run-html", AccessMode: harnesses.AccessReadOnly})
	if err != nil {
		t.Fatal(err)
	}

	turnIDs := make([]string, 0, 16)
	for i := 0; i < 15; i++ {
		turn, err := svc.Send(context.Background(), SendInput{OperationID: fmt.Sprintf("op_c15_%02d", i), WorkerID: wDone.ID, Prompt: fmt.Sprintf("done %d", i)})
		if err != nil {
			t.Fatal(err)
		}
		if done, _, _, err := svc.Wait(context.Background(), turn.ID, ws.ID, 5*time.Second); err != nil || done.State != TurnCompleted {
			t.Fatalf("done turn %d: state=%s err=%v", i, done.State, err)
		}
		turnIDs = append(turnIDs, turn.ID)
	}
	runTurn, err := svc.Send(context.Background(), SendInput{OperationID: "op_run_html", WorkerID: wRun.ID, Prompt: "running-html"})
	if err != nil {
		t.Fatal(err)
	}
	<-commentarySent
	turnIDs = append(turnIDs, runTurn.ID)

	items, _, err := svc.WaitAny(context.Background(), turnIDs, ws.ID, 3*time.Second)
	if err != nil || len(items) != 16 {
		t.Fatalf("WaitAny(15 done + 1 html running): len=%d err=%v", len(items), err)
	}
	runItem := items[15]
	if runItem.LatestCommentarySeq != 1 || !runItem.CommentariesTruncated || runItem.DeliveredCommentarySeq == nil || *runItem.DeliveredCommentarySeq != 0 {
		t.Fatalf("expected latest=1, truncated=true, delivered=0; got %+v", runItem)
	}
	if len(runItem.Commentaries) == 1 && (!runItem.Commentaries[0].Truncated || strings.Contains(runItem.Commentaries[0].Text, tailSentence)) {
		t.Fatalf("expected truncated commentary entry without tail sentence, got %+v", runItem.Commentaries[0])
	}

	// Resume using DeliveredCommentarySeq via WaitWithOptions -> must recover full commentary including tail sentence.
	_, _, _, resumed, err := svc.WaitWithOptions(context.Background(), runTurn.ID, ws.ID, WaitOptions{
		WaitDuration:          3 * time.Second,
		AfterCommentarySeq:    *runItem.DeliveredCommentarySeq,
		HasAfterCommentarySeq: true,
		WakeOnCommentary:      true,
	})
	if err != nil || len(resumed) != 1 || resumed[0].Text != htmlCommentary || !strings.Contains(resumed[0].Text, tailSentence) {
		t.Fatalf("resuming from delivered_commentary_seq=%d did not recover full commentary: resumed=%+v err=%v", *runItem.DeliveredCommentarySeq, resumed, err)
	}
}

func TestWaitExplicitZeroAfterCommentarySeqRecoversFirstEntryAfterNewUpdates(t *testing.T) {
	const tailSentence = "EXPLICIT_ZERO_FIRST_ENTRY_TAIL"
	firstText := strings.Repeat(`<p>&lt;item&gt;</p>`, 95) + " " + tailSentence
	allSent := make(chan struct{})
	release := make(chan struct{})
	fa := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			ch := make(chan TurnOutcome, 1)
			go func() {
				if onEvent != nil {
					for i, txt := range []string{firstText, "Second commentary update", "Third commentary update"} {
						it := NativeItem{ItemID: fmt.Sprintf("c_z_%d", i+1), Type: "agentMessage", Phase: "commentary", Text: txt}
						onEvent(AdapterEvent{Kind: "commentary", ItemID: it.ItemID, Phase: "commentary", Summary: truncateSummary(txt, 160), Item: &it})
					}
				}
				close(allSent)
				<-release
				ch <- TurnOutcome{State: TurnCancelled, NativeTurnID: "nt_z3"}
			}()
			return "nt_z3", ch, nil
		},
	}
	svc, _, ws := setupTestService(t, fa)
	defer func() {
		close(release)
		_ = svc.Close(context.Background())
	}()
	w, err := svc.Create(context.Background(), CreateInput{HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "z3", AccessMode: harnesses.AccessReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := svc.Send(context.Background(), SendInput{OperationID: "op_z3", WorkerID: w.ID, Prompt: "three commentaries"})
	if err != nil {
		t.Fatal(err)
	}
	<-allSent

	// Omitted cursor keeps default tail [2, 3].
	_, _, _, omitted, err := svc.WaitWithOptions(context.Background(), turn.ID, ws.ID, WaitOptions{
		WaitDuration:     3 * time.Second,
		WakeOnCommentary: true,
	})
	if err != nil || len(omitted) != 2 || omitted[0].Seq != 2 || omitted[1].Seq != 3 {
		t.Fatalf("omitted after_commentary_seq expected [2, 3], got %+v err=%v", omitted, err)
	}

	// Explicit 0 returns [1, 2, 3] with complete first commentary text.
	_, _, _, fromZero, err := svc.WaitWithOptions(context.Background(), turn.ID, ws.ID, WaitOptions{
		WaitDuration:          3 * time.Second,
		AfterCommentarySeq:    0,
		HasAfterCommentarySeq: true,
		WakeOnCommentary:      true,
	})
	if err != nil || len(fromZero) != 3 || fromZero[0].Seq != 1 || fromZero[1].Seq != 2 || fromZero[2].Seq != 3 || fromZero[0].Text != firstText {
		t.Fatalf("explicit after_commentary_seq=0 expected [1, 2, 3] with full seq=1 text, got %+v err=%v", fromZero, err)
	}

	// Explicit 1 returns [2, 3].
	_, _, _, fromOne, err := svc.WaitWithOptions(context.Background(), turn.ID, ws.ID, WaitOptions{
		WaitDuration:          3 * time.Second,
		AfterCommentarySeq:    1,
		HasAfterCommentarySeq: true,
		WakeOnCommentary:      true,
	})
	if err != nil || len(fromOne) != 2 || fromOne[0].Seq != 2 || fromOne[1].Seq != 3 {
		t.Fatalf("explicit after_commentary_seq=1 expected [2, 3], got %+v err=%v", fromOne, err)
	}
}
