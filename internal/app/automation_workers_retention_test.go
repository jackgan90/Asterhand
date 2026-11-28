package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/harnesses"
	"github.com/jackgan90/Asterhand/internal/operations"
	"github.com/jackgan90/Asterhand/internal/workers"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	_ "modernc.org/sqlite"
)

func TestPublicWorkersWaitAndWaitAnySurfaceExpiredAnswerConsistently(t *testing.T) {
	const answer33 = "Thirty-three bytes of final text!"
	if len(answer33) != 33 {
		t.Fatalf("fixture length=%d, want 33", len(answer33))
	}
	adapter := &stubWorkerAdapter{
		startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
			ch := make(chan workers.TurnOutcome, 1)
			ch <- workers.TurnOutcome{
				State:        workers.TurnCompleted,
				NativeTurnID: "nt_pub_exp",
				Items:        []workers.NativeItem{{ItemID: "f_pub_exp", Type: "agentMessage", Phase: "final_answer", Text: answer33}},
			}
			return "nt_pub_exp", ch, nil
		},
	}

	n, ws, tok := setupM1WorkerNode(t, adapter)
	a := n.app
	cs, closeMCP := openTestWaitMCPSession(t, a, tok)
	defer closeMCP()

	w := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "expired-view-worker", AccessMode: harnesses.AccessReadOnly,
	}).Data.(workers.Worker)
	turn := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{
		WorkerID: w.ID, WorkspaceID: ws.ID, Prompt: "short report",
	}).Data.(workers.Turn)

	// Confirm completed answer is readable before retention expiry.
	wBefore := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{TurnID: turn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5)})
	if wBefore.Error != nil || wBefore.Data.(workerWaitView).Answer != answer33 || wBefore.Data.(workerWaitView).Expired {
		t.Fatalf("unexpected pre-expiry workers_wait: %+v", wBefore)
	}

	// Simulate 8 days passing under online retention maintenance.
	a.Workers.PruneRetention(time.Now().UTC().Add(8 * 24 * time.Hour))

	// 1. workers_wait surfaces expired=true, preserves result_bytes=33, and explains expiry in next_step.
	wAfter := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{TurnID: turn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(1)})
	if wAfter.Error != nil {
		t.Fatalf("workers_wait after expiry failed: %+v", wAfter.Error)
	}
	wv := wAfter.Data.(workerWaitView)
	if wv.State != workers.TurnCompleted || !wv.AnswerAvailable || !wv.Expired || wv.ResultBytes != 33 || wv.Answer != "" || wv.Truncated || !strings.Contains(wv.NextStep, "expired") {
		t.Fatalf("unexpected expired workers_wait view: %+v", wv)
	}

	// 2. workers_wait_any surfaces expired=true and preserves result_bytes=33.
	waAfter := testAutomationCall(a, tok, "workers_wait_any", "", workerWaitAnyInput{TurnIDs: []string{turn.ID}, WorkspaceID: ws.ID, WaitSeconds: automation.Int(1)})
	if waAfter.Error != nil {
		t.Fatalf("workers_wait_any after expiry failed: %+v", waAfter.Error)
	}
	waItems := waAfter.Data.(workerWaitAnyView).Items
	if len(waItems) != 1 || !waItems[0].Expired || waItems[0].ResultBytes != 33 || waItems[0].AnswerPreview != "" || waItems[0].Truncated {
		t.Fatalf("unexpected expired workers_wait_any item: %+v", waItems)
	}

	// 3. workers_result remains consistent (expired=true, total_bytes=33).
	resAfter := testAutomationCall(a, tok, "workers_result", "", workerResultInput{TurnID: turn.ID, WorkspaceID: ws.ID})
	if resAfter.Error != nil {
		t.Fatalf("workers_result after expiry failed: %+v", resAfter.Error)
	}
	rp := resAfter.Data.(workers.ResultPage)
	if !rp.Expired || rp.TotalBytes != 33 || rp.Text != "" || !rp.Complete {
		t.Fatalf("unexpected expired workers_result page: %+v", rp)
	}

	// 4. MCP content[].text exposes expired=true on both workers_wait and workers_wait_any.
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{"workers_wait", map[string]any{"turn_id": turn.ID, "workspace_id": ws.ID, "wait_seconds": 1}},
		{"workers_wait_any", map[string]any{"turn_ids": []string{turn.ID}, "workspace_id": ws.ID, "wait_seconds": 1}},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: call.name, Arguments: call.args})
		cancel()
		if err != nil || res.IsError || len(res.Content) == 0 {
			t.Fatalf("MCP %s failed: res=%+v err=%v", call.name, res, err)
		}
		txt := res.Content[0].(*mcp.TextContent).Text
		if !strings.Contains(txt, `"expired":true`) {
			t.Fatalf("MCP %s text missing expired=true: %s", call.name, txt)
		}
	}
}

func ageWorkerTurnsInSQLite(t *testing.T, home string, age time.Duration) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(home, "workers", "workers.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cutoff := time.Now().UTC().Add(-age).UnixMilli()
	if _, err := db.Exec(`UPDATE turns SET updated_at=?`, cutoff); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE turn_results SET updated_at=?`, cutoff); err != nil {
		t.Fatal(err)
	}
}

func TestPendingWorkersSendReceiptReconcilesBefore30DaySummaryPrune(t *testing.T) {
	n, ws, tok := setupM1WorkerNode(t, &stubWorkerAdapter{})
	a := n.app
	pCur, _, err := a.Harnesses.AuthenticateCaller(tok)
	if err != nil {
		t.Fatal(err)
	}
	w := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "receipt-prune-worker", AccessMode: harnesses.AccessReadOnly,
	}).Data.(workers.Worker)

	// 1. Online maintenance after 31 days: interrupted workers_send receipt (still prepared)
	//    must be reconciled to completed BEFORE the 31-day turn summary is evicted.
	opOnline := operations.NewID(time.Now())
	identOnline := operations.Identity{Actor: pCur.ID, Node: a.Config.Get().NodeID, ID: opOnline, Name: "workers_send", Target: w.ID, Digest: "d-online"}
	turnOnline, err := a.Workers.Send(context.Background(), workers.SendInput{
		OperationID: opOnline, OperationKey: identOnline.NativeID(), ActorID: pCur.ID, CallerID: pCur.CallerID,
		WorkerID: w.ID, ExpectedWorkspaceID: ws.ID, Prompt: "online crash receipt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := a.Workers.Wait(context.Background(), turnOnline.ID, ws.ID, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Store.Begin(identOnline, time.Now()); err != nil {
		t.Fatal(err)
	}

	// Simulate Day 31 online retention prune.
	a.Workers.PruneRetention(time.Now().UTC().Add(31 * 24 * time.Hour))

	recOnline := testAutomationCall(a, tok, "operations_get", "", map[string]string{"operation_id": opOnline})
	if recOnline.Error != nil {
		t.Fatalf("operations_get after Day 31 online prune failed: %+v", recOnline.Error)
	}
	rOnline := recOnline.Data.(operations.Receipt)
	if rOnline.State != "completed" || !rOnline.Accepted || !strings.Contains(string(rOnline.Result), turnOnline.ID) {
		t.Fatalf("expected Day 31 online receipt reconciled with turn_id %s, got state=%s result=%s", turnOnline.ID, rOnline.State, string(rOnline.Result))
	}
	if _, err := a.Workers.GetTurn(turnOnline.ID, ws.ID); err == nil {
		t.Fatalf("expected reconciled 31-day turn summary %s to be pruned after receipt completion", turnOnline.ID)
	}

	// 2. Startup cleanup after 31 days via full app.New restart:
	//    interrupted workers_send receipt must also be reconciled before startup summary prune.
	opRestart := operations.NewID(time.Now())
	identRestart := operations.Identity{Actor: pCur.ID, Node: a.Config.Get().NodeID, ID: opRestart, Name: "workers_send", Target: w.ID, Digest: "d-restart"}
	turnRestart, err := a.Workers.Send(context.Background(), workers.SendInput{
		OperationID: opRestart, OperationKey: identRestart.NativeID(), ActorID: pCur.ID, CallerID: pCur.CallerID,
		WorkerID: w.ID, ExpectedWorkspaceID: ws.ID, Prompt: "restart crash receipt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := a.Workers.Wait(context.Background(), turnRestart.ID, ws.ID, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Store.Begin(identRestart, time.Now()); err != nil {
		t.Fatal(err)
	}
	home := a.Config.Home
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}

	// Age turns in workers.db to 31 days old and reopen full App.
	ageWorkerTurnsInSQLite(t, home, 31*24*time.Hour)
	cfgMgr, err := config.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := New(cfgMgr, &fakeExecutor{})
	if err != nil {
		t.Fatalf("app.New restart failed: %v", err)
	}
	defer func() { _ = restarted.Close() }()

	recRestart, err := restarted.Store.Lookup(identRestart.Actor, identRestart.Node, identRestart.ID)
	if err != nil {
		t.Fatalf("restarted Store.Lookup failed: %v", err)
	}
	if recRestart.State != "completed" || !recRestart.Accepted || !strings.Contains(string(recRestart.Result), turnRestart.ID) {
		t.Fatalf("expected startup prune to reconcile receipt with turn_id %s, got state=%s result=%s", turnRestart.ID, recRestart.State, string(recRestart.Result))
	}
	if _, err := restarted.Workers.GetTurn(turnRestart.ID, ws.ID); err == nil {
		t.Fatalf("expected reconciled 31-day turn %s pruned on startup", turnRestart.ID)
	}
	var parsed map[string]any
	if err := json.Unmarshal(recRestart.Result, &parsed); err != nil || parsed["turn_id"] != turnRestart.ID {
		t.Fatalf("expected parsed turn_id=%s in reconciled receipt result %s", turnRestart.ID, string(recRestart.Result))
	}
}

func TestPublicWorkersResultTruncatedDistinguishesExactFromOver2MiB(t *testing.T) {
	exact2MiB := strings.Repeat("A", workers.MaxResultBytes)
	over2MiB := exact2MiB + "END"
	bodies := []string{exact2MiB, over2MiB}
	idx := 0
	adapter := &stubWorkerAdapter{
		startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
			cur := idx
			idx++
			ch := make(chan workers.TurnOutcome, 1)
			ch <- workers.TurnOutcome{
				State:        workers.TurnCompleted,
				NativeTurnID: "nt_2m_" + spec.Prompt,
				Items:        []workers.NativeItem{{ItemID: "f_2m", Type: "agentMessage", Phase: "final_answer", Text: bodies[cur]}},
			}
			return "nt_2m_" + spec.Prompt, ch, nil
		},
	}

	n, ws, tok := setupM1WorkerNode(t, adapter)
	a := n.app
	w := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "cap-2m-worker", AccessMode: harnesses.AccessReadOnly,
	}).Data.(workers.Worker)

	for _, tc := range []struct {
		name          string
		wantTruncated bool
	}{
		{name: "exact", wantTruncated: false},
		{name: "over", wantTruncated: true},
	} {
		turn := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{
			WorkerID: w.ID, WorkspaceID: ws.ID, Prompt: tc.name,
		}).Data.(workers.Turn)

		wr := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{TurnID: turn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5)})
		if wr.Error != nil {
			t.Fatalf("%s workers_wait: %+v", tc.name, wr.Error)
		}
		wv := wr.Data.(workerWaitView)
		if wv.ResultTruncated != tc.wantTruncated || wv.ResultBytes != workers.MaxResultBytes {
			t.Fatalf("%s workers_wait result_truncated=%v (want %v) result_bytes=%d", tc.name, wv.ResultTruncated, tc.wantTruncated, wv.ResultBytes)
		}

		wa := testAutomationCall(a, tok, "workers_wait_any", "", workerWaitAnyInput{TurnIDs: []string{turn.ID}, WorkspaceID: ws.ID, WaitSeconds: automation.Int(1)})
		if wa.Error != nil {
			t.Fatalf("%s workers_wait_any: %+v", tc.name, wa.Error)
		}
		if item := wa.Data.(workerWaitAnyView).Items[0]; item.ResultTruncated != tc.wantTruncated {
			t.Fatalf("%s workers_wait_any result_truncated=%v, want %v", tc.name, item.ResultTruncated, tc.wantTruncated)
		}

		rr := testAutomationCall(a, tok, "workers_result", "", workerResultInput{
			TurnID: turn.ID, WorkspaceID: ws.ID, Cursor: "2097151",
		})
		if rr.Error != nil {
			t.Fatalf("%s workers_result: %+v", tc.name, rr.Error)
		}
		page := rr.Data.(workers.ResultPage)
		if page.Text != "A" || page.TotalBytes != workers.MaxResultBytes || !page.Complete || page.ResultTruncated != tc.wantTruncated {
			t.Fatalf("%s last page unexpected: text=%q total=%d complete=%v result_truncated=%v (want %v)", tc.name, page.Text, page.TotalBytes, page.Complete, page.ResultTruncated, tc.wantTruncated)
		}
	}
}

func TestPublicWorkersInterruptedLongPartialOutputPaginatesViaResultAndMCP(t *testing.T) {
	const tailMarker = "FINAL_PARTIAL_TAIL_CHECK: <table><tr><td>keep-me</td></tr></table>"
	fullPartial := strings.Repeat("<tr><td>review-row</td><td>alpha & beta</td></tr>\n", 220) + tailMarker
	adapter := &stubWorkerAdapter{
		startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
			ch := make(chan workers.TurnOutcome, 1)
			ch <- workers.TurnOutcome{
				State:        workers.TurnInterrupted,
				NativeTurnID: "nt_partial_max_tokens",
				ErrorCode:    "max_tokens",
				ErrorMessage: "DeepSeek output reached max_tokens limit",
				Items:        []workers.NativeItem{{ItemID: "p_1", Type: "agentMessage", Phase: "partial_answer", Text: fullPartial}},
			}
			return "nt_partial_max_tokens", ch, nil
		},
	}

	n, ws, tok := setupM1WorkerNode(t, adapter)
	a := n.app
	cs, closeMCP := openTestWaitMCPSession(t, a, tok)
	defer closeMCP()

	w := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "partial-paginate", AccessMode: harnesses.AccessReadOnly,
	}).Data.(workers.Worker)
	turn := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{
		WorkerID: w.ID, WorkspaceID: ws.ID, Prompt: "long html review",
	}).Data.(workers.Turn)

	wr := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{TurnID: turn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5)})
	if wr.Error != nil {
		t.Fatalf("workers_wait failed: %+v", wr.Error)
	}
	wv := wr.Data.(workerWaitView)
	if wv.State != workers.TurnInterrupted || wv.ErrorCode != "max_tokens" || wv.AnswerAvailable || wv.Answer != "" || wv.ResultBytes != len(fullPartial) {
		t.Fatalf("unexpected interrupted workers_wait view: %+v", wv)
	}
	if strings.Contains(wv.PartialOutput, tailMarker) || !strings.Contains(wv.NextStep, "workers_result") {
		t.Fatalf("expected 4 KiB partial_output preview and workers_result next_step, got len=%d next_step=%q", len(wv.PartialOutput), wv.NextStep)
	}

	// Paginate full partial output via MCP content[].text to prove text-only MCP clients recover the tail cleanly.
	var rebuilt strings.Builder
	cursor := "0"
	for pages := 0; pages < 16; pages++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "workers_result",
			Arguments: map[string]any{
				"turn_id":      turn.ID,
				"workspace_id": ws.ID,
				"cursor":       cursor,
				"max_bytes":    4096,
			},
		})
		cancel()
		if err != nil || res.IsError || len(res.Content) == 0 {
			t.Fatalf("MCP workers_result(cursor=%s) failed: res=%+v err=%v", cursor, res, err)
		}
		rawText := res.Content[0].(*mcp.TextContent).Text
		var envelope struct {
			TextTruncated bool               `json:"text_truncated"`
			NextStep      string             `json:"next_step"`
			Data          workers.ResultPage `json:"data"`
		}
		if err := json.Unmarshal([]byte(rawText), &envelope); err != nil {
			t.Fatalf("unmarshal MCP workers_result text: %v", err)
		}
		if envelope.TextTruncated || !envelope.Data.Partial || envelope.Data.AnswerAvailable || envelope.Data.Expired {
			t.Fatalf("unexpected MCP partial page at cursor=%s: trunc=%v data=%+v", cursor, envelope.TextTruncated, envelope.Data)
		}
		rebuilt.WriteString(envelope.Data.Text)
		if envelope.Data.Complete {
			break
		}
		if envelope.Data.NextCursor == "" || !strings.Contains(envelope.NextStep, "partial") {
			t.Fatalf("expected next_cursor and partial next_step hint, got cursor=%q next_step=%q", envelope.Data.NextCursor, envelope.NextStep)
		}
		cursor = envelope.Data.NextCursor
	}
	if rebuilt.String() != fullPartial {
		t.Fatalf("MCP rebuilt partial output mismatch: got %d bytes, want %d bytes, hasTail=%v",
			rebuilt.Len(), len(fullPartial), strings.Contains(rebuilt.String(), tailMarker))
	}

	// After 7-day retention prune, workers_wait and workers_wait_any must both
	// mark expired=true, preserve the 4 KiB partial_output summary, and drop the
	// workers_result pagination prompt.
	a.Workers.PruneRetention(time.Now().UTC().Add(8 * 24 * time.Hour))
	assertExpiredPartialWaitAndMCP(t, a, cs, tok, ws.ID, turn.ID, len(fullPartial))
}

func assertExpiredPartialWaitAndMCP(t *testing.T, a *App, cs *mcp.ClientSession, tok, wsID, turnID string, wantBytes int) {
	t.Helper()
	wrExp := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{TurnID: turnID, WorkspaceID: wsID, WaitSeconds: automation.Int(1)})
	if wrExp.Error != nil {
		t.Fatalf("workers_wait after partial expiry failed: %+v", wrExp.Error)
	}
	wvExp := wrExp.Data.(workerWaitView)
	if !wvExp.Expired || wvExp.AnswerAvailable || wvExp.ResultBytes != wantBytes || wvExp.PartialOutput == "" ||
		!strings.Contains(wvExp.NextStep, "expired") || strings.Contains(wvExp.NextStep, "workers_result") {
		t.Fatalf("unexpected expired partial workers_wait view: expired=%v avail=%v bytes=%d partialLen=%d next_step=%q",
			wvExp.Expired, wvExp.AnswerAvailable, wvExp.ResultBytes, len(wvExp.PartialOutput), wvExp.NextStep)
	}
	waExp := testAutomationCall(a, tok, "workers_wait_any", "", workerWaitAnyInput{TurnIDs: []string{turnID}, WorkspaceID: wsID, WaitSeconds: automation.Int(1)})
	if waExp.Error != nil {
		t.Fatalf("workers_wait_any after partial expiry failed: %+v", waExp.Error)
	}
	if items := waExp.Data.(workerWaitAnyView).Items; len(items) != 1 || !items[0].Expired || items[0].AnswerAvailable || items[0].ResultBytes != wantBytes || items[0].Truncated {
		t.Fatalf("unexpected expired partial workers_wait_any items: %+v", items)
	}
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{"workers_wait", map[string]any{"turn_id": turnID, "workspace_id": wsID, "wait_seconds": 1}},
		{"workers_wait_any", map[string]any{"turn_ids": []string{turnID}, "workspace_id": wsID, "wait_seconds": 1}},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: call.name, Arguments: call.args})
		cancel()
		if err != nil || res.IsError || len(res.Content) == 0 {
			t.Fatalf("MCP %s after partial expiry failed: res=%+v err=%v", call.name, res, err)
		}
		txt := res.Content[0].(*mcp.TextContent).Text
		if !strings.Contains(txt, `"expired":true`) || (call.name == "workers_wait" && strings.Contains(txt, "workers_result")) {
			t.Fatalf("unexpected MCP %s text after partial expiry: %s", call.name, txt)
		}
	}
}

func TestPublicWorkersGetOverBudgetAfterExpiredPartialHistoryGuidesWait(t *testing.T) {
	// Six interrupted turns (~56 KiB raw) exceed the 48 KiB MCP text cap; seven
	// interrupted turns (~66 KiB raw) exceed the 64 KiB public response cap.
	// workers_get must budget before returning so both 6-turn and 7-turn queries
	// succeed via public API and MCP without losing worker/turn summaries.
	htmlPartial := strings.Repeat("<table><tr><td>partial-item</td></tr></table>\n", 200)
	adapter := &stubWorkerAdapter{
		startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
			ch := make(chan workers.TurnOutcome, 1)
			ch <- workers.TurnOutcome{
				State:        workers.TurnInterrupted,
				NativeTurnID: "nt_hist_" + spec.Prompt,
				ErrorCode:    "max_tokens",
				ErrorMessage: "reached max_tokens",
				Items:        []workers.NativeItem{{ItemID: "p", Type: "agentMessage", Phase: "partial_answer", Text: htmlPartial}},
			}
			return "nt_hist_" + spec.Prompt, ch, nil
		},
	}

	n, ws, tok := setupM1WorkerNode(t, adapter)
	a := n.app
	cs, closeMCP := openTestWaitMCPSession(t, a, tok)
	defer closeMCP()

	w := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "hist-overbudget", AccessMode: harnesses.AccessReadOnly,
	}).Data.(workers.Worker)

	turnIDs := make([]string, 0, 7)
	addTurn := func(idx int) {
		turn := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{
			WorkerID: w.ID, WorkspaceID: ws.ID, Prompt: "hist-" + string(rune('1'+idx)),
		}).Data.(workers.Turn)
		wr := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{TurnID: turn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5)})
		if wr.Error != nil || wr.Data.(workerWaitView).State != workers.TurnInterrupted {
			t.Fatalf("turn %d did not settle interrupted: %+v", idx, wr)
		}
		turnIDs = append(turnIDs, turn.ID)
	}

	// 1 turn fits cleanly within budget: full partial_output preserved untouched.
	addTurn(0)
	g1 := testAutomationCall(a, tok, "workers_get", "", workerGetInput{WorkerID: w.ID, WorkspaceID: ws.ID})
	if g1.Error != nil {
		t.Fatalf("1-turn workers_get failed: %+v", g1.Error)
	}
	v1 := g1.Data.(workerGetView)
	if v1.HistoryTruncated || v1.NextStep != "" || len(v1.Turns) != 1 || v1.Turns[0].PartialOutput == "" {
		t.Fatalf("expected uncompacted 1-turn workers_get, got trunc=%v next=%q turns=%d partialLen=%d", v1.HistoryTruncated, v1.NextStep, len(v1.Turns), len(v1.Turns[0].PartialOutput))
	}

	for i := 1; i < 6; i++ {
		addTurn(i)
	}
	a.Workers.PruneRetention(time.Now().UTC().Add(8 * 24 * time.Hour))
	assertBudgetedWorkersGetAndMCP(t, a, cs, tok, ws.ID, w.ID, turnIDs)

	// 7th turn pushes raw history past 64 KiB public automation.MaxResponse.
	addTurn(6)
	a.Workers.PruneRetention(time.Now().UTC().Add(16 * 24 * time.Hour))
	assertBudgetedWorkersGetAndMCP(t, a, cs, tok, ws.ID, w.ID, turnIDs)
}

func assertBudgetedWorkersGetAndMCP(t *testing.T, a *App, cs *mcp.ClientSession, tok, wsID, workerID string, wantTurnIDs []string) {
	t.Helper()
	pub := testAutomationCall(a, tok, "workers_get", "", workerGetInput{WorkerID: workerID, WorkspaceID: wsID})
	if pub.Error != nil {
		t.Fatalf("public workers_get(%d turns) failed: %+v", len(wantTurnIDs), pub.Error)
	}
	gv := pub.Data.(workerGetView)
	if !gv.HistoryTruncated || gv.Worker.ID != workerID || len(gv.Turns) != len(wantTurnIDs) ||
		!strings.Contains(gv.NextStep, "workers_wait(") || strings.Contains(gv.NextStep, "workers_result") {
		t.Fatalf("unexpected budgeted public workers_get(%d turns): trunc=%v worker=%s turns=%d next=%q",
			len(wantTurnIDs), gv.HistoryTruncated, gv.Worker.ID, len(gv.Turns), gv.NextStep)
	}
	seen := map[string]bool{}
	for _, tm := range gv.Turns {
		if tm.ID == "" || tm.State != workers.TurnInterrupted || tm.PartialOutput != "" {
			t.Fatalf("unexpected compacted public turn: id=%s state=%s partialLen=%d", tm.ID, tm.State, len(tm.PartialOutput))
		}
		seen[tm.ID] = true
	}
	for _, want := range wantTurnIDs {
		if !seen[want] {
			t.Fatalf("missing turn_id %s in public workers_get(%d turns)", want, len(wantTurnIDs))
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "workers_get", Arguments: map[string]any{"worker_id": workerID, "workspace_id": wsID}})
	cancel()
	if err != nil || res.IsError || len(res.Content) == 0 {
		t.Fatalf("MCP workers_get(%d turns) failed: res=%+v err=%v", len(wantTurnIDs), res, err)
	}
	txt := res.Content[0].(*mcp.TextContent).Text
	var env struct {
		TextTruncated bool          `json:"text_truncated"`
		NextStep      string        `json:"next_step"`
		Data          workerGetView `json:"data"`
	}
	if err := json.Unmarshal([]byte(txt), &env); err != nil {
		t.Fatalf("unmarshal MCP workers_get(%d turns) text: %v\n%s", len(wantTurnIDs), err, txt)
	}
	if env.TextTruncated || !env.Data.HistoryTruncated || env.Data.Worker.ID != workerID || len(env.Data.Turns) != len(wantTurnIDs) ||
		!strings.Contains(env.Data.NextStep, "workers_wait(") || strings.Contains(txt, "workers_result") {
		t.Fatalf("unexpected MCP workers_get(%d turns) text: text_trunc=%v hist_trunc=%v turns=%d next=%q",
			len(wantTurnIDs), env.TextTruncated, env.Data.HistoryTruncated, len(env.Data.Turns), env.Data.NextStep)
	}
	// Following the workers_wait guidance on the newest turn confirms expired=true + 4 KiB preview.
	wr := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{TurnID: gv.Turns[0].ID, WorkspaceID: wsID, WaitSeconds: automation.Int(1)})
	if wr.Error != nil || !wr.Data.(workerWaitView).Expired || wr.Data.(workerWaitView).PartialOutput == "" {
		t.Fatalf("follow-up workers_wait(%s) unexpected: %+v", gv.Turns[0].ID, wr)
	}
}
