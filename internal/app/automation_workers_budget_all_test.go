package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/harnesses"
	"github.com/jackgan90/Asterhand/internal/operations"
	"github.com/jackgan90/Asterhand/internal/workers"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func callTestMCPJSON(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || res.IsError || len(res.Content) == 0 {
		t.Fatalf("MCP %s failed: res=%+v err=%v", name, res, err)
	}
	txt := res.Content[0].(*mcp.TextContent).Text
	if len(txt) > 48<<10 {
		t.Fatalf("MCP %s text exceeded 48 KiB: %d", name, len(txt))
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(txt), &parsed); err != nil {
		t.Fatalf("MCP %s text is not valid JSON: %v\n%s", name, err, txt)
	}
	return txt, parsed
}

func TestWorkersCreateCloseListGetLongRoleHintPublicAndMCP(t *testing.T) {
	adapter := &stubWorkerAdapter{
		startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
			ch := make(chan workers.TurnOutcome, 1)
			ch <- workers.TurnOutcome{
				State:        workers.TurnCompleted,
				NativeTurnID: "nt_short_" + spec.Prompt,
				Items:        []workers.NativeItem{{ItemID: "f", Type: "agentMessage", Phase: "final_answer", Text: "Reviewed."}},
			}
			return "nt_short_" + spec.Prompt, ch, nil
		},
	}
	n, ws, tok := setupM1WorkerNode(t, adapter)
	a := n.app
	cs, closeMCP := openTestWaitMCPSession(t, a, tok)
	defer closeMCP()

	// 1. Two workers in the same workspace with 35 KiB role_hint each -> workers_list succeeds via public & MCP.
	role35K := strings.Repeat("Role instruction line for reviewer.\n", 1000)
	for i := 0; i < 2; i++ {
		cr := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
			HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: fmt.Sprintf("pair-%d", i),
			AccessMode: harnesses.AccessReadOnly, RoleHint: role35K,
		})
		if cr.Error != nil {
			t.Fatalf("create pair-%d failed: %+v", i, cr.Error)
		}
	}
	listPub := testAutomationCall(a, tok, "workers_list", "", workerListInput{WorkspaceID: ws.ID})
	if listPub.Error != nil {
		t.Fatalf("public workers_list(2x35K) failed: %+v", listPub.Error)
	}
	lv := listPub.Data.(workerListView)
	if !lv.Truncated || len(lv.Items) != 2 || !slices.Contains(lv.Items[0].DetailsOmitted, "role_hint") {
		t.Fatalf("expected compacted 2-item workers_list with details_omitted=[role_hint], got %+v", lv)
	}
	_, listMCP := callTestMCPJSON(t, cs, "workers_list", map[string]any{"workspace_id": ws.ID})
	if dataMap, _ := listMCP["data"].(map[string]any); len(dataMap["items"].([]any)) != 2 {
		t.Fatalf("expected 2 items in MCP workers_list, got %+v", listMCP)
	}

	// 2. 67 KiB (68,608-byte) role_hint -> workers_create succeeds via public & MCP, stores full role_hint, receipt matches.
	role67K := strings.Repeat("X", 67<<10)
	createOpID := operations.NewID(time.Now())
	cr67 := testAutomationCall(a, tok, "workers_create", createOpID, workerCreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "big-role",
		AccessMode: harnesses.AccessReadOnly, RoleHint: role67K,
	})
	if cr67.Error != nil || cr67.Receipt == nil || cr67.Receipt.State != "completed" {
		t.Fatalf("workers_create(67K) failed: err=%+v receipt=%+v", cr67.Error, cr67.Receipt)
	}
	w67 := cr67.Data.(workers.Worker)
	if !slices.Contains(w67.DetailsOmitted, "role_hint") || w67.RoleHint != "" {
		t.Fatalf("expected role_hint omitted from create response, got omitted=%v len=%d", w67.DetailsOmitted, len(w67.RoleHint))
	}
	storedW, _, err := a.Workers.Get(w67.ID, ws.ID)
	if err != nil || len(storedW.RoleHint) != len(role67K) {
		t.Fatalf("expected full 67K role_hint stored in SQLite, got len=%d err=%v", len(storedW.RoleHint), err)
	}
	if opGet := testAutomationCall(a, tok, "operations_get", "", map[string]any{"operation_id": createOpID}); opGet.Error != nil {
		t.Fatalf("operations_get for 67K workers_create failed: %+v", opGet.Error)
	}

	// 3. workers_get at 0 turns -> returns worker summary + empty turns + NO workers_send next_step.
	g0 := testAutomationCall(a, tok, "workers_get", "", workerGetInput{WorkerID: w67.ID, WorkspaceID: ws.ID})
	if g0.Error != nil {
		t.Fatalf("workers_get(67K, 0 turns) failed: %+v", g0.Error)
	}
	gv0 := g0.Data.(workerGetView)
	if gv0.Worker.ID != w67.ID || len(gv0.Turns) != 0 || gv0.HistoryTruncated || gv0.NextStep != "" || !slices.Contains(gv0.Worker.DetailsOmitted, "role_hint") {
		t.Fatalf("unexpected 0-turn 67K workers_get: %+v", gv0)
	}
	txt0, _ := callTestMCPJSON(t, cs, "workers_get", map[string]any{"worker_id": w67.ID, "workspace_id": ws.ID})
	if strings.Contains(txt0, "workers_send") {
		t.Fatalf("0-turn workers_get must not suggest workers_send: %s", txt0)
	}

	// 4. After 10 short turns -> workers_get succeeds via public & MCP, preserving all 10 turns.
	for i := 0; i < 10; i++ {
		turn := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{
			WorkerID: w67.ID, WorkspaceID: ws.ID, Prompt: fmt.Sprintf("t%d", i),
		}).Data.(workers.Turn)
		if wr := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{TurnID: turn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5)}); wr.Error != nil {
			t.Fatalf("turn %d wait failed: %+v", i, wr.Error)
		}
	}
	if g10 := testAutomationCall(a, tok, "workers_get", "", workerGetInput{WorkerID: w67.ID, WorkspaceID: ws.ID}); g10.Error != nil || len(g10.Data.(workerGetView).Turns) != 10 {
		t.Fatalf("workers_get(67K, 10 turns) failed: err=%+v data=%+v", g10.Error, g10.Data)
	}
	if _, g10MCP := callTestMCPJSON(t, cs, "workers_get", map[string]any{"worker_id": w67.ID, "workspace_id": ws.ID}); len(g10MCP["data"].(map[string]any)["turns"].([]any)) != 10 {
		t.Fatalf("expected 10 turns in MCP workers_get, got %+v", g10MCP)
	}
	if _, closeMCP := callTestMCPJSON(t, cs, "workers_close", map[string]any{"operation_id": operations.NewID(time.Now()), "worker_id": w67.ID, "workspace_id": ws.ID}); closeMCP["data"].(map[string]any)["state"] != workers.WorkerClosed {
		t.Fatalf("expected closed state from MCP workers_close, got %+v", closeMCP)
	}
}

func TestWorkersListKeysetCursorPaginationCompletesUnderBudget(t *testing.T) {
	n, ws, tok := setupM1WorkerNode(t, &stubWorkerAdapter{})
	a := n.app
	cs, closeMCP := openTestWaitMCPSession(t, a, tok)
	defer closeMCP()

	wantIDs := map[string]bool{}
	for i := 0; i < 6; i++ {
		w := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
			HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: fmt.Sprintf("pg-%d", i), AccessMode: harnesses.AccessReadOnly,
		}).Data.(workers.Worker)
		wantIDs[w.ID] = true
		time.Sleep(2 * time.Millisecond)
	}
	if bad := testAutomationCall(a, tok, "workers_list", "", workerListInput{WorkspaceID: ws.ID, Cursor: "bad-cursor"}); bad.Error == nil || bad.Error.Code != "invalid_cursor" {
		t.Fatalf("expected invalid_cursor, got %+v", bad.Error)
	}

	gotIDs := map[string]bool{}
	cursor, pages := "", 0
	for {
		args := map[string]any{"workspace_id": ws.ID, "limit": 2}
		if cursor != "" {
			args["cursor"] = cursor
		}
		_, parsed := callTestMCPJSON(t, cs, "workers_list", args)
		data := parsed["data"].(map[string]any)
		for _, raw := range data["items"].([]any) {
			id := raw.(map[string]any)["worker_id"].(string)
			if gotIDs[id] {
				t.Fatalf("duplicate worker_id %s across cursor pages", id)
			}
			gotIDs[id] = true
		}
		pages++
		if hasMore, _ := data["has_more"].(bool); !hasMore {
			break
		}
		next, _ := data["next_cursor"].(string)
		if next == "" {
			t.Fatalf("expected next_cursor when has_more=true: %+v", data)
		}
		cursor = next
	}
	if pages != 3 || len(gotIDs) != len(wantIDs) {
		t.Fatalf("expected 3 pages covering 6 workers, got pages=%d count=%d", pages, len(gotIDs))
	}

	// Verify budget-split fallback on 180 workers emits a valid keyset cursor within 44 KiB.
	many := make([]workers.Worker, 180)
	base := time.Now().UTC()
	for i := range many {
		many[i] = workers.Worker{
			ID: fmt.Sprintf("wkr_%04d", i), WorkspaceID: ws.ID, Alias: fmt.Sprintf("a%03d", i),
			HarnessID: harnesses.HarnessCodex, AccessMode: harnesses.AccessReadOnly, State: workers.WorkerIdle,
			RoleHint: strings.Repeat("R", 5000), UpdatedAt: base.Add(-time.Duration(i) * time.Second), CreatedAt: base,
		}
	}
	v := buildWorkerListView(many, false, []any{"workers_list"}, "rev1")
	if !v.Truncated || !v.HasMore || v.NextCursor == "" || len(v.Items) == 0 || len(v.Items) >= len(many) || serializedJSONBytes(v) > workerViewMaxPayloadJSONBytes {
		t.Fatalf("expected budget-split workerListView <=44 KiB with cursor, got len=%d bytes=%d cursor=%q", len(v.Items), serializedJSONBytes(v), v.NextCursor)
	}
}

func TestWorkersListCursorRejectsStaleSnapshotAfterLiveTurn(t *testing.T) {
	adapter := &stubWorkerAdapter{
		startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
			ch := make(chan workers.TurnOutcome, 1)
			ch <- workers.TurnOutcome{
				State:        workers.TurnCompleted,
				NativeTurnID: "nt_upd_" + spec.Prompt,
				Items:        []workers.NativeItem{{ItemID: "f", Type: "agentMessage", Phase: "final_answer", Text: "OK"}},
			}
			return "nt_upd_" + spec.Prompt, ch, nil
		},
	}
	n, ws, tok := setupM1WorkerNode(t, adapter)
	a := n.app
	cs, closeMCP := openTestWaitMCPSession(t, a, tok)
	defer closeMCP()

	// P2 #1: 4 workers, limit=1; running a turn on an unvisited worker during pagination
	// must reject the stale cursor with conflict rather than silently dropping the worker.
	created := make([]workers.Worker, 0, 4)
	for i := 0; i < 4; i++ {
		w := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
			HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: fmt.Sprintf("live-%d", i), AccessMode: harnesses.AccessReadOnly,
		}).Data.(workers.Worker)
		created = append(created, w)
		time.Sleep(2 * time.Millisecond)
	}
	p1 := testAutomationCall(a, tok, "workers_list", "", workerListInput{WorkspaceID: ws.ID, Limit: automation.Int(1)})
	if p1.Error != nil {
		t.Fatalf("page 1 workers_list failed: %+v", p1.Error)
	}
	lv1 := p1.Data.(workerListView)
	if len(lv1.Items) != 1 || !lv1.HasMore || lv1.NextCursor == "" {
		t.Fatalf("unexpected page 1: %+v", lv1)
	}
	// Run a turn on the oldest (unvisited) worker so its updated_at jumps ahead of page 1.
	unvisited := created[0]
	turn := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{
		WorkerID: unvisited.ID, WorkspaceID: ws.ID, Prompt: "bump updated_at",
	}).Data.(workers.Turn)
	if wr := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{TurnID: turn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5)}); wr.Error != nil {
		t.Fatalf("wait failed: %+v", wr.Error)
	}
	p2 := testAutomationCall(a, tok, "workers_list", "", workerListInput{WorkspaceID: ws.ID, Limit: automation.Int(1), Cursor: lv1.NextCursor})
	if p2.Error == nil || p2.Error.Code != "conflict" {
		t.Fatalf("expected conflict on stale workers_list cursor after live turn, got err=%+v data=%+v", p2.Error, p2.Data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	mcpStale, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "workers_list", Arguments: map[string]any{"workspace_id": ws.ID, "limit": 1, "cursor": lv1.NextCursor}})
	cancel()
	if err != nil || !mcpStale.IsError || len(mcpStale.Content) == 0 || !strings.Contains(mcpStale.Content[0].(*mcp.TextContent).Text, "conflict") {
		t.Fatalf("expected MCP conflict on stale workers_list cursor, got res=%+v err=%v", mcpStale, err)
	}
	// Fresh list without cursor returns the updated worker first and all 4 cleanly.
	fresh := testAutomationCall(a, tok, "workers_list", "", workerListInput{WorkspaceID: ws.ID, Limit: automation.Int(10)})
	if fresh.Error != nil || len(fresh.Data.(workerListView).Items) != 4 || fresh.Data.(workerListView).Items[0].ID != unvisited.ID {
		t.Fatalf("expected fresh list starting with updated worker %s, got %+v", unvisited.ID, fresh)
	}
}

func TestWorkersListCompactionPreservesSessionAutoApprove(t *testing.T) {
	n, ws, tok := setupM1WorkerNode(t, &stubWorkerAdapter{})
	a := n.app
	cs, closeMCP := openTestWaitMCPSession(t, a, tok)
	defer closeMCP()

	en := true
	if _, err := a.Harnesses.UpdatePolicy(harnesses.UpdatePolicyInput{HarnessID: harnesses.HarnessCodex, AllowWorkspaceWrite: &en}); err != nil {
		t.Fatal(err)
	}
	wAuth := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "auth-writer",
		AccessMode: harnesses.AccessWorkspaceWrite, ApprovalMode: workers.ApprovalModeAskHuman,
	}).Data.(workers.Worker)
	if _, err := a.Workers.SetSessionAutoApproveHuman(wAuth.ID, true); err != nil {
		t.Fatalf("SetSessionAutoApproveHuman: %v", err)
	}
	for i := 0; i < 70; i++ {
		if cr := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
			HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: fmt.Sprintf("bulk-%02d", i), AccessMode: harnesses.AccessReadOnly,
		}); cr.Error != nil {
			t.Fatalf("create bulk-%02d: %+v", i, cr.Error)
		}
	}
	l71 := testAutomationCall(a, tok, "workers_list", "", workerListInput{WorkspaceID: ws.ID, Limit: automation.Int(100)})
	if l71.Error != nil {
		t.Fatalf("workers_list(71) failed: %+v", l71.Error)
	}
	lv71 := l71.Data.(workerListView)
	if len(lv71.Items) != 71 || !lv71.Truncated {
		t.Fatalf("expected 71 compacted workers, got len=%d trunc=%v", len(lv71.Items), lv71.Truncated)
	}
	foundAuth := false
	for _, item := range lv71.Items {
		if item.ID == wAuth.ID {
			foundAuth = true
			if !item.SessionAutoApprove {
				t.Fatalf("compacted workers_list hid session_auto_approve=true for %s: %+v", wAuth.ID, item)
			}
		}
	}
	if !foundAuth {
		t.Fatalf("auth-writer %s missing from 71-item list", wAuth.ID)
	}
	_, mcpList := callTestMCPJSON(t, cs, "workers_list", map[string]any{"workspace_id": ws.ID, "limit": 100})
	mcpFound := false
	for _, raw := range mcpList["data"].(map[string]any)["items"].([]any) {
		m := raw.(map[string]any)
		if m["worker_id"] == wAuth.ID {
			mcpFound = true
			if auto, _ := m["session_auto_approve"].(bool); !auto {
				t.Fatalf("MCP workers_list hid session_auto_approve=true for %s: %+v", wAuth.ID, m)
			}
		}
	}
	if !mcpFound {
		t.Fatalf("auth-writer %s missing from MCP 71-item list", wAuth.ID)
	}
}

func TestWorkersGet38KRoleHintPreservesPartialOutput(t *testing.T) {
	// P3 #3: 37,999-byte role_hint (fits alone in 44 KiB) + 1 interrupted turn
	// with HTML-escaped 4,099-byte partial_output (~10 KiB JSON) totals ~49 KiB;
	// omitting role_hint first keeps full partial_output intact.
	role38K := strings.Repeat("R", 37999)
	partialLong := strings.Repeat("<table><tr><td>partial-finding</td></tr></table>\n", 150)
	adapter := &stubWorkerAdapter{
		startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
			ch := make(chan workers.TurnOutcome, 1)
			ch <- workers.TurnOutcome{
				State:        workers.TurnInterrupted,
				NativeTurnID: "nt_p3_role",
				ErrorCode:    "max_tokens",
				Items:        []workers.NativeItem{{ItemID: "p", Type: "agentMessage", Phase: "partial_answer", Text: partialLong}},
			}
			return "nt_p3_role", ch, nil
		},
	}
	n, ws, tok := setupM1WorkerNode(t, adapter)
	a := n.app
	cs, closeMCP := openTestWaitMCPSession(t, a, tok)
	defer closeMCP()

	w := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "role-38k", AccessMode: harnesses.AccessReadOnly, RoleHint: role38K,
	}).Data.(workers.Worker)
	turn := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{
		WorkerID: w.ID, WorkspaceID: ws.ID, Prompt: "review",
	}).Data.(workers.Turn)
	if wr := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{TurnID: turn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5)}); wr.Error != nil {
		t.Fatalf("wait failed: %+v", wr.Error)
	}

	pub := testAutomationCall(a, tok, "workers_get", "", workerGetInput{WorkerID: w.ID, WorkspaceID: ws.ID})
	if pub.Error != nil {
		t.Fatalf("public workers_get failed: %+v", pub.Error)
	}
	gv := pub.Data.(workerGetView)
	if gv.HistoryTruncated || gv.Worker.RoleHint != "" || !slices.Contains(gv.Worker.DetailsOmitted, "role_hint") || len(gv.Turns) != 1 || len(gv.Turns[0].PartialOutput) != 4099 {
		t.Fatalf("expected role_hint omitted and 4099-byte partial_output preserved, got histTrunc=%v omitted=%v partialLen=%d",
			gv.HistoryTruncated, gv.Worker.DetailsOmitted, len(gv.Turns[0].PartialOutput))
	}
	_, mcpGet := callTestMCPJSON(t, cs, "workers_get", map[string]any{"worker_id": w.ID, "workspace_id": ws.ID})
	turnsMCP := mcpGet["data"].(map[string]any)["turns"].([]any)
	if po, _ := turnsMCP[0].(map[string]any)["partial_output"].(string); len(po) != 4099 {
		t.Fatalf("MCP workers_get lost partial_output: got len=%d", len(po))
	}
}

type steerReplyTestAdapter struct {
	stubWorkerAdapter
	steerCh chan string
}

func (a *steerReplyTestAdapter) SteerTurn(ctx context.Context, handle workers.ThreadHandle, nativeTurnID, text string) error {
	a.steerCh <- text
	return nil
}

func TestWorkersSteerReplyCancelPublicAndMCP(t *testing.T) {
	steerCh := make(chan string, 1)
	started := make(chan struct{}, 2)
	releaseTurn := make(chan struct{})
	ad := &steerReplyTestAdapter{
		steerCh: steerCh,
		stubWorkerAdapter: stubWorkerAdapter{
			startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
				ch := make(chan workers.TurnOutcome, 1)
				go func() {
					started <- struct{}{}
					<-steerCh
					if spec.OnInteraction != nil {
						_, _ = spec.OnInteraction(context.Background(), workers.InteractionRequest{
							Kind: workers.InteractionInput, Summary: "Need scope decision",
							Questions: []workers.InteractionQuestion{{ID: "q1", Question: "Include tests?"}},
						})
					}
					<-releaseTurn
					ch <- workers.TurnOutcome{State: workers.TurnCancelled, NativeTurnID: "nt_sr_1", ErrorCode: "cancelled"}
				}()
				return "nt_sr_1", ch, nil
			},
		},
	}
	n, ws, tok := setupM1WorkerNode(t, ad)
	a := n.app
	cs, closeMCP := openTestWaitMCPSession(t, a, tok)
	defer closeMCP()

	w := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "ctl-e2e", AccessMode: harnesses.AccessReadOnly,
	}).Data.(workers.Worker)
	turn := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{
		WorkerID: w.ID, WorkspaceID: ws.ID, Prompt: "check code",
	}).Data.(workers.Turn)
	<-started

	// 1. workers_steer via MCP preserves steered=true and delivery_mode="rpc".
	_, steerMCP := callTestMCPJSON(t, cs, "workers_steer", map[string]any{
		"operation_id": operations.NewID(time.Now()), "turn_id": turn.ID, "workspace_id": ws.ID, "text": "focus on auth",
	})
	steerData := steerMCP["data"].(map[string]any)
	if steerData["steered"] != true || steerData["delivery_mode"] != "rpc" {
		t.Fatalf("unexpected MCP workers_steer data: %+v", steerData)
	}

	// 2. Wait for waiting_input, then workers_reply (defer_seconds=120) via MCP preserves
	// exact request_id, deferred, and RFC3339 escalate_at matching receipt_summary.
	waitInp := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{TurnID: turn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5)})
	if waitInp.Error != nil || waitInp.Data.(workerWaitView).State != workers.TurnWaitingInput || waitInp.Data.(workerWaitView).PendingInteraction == nil {
		t.Fatalf("expected waiting_input with pending_interaction, got %+v", waitInp)
	}
	wantReqID := waitInp.Data.(workerWaitView).PendingInteraction.RequestID
	if wantReqID == "" {
		t.Fatalf("expected non-empty pending interaction request_id: %+v", waitInp.Data)
	}
	replyOpID := operations.NewID(time.Now())
	_, replyMCP := callTestMCPJSON(t, cs, "workers_reply", map[string]any{
		"operation_id": replyOpID, "turn_id": turn.ID, "workspace_id": ws.ID, "defer_seconds": 120, "note": "verifying",
	})
	replyData, _ := replyMCP["data"].(map[string]any)
	gotReqID, reqOK := replyData["request_id"].(string)
	gotEscRaw, escOK := replyData["escalate_at"].(string)
	gotEscTime, escErr := time.Parse(time.RFC3339Nano, gotEscRaw)
	if replyData["deferred"] != true || !reqOK || gotReqID != wantReqID || !escOK || escErr != nil || gotEscTime.IsZero() {
		t.Fatalf("unexpected MCP workers_reply data (want req=%q): %+v (escErr=%v)", wantReqID, replyData, escErr)
	}
	recSum, recOK := replyMCP["receipt_summary"].(map[string]any)
	recReqID, recReqOK := recSum["request_id"].(string)
	recEscRaw, recEscOK := recSum["escalate_at"].(string)
	recEscTime, recEscErr := time.Parse(time.RFC3339Nano, recEscRaw)
	if !recOK || !recReqOK || recReqID != wantReqID || !recEscOK || recEscErr != nil || !recEscTime.Equal(gotEscTime) {
		t.Fatalf("MCP workers_reply receipt_summary mismatch: summary=%+v wantReq=%q wantEsc=%v", recSum, wantReqID, gotEscTime)
	}

	// 3. workers_cancel via MCP cleanly cancels the active turn.
	close(releaseTurn)
	_, cancelMCP := callTestMCPJSON(t, cs, "workers_cancel", map[string]any{
		"operation_id": operations.NewID(time.Now()), "turn_id": turn.ID, "workspace_id": ws.ID,
	})
	if st, _ := cancelMCP["data"].(map[string]any)["state"].(string); st != workers.TurnCancelled && st != workers.TurnInterrupted {
		t.Fatalf("unexpected MCP workers_cancel state: %+v", cancelMCP)
	}
}

func TestWorkersLogsForwardBackwardBudgetAndSingleEntryTruncation(t *testing.T) {
	adapter := &stubWorkerAdapter{
		startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
			for i := 1; i <= 25; i++ {
				onEvent(workers.AdapterEvent{Kind: "tool", Summary: fmt.Sprintf("step-%02d: %s", i, strings.Repeat("<td>log</td>", 120))})
			}
			onEvent(workers.AdapterEvent{Kind: "tool", Summary: "HUGE:" + strings.Repeat("<table>", 9000)})
			ch := make(chan workers.TurnOutcome, 1)
			ch <- workers.TurnOutcome{
				State:        workers.TurnCompleted,
				NativeTurnID: "nt_logs_budget",
				Items:        []workers.NativeItem{{ItemID: "f", Type: "agentMessage", Phase: "final_answer", Text: "Done."}},
			}
			return "nt_logs_budget", ch, nil
		},
	}
	n, ws, tok := setupM1WorkerNode(t, adapter)
	a := n.app
	cs, closeMCP := openTestWaitMCPSession(t, a, tok)
	defer closeMCP()

	w := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "logs-budget", AccessMode: harnesses.AccessReadOnly,
	}).Data.(workers.Worker)
	turn := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{
		WorkerID: w.ID, WorkspaceID: ws.ID, Prompt: "emit logs",
	}).Data.(workers.Turn)
	if wr := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{TurnID: turn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5)}); wr.Error != nil {
		t.Fatalf("wait failed: %+v", wr.Error)
	}

	// Forward pagination via MCP recovers every seq without response_too_large and marks the oversized entry truncated.
	var forwardSeqs []int64
	sawEntryTruncated := false
	afterSeq := int64(0)
	for pages := 0; pages < 30; pages++ {
		_, parsed := callTestMCPJSON(t, cs, "workers_logs", map[string]any{"turn_id": turn.ID, "workspace_id": ws.ID, "after_seq": afterSeq, "limit": 50})
		data := parsed["data"].(map[string]any)
		entries := data["entries"].([]any)
		for _, raw := range entries {
			em := raw.(map[string]any)
			forwardSeqs = append(forwardSeqs, int64(em["seq"].(float64)))
			if tr, _ := em["truncated"].(bool); tr {
				sawEntryTruncated = true
			}
		}
		if hasMore, _ := data["has_more"].(bool); !hasMore {
			break
		}
		next := int64(data["next_after_seq"].(float64))
		if next <= afterSeq {
			t.Fatalf("next_after_seq did not advance: %d <= %d", next, afterSeq)
		}
		afterSeq = next
	}
	if len(forwardSeqs) < 26 || !sawEntryTruncated {
		t.Fatalf("expected >=26 forward log seqs and truncated single huge entry, got count=%d truncated=%v", len(forwardSeqs), sawEntryTruncated)
	}

	// Backward pagination starting from tail=true walks all the way back to seq 1 using earlier_before_seq.
	var backSeqs []int64
	beforeSeq := int64(0)
	for pages := 0; pages < 30; pages++ {
		args := map[string]any{"turn_id": turn.ID, "workspace_id": ws.ID, "tail": true, "limit": 50}
		if beforeSeq > 0 {
			args["before_seq"] = beforeSeq
		}
		_, parsed := callTestMCPJSON(t, cs, "workers_logs", args)
		data := parsed["data"].(map[string]any)
		entries := data["entries"].([]any)
		pageSeqs := make([]int64, 0, len(entries))
		for _, raw := range entries {
			pageSeqs = append(pageSeqs, int64(raw.(map[string]any)["seq"].(float64)))
		}
		backSeqs = append(pageSeqs, backSeqs...)
		if hasMore, _ := data["has_more"].(bool); !hasMore {
			break
		}
		earlier := int64(data["earlier_before_seq"].(float64))
		if earlier <= 1 || (beforeSeq > 0 && earlier >= beforeSeq) {
			t.Fatalf("earlier_before_seq did not move backward: %d (prev %d)", earlier, beforeSeq)
		}
		beforeSeq = earlier
	}
	if !slices.Equal(forwardSeqs, backSeqs) {
		t.Fatalf("backward tail pagination mismatch: forward=%d backward=%d", len(forwardSeqs), len(backSeqs))
	}
}

func TestWorkersBudgetGuaranteesPreserveActionResultsAndWaitCursors(t *testing.T) {
	heavyTurn := workers.Turn{
		ID: "turn_heavy", WorkerID: "wkr_1", WorkspaceID: "ws_1", HarnessID: harnesses.HarnessCodex,
		State: workers.TurnWaitingInput, AnswerAvailable: false, ResultBytes: 12000,
		PartialOutput: strings.Repeat("<table>", 1500),
		ErrorMessage:  strings.Repeat("E", 40000),
		PendingInteraction: &workers.InteractionView{
			RequestID: "req_1", Kind: workers.InteractionInput, Command: strings.Repeat("C", 60000),
		},
	}
	if b := serializedJSONBytes(fitTurnViewBudget(heavyTurn, workerViewMaxPayloadJSONBytes)); b > workerViewMaxPayloadJSONBytes {
		t.Fatalf("fitTurnViewBudget exceeded 44 KiB: %d", b)
	}
	sv := fitSteerViewBudget(workerSteerView{Turn: heavyTurn, Steered: true, DeliveryMode: "tool_boundary", NextStep: "wait"})
	if !sv.Steered || sv.DeliveryMode != "tool_boundary" || serializedJSONBytes(sv) > workerViewMaxPayloadJSONBytes {
		t.Fatalf("fitSteerViewBudget failed to preserve delivery_mode or budget: %+v bytes=%d", sv, serializedJSONBytes(sv))
	}
	escAt := time.Now().UTC().Add(2 * time.Minute)
	rv := fitReplyViewBudget(workerReplyView{Turn: heavyTurn, RequestID: "req_1", Deferred: true, EscalateAt: &escAt})
	if rv.RequestID != "req_1" || !rv.Deferred || rv.EscalateAt == nil || serializedJSONBytes(rv) > workerViewMaxPayloadJSONBytes {
		t.Fatalf("fitReplyViewBudget failed to preserve request_id/escalate_at or budget: %+v bytes=%d", rv, serializedJSONBytes(rv))
	}

	// Verify minimalWorkerWaitView preserves NextCursor="0" and DeliveredCommentarySeq=0 when base exceeds budget.
	zeroSeq := int64(0)
	heavyTurn.State = workers.TurnRunning
	heavyTurn.LatestCommentarySeq = 3
	wv := buildWorkerWaitView(heavyTurn, "", true, false, []workers.CommentaryEntry{{Seq: 1, Text: "c1"}}, true, true, 0)
	if wv.TurnID != "turn_heavy" || serializedJSONBytes(wv) > workerWaitMaxPayloadJSONBytes {
		t.Fatalf("buildWorkerWaitView(verbose=true) exceeded budget: bytes=%d", serializedJSONBytes(wv))
	}
	minWV := minimalWorkerWaitView(heavyTurn, workerWaitView{
		WorkerID: "wkr_1", TurnID: "turn_heavy", State: workers.TurnCompleted, AnswerAvailable: true,
		Truncated: true, NextCursor: "0", LatestCommentarySeq: 3, DeliveredCommentarySeq: &zeroSeq, CommentariesTruncated: true,
	})
	if minWV.NextCursor != "0" || minWV.DeliveredCommentarySeq == nil || *minWV.DeliveredCommentarySeq != 0 {
		t.Fatalf("minimalWorkerWaitView lost cursor/seq=0: %+v", minWV)
	}
}
