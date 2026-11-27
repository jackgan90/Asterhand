package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/harnesses"
	"github.com/jackgan90/Asterhand/internal/operations"
	"github.com/jackgan90/Asterhand/internal/workers"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type registryDirectCaller struct {
	app   *App
	token string
}

func (c *registryDirectCaller) Call(ctx context.Context, op, id string, args json.RawMessage) (automation.Response, error) {
	return c.app.Automation.Registry.Dispatch(ctx, c.token, automation.Request{
		Version:   automation.APIVersion,
		Node:      c.app.Config.Get().NodeID,
		Operation: op,
		ID:        id,
		Arguments: args,
	}), nil
}

func (c *registryDirectCaller) FetchDefinitions(ctx context.Context) (string, []automation.Definition, error) {
	p, err := c.app.Automation.Registry.Authorize(c.token)
	if err != nil {
		return "", nil, err
	}
	return c.app.Automation.Registry.Instance, c.app.Automation.Registry.Definitions(p), nil
}

func dispatchCompletedPublicTurns(t *testing.T, a *App, tok string, ws harnesses.DelegatedWorkspace, workerID string, count int) []string {
	t.Helper()
	turnIDs := make([]string, 0, count)
	for i := 0; i < count; i++ {
		sr := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{
			WorkerID: workerID, WorkspaceID: ws.ID, Prompt: fmt.Sprintf("review module %02d", i),
		})
		if sr.Error != nil {
			t.Fatalf("workers_send %d: %+v", i, sr.Error)
		}
		turn := sr.Data.(workers.Turn)
		wr := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{
			TurnID: turn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5),
		})
		if wr.Error != nil || wr.Data.(workerWaitView).State != workers.TurnCompleted {
			t.Fatalf("workers_wait %d: resp=%+v", i, wr)
		}
		turnIDs = append(turnIDs, turn.ID)
	}
	return turnIDs
}

func assertPublicResultReconstructs(t *testing.T, a *App, tok, wsID string, it workers.WaitAnyItem, want string) {
	t.Helper()
	if !it.Truncated || it.NextCursor != strconv.Itoa(len(it.AnswerPreview)) {
		t.Fatalf("turn %s expected truncated preview with exact UTF-8 byte cursor, got trunc=%v cursor=%q previewLen=%d", it.TurnID, it.Truncated, it.NextCursor, len(it.AnswerPreview))
	}
	var full strings.Builder
	full.WriteString(it.AnswerPreview)
	cursor := it.NextCursor
	for cursor != "" {
		rr := testAutomationCall(a, tok, "workers_result", "", workerResultInput{
			TurnID: it.TurnID, WorkspaceID: wsID, Cursor: cursor,
		})
		if rr.Error != nil {
			t.Fatalf("turn %s workers_result(cursor=%q): %+v", it.TurnID, cursor, rr.Error)
		}
		page := rr.Data.(workers.ResultPage)
		full.WriteString(page.Text)
		if page.Complete {
			cursor = ""
		} else {
			cursor = page.NextCursor
		}
	}
	if full.String() != want {
		t.Fatalf("turn %s full answer mismatch: got %d bytes, want %d bytes", it.TurnID, full.Len(), len(want))
	}
}

func assertMCPWaitAnyUnclipped(t *testing.T, a *App, tok, wsID string, turnIDs []string) {
	t.Helper()
	p, err := a.Automation.Registry.Authorize(tok)
	if err != nil {
		t.Fatal(err)
	}
	host := automation.NewMCPHost(&registryDirectCaller{app: a, token: tok}, p, a.Automation.Registry.Definitions(p))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t1, t2 := mcp.NewInMemoryTransports()
	ss, err := host.Server.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "wait-any-16-test", Version: "1"}, nil).Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "workers_wait_any",
		Arguments: map[string]any{"turn_ids": turnIDs, "workspace_id": wsID, "wait_seconds": 1},
	})
	if err != nil || res.IsError || len(res.Content) == 0 {
		t.Fatalf("MCP workers_wait_any failed: res=%+v err=%v", res, err)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	if strings.Contains(tc.Text, `"text_truncated":true`) {
		t.Fatalf("MCP text projection unexpectedly truncated 16-turn workers_wait_any response (len=%d)", len(tc.Text))
	}
	for _, tid := range turnIDs {
		if !strings.Contains(tc.Text, tid) {
			t.Fatalf("MCP text projection missing turn_id %s", tid)
		}
	}
}

func TestPublicWorkersWaitAny16LongTurnsFitsResponseAndMCPTextBudget(t *testing.T) {
	answers := make([]string, 16)
	for i := 0; i < 16; i++ {
		if i%2 == 0 {
			answers[i] = strings.TrimSpace(fmt.Sprintf("## Module %02d Review\n", i) + strings.Repeat("Finding: check bounds & <ctx.Done()> before write.\n", 140))
		} else {
			answers[i] = strings.TrimSpace(fmt.Sprintf("## 模块 %02d 审查结论\n", i) + strings.Repeat("建议：补齐边界校验与状态同步逻辑。\n", 140))
		}
	}
	nextIdx := 0
	adapter := &stubWorkerAdapter{
		startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
			cur := nextIdx
			nextIdx++
			ch := make(chan workers.TurnOutcome, 1)
			ch <- workers.TurnOutcome{
				State:        workers.TurnCompleted,
				NativeTurnID: fmt.Sprintf("nt_pub16_%02d", cur),
				Items:        []workers.NativeItem{{ItemID: fmt.Sprintf("f_%02d", cur), Type: "agentMessage", Phase: "final_answer", Text: answers[cur]}},
			}
			return fmt.Sprintf("nt_pub16_%02d", cur), ch, nil
		},
	}

	n, ws, tok := setupM1WorkerNode(t, adapter)
	a := n.app
	cr := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "pub-wait-any-16", AccessMode: harnesses.AccessReadOnly,
	})
	if cr.Error != nil {
		t.Fatalf("workers_create: %+v", cr.Error)
	}
	w := cr.Data.(workers.Worker)
	turnIDs := dispatchCompletedPublicTurns(t, a, tok, ws, w.ID, 16)

	waResp := testAutomationCall(a, tok, "workers_wait_any", "", workerWaitAnyInput{
		TurnIDs: turnIDs, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5),
	})
	if waResp.Error != nil {
		t.Fatalf("workers_wait_any(16 long turns) failed: %+v", waResp.Error)
	}
	if rawResp, err := json.Marshal(waResp); err != nil || len(rawResp) > automation.MaxResponse {
		t.Fatalf("workers_wait_any response %d bytes exceeds MaxResponse %d (err=%v)", len(rawResp), automation.MaxResponse, err)
	}
	view := waResp.Data.(workerWaitAnyView)
	if view.TriggeredTurnID != turnIDs[0] || len(view.Items) != 16 {
		t.Fatalf("expected triggered=%s with 16 items, got triggered=%q len=%d", turnIDs[0], view.TriggeredTurnID, len(view.Items))
	}
	for i, it := range view.Items {
		if it.TurnID != turnIDs[i] || it.State != workers.TurnCompleted || !it.AnswerAvailable {
			t.Fatalf("item[%d] unexpected status: %+v", i, it)
		}
		assertPublicResultReconstructs(t, a, tok, ws.ID, it, answers[i])
	}
	assertMCPWaitAnyUnclipped(t, a, tok, ws.ID, turnIDs)
}

func makePublicOversizedGofmtCommand() string {
	var b strings.Builder
	b.WriteString("gofmt -w")
	for i := 0; i < 584; i++ {
		fmt.Fprintf(&b, " internal/module_%02d/handler_source_%03d.go", i%20, i)
	}
	return b.String()
}

func TestPublicWorkersWaitAnyOversizedPendingApprovalsFitRegistryAndMCP(t *testing.T) {
	bigCmd := makePublicOversizedGofmtCommand()
	release := make(chan struct{})
	adapter := &stubWorkerAdapter{
		startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
			ch := make(chan workers.TurnOutcome, 1)
			go func() {
				_, _ = spec.OnInteraction(context.Background(), workers.InteractionRequest{
					Kind:     workers.InteractionApproval,
					ToolName: "bash",
					Summary:  "Format 584 Go files with gofmt",
					Command:  bigCmd,
				})
				<-release
				ch <- workers.TurnOutcome{State: workers.TurnCancelled, NativeTurnID: "nt_pub_app"}
			}()
			return "nt_pub_app", ch, nil
		},
	}

	n, _, tok := setupM1WorkerNode(t, adapter)
	defer close(release)
	a := n.app
	tr := true
	if _, err := a.Harnesses.UpdatePolicy(harnesses.UpdatePolicyInput{HarnessID: harnesses.HarnessCodex, AllowWorkspaceWrite: &tr}); err != nil {
		t.Fatal(err)
	}

	workerIDs := make([]string, 0, 3)
	turnIDs := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		dir := t.TempDir()
		cr := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
			HarnessID: harnesses.HarnessCodex, WorkspaceRoot: dir, Alias: fmt.Sprintf("pub-fmt-%d", i),
			AccessMode: harnesses.AccessWorkspaceWrite, ApprovalMode: workers.ApprovalModeAskHuman,
		})
		if cr.Error != nil {
			t.Fatalf("workers_create %d: %+v", i, cr.Error)
		}
		w := cr.Data.(workers.Worker)
		sr := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{WorkerID: w.ID, Prompt: "format repo"})
		if sr.Error != nil {
			t.Fatalf("workers_send %d: %+v", i, sr.Error)
		}
		workerIDs = append(workerIDs, w.ID)
		turnIDs = append(turnIDs, sr.Data.(workers.Turn).ID)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if len(a.Workers.ListHumanInteractions()) == 3 {
			break
		}
	}

	for _, count := range []int{1, 2, 3} {
		subset := turnIDs[:count]
		waResp := testAutomationCall(a, tok, "workers_wait_any", "", workerWaitAnyInput{TurnIDs: subset, WaitSeconds: automation.Int(1)})
		if waResp.Error != nil {
			t.Fatalf("workers_wait_any(%d approvals) failed: %+v", count, waResp.Error)
		}
		if raw, err := json.Marshal(waResp); err != nil || len(raw) > automation.MaxResponse {
			t.Fatalf("workers_wait_any(%d approvals) size=%d exceeds MaxResponse (err=%v)", count, len(raw), err)
		}
		view := waResp.Data.(workerWaitAnyView)
		if len(view.Items) != count {
			t.Fatalf("expected %d items, got %d", count, len(view.Items))
		}
		for i, it := range view.Items {
			pi := it.PendingInteraction
			if it.State != workers.TurnWaitingApproval || pi == nil || pi.RequestID == "" || pi.Kind != workers.InteractionApproval || !pi.VisibleToHuman || pi.Summary == "" {
				t.Fatalf("count=%d item[%d] missing required approval fields: %+v", count, i, it)
			}
			if count == 1 {
				if pi.DetailsTruncated || pi.Command != bigCmd {
					t.Fatalf("count=1 item[0] should fit without truncation: trunc=%v cmdLen=%d", pi.DetailsTruncated, len(pi.Command))
				}
			} else if !pi.DetailsTruncated || pi.Command != "" || pi.InspectHint == "" {
				t.Fatalf("count=%d item[%d] expected compacted approval with inspect_hint: %+v", count, i, pi)
			}
		}
		assertMCPWaitAnyUnclipped(t, a, tok, "", subset)
	}

	// Single-worker inspection and human approval UI must both retain the complete command.
	getResp := testAutomationCall(a, tok, "workers_get", "", workerGetInput{WorkerID: workerIDs[0]})
	if getResp.Error != nil {
		t.Fatalf("workers_get failed: %+v", getResp.Error)
	}
	gv := getResp.Data.(workerGetView)
	if len(gv.Turns) == 0 || gv.Turns[0].PendingInteraction == nil || gv.Turns[0].PendingInteraction.Command != bigCmd {
		t.Fatalf("workers_get did not preserve full approval command")
	}
	for _, h := range a.Workers.ListHumanInteractions() {
		if h.Command != bigCmd {
			t.Fatalf("human approval %s lost full command", h.RequestID)
		}
	}
}

func assertPublicThreeCommentaryCursorModes(t *testing.T, a *App, tok, wsID, turnID, fullFirst, tailSentence string) {
	t.Helper()
	waitOmitted := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{
		TurnID: turnID, WorkspaceID: wsID, WaitSeconds: automation.Int(5), WakeOnCommentary: true,
	})
	if waitOmitted.Error != nil {
		t.Fatalf("workers_wait omitted cursor failed: %+v", waitOmitted.Error)
	}
	if comms := waitOmitted.Data.(workerWaitView).Commentaries; len(comms) != 2 || comms[0].Seq != 2 || comms[1].Seq != 3 {
		t.Fatalf("omitted after_commentary_seq expected [2, 3], got %+v", comms)
	}

	waitZero := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{
		TurnID: turnID, WorkspaceID: wsID, WaitSeconds: automation.Int(5),
		AfterCommentarySeq: automation.Int64(0), WakeOnCommentary: true,
	})
	if waitZero.Error != nil {
		t.Fatalf("workers_wait explicit 0 failed: %+v", waitZero.Error)
	}
	if comms := waitZero.Data.(workerWaitView).Commentaries; len(comms) != 3 || comms[0].Seq != 1 || comms[1].Seq != 2 || comms[2].Seq != 3 || comms[0].Text != fullFirst || !strings.Contains(comms[0].Text, tailSentence) {
		t.Fatalf("explicit after_commentary_seq=0 expected [1, 2, 3] with full seq=1 text, got %+v", comms)
	}

	waitOne := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{
		TurnID: turnID, WorkspaceID: wsID, WaitSeconds: automation.Int(5),
		AfterCommentarySeq: automation.Int64(1), WakeOnCommentary: true,
	})
	if waitOne.Error != nil {
		t.Fatalf("workers_wait explicit 1 failed: %+v", waitOne.Error)
	}
	if comms := waitOne.Data.(workerWaitView).Commentaries; len(comms) != 2 || comms[0].Seq != 2 || comms[1].Seq != 3 {
		t.Fatalf("explicit after_commentary_seq=1 expected [2, 3], got %+v", comms)
	}
}

func TestPublicWorkersWaitAnyTruncatedCommentaryResumesCompleteTail(t *testing.T) {
	const tailSentence = "FINAL_HTML_REVIEW_SENTENCE_INTACT"
	htmlCommentary := strings.Repeat(`<section data-k="v">&lt;check&gt;</section>`, 42) + " " + tailSentence
	answers := make([]string, 15)
	for i := range answers {
		answers[i] = strings.TrimSpace(fmt.Sprintf("Review %02d: ", i) + strings.Repeat("finding line with details\n", 260))
	}
	commentary1Sent, emitMore, commentaries3Sent, releaseRunning := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	idx := 0
	adapter := &stubWorkerAdapter{
		startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
			if spec.Prompt == "html-running" {
				ch := make(chan workers.TurnOutcome, 1)
				go func() {
					if onEvent != nil {
						it1 := workers.NativeItem{ItemID: "html_c1", Type: "agentMessage", Phase: "commentary", Text: htmlCommentary}
						onEvent(workers.AdapterEvent{Kind: "commentary", ItemID: it1.ItemID, Phase: "commentary", Summary: "HTML review", Item: &it1})
					}
					close(commentary1Sent)
					<-emitMore
					if onEvent != nil {
						for i, txt := range []string{"Second commentary update", "Third commentary update"} {
							it := workers.NativeItem{ItemID: fmt.Sprintf("html_c%d", i+2), Type: "agentMessage", Phase: "commentary", Text: txt}
							onEvent(workers.AdapterEvent{Kind: "commentary", ItemID: it.ItemID, Phase: "commentary", Summary: txt, Item: &it})
						}
					}
					close(commentaries3Sent)
					<-releaseRunning
					ch <- workers.TurnOutcome{State: workers.TurnCancelled, NativeTurnID: "nt_html_run"}
				}()
				return "nt_html_run", ch, nil
			}
			cur := idx
			idx++
			ch := make(chan workers.TurnOutcome, 1)
			ch <- workers.TurnOutcome{
				State:        workers.TurnCompleted,
				NativeTurnID: fmt.Sprintf("nt_c15_%d", cur),
				Items:        []workers.NativeItem{{ItemID: fmt.Sprintf("f_%d", cur), Type: "agentMessage", Phase: "final_answer", Text: answers[cur]}},
			}
			return fmt.Sprintf("nt_c15_%d", cur), ch, nil
		},
	}

	n, ws, tok := setupM1WorkerNode(t, adapter)
	defer close(releaseRunning)
	a := n.app
	wDone := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "pub-done15", AccessMode: harnesses.AccessReadOnly,
	}).Data.(workers.Worker)
	wRun := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "pub-html-run", AccessMode: harnesses.AccessReadOnly,
	}).Data.(workers.Worker)

	turnIDs := dispatchCompletedPublicTurns(t, a, tok, ws, wDone.ID, 15)
	runTurn := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{
		WorkerID: wRun.ID, WorkspaceID: ws.ID, Prompt: "html-running",
	}).Data.(workers.Turn)
	<-commentary1Sent
	turnIDs = append(turnIDs, runTurn.ID)

	waResp := testAutomationCall(a, tok, "workers_wait_any", "", workerWaitAnyInput{TurnIDs: turnIDs, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5)})
	if waResp.Error != nil {
		t.Fatalf("workers_wait_any failed: %+v", waResp.Error)
	}
	if rawJSON, _ := json.Marshal(waResp.Data); !strings.Contains(string(rawJSON), `"delivered_commentary_seq":0`) || !strings.Contains(string(rawJSON), `"commentaries_truncated":true`) {
		t.Fatalf("expected explicit delivered_commentary_seq:0 and commentaries_truncated:true in JSON")
	}
	runItem := waResp.Data.(workerWaitAnyView).Items[15]
	if runItem.LatestCommentarySeq != 1 || !runItem.CommentariesTruncated || runItem.DeliveredCommentarySeq == nil || *runItem.DeliveredCommentarySeq != 0 {
		t.Fatalf("unexpected running commentary cursor fields: %+v", runItem)
	}

	// Immediate resume with explicit 0 recovers [1] in full.
	wait1 := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{
		TurnID: runTurn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5),
		AfterCommentarySeq: automation.Int64(*runItem.DeliveredCommentarySeq), WakeOnCommentary: true,
	})
	if wait1.Error != nil {
		t.Fatalf("immediate workers_wait resume failed: %+v", wait1.Error)
	}
	if comms := wait1.Data.(workerWaitView).Commentaries; len(comms) != 1 || comms[0].Seq != 1 || comms[0].Text != htmlCommentary {
		t.Fatalf("immediate resume expected full [1], got %+v", comms)
	}

	// Emit commentaries #2 and #3 on the same turn, then assert omitted / explicit 0 / explicit 1 behavior.
	close(emitMore)
	<-commentaries3Sent
	assertPublicThreeCommentaryCursorModes(t, a, tok, ws.ID, runTurn.ID, htmlCommentary, tailSentence)
}
