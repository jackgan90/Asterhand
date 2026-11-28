package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/harnesses"
	"github.com/jackgan90/Asterhand/internal/operations"
	"github.com/jackgan90/Asterhand/internal/workers"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPublicWorkersTurnsPagingPromptAndSummary(t *testing.T) {
	n, ws, tok := setupM1WorkerNode(t, &stubWorkerAdapter{})
	a := n.app
	cs, closeMCP := openTestWaitMCPSession(t, a, tok)
	defer closeMCP()

	w := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "timeline-worker", AccessMode: harnesses.AccessReadOnly,
	}).Data.(workers.Worker)
	prompts := []string{"first task", "second task 中文", "third task"}
	var turnIDs []string
	for _, p := range prompts {
		tr := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{WorkerID: w.ID, WorkspaceID: ws.ID, Prompt: p}).Data.(workers.Turn)
		wv := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{TurnID: tr.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5)})
		if wv.Error != nil || wv.Data.(workerWaitView).State != workers.TurnCompleted {
			t.Fatalf("turn did not complete: %+v", wv)
		}
		turnIDs = append(turnIDs, tr.ID)
	}

	page1 := testAutomationCall(a, tok, "workers_turns", "", workerTurnsInput{WorkerID: w.ID, WorkspaceID: ws.ID, Limit: automation.Int(2)})
	if page1.Error != nil {
		t.Fatalf("workers_turns page 1: %+v", page1.Error)
	}
	v1 := page1.Data.(workerTurnsView)
	if len(v1.Items) != 2 || v1.Items[0].TurnSeq != 3 || v1.Items[1].TurnSeq != 2 || !v1.HasMore || v1.NextCursor == "" ||
		v1.Items[0].PromptPreview != "third task" || v1.Items[0].PromptBytes != len("third task") ||
		!strings.Contains(v1.NextStep, `workspace_id="`+ws.ID+`"`) {
		t.Fatalf("unexpected first page or missing workspace_id in next_step: %+v", v1)
	}
	page2 := testAutomationCall(a, tok, "workers_turns", "", workerTurnsInput{WorkerID: w.ID, WorkspaceID: ws.ID, Limit: automation.Int(2), Cursor: v1.NextCursor})
	if page2.Error != nil {
		t.Fatalf("workers_turns page 2: %+v", page2.Error)
	}
	v2 := page2.Data.(workerTurnsView)
	if len(v2.Items) != 1 || v2.Items[0].TurnSeq != 1 || v2.HasMore || v2.NextCursor != "" {
		t.Fatalf("unexpected second page: %+v", v2)
	}
	mismatch := testAutomationCall(a, tok, "workers_turns", "", workerTurnsInput{WorkerID: w.ID, WorkspaceID: ws.ID, Filter: "attention", Cursor: v1.NextCursor})
	if mismatch.Error == nil || mismatch.Error.Code != "invalid_cursor" {
		t.Fatalf("cursor reused with another filter must be rejected, got %+v", mismatch.Error)
	}

	prompt := testAutomationCall(a, tok, "workers_result", "", workerResultInput{TurnID: turnIDs[1], WorkspaceID: ws.ID, Part: "prompt"})
	if prompt.Error != nil {
		t.Fatalf("workers_result part=prompt: %+v", prompt.Error)
	}
	if pp := prompt.Data.(workers.ResultPage); pp.Text != prompts[1] || pp.Part != "prompt" || !pp.Complete {
		t.Fatalf("unexpected prompt page: %+v", pp)
	}
	answer := testAutomationCall(a, tok, "workers_result", "", workerResultInput{TurnID: turnIDs[1], WorkspaceID: ws.ID})
	if answer.Error != nil || !strings.Contains(answer.Data.(workers.ResultPage).Text, "Finding") || answer.Data.(workers.ResultPage).Part != "" {
		t.Fatalf("default part must keep reading the answer: %+v", answer)
	}
	if bad := testAutomationCall(a, tok, "workers_result", "", workerResultInput{TurnID: turnIDs[1], Part: "bogus"}); bad.Error == nil || bad.Error.Code != "invalid_parameters" {
		t.Fatalf("unknown part must be rejected, got %+v", bad.Error)
	}

	get := testAutomationCall(a, tok, "workers_get", "", workerGetInput{WorkerID: w.ID, WorkspaceID: ws.ID})
	if get.Error != nil {
		t.Fatalf("workers_get: %+v", get.Error)
	}
	gv := get.Data.(workerGetView)
	if gv.Summary == nil || gv.Summary.TurnCount != 3 || gv.Summary.StateCounts[workers.TurnCompleted] != 3 || gv.TurnsHasMore || len(gv.Turns) != 3 || gv.Turns[0].TurnSeq != 3 {
		t.Fatalf("unexpected workers_get summary view: %+v", gv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "workers_turns", Arguments: map[string]any{"worker_id": w.ID, "limit": 2}})
	if err != nil || res.IsError || len(res.Content) == 0 {
		t.Fatalf("MCP workers_turns failed: res=%+v err=%v", res, err)
	}
	txt := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(txt, `"turn_seq":3`) || !strings.Contains(txt, `"next_cursor"`) {
		t.Fatalf("MCP workers_turns text missing timeline fields: %s", txt)
	}
}

func TestWorkersTurnsViewShortensPageWithinBudget(t *testing.T) {
	base := time.Now().UTC()
	items := make([]workers.TurnListItem, 50)
	for i := range items {
		items[i] = workers.TurnListItem{
			TurnID:        "turn_budget_" + strings.Repeat("x", 4) + string(rune('A'+i%26)) + string(rune('a'+i/26)),
			WorkerID:      "w_budget",
			TurnSeq:       50 - i,
			State:         workers.TurnFailed,
			PromptPreview: strings.Repeat("长", 80),
			ErrorCode:     strings.Repeat("e", 1500),
			StartedAt:     base.Add(-time.Duration(i) * time.Minute),
		}
	}
	binding := []any{"workers_turns", "p", "w_budget", "ws_1", "attention"}
	view := buildWorkerTurnsView("w_budget", "ws_1", "attention", "cur_req_1", items, false, binding)
	if serializedJSONBytes(view) > workerViewMaxPayloadJSONBytes || !view.Truncated || !view.HasMore || view.Cursor != "cur_req_1" || len(view.Items) == 0 || len(view.Items) >= len(items) {
		t.Fatalf("page must shrink within budget: items=%d bytes=%d truncated=%v cursor=%q", len(view.Items), serializedJSONBytes(view), view.Truncated, view.Cursor)
	}
	if !strings.Contains(view.NextStep, `workspace_id="ws_1"`) || !strings.Contains(view.NextStep, `filter="attention"`) {
		t.Fatalf("workers_turns next_step must carry workspace_id and filter, got %q", view.NextStep)
	}
	cur, err := automation.ParseCursor(view.NextCursor, binding)
	last := view.Items[len(view.Items)-1]
	if err != nil || cur.After != workers.TurnListItemAfter(last) {
		t.Fatalf("cursor must continue after the last delivered turn: %+v err=%v", cur, err)
	}
}

func TestMCPWorkersTurnsCompactsOversizeSingleItemErrorCode(t *testing.T) {
	adapter := &stubWorkerAdapter{
		startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
			ch := make(chan workers.TurnOutcome, 1)
			if strings.HasPrefix(spec.Prompt, "HUGE_ERROR_CODE") {
				ch <- workers.TurnOutcome{
					State:        workers.TurnInterrupted,
					NativeTurnID: "nt_huge_err",
					ErrorCode:    strings.Repeat("z", 50<<10),
				}
				return "nt_huge_err", ch, nil
			}
			ch <- workers.TurnOutcome{
				State:        workers.TurnCompleted,
				NativeTurnID: "nt_ok",
				Items:        []workers.NativeItem{{ItemID: "f1", Type: "agentMessage", Phase: "final_answer", Text: "ok"}},
			}
			return "nt_ok", ch, nil
		},
	}
	n, ws, tok := setupM1WorkerNode(t, adapter)
	a := n.app
	cs, closeMCP := openTestWaitMCPSession(t, a, tok)
	defer closeMCP()

	w := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "oversize-err-worker", AccessMode: harnesses.AccessReadOnly,
	}).Data.(workers.Worker)

	trOld := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{
		WorkerID: w.ID, WorkspaceID: ws.ID, Prompt: "HUGE_ERROR_CODE turn",
	}).Data.(workers.Turn)
	_ = testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{TurnID: trOld.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5)})

	trNew := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{
		WorkerID: w.ID, WorkspaceID: ws.ID, Prompt: "latest short turn",
	}).Data.(workers.Turn)
	_ = testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{TurnID: trNew.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5)})

	first := testAutomationCall(a, tok, "workers_turns", "", workerTurnsInput{
		WorkerID: w.ID, WorkspaceID: ws.ID, Limit: automation.Int(1),
	}).Data.(workerTurnsView)
	if first.NextCursor == "" {
		t.Fatalf("expected next_cursor after page 1, got %+v", first)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "workers_turns",
		Arguments: map[string]any{"worker_id": w.ID, "workspace_id": ws.ID, "cursor": first.NextCursor, "limit": 1},
	})
	if err != nil || res.IsError || len(res.Content) == 0 {
		t.Fatalf("MCP workers_turns page 2 failed: res=%+v err=%v", res, err)
	}
	txt := res.Content[0].(*mcp.TextContent).Text
	if strings.Contains(txt, `"text_truncated":true`) {
		t.Fatalf("single item with 50 KiB error_code must fit MCP budget after compaction without dropping items: %s", txt)
	}
	if !strings.Contains(txt, trOld.ID) || !strings.Contains(txt, `"state":"interrupted"`) || !strings.Contains(txt, `"error_code_truncated":true`) {
		t.Fatalf("expected compacted turn identity and error_code_truncated=true on page 2, got: %s", txt)
	}
}

func TestMCPWorkersResultPromptNextStepAndWebPartialPreload(t *testing.T) {
	partialBody := strings.TrimSpace(strings.Repeat("Partial analysis line before interrupt.\n", 600)) // ~24 KiB > 16 KiB first page
	adapter := &stubWorkerAdapter{
		startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
			ch := make(chan workers.TurnOutcome, 1)
			if strings.HasPrefix(spec.Prompt, "FAIL_WITH_PARTIAL") {
				ch <- workers.TurnOutcome{
					State:        workers.TurnFailed,
					NativeTurnID: "nt_partial",
					ErrorCode:    "max_tokens",
					ErrorMessage: "output limit hit",
					Items:        []workers.NativeItem{{ItemID: "p1", Type: "agentMessage", Phase: "partial_answer", Text: partialBody}},
				}
				return "nt_partial", ch, nil
			}
			ch <- workers.TurnOutcome{
				State:        workers.TurnCompleted,
				NativeTurnID: "nt_ok",
				Items:        []workers.NativeItem{{ItemID: "f1", Type: "agentMessage", Phase: "final_answer", Text: "done"}},
			}
			return "nt_ok", ch, nil
		},
	}
	n, ws, tok := setupM1WorkerNode(t, adapter)
	a := n.app
	cs, closeMCP := openTestWaitMCPSession(t, a, tok)
	defer closeMCP()

	w := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "prompt-hint-worker", AccessMode: harnesses.AccessReadOnly,
	}).Data.(workers.Worker)

	// 1. MCP workers_result(part="prompt") normal pagination hint must preserve part="prompt".
	longPrompt := strings.Repeat("长任务说明段落内容。", 400) // ~12 KiB
	trPrompt := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{
		WorkerID: w.ID, WorkspaceID: ws.ID, Prompt: longPrompt,
	}).Data.(workers.Turn)
	_ = testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{TurnID: trPrompt.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5)})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mcpRes, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "workers_result",
		Arguments: map[string]any{"turn_id": trPrompt.ID, "workspace_id": ws.ID, "part": "prompt", "max_bytes": 4096},
	})
	if err != nil || mcpRes.IsError || len(mcpRes.Content) == 0 {
		t.Fatalf("MCP workers_result(part=prompt) failed: res=%+v err=%v", mcpRes, err)
	}
	mcpTxt := mcpRes.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(mcpTxt, `part=\"prompt\"`) {
		t.Fatalf("MCP workers_result normal pagination next_step must include part=\"prompt\", got: %s", mcpTxt)
	}

	// 2. Web /api/workers/logs preloads retained partial output for failed turns so UI can paginate beyond 4 KiB.
	trFail := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{
		WorkerID: w.ID, WorkspaceID: ws.ID, Prompt: "FAIL_WITH_PARTIAL please",
	}).Data.(workers.Turn)
	_ = testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{TurnID: trFail.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5)})

	req := httptest.NewRequest(http.MethodGet, "/api/workers/logs?turn_id="+trFail.ID, nil)
	rec := httptest.NewRecorder()
	a.workersAPI(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET /api/workers/logs status=%d body=%s", rec.Code, rec.Body.String())
	}
	var logsPayload struct {
		Result *workers.ResultPage `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &logsPayload); err != nil {
		t.Fatal(err)
	}
	if logsPayload.Result == nil || !logsPayload.Result.Partial || logsPayload.Result.Complete || logsPayload.Result.NextCursor == "" {
		t.Fatalf("expected preloaded paginated partial ResultPage on failed turn, got %+v", logsPayload.Result)
	}
	req2 := httptest.NewRequest(http.MethodGet, "/api/workers/result?turn_id="+trFail.ID+"&cursor="+logsPayload.Result.NextCursor, nil)
	rec2 := httptest.NewRecorder()
	a.workersAPI(rec2, req2)
	if rec2.Code != 200 {
		t.Fatalf("GET /api/workers/result status=%d body=%s", rec2.Code, rec2.Body.String())
	}
	var page2 workers.ResultPage
	if err := json.Unmarshal(rec2.Body.Bytes(), &page2); err != nil {
		t.Fatal(err)
	}
	if !page2.Partial || !page2.Complete || logsPayload.Result.Text+page2.Text != partialBody {
		t.Fatalf("reassembled partial output mismatch: complete=%v len=%d want=%d", page2.Complete, len(logsPayload.Result.Text+page2.Text), len(partialBody))
	}
}
