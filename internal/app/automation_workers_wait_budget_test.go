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

func openTestWaitMCPSession(t *testing.T, a *App, tok string) (*mcp.ClientSession, func()) {
	t.Helper()
	p, err := a.Automation.Registry.Authorize(tok)
	if err != nil {
		t.Fatal(err)
	}
	host := automation.NewMCPHost(&registryDirectCaller{app: a, token: tok}, p, a.Automation.Registry.Definitions(p))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t1, t2 := mcp.NewInMemoryTransports()
	ss, err := host.Server.Connect(ctx, t1, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "wait-budget-test", Version: "1"}, nil).Connect(ctx, t2, nil)
	if err != nil {
		ss.Close()
		cancel()
		t.Fatal(err)
	}
	return cs, func() {
		cs.Close()
		ss.Close()
		cancel()
	}
}

func makeHTMLCommentaries(count int, repeats int) []string {
	out := make([]string, count)
	for i := 0; i < count; i++ {
		body := strings.Repeat(fmt.Sprintf(`<div class="row" data-sec="%02d"><span>a &amp; b &lt; c &gt; d</span></div>`, i+1), repeats)
		out[i] = fmt.Sprintf(`<!-- review-comment-%02d --> %s <!-- end-%02d -->`, i+1, body, i+1)
	}
	return out
}

func makeHTMLFinalAnswer(sections int) string {
	var b strings.Builder
	b.WriteString("<!doctype html><html><head><title>Asterhand Audit</title></head><body>\n")
	for i := 0; i < sections; i++ {
		fmt.Fprintf(&b, "<section id=\"s-%03d\"><h2>模块 %03d &amp; 校验</h2><p data-ok=\"1\">&lt;tag-%03d&gt; body &gt; value</p></section>\n", i, i, i)
	}
	b.WriteString("</body></html>")
	return strings.TrimSpace(b.String())
}

func assertWaitViewReconstructsAnswer(t *testing.T, a *App, tok, wsID string, wv workerWaitView, want string) {
	t.Helper()
	var full strings.Builder
	full.WriteString(wv.Answer)
	if wv.Truncated {
		if wv.NextCursor != strconv.Itoa(len(wv.Answer)) {
			t.Fatalf("expected next_cursor=%d matching preview bytes, got %q", len(wv.Answer), wv.NextCursor)
		}
		cursor := wv.NextCursor
		for cursor != "" {
			rr := testAutomationCall(a, tok, "workers_result", "", workerResultInput{
				TurnID: wv.TurnID, WorkspaceID: wsID, Cursor: cursor,
			})
			if rr.Error != nil {
				t.Fatalf("workers_result(cursor=%q) failed: %+v", cursor, rr.Error)
			}
			page := rr.Data.(workers.ResultPage)
			full.WriteString(page.Text)
			if page.Complete {
				cursor = ""
			} else {
				cursor = page.NextCursor
			}
		}
	}
	if full.String() != want {
		t.Fatalf("reconstructed final answer mismatch: got %d bytes, want %d bytes", full.Len(), len(want))
	}
}

func assertMCPWaitPageUnclipped(t *testing.T, cs *mcp.ClientSession, args map[string]any, wantCount int, wantFirstSeq int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "workers_wait", Arguments: args})
	if err != nil || res.IsError || len(res.Content) == 0 {
		t.Fatalf("MCP workers_wait(%v) failed: res=%+v err=%v", args, res, err)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(tc.Text), &payload); err != nil {
		t.Fatalf("unmarshal MCP text failed: %v", err)
	}
	if trunc, _ := payload["text_truncated"].(bool); trunc {
		t.Fatalf("MCP workers_wait triggered text_truncated (len=%d): %s", len(tc.Text), tc.Text[:min(400, len(tc.Text))])
	}
	dataMap, _ := payload["data"].(map[string]any)
	comms, _ := dataMap["commentaries"].([]any)
	if len(comms) != wantCount {
		t.Fatalf("MCP workers_wait(%v) expected %d commentaries in content[].text, got %d", args, wantCount, len(comms))
	}
	if wantCount > 0 {
		first, _ := comms[0].(map[string]any)
		if gotSeq := int64(first["seq"].(float64)); gotSeq != wantFirstSeq {
			t.Fatalf("MCP workers_wait(%v) expected first seq=%d, got %d", args, wantFirstSeq, gotSeq)
		}
	}
}

func paginateCommentariesFromCursor(
	t *testing.T,
	a *App,
	cs *mcp.ClientSession,
	tok, wsID, turnID string,
	startCursor int64,
	wantTexts []string,
	firstWantSeq int64,
) (int, workerWaitView) {
	t.Helper()
	cursor := startCursor
	collected := make([]workers.CommentaryEntry, 0, len(wantTexts))
	pages := 0
	var firstView workerWaitView
	for {
		pages++
		if pages > 25 {
			t.Fatalf("commentary pagination did not terminate after %d pages (cursor=%d)", pages, cursor)
		}
		resp := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{
			TurnID: turnID, WorkspaceID: wsID, WaitSeconds: automation.Int(1),
			AfterCommentarySeq: automation.Int64(cursor), WakeOnCommentary: true,
		})
		if resp.Error != nil {
			t.Fatalf("workers_wait(after_commentary_seq=%d) failed: %+v", cursor, resp.Error)
		}
		wv := resp.Data.(workerWaitView)
		if pages == 1 {
			firstView = wv
		}
		raw, err := json.Marshal(wv)
		if err != nil {
			t.Fatalf("marshal workerWaitView: %v", err)
		}
		if len(raw) > workerWaitMaxPayloadJSONBytes {
			t.Fatalf("page %d exceeded %d-byte JSON budget: got %d bytes", pages, workerWaitMaxPayloadJSONBytes, len(raw))
		}
		if len(wv.Commentaries) == 0 {
			t.Fatalf("page %d delivered 0 commentaries while paginating from cursor=%d", pages, cursor)
		}
		assertMCPWaitPageUnclipped(t, cs, map[string]any{
			"turn_id":              turnID,
			"workspace_id":         wsID,
			"wait_seconds":         1,
			"after_commentary_seq": cursor,
			"wake_on_commentary":   true,
		}, len(wv.Commentaries), wv.Commentaries[0].Seq)

		for _, c := range wv.Commentaries {
			if c.Truncated {
				t.Fatalf("expected complete commentary seq=%d on page %d, got truncated=true", c.Seq, pages)
			}
			collected = append(collected, c)
		}
		if wv.DeliveredCommentarySeq == nil {
			t.Fatalf("page %d missing delivered_commentary_seq", pages)
		}
		if *wv.DeliveredCommentarySeq != wv.Commentaries[len(wv.Commentaries)-1].Seq {
			t.Fatalf("page %d delivered_commentary_seq=%d != last delivered seq=%d", pages, *wv.DeliveredCommentarySeq, wv.Commentaries[len(wv.Commentaries)-1].Seq)
		}
		if !wv.CommentariesTruncated {
			break
		}
		if *wv.DeliveredCommentarySeq <= cursor {
			t.Fatalf("page %d failed to advance delivered_commentary_seq (%d <= %d)", pages, *wv.DeliveredCommentarySeq, cursor)
		}
		if !strings.Contains(wv.NextStep, fmt.Sprintf("after_commentary_seq=%d", *wv.DeliveredCommentarySeq)) {
			t.Fatalf("page %d next_step missing resume cursor %d: %q", pages, *wv.DeliveredCommentarySeq, wv.NextStep)
		}
		cursor = *wv.DeliveredCommentarySeq
	}
	if len(collected) != len(wantTexts) {
		t.Fatalf("reconstructed %d commentaries, want %d", len(collected), len(wantTexts))
	}
	for i, want := range wantTexts {
		wantSeq := firstWantSeq + int64(i)
		if collected[i].Seq != wantSeq {
			t.Fatalf("commentary[%d] seq=%d, want %d", i, collected[i].Seq, wantSeq)
		}
		if collected[i].Text != want {
			t.Fatalf("commentary seq=%d text mismatch (got %d bytes, want %d bytes)", wantSeq, len(collected[i].Text), len(want))
		}
	}
	return pages, firstView
}

func TestPublicWorkersWaitCompletedHTMLCommentaryResumeAndMCPBudget(t *testing.T) {
	// Reproduces the exact 17-HTML-commentary + full-HTML-answer completed turn
	// scenario plus explicit 0, explicit 1, last 10 (after=7), and omitted cursor.
	comments17 := makeHTMLCommentaries(17, 20) // ~1.6 KiB raw HTML each -> >40 KiB JSON total
	finalHTML := makeHTMLFinalAnswer(240)      // ~28 KiB raw HTML -> >36 KiB JSON alone
	allEmitted := make(chan struct{})
	releaseFinish := make(chan struct{})

	adapter := &stubWorkerAdapter{
		startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
			ch := make(chan workers.TurnOutcome, 1)
			go func() {
				for i, txt := range comments17 {
					it := workers.NativeItem{ItemID: fmt.Sprintf("c17_%02d", i+1), Type: "agentMessage", Phase: "commentary", Text: txt}
					onEvent(workers.AdapterEvent{Kind: "commentary", ItemID: it.ItemID, Phase: "commentary", Summary: fmt.Sprintf("review %d", i+1), Item: &it})
				}
				close(allEmitted)
				<-releaseFinish
				ch <- workers.TurnOutcome{
					State:        workers.TurnCompleted,
					NativeTurnID: "nt_html_17",
					Items:        []workers.NativeItem{{ItemID: "final_html", Type: "agentMessage", Phase: "final_answer", Text: finalHTML}},
				}
			}()
			return "nt_html_17", ch, nil
		},
	}

	n, ws, tok := setupM1WorkerNode(t, adapter)
	a := n.app
	cs, closeMCP := openTestWaitMCPSession(t, a, tok)
	defer closeMCP()

	w := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "html-17-worker", AccessMode: harnesses.AccessReadOnly,
	}).Data.(workers.Worker)
	turn := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{
		WorkerID: w.ID, WorkspaceID: ws.ID, Prompt: "review html template",
	}).Data.(workers.Turn)
	<-allEmitted

	// 1. While running, explicit 0 succeeds and reconstructs all 17 commentaries via MCP + Registry.
	paginateCommentariesFromCursor(t, a, cs, tok, ws.ID, turn.ID, 0, comments17, 1)

	// Finish turn and wait until completed.
	close(releaseFinish)
	omittedResp := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{
		TurnID: turn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5),
	})
	if omittedResp.Error != nil {
		t.Fatalf("completed omitted workers_wait failed: %+v", omittedResp.Error)
	}
	omittedView := omittedResp.Data.(workerWaitView)
	if omittedView.State != workers.TurnCompleted {
		t.Fatalf("expected completed state, got %s", omittedView.State)
	}
	if len(omittedView.Commentaries) != 2 || omittedView.Commentaries[0].Seq != 16 || omittedView.Commentaries[1].Seq != 17 {
		t.Fatalf("omitted cursor expected tail [16, 17], got %+v", omittedView.Commentaries)
	}
	assertMCPWaitPageUnclipped(t, cs, map[string]any{"turn_id": turn.ID, "workspace_id": ws.ID, "wait_seconds": 5}, 2, 16)
	assertWaitViewReconstructsAnswer(t, a, tok, ws.ID, omittedView, finalHTML)

	// 2. Completed + explicit 0: must never fail response_too_large or clip MCP text;
	// reconstructs all 17 commentaries AND full final HTML answer via workers_result.
	_, firstPage := paginateCommentariesFromCursor(t, a, cs, tok, ws.ID, turn.ID, 0, comments17, 1)
	assertWaitViewReconstructsAnswer(t, a, tok, ws.ID, firstPage, finalHTML)

	// 3. Completed + explicit 1: reconstructs [2..17] cleanly in Registry & MCP.
	paginateCommentariesFromCursor(t, a, cs, tok, ws.ID, turn.ID, 1, comments17[1:], 2)

	// 4. Completed + explicit 7 (last 10): previously 55,692 bytes & clipped MCP to 0 commentaries;
	// now fits cleanly in both Registry and MCP text without text_truncated.
	paginateCommentariesFromCursor(t, a, cs, tok, ws.ID, turn.ID, 7, comments17[7:], 8)

	// 5. Completed + verbose=true + omitted cursor still stays inside 46 KiB and reconstructs full HTML.
	verboseResp := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{
		TurnID: turn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5), Verbose: true,
	})
	if verboseResp.Error != nil {
		t.Fatalf("completed verbose workers_wait failed: %+v", verboseResp.Error)
	}
	verboseView := verboseResp.Data.(workerWaitView)
	if raw, _ := json.Marshal(verboseView); len(raw) > workerWaitMaxPayloadJSONBytes {
		t.Fatalf("verbose workers_wait exceeded budget: %d > %d", len(raw), workerWaitMaxPayloadJSONBytes)
	}
	assertWaitViewReconstructsAnswer(t, a, tok, ws.ID, verboseView, finalHTML)
}

func TestPublicWorkersWaitMultiPageCommentaryPaginationAndDenseHTMLAnswer(t *testing.T) {
	// 20 dense HTML commentaries (~1.95 KiB raw each -> ~60+ KiB JSON) strictly require
	// multiple workers_wait pages even on their own, both while running and after completion.
	comments20 := makeHTMLCommentaries(20, 24)
	denseHTMLAnswer := strings.Repeat(`<section data-a="1">&lt;x&gt;&amp;中文</section>`, 700) // >32 KiB raw, >50 KiB JSON for 24 KiB prefix
	allEmitted := make(chan struct{})
	releaseFinish := make(chan struct{})

	adapter := &stubWorkerAdapter{
		startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
			ch := make(chan workers.TurnOutcome, 1)
			go func() {
				for i, txt := range comments20 {
					it := workers.NativeItem{ItemID: fmt.Sprintf("c20_%02d", i+1), Type: "agentMessage", Phase: "commentary", Text: txt}
					onEvent(workers.AdapterEvent{Kind: "commentary", ItemID: it.ItemID, Phase: "commentary", Summary: fmt.Sprintf("chunk %d", i+1), Item: &it})
				}
				close(allEmitted)
				<-releaseFinish
				ch <- workers.TurnOutcome{
					State:        workers.TurnCompleted,
					NativeTurnID: "nt_html_20",
					Items:        []workers.NativeItem{{ItemID: "final_dense", Type: "agentMessage", Phase: "final_answer", Text: denseHTMLAnswer}},
				}
			}()
			return "nt_html_20", ch, nil
		},
	}

	n, ws, tok := setupM1WorkerNode(t, adapter)
	a := n.app
	cs, closeMCP := openTestWaitMCPSession(t, a, tok)
	defer closeMCP()

	w := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "html-20-worker", AccessMode: harnesses.AccessReadOnly,
	}).Data.(workers.Worker)
	turn := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{
		WorkerID: w.ID, WorkspaceID: ws.ID, Prompt: "deep html audit",
	}).Data.(workers.Turn)
	<-allEmitted

	// While running: must paginate across >= 2 pages and reconstruct all 20 commentaries intact.
	runningPages, _ := paginateCommentariesFromCursor(t, a, cs, tok, ws.ID, turn.ID, 0, comments20, 1)
	if runningPages < 2 {
		t.Fatalf("expected >=2 commentary pages while running, got %d", runningPages)
	}

	// After completion: must paginate across >= 2 pages, keep MCP unclipped on every page,
	// and seamlessly reconstruct the dense HTML final answer via workers_result.
	close(releaseFinish)
	omittedResp := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{
		TurnID: turn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5),
	})
	if omittedResp.Error != nil {
		t.Fatalf("completed dense HTML omitted wait failed: %+v", omittedResp.Error)
	}
	omittedView := omittedResp.Data.(workerWaitView)
	if omittedView.State != workers.TurnCompleted {
		t.Fatalf("expected completed, got %s", omittedView.State)
	}
	if raw, _ := json.Marshal(omittedView); len(raw) > workerWaitMaxPayloadJSONBytes {
		t.Fatalf("omitted dense HTML wait exceeded budget: %d > %d", len(raw), workerWaitMaxPayloadJSONBytes)
	}
	assertMCPWaitPageUnclipped(t, cs, map[string]any{"turn_id": turn.ID, "workspace_id": ws.ID, "wait_seconds": 5}, 2, 19)
	assertWaitViewReconstructsAnswer(t, a, tok, ws.ID, omittedView, denseHTMLAnswer)

	donePages, firstDonePage := paginateCommentariesFromCursor(t, a, cs, tok, ws.ID, turn.ID, 0, comments20, 1)
	if donePages < 2 {
		t.Fatalf("expected >=2 commentary pages after completion, got %d", donePages)
	}
	assertWaitViewReconstructsAnswer(t, a, tok, ws.ID, firstDonePage, denseHTMLAnswer)
}

func TestPublicWorkersWaitPreservesLargeSingleTurnApprovalWhilePaginatingCommentaries(t *testing.T) {
	largeCommand := "gofmt -w " + strings.Repeat("internal/workers/file_example_path.go ", 620) // ~23.5 KiB
	comments12 := makeHTMLCommentaries(12, 20)
	ready := make(chan struct{})
	release := make(chan struct{})

	adapter := &stubWorkerAdapter{
		startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
			ch := make(chan workers.TurnOutcome, 1)
			go func() {
				for i, txt := range comments12 {
					it := workers.NativeItem{ItemID: fmt.Sprintf("ap_c_%02d", i+1), Type: "agentMessage", Phase: "commentary", Text: txt}
					onEvent(workers.AdapterEvent{Kind: "commentary", ItemID: it.ItemID, Phase: "commentary", Summary: "checking", Item: &it})
				}
				go func() {
					_, _ = spec.OnInteraction(context.Background(), workers.InteractionRequest{
						Kind:    workers.InteractionApproval,
						Summary: "Run gofmt across repository",
						Command: largeCommand,
					})
				}()
				close(ready)
				<-release
				ch <- workers.TurnOutcome{State: workers.TurnCancelled, NativeTurnID: "nt_ap_c"}
			}()
			return "nt_ap_c", ch, nil
		},
	}

	n, ws, tok := setupM1WorkerNode(t, adapter)
	defer close(release)
	a := n.app
	tr := true
	if _, err := a.Harnesses.UpdatePolicy(harnesses.UpdatePolicyInput{HarnessID: harnesses.HarnessCodex, AllowWorkspaceWrite: &tr}); err != nil {
		t.Fatal(err)
	}
	cs, closeMCP := openTestWaitMCPSession(t, a, tok)
	defer closeMCP()

	cr := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "approval-comment-worker",
		AccessMode: harnesses.AccessWorkspaceWrite, ApprovalMode: workers.ApprovalModeAskHuman,
	})
	if cr.Error != nil {
		t.Fatalf("workers_create: %+v", cr.Error)
	}
	w := cr.Data.(workers.Worker)
	sr := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{
		WorkerID: w.ID, WorkspaceID: ws.ID, Prompt: "format repo",
	})
	if sr.Error != nil {
		t.Fatalf("workers_send: %+v", sr.Error)
	}
	turn := sr.Data.(workers.Turn)
	<-ready

	deadline := time.Now().Add(5 * time.Second)
	for {
		wResp := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{
			TurnID: turn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(1),
		})
		if wResp.Error == nil && wResp.Data.(workerWaitView).State == workers.TurnWaitingApproval {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("turn did not enter waiting_approval in time")
		}
		time.Sleep(25 * time.Millisecond)
	}

	pages, firstView := paginateCommentariesFromCursor(t, a, cs, tok, ws.ID, turn.ID, 0, comments12, 1)
	if pages < 2 {
		t.Fatalf("expected commentaries to paginate cleanly alongside 23 KiB approval command, got %d page(s)", pages)
	}
	if firstView.PendingInteraction == nil || firstView.PendingInteraction.Command != largeCommand || firstView.PendingInteraction.DetailsTruncated {
		t.Fatalf("single-turn workers_wait must preserve full 23 KiB approval command without truncation")
	}
}

func TestPublicWorkersWaitHugeApprovalCompactsViewToAdvanceCommentaryCursor(t *testing.T) {
	// Reproduces a ~32 KiB HTML approval command (~44 KiB JSON) that fits alone
	// inside 46 KiB but leaves too little space for a 1,992-byte HTML commentary.
	commentary1 := strings.Repeat(`<div class="audit" data-item="1">&lt;check&gt; &amp; ok</div>`, 32) + " TAIL_COMMENTARY_1_INTACT"
	hugeHTMLCmd := "write-templates --html " + strings.Repeat(`<section class="tpl"><h1>&lt;Title&gt; &amp; Body</h1><p data-v="1">content</p></section>`, 263)
	ready := make(chan struct{})
	release := make(chan struct{})

	adapter := &stubWorkerAdapter{
		startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
			ch := make(chan workers.TurnOutcome, 1)
			go func() {
				it := workers.NativeItem{ItemID: "huge_ap_c1", Type: "agentMessage", Phase: "commentary", Text: commentary1}
				onEvent(workers.AdapterEvent{Kind: "commentary", ItemID: it.ItemID, Phase: "commentary", Summary: "template audit", Item: &it})
				go func() {
					_, _ = spec.OnInteraction(context.Background(), workers.InteractionRequest{
						Kind:    workers.InteractionApproval,
						Summary: "Write two large HTML templates",
						Command: hugeHTMLCmd,
					})
				}()
				close(ready)
				<-release
				ch <- workers.TurnOutcome{State: workers.TurnCancelled, NativeTurnID: "nt_huge_ap"}
			}()
			return "nt_huge_ap", ch, nil
		},
	}

	n, ws, tok := setupM1WorkerNode(t, adapter)
	defer close(release)
	a := n.app
	tr := true
	if _, err := a.Harnesses.UpdatePolicy(harnesses.UpdatePolicyInput{HarnessID: harnesses.HarnessCodex, AllowWorkspaceWrite: &tr}); err != nil {
		t.Fatal(err)
	}
	cs, closeMCP := openTestWaitMCPSession(t, a, tok)
	defer closeMCP()

	cr := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "huge-approval-worker",
		AccessMode: harnesses.AccessWorkspaceWrite, ApprovalMode: workers.ApprovalModeAskHuman,
	})
	if cr.Error != nil {
		t.Fatalf("workers_create: %+v", cr.Error)
	}
	w := cr.Data.(workers.Worker)
	sr := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{
		WorkerID: w.ID, WorkspaceID: ws.ID, Prompt: "update templates",
	})
	if sr.Error != nil {
		t.Fatalf("workers_send: %+v", sr.Error)
	}
	turn := sr.Data.(workers.Turn)
	<-ready

	deadline := time.Now().Add(5 * time.Second)
	for {
		if pending := a.Workers.ListHumanInteractions(); len(pending) == 1 && pending[0].TurnID == turn.ID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending approval interaction did not appear in time")
		}
		time.Sleep(15 * time.Millisecond)
	}

	// Explicit cursor 0 must advance delivered_commentary_seq to 1 and deliver full commentary #1
	// immediately instead of stalling at delivered=0 with a truncated prefix.
	start := time.Now()
	resp := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{
		TurnID: turn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(30),
		AfterCommentarySeq: automation.Int64(0), WakeOnCommentary: true,
	})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("expected immediate wake for unread commentary during waiting_approval, took %v", elapsed)
	}
	if resp.Error != nil {
		t.Fatalf("workers_wait(after_commentary_seq=0) failed: %+v", resp.Error)
	}
	wv := resp.Data.(workerWaitView)
	if len(wv.Commentaries) != 1 || wv.Commentaries[0].Seq != 1 || wv.Commentaries[0].Truncated || wv.Commentaries[0].Text != commentary1 {
		t.Fatalf("expected full commentary #1 delivered without truncation, got %+v", wv.Commentaries)
	}
	if wv.CommentariesTruncated || wv.DeliveredCommentarySeq == nil || *wv.DeliveredCommentarySeq != 1 {
		t.Fatalf("expected delivered_commentary_seq=1 and commentaries_truncated=false, got delivered=%v trunc=%v", wv.DeliveredCommentarySeq, wv.CommentariesTruncated)
	}
	if wv.PendingInteraction == nil || wv.PendingInteraction.RequestID == "" || !wv.PendingInteraction.DetailsTruncated || wv.PendingInteraction.InspectHint == "" {
		t.Fatalf("expected compacted pending_interaction with request_id and inspect_hint, got %+v", wv.PendingInteraction)
	}
	assertMCPWaitPageUnclipped(t, cs, map[string]any{
		"turn_id": turn.ID, "workspace_id": ws.ID, "wait_seconds": 30,
		"after_commentary_seq": 0, "wake_on_commentary": true,
	}, 1, 1)

	// Human approval queue must keep the complete command untouched.
	pending := a.Workers.ListHumanInteractions()
	if len(pending) != 1 || pending[0].Command != hugeHTMLCmd || pending[0].DetailsTruncated {
		t.Fatalf("human approval UI must keep full command untouched, got len=%d truncated=%v", len(pending), len(pending) > 0 && pending[0].DetailsTruncated)
	}

	// Once commentaries are caught up (after_commentary_seq=1), workers_wait returns the full command again.
	fullApprovalResp := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{
		TurnID: turn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(1),
		AfterCommentarySeq: automation.Int64(1),
	})
	if fullApprovalResp.Error != nil {
		t.Fatalf("workers_wait(after_commentary_seq=1) failed: %+v", fullApprovalResp.Error)
	}
	fullApprovalView := fullApprovalResp.Data.(workerWaitView)
	if fullApprovalView.PendingInteraction == nil || fullApprovalView.PendingInteraction.Command != hugeHTMLCmd || fullApprovalView.PendingInteraction.DetailsTruncated {
		t.Fatalf("expected full command once no commentaries compete for budget, got %+v", fullApprovalView.PendingInteraction)
	}
}

func parseNextStepWaitResume(t *testing.T, hint string) (int64, bool) {
	t.Helper()
	const key = "after_commentary_seq="
	idx := strings.Index(hint, key)
	if idx < 0 {
		t.Fatalf("next_step missing %q: %q", key, hint)
	}
	rest := hint[idx+len(key):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	seq, err := strconv.ParseInt(rest[:end], 10, 64)
	if err != nil {
		t.Fatalf("parse after_commentary_seq from next_step %q: %v", hint, err)
	}
	return seq, strings.Contains(hint, "wake_on_commentary=true")
}

func TestPublicWorkersWaitRunningNextStepPaginatesImmediately(t *testing.T) {
	comments20 := makeHTMLCommentaries(20, 24)
	allEmitted := make(chan struct{})
	release := make(chan struct{})

	adapter := &stubWorkerAdapter{
		startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
			ch := make(chan workers.TurnOutcome, 1)
			go func() {
				for i, txt := range comments20 {
					it := workers.NativeItem{ItemID: fmt.Sprintf("run20_%02d", i+1), Type: "agentMessage", Phase: "commentary", Text: txt}
					onEvent(workers.AdapterEvent{Kind: "commentary", ItemID: it.ItemID, Phase: "commentary", Summary: fmt.Sprintf("step %d", i+1), Item: &it})
				}
				close(allEmitted)
				<-release
				ch <- workers.TurnOutcome{State: workers.TurnCancelled, NativeTurnID: "nt_run20"}
			}()
			return "nt_run20", ch, nil
		},
	}

	n, ws, tok := setupM1WorkerNode(t, adapter)
	defer close(release)
	a := n.app
	cs, closeMCP := openTestWaitMCPSession(t, a, tok)
	defer closeMCP()

	w := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "running-next-step-worker", AccessMode: harnesses.AccessReadOnly,
	}).Data.(workers.Worker)
	turn := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{
		WorkerID: w.ID, WorkspaceID: ws.ID, Prompt: "stream 20 commentaries",
	}).Data.(workers.Turn)
	<-allEmitted

	// Page 1 returns a paginated slice with a running next_step that includes wake_on_commentary=true.
	p1Resp := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{
		TurnID: turn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(30),
		AfterCommentarySeq: automation.Int64(0), WakeOnCommentary: true,
	})
	if p1Resp.Error != nil {
		t.Fatalf("page 1 failed: %+v", p1Resp.Error)
	}
	p1 := p1Resp.Data.(workerWaitView)
	if !p1.CommentariesTruncated || p1.DeliveredCommentarySeq == nil || *p1.DeliveredCommentarySeq >= 20 {
		t.Fatalf("expected paginated page 1, got trunc=%v delivered=%v", p1.CommentariesTruncated, p1.DeliveredCommentarySeq)
	}
	nextSeq, wake := parseNextStepWaitResume(t, p1.NextStep)
	if nextSeq != *p1.DeliveredCommentarySeq || !wake {
		t.Fatalf("running next_step must advertise after_commentary_seq=%d and wake_on_commentary=true, got seq=%d wake=%v hint=%q", *p1.DeliveredCommentarySeq, nextSeq, wake, p1.NextStep)
	}

	// Following next_step verbatim with a full 30s wait_seconds must return buffered remaining
	// commentaries immediately (milliseconds, not 30 seconds) in both Registry and MCP.
	start := time.Now()
	p2Resp := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{
		TurnID: turn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(30),
		AfterCommentarySeq: automation.Int64(nextSeq), WakeOnCommentary: wake,
	})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("following running next_step waited %v instead of waking immediately", elapsed)
	}
	if p2Resp.Error != nil {
		t.Fatalf("page 2 failed: %+v", p2Resp.Error)
	}
	p2 := p2Resp.Data.(workerWaitView)
	if p2.CommentariesTruncated || p2.DeliveredCommentarySeq == nil || *p2.DeliveredCommentarySeq != 20 {
		t.Fatalf("expected final page 2 delivering through 20, got trunc=%v delivered=%v", p2.CommentariesTruncated, p2.DeliveredCommentarySeq)
	}
	all := append(append([]workers.CommentaryEntry(nil), p1.Commentaries...), p2.Commentaries...)
	if len(all) != 20 {
		t.Fatalf("expected 20 total commentaries across pages, got %d", len(all))
	}
	for i, want := range comments20 {
		if all[i].Seq != int64(i+1) || all[i].Text != want {
			t.Fatalf("commentary[%d] mismatch: seq=%d", i, all[i].Seq)
		}
	}
	assertMCPWaitPageUnclipped(t, cs, map[string]any{
		"turn_id": turn.ID, "workspace_id": ws.ID, "wait_seconds": 30,
		"after_commentary_seq": nextSeq, "wake_on_commentary": wake,
	}, len(p2.Commentaries), p2.Commentaries[0].Seq)
}

func makeMCPHTMLTableReport(rows int) string {
	var b strings.Builder
	b.WriteString("<table><thead><tr><th>File</th><th>Check</th><th>Detail &amp; Snippet</th></tr></thead><tbody>\n")
	for i := 0; i < rows; i++ {
		fmt.Fprintf(&b, "<tr><td>pkg/file_%03d.go</td><td>&lt;rule-%03d&gt; 核查</td><td><code>x &lt; y &amp;&amp; z &gt; %d</code></td></tr>\n", i, i, i)
	}
	b.WriteString("</tbody></table>")
	return strings.TrimSpace(b.String())
}

func readAllResultViaMCPTextOnly(t *testing.T, cs *mcp.ClientSession, turnID, wsID, startCursor string, maxBytes int) string {
	t.Helper()
	var out strings.Builder
	cursor := startCursor
	for page := 1; page <= 30; page++ {
		args := map[string]any{"turn_id": turnID, "workspace_id": wsID}
		if cursor != "" {
			args["cursor"] = cursor
		}
		if maxBytes > 0 {
			args["max_bytes"] = maxBytes
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "workers_result", Arguments: args})
		cancel()
		if err != nil || res.IsError || len(res.Content) == 0 {
			t.Fatalf("MCP workers_result(page=%d cursor=%q) failed: res=%+v err=%v", page, cursor, res, err)
		}
		tc, ok := res.Content[0].(*mcp.TextContent)
		if !ok {
			t.Fatalf("expected TextContent, got %T", res.Content[0])
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(tc.Text), &payload); err != nil {
			t.Fatalf("unmarshal MCP workers_result text: %v", err)
		}
		if trunc, _ := payload["text_truncated"].(bool); trunc {
			t.Fatalf("MCP workers_result(page=%d cursor=%q) triggered text_truncated: %s", page, cursor, tc.Text[:min(400, len(tc.Text))])
		}
		dataMap, _ := payload["data"].(map[string]any)
		txt, _ := dataMap["text"].(string)
		if txt == "" {
			t.Fatalf("MCP workers_result(page=%d cursor=%q) delivered 0 text bytes", page, cursor)
		}
		out.WriteString(txt)
		complete, _ := dataMap["complete"].(bool)
		nextCur, _ := dataMap["next_cursor"].(string)
		if complete {
			if nextCur != "" {
				t.Fatalf("complete page returned non-empty next_cursor=%q", nextCur)
			}
			return out.String()
		}
		if nextCur == "" || nextCur == cursor {
			t.Fatalf("incomplete page %d did not advance next_cursor (cur=%q next=%q)", page, cursor, nextCur)
		}
		cursor = nextCur
	}
	t.Fatalf("MCP workers_result pagination exceeded 30 pages (cursor=%q)", cursor)
	return ""
}

func TestMCPTextOnlyClientReconstructsHTMLTableReportFromWaitAndResult(t *testing.T) {
	// ~58 KiB HTML table report where default 24 KiB raw chunks expand > 55 KiB in JSON.
	htmlReport := makeMCPHTMLTableReport(520)
	if len(htmlReport) < 55000 {
		t.Fatalf("fixture too small: %d bytes", len(htmlReport))
	}
	adapter := &stubWorkerAdapter{
		startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
			ch := make(chan workers.TurnOutcome, 1)
			ch <- workers.TurnOutcome{
				State:        workers.TurnCompleted,
				NativeTurnID: "nt_html_tbl",
				Items:        []workers.NativeItem{{ItemID: "f_tbl", Type: "agentMessage", Phase: "final_answer", Text: htmlReport}},
			}
			return "nt_html_tbl", ch, nil
		},
	}

	n, ws, tok := setupM1WorkerNode(t, adapter)
	a := n.app
	cs, closeMCP := openTestWaitMCPSession(t, a, tok)
	defer closeMCP()

	w := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "html-table-worker", AccessMode: harnesses.AccessReadOnly,
	}).Data.(workers.Worker)
	turn := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{
		WorkerID: w.ID, WorkspaceID: ws.ID, Prompt: "generate html table report",
	}).Data.(workers.Turn)

	// 1. Follow workers_wait.next_cursor strictly via MCP content[].text.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	waitRes, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "workers_wait",
		Arguments: map[string]any{"turn_id": turn.ID, "workspace_id": ws.ID, "wait_seconds": 5},
	})
	cancel()
	if err != nil || waitRes.IsError || len(waitRes.Content) == 0 {
		t.Fatalf("MCP workers_wait failed: res=%+v err=%v", waitRes, err)
	}
	var waitPayload map[string]any
	if err := json.Unmarshal([]byte(waitRes.Content[0].(*mcp.TextContent).Text), &waitPayload); err != nil {
		t.Fatalf("unmarshal MCP workers_wait text: %v", err)
	}
	if trunc, _ := waitPayload["text_truncated"].(bool); trunc {
		t.Fatalf("MCP workers_wait triggered text_truncated")
	}
	waitData, _ := waitPayload["data"].(map[string]any)
	waitAnswer, _ := waitData["answer"].(string)
	waitCursor, _ := waitData["next_cursor"].(string)
	if waitAnswer == "" || waitCursor != strconv.Itoa(len(waitAnswer)) {
		t.Fatalf("unexpected MCP workers_wait preview/cursor: len=%d cursor=%q", len(waitAnswer), waitCursor)
	}
	rest := readAllResultViaMCPTextOnly(t, cs, turn.ID, ws.ID, waitCursor, 0)
	if waitAnswer+rest != htmlReport {
		t.Fatalf("MCP text-only wait+result reconstruction mismatch: got %d bytes, want %d bytes", len(waitAnswer)+len(rest), len(htmlReport))
	}

	// 2. Also verify reading from cursor="0" with default max_bytes and max_bytes=32768 via MCP text only.
	if fromStart := readAllResultViaMCPTextOnly(t, cs, turn.ID, ws.ID, "0", 0); fromStart != htmlReport {
		t.Fatalf("MCP text-only fromStart(default) mismatch: got %d bytes, want %d bytes", len(fromStart), len(htmlReport))
	}
	if fromStart32K := readAllResultViaMCPTextOnly(t, cs, turn.ID, ws.ID, "0", 32<<10); fromStart32K != htmlReport {
		t.Fatalf("MCP text-only fromStart(32KiB) mismatch: got %d bytes, want %d bytes", len(fromStart32K), len(htmlReport))
	}
}
