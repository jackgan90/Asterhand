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
