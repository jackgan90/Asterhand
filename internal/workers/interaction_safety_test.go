package workers

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/harnesses"
)

// Secret input questions must escalate directly to human UI, forbid caller
// workers_reply, and redact the answer from persisted turn_logs.
func TestSecretQuestionImmediateHumanRedactedLogAndCallerForbidden(t *testing.T) {
	const secretVal = "sk-super-secret-token-987654"
	var delivered string
	var mu sync.Mutex

	adapter := &interactiveFakeAdapter{
		harnessID: harnesses.HarnessCodex,
		run: func(ctx context.Context, spec TurnStartSpec, onEvent func(AdapterEvent)) TurnOutcome {
			dec, err := spec.OnInteraction(ctx, InteractionRequest{
				Kind:     InteractionInput,
				ToolName: "requestUserInput",
				Summary:  "Enter API key",
				Questions: []InteractionQuestion{{
					ID:       "api_key",
					Question: "Enter API key",
					IsSecret: true,
				}},
			})
			if err != nil {
				return TurnOutcome{State: TurnFailed, ErrorMessage: err.Error()}
			}
			mu.Lock()
			if vals := dec.Answers["api_key"]; len(vals) > 0 {
				delivered = vals[0]
			} else {
				delivered = dec.Text
			}
			mu.Unlock()
			return TurnOutcome{
				State: TurnCompleted,
				Items: []NativeItem{{ItemID: "f1", Type: "agentMessage", Phase: "final_answer", Text: "configured"}},
			}
		},
	}

	svc, ws := setupInteractiveTestService(t, harnesses.HarnessCodex, adapter)
	w, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_sec_c1", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws,
		AccessMode: harnesses.AccessWorkspaceWrite, ApprovalMode: ApprovalModeAskHuman,
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_sec_s1", WorkerID: w.ID, Prompt: "setup secret",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Must appear immediately in ListHumanInteractions (skipping caller grace).
	var reqView InteractionView
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if items := svc.ListHumanInteractions(); len(items) > 0 {
			reqView = items[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if reqView.RequestID == "" || !reqView.VisibleToHuman || reqView.EscalateAt != nil {
		t.Fatalf("expected secret input immediately visible to human without grace timer, got %+v", reqView)
	}

	// Caller A must be forbidden from answering a secret question via ReplyTurn.
	if _, _, err := svc.ReplyTurn(ReplyInput{TurnID: turn.ID, Text: secretVal}); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("expected forbidden when caller tries to reply to secret question, got %v", err)
	}

	// Human submits the secret answer.
	if _, err := svc.DecideInteractionHuman(reqView.RequestID, InteractionDecision{
		Decision: DecisionAnswer,
		Answers:  map[string][]string{"api_key": {secretVal}},
	}); err != nil {
		t.Fatalf("DecideInteractionHuman failed: %v", err)
	}

	doneTurn, _, _, err := svc.Wait(context.Background(), turn.ID, "", 3*time.Second)
	if err != nil || doneTurn.State != TurnCompleted {
		t.Fatalf("expected completed turn, got %s err=%v", doneTurn.State, err)
	}
	mu.Lock()
	gotSecret := delivered
	mu.Unlock()
	if gotSecret != secretVal {
		t.Fatalf("expected full secret delivered to adapter, got %q", gotSecret)
	}

	// Persisted turn_logs must NEVER contain the secret value.
	_, entries, _, err := svc.ReadLogs(turn.ID, "", 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	foundRedacted := false
	for _, e := range entries {
		if strings.Contains(e.Summary, secretVal) || strings.Contains(e.Summary, "sk-super") {
			t.Fatalf("secret leaked into persisted log entry: %+v", e)
		}
		if e.Kind == "interaction_resolved" && strings.Contains(e.Summary, "secret redacted") {
			foundRedacted = true
		}
	}
	if !foundRedacted {
		t.Fatalf("expected redacted interaction_resolved log entry, got %+v", entries)
	}
}

// Codex never receives acceptForSession or scope:"session", and DeepSeek
// selects options strictly by ACP kind without escalating to allow_always.
func TestCodexAndDeepSeekApprovalScopeNeverExceedsSingleGrant(t *testing.T) {
	// Codex commandExecution / fileChange allow_session -> "accept" (not acceptForSession).
	for _, method := range []string{
		"item/commandExecution/requestApproval",
		"item/fileChange/requestApproval",
		"execCommandApproval",
		"applyPatchApproval",
	} {
		res, rpcErr, interrupt := buildCodexInteractionReply(method, nil, InteractionRequest{Kind: InteractionApproval}, InteractionDecision{Decision: DecisionAllowSession})
		if rpcErr != nil || interrupt {
			t.Fatalf("%s unexpected err=%v interrupt=%v", method, rpcErr, interrupt)
		}
		m, _ := res.(map[string]any)
		if m["decision"] != "accept" {
			t.Fatalf("%s expected single-request decision=accept for allow_session, got %+v", method, m)
		}
	}

	// Codex item/permissions/requestApproval allow_session -> scope:"turn" (not "session").
	permParams := json.RawMessage(`{"permissions":{"network":{"enabled":true}}}`)
	res, rpcErr, interrupt := buildCodexInteractionReply("item/permissions/requestApproval", permParams, InteractionRequest{Kind: InteractionApproval}, InteractionDecision{Decision: DecisionAllowSession})
	if rpcErr != nil || interrupt {
		t.Fatalf("permissions unexpected err=%v interrupt=%v", rpcErr, interrupt)
	}
	pm, _ := res.(map[string]any)
	if pm["scope"] != "turn" {
		t.Fatalf("permissions expected scope=turn for allow_session, got %+v", pm)
	}

	// DeepSeek with opaque optionIds where allow_always comes FIRST: must pick kind=="allow_once".
	rawOpaque := json.RawMessage(`{
		"toolCall":{"toolCallId":"tc_opaque","title":"pwsh: echo hi"},
		"options":[
			{"optionId":"opt_a","kind":"allow_always","name":"Always"},
			{"optionId":"opt_b","kind":"allow_once","name":"Once"},
			{"optionId":"opt_c","kind":"reject_always","name":"Never"},
			{"optionId":"opt_d","kind":"reject_once","name":"Reject once"}
		]
	}`)
	_, opts := parseDeepSeekPermissionRequest(rawOpaque, nil, "", "")
	for _, dec := range []string{DecisionAllowOnce, DecisionAllowSession} {
		resp, shouldCancel := buildDeepSeekPermissionReply(opts, InteractionDecision{Decision: dec})
		if shouldCancel {
			t.Fatalf("decision %s must not cancel turn", dec)
		}
		outcome, _ := resp["outcome"].(map[string]any)
		if outcome["outcome"] != "selected" || outcome["optionId"] != "opt_b" {
			t.Fatalf("decision %s expected opt_b (allow_once), got %+v", dec, resp)
		}
	}
	denyResp, shouldCancel := buildDeepSeekPermissionReply(opts, InteractionDecision{Decision: DecisionDeny})
	if shouldCancel {
		t.Fatalf("deny must not cancel turn")
	}
	if outcome, _ := denyResp["outcome"].(map[string]any); outcome["optionId"] != "opt_d" {
		t.Fatalf("deny expected opt_d (reject_once), got %+v", denyResp)
	}

	// DeepSeek offering ONLY allow_always: allow_once must NEVER silently upgrade to allow_always.
	rawOnlyAlways := json.RawMessage(`{
		"toolCall":{"toolCallId":"tc_always"},
		"options":[{"optionId":"opt_always","kind":"allow_always"}]
	}`)
	_, alwaysOnlyOpts := parseDeepSeekPermissionRequest(rawOnlyAlways, nil, "", "")
	noUpgradeResp, _ := buildDeepSeekPermissionReply(alwaysOnlyOpts, InteractionDecision{Decision: DecisionAllowOnce})
	if outcome, _ := noUpgradeResp["outcome"].(map[string]any); outcome["outcome"] != "cancelled" {
		t.Fatalf("expected cancelled instead of upgrading allow_once to allow_always, got %+v", noUpgradeResp)
	}

	// Revoking session_auto_approve on an idle worker evicts & closes its resident process.
	var closedCount atomic.Int32
	fa := &fakeAdapter{
		closeFn: func(h ThreadHandle) error {
			closedCount.Add(1)
			return nil
		},
	}
	svc, hs, ws := setupTestService(t, fa)
	enableWrite := true
	if _, err := hs.UpdatePolicy(harnesses.UpdatePolicyInput{HarnessID: harnesses.HarnessCodex, AllowWorkspaceWrite: &enableWrite}); err != nil {
		t.Fatal(err)
	}
	w, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_rev_res_1", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath,
		AccessMode: harnesses.AccessWorkspaceWrite, ApprovalMode: ApprovalModeAskHuman,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetSessionAutoApproveHuman(w.ID, true); err != nil {
		t.Fatal(err)
	}
	turn, err := svc.Send(context.Background(), SendInput{OperationID: "op_rev_res_s1", WorkerID: w.ID, Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.Wait(context.Background(), turn.ID, ws.ID, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetSessionAutoApproveHuman(w.ID, false); err != nil {
		t.Fatal(err)
	}
	if closedCount.Load() != 1 {
		t.Fatalf("expected idle resident closed on session_auto_approve revoke, got closeCount=%d", closedCount.Load())
	}
}

// When two questions are pending in the same turn, answering the first
// keeps the turn in waiting_input, exposes the second question, and keeps
// StallTimeout/WallClockTimeout paused until the final question is resolved.
func TestConcurrentTwoQuestionsStayWaitingAndPausedUntilBothResolved(t *testing.T) {
	var ans1, ans2 string
	adapter := &interactiveFakeAdapter{
		harnessID: harnesses.HarnessCodex,
		run: func(ctx context.Context, spec TurnStartSpec, onEvent func(AdapterEvent)) TurnOutcome {
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				d, err := spec.OnInteraction(ctx, InteractionRequest{
					Kind:      InteractionInput,
					ToolName:  "requestUserInput",
					Summary:   "First question",
					Questions: []InteractionQuestion{{ID: "q1", Question: "First question"}},
				})
				if err == nil {
					ans1 = d.Text
				}
			}()
			go func() {
				defer wg.Done()
				// Small stagger so creation order is deterministic.
				time.Sleep(25 * time.Millisecond)
				d, err := spec.OnInteraction(ctx, InteractionRequest{
					Kind:      InteractionInput,
					ToolName:  "requestUserInput",
					Summary:   "Second question",
					Questions: []InteractionQuestion{{ID: "q2", Question: "Second question"}},
				})
				if err == nil {
					ans2 = d.Text
				}
			}()
			wg.Wait()
			return TurnOutcome{
				State: TurnCompleted,
				Items: []NativeItem{{ItemID: "f", Type: "agentMessage", Phase: "final_answer", Text: ans1 + "+" + ans2}},
			}
		},
	}

	svc, ws := setupInteractiveTestService(t, harnesses.HarnessCodex, adapter)
	svc.SetBudgets(Budgets{
		WallClockTimeout:   300 * time.Millisecond,
		StallTimeout:       200 * time.Millisecond,
		InputCallerGrace:   5 * time.Second,
		InteractionTimeout: 5 * time.Second,
	})

	w, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_twoq_c1", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws,
		AccessMode: harnesses.AccessWorkspaceWrite, ApprovalMode: ApprovalModeAskHuman,
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_twoq_s1", WorkerID: w.ID, Prompt: "ask two",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Wait until both questions are pending simultaneously.
	var pending []InteractionView
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pending = svc.ListAllPendingInteractions()
		if len(pending) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(pending) != 2 {
		t.Fatalf("expected 2 concurrent pending interactions, got %d", len(pending))
	}

	// Answer the first question.
	afterFirst, _, err := svc.ReplyTurn(ReplyInput{
		TurnID:    turn.ID,
		RequestID: pending[0].RequestID,
		Text:      "A1",
	})
	if err != nil {
		t.Fatalf("ReplyTurn first question failed: %v", err)
	}
	if afterFirst.State != TurnWaitingInput {
		t.Fatalf("expected turn to remain waiting_input after answering 1 of 2 questions, got %s", afterFirst.State)
	}
	if afterFirst.PendingInteraction == nil || afterFirst.PendingInteraction.RequestID != pending[1].RequestID {
		t.Fatalf("expected pending_interaction to advance to second question %s, got %+v", pending[1].RequestID, afterFirst.PendingInteraction)
	}

	// Sleep longer than StallTimeout (200ms) and WallClockTimeout (300ms):
	// timer MUST remain paused while the second question is still pending.
	time.Sleep(380 * time.Millisecond)
	midTurn, err := svc.GetTurn(turn.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if midTurn.State != TurnWaitingInput || midTurn.PendingInteraction == nil || midTurn.PendingInteraction.RequestID != pending[1].RequestID {
		t.Fatalf("expected turn still waiting_input on second question without timeout, got %+v", midTurn)
	}

	// Answer the second question by explicit request_id -> turn resumes & completes.
	if _, _, err := svc.ReplyTurn(ReplyInput{
		TurnID:    turn.ID,
		RequestID: pending[1].RequestID,
		Text:      "A2",
	}); err != nil {
		t.Fatalf("ReplyTurn second question failed: %v", err)
	}

	doneTurn, preview, _, err := svc.Wait(context.Background(), turn.ID, "", 3*time.Second)
	if err != nil || doneTurn.State != TurnCompleted || preview != "A1+A2" {
		t.Fatalf("expected completed turn with A1+A2, got state=%s preview=%q err=%v", doneTurn.State, preview, err)
	}
}

// Revoking session_auto_approve immediately after human allow_session must
// never be reverted to true by a late-waking native interaction callback.
func TestRevokedSessionAutoApproveNotReenabledByLateCallback(t *testing.T) {
	firstDecisionSeen := make(chan struct{})
	releaseSecondReq := make(chan struct{})
	var secondDecision string
	var mu sync.Mutex

	adapter := &interactiveFakeAdapter{
		harnessID: harnesses.HarnessCodex,
		run: func(ctx context.Context, spec TurnStartSpec, onEvent func(AdapterEvent)) TurnOutcome {
			d1, err := spec.OnInteraction(ctx, InteractionRequest{
				Kind:     InteractionApproval,
				ToolName: "commandExecution",
				Summary:  "First safe command",
				Command:  "echo first",
			})
			if err != nil || d1.Decision != DecisionAllowSession {
				return TurnOutcome{State: TurnFailed, ErrorMessage: "unexpected d1"}
			}
			close(firstDecisionSeen)
			<-releaseSecondReq

			d2, err := spec.OnInteraction(ctx, InteractionRequest{
				Kind:     InteractionApproval,
				ToolName: "commandExecution",
				Summary:  "Second safe command after revoke",
				Command:  "echo second",
			})
			if err != nil {
				return TurnOutcome{State: TurnFailed, ErrorMessage: err.Error()}
			}
			mu.Lock()
			secondDecision = d2.Decision + "/" + d2.Actor
			mu.Unlock()
			return TurnOutcome{
				State: TurnCompleted,
				Items: []NativeItem{{ItemID: "f1", Type: "agentMessage", Phase: "final_answer", Text: "ok"}},
			}
		},
	}

	svc, ws := setupInteractiveTestService(t, harnesses.HarnessCodex, adapter)
	w, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_rev_auto_c1", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws,
		AccessMode: harnesses.AccessWorkspaceWrite, ApprovalMode: ApprovalModeAskHuman,
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_rev_auto_s1", WorkerID: w.ID, Prompt: "run commands",
	})
	if err != nil {
		t.Fatal(err)
	}

	var req1 string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if list := svc.ListHumanInteractions(); len(list) > 0 {
			req1 = list[0].RequestID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if req1 == "" {
		t.Fatal("expected first approval pending")
	}

	// Approve session and immediately revoke before/concurrently with callback waking up.
	if _, err := svc.DecideInteractionHuman(req1, InteractionDecision{Decision: DecisionAllowSession}); err != nil {
		t.Fatal(err)
	}
	revokedW, err := svc.SetSessionAutoApproveHuman(w.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if revokedW.SessionAutoApprove {
		t.Fatal("expected revoked worker SessionAutoApprove=false")
	}

	<-firstDecisionSeen
	// Give callback time to finish any post-decision steps and check persisted state.
	time.Sleep(30 * time.Millisecond)
	gotW, _, err := svc.Get(w.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if gotW.SessionAutoApprove {
		t.Fatal("late interaction callback re-enabled revoked SessionAutoApprove!")
	}

	// Trigger second approval: it MUST prompt human instead of auto-approving.
	close(releaseSecondReq)
	var req2 string
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if list := svc.ListHumanInteractions(); len(list) > 0 {
			req2 = list[0].RequestID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if req2 == "" {
		t.Fatal("expected second approval to prompt human after revoke")
	}
	if _, err := svc.DecideInteractionHuman(req2, InteractionDecision{Decision: DecisionAllowOnce}); err != nil {
		t.Fatal(err)
	}
	doneT, _, _, err := svc.Wait(context.Background(), turn.ID, "", 3*time.Second)
	if err != nil || doneT.State != TurnCompleted {
		t.Fatalf("expected completed turn, got %s err=%v", doneT.State, err)
	}
	mu.Lock()
	gotD2 := secondDecision
	mu.Unlock()
	if gotD2 != "allow_once/human" {
		t.Fatalf("expected second decision from human, got %q", gotD2)
	}
}

// Answering 1 of 2 pending questions keeps waiting progress for Q2 (never
// "resuming..."), and deferring Q2 returns Q2's own RequestID & EscalateAt.
func TestMultiQuestionProgressAndTargetDeferReceipt(t *testing.T) {
	adapter := &interactiveFakeAdapter{
		harnessID: harnesses.HarnessCodex,
		run: func(ctx context.Context, spec TurnStartSpec, onEvent func(AdapterEvent)) TurnOutcome {
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, _ = spec.OnInteraction(ctx, InteractionRequest{
					Kind:      InteractionInput,
					Summary:   "Question One",
					Questions: []InteractionQuestion{{ID: "q1", Question: "Question One"}},
				})
			}()
			go func() {
				defer wg.Done()
				time.Sleep(25 * time.Millisecond)
				_, _ = spec.OnInteraction(ctx, InteractionRequest{
					Kind:      InteractionInput,
					Summary:   "Question Two",
					Questions: []InteractionQuestion{{ID: "q2", Question: "Question Two"}},
				})
			}()
			wg.Wait()
			return TurnOutcome{
				State: TurnCompleted,
				Items: []NativeItem{{ItemID: "f1", Type: "agentMessage", Phase: "final_answer", Text: "ok"}},
			}
		},
	}

	svc, ws := setupInteractiveTestService(t, harnesses.HarnessCodex, adapter)
	svc.SetBudgets(Budgets{InputCallerGrace: 15 * time.Second})
	w, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_multiq_c1", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws,
		AccessMode: harnesses.AccessWorkspaceWrite, ApprovalMode: ApprovalModeAskHuman,
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_multiq_s1", WorkerID: w.ID, Prompt: "two questions",
	})
	if err != nil {
		t.Fatal(err)
	}

	var pending []InteractionView
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pending = svc.ListAllPendingInteractions()
		if len(pending) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending interactions, got %d", len(pending))
	}
	q1, q2 := pending[0], pending[1]

	// Defer Q2 by 180s -> receipt must report Q2's RequestID and ~180s EscalateAt (NOT Q1's 15s).
	beforeDefer := time.Now().UTC()
	defRes, err := svc.ReplyTurnWithReceipt(ReplyInput{
		TurnID:       turn.ID,
		RequestID:    q2.RequestID,
		DeferSeconds: 180,
		Note:         "Verifying Q2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if defRes.RequestID != q2.RequestID || !defRes.Deferred || defRes.EscalateAt == nil {
		t.Fatalf("expected defer receipt targeting Q2 %s, got %+v", q2.RequestID, defRes)
	}
	if delta := defRes.EscalateAt.Sub(beforeDefer); delta < 150*time.Second || delta > 210*time.Second {
		t.Fatalf("expected Q2 EscalateAt ~180s from now, got delta=%v (possibly returned Q1's escalate_at!)", delta)
	}

	// Answer Q1 -> turn remains waiting_input on Q2, and Progress MUST reflect Q2 (NOT "resuming...").
	ans1Res, err := svc.ReplyTurnWithReceipt(ReplyInput{
		TurnID:    turn.ID,
		RequestID: q1.RequestID,
		Text:      "ans1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ans1Res.Turn.State != TurnWaitingInput {
		t.Fatalf("expected waiting_input, got %s", ans1Res.Turn.State)
	}
	if strings.Contains(strings.ToLower(ans1Res.Turn.Progress), "resum") || !strings.Contains(ans1Res.Turn.Progress, "Question Two") {
		t.Fatalf("expected progress to reflect remaining head Q2 (not resuming), got %q", ans1Res.Turn.Progress)
	}

	// Clean up by answering Q2.
	if _, err := svc.ReplyTurnWithReceipt(ReplyInput{TurnID: turn.ID, RequestID: q2.RequestID, Text: "ans2"}); err != nil {
		t.Fatal(err)
	}
	_, _, _, _ = svc.Wait(context.Background(), turn.ID, "", 3*time.Second)
}

// If persisting SessionAutoApprove=true fails in DecideInteractionHuman,
// the API must return an error, keep the interaction pending for retry, and
// must NOT deliver allow_session to the native adapter.
func TestAllowSessionPersistFailureKeepsRequestPending(t *testing.T) {
	var delivered atomic.Int32
	adapter := &interactiveFakeAdapter{
		harnessID: harnesses.HarnessCodex,
		run: func(ctx context.Context, spec TurnStartSpec, onEvent func(AdapterEvent)) TurnOutcome {
			dec, err := spec.OnInteraction(ctx, InteractionRequest{
				Kind:     InteractionApproval,
				ToolName: "commandExecution",
				Summary:  "Run unit tests",
				Command:  "go test ./...",
			})
			if err != nil {
				return TurnOutcome{State: TurnFailed, ErrorMessage: err.Error()}
			}
			delivered.Add(1)
			if dec.Decision != DecisionAllowSession {
				return TurnOutcome{State: TurnFailed, ErrorMessage: "unexpected decision: " + dec.Decision}
			}
			return TurnOutcome{
				State: TurnCompleted,
				Items: []NativeItem{{ItemID: "f1", Type: "agentMessage", Phase: "final_answer", Text: "tests passed"}},
			}
		},
	}

	svc, ws := setupInteractiveTestService(t, harnesses.HarnessCodex, adapter)
	w, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_allow_fail_w", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws,
		AccessMode: harnesses.AccessWorkspaceWrite, ApprovalMode: ApprovalModeAskHuman,
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_allow_fail_s", WorkerID: w.ID, Prompt: "run tests",
	})
	if err != nil {
		t.Fatal(err)
	}

	var reqID string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if list := svc.ListHumanInteractions(); len(list) > 0 {
			reqID = list[0].RequestID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if reqID == "" {
		t.Fatal("expected pending approval interaction")
	}

	// Inject SQLite failure when updating workers table.
	if _, err := svc.store.db.Exec(`CREATE TRIGGER fail_worker_auto_approve BEFORE UPDATE ON workers BEGIN SELECT RAISE(ABORT, 'inject_worker_save_fail'); END;`); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.DecideInteractionHuman(reqID, InteractionDecision{Decision: DecisionAllowSession}); err == nil || !strings.Contains(err.Error(), "inject_worker_save_fail") {
		t.Fatalf("expected DecideInteractionHuman to return storage error, got %v", err)
	}
	if delivered.Load() != 0 {
		t.Fatalf("native adapter must NOT receive allow_session when persistence failed, got %d", delivered.Load())
	}
	if list := svc.ListHumanInteractions(); len(list) != 1 || list[0].RequestID != reqID {
		t.Fatalf("expected interaction %s to remain pending after storage failure, got %+v", reqID, list)
	}
	wMid, _, err := svc.Get(w.ID, w.WorkspaceID)
	if err != nil || wMid.SessionAutoApprove {
		t.Fatalf("expected worker SessionAutoApprove=false after failed persist, got %+v err=%v", wMid, err)
	}

	// Clear fault trigger and retry: must now succeed and persist SessionAutoApprove=true.
	if _, err := svc.store.db.Exec(`DROP TRIGGER fail_worker_auto_approve;`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DecideInteractionHuman(reqID, InteractionDecision{Decision: DecisionAllowSession}); err != nil {
		t.Fatalf("retry DecideInteractionHuman failed: %v", err)
	}
	wt, _, _, err := svc.Wait(context.Background(), turn.ID, w.WorkspaceID, 3*time.Second)
	if err != nil || wt.State != TurnCompleted {
		t.Fatalf("expected turn completed after retry, got %+v err=%v", wt, err)
	}
	wDone, _, err := svc.Get(w.ID, w.WorkspaceID)
	if err != nil || !wDone.SessionAutoApprove {
		t.Fatalf("expected persisted SessionAutoApprove=true after retry, got %+v err=%v", wDone, err)
	}
}

// A human allow_session decision racing with native request cancellation must
// arbitrate under s.mu so a failed/conflicted human call NEVER leaves
// SessionAutoApprove=true, and a committed allow_session is consumed identically
// by both DB and native callback.
func TestHumanAllowSessionRaceWithCancelNeverLeaksGrant(t *testing.T) {
	t.Run("blocked_write_and_concurrent_cancel_stay_consistent", func(t *testing.T) {
		var nativeDec string
		var decMu sync.Mutex
		reqCancelCh := make(chan context.CancelFunc, 1)

		adapter := &interactiveFakeAdapter{
			harnessID: harnesses.HarnessCodex,
			run: func(ctx context.Context, spec TurnStartSpec, onEvent func(AdapterEvent)) TurnOutcome {
				reqCtx, reqCancel := context.WithCancel(ctx)
				reqCancelCh <- reqCancel
				defer reqCancel()
				d, err := spec.OnInteraction(reqCtx, InteractionRequest{
					Kind: InteractionApproval, ToolName: "Bash", Command: "echo ok",
				})
				decMu.Lock()
				if err == nil {
					nativeDec = d.Decision
				}
				decMu.Unlock()
				return TurnOutcome{
					State: TurnCompleted,
					Items: []NativeItem{{ItemID: "f1", Type: "agentMessage", Phase: "final_answer", Text: "done"}},
				}
			},
		}

		svc, ws := setupInteractiveTestService(t, harnesses.HarnessCodex, adapter)
		w, err := svc.Create(context.Background(), CreateInput{
			OperationID: "op_allow_race_w", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws,
			AccessMode: harnesses.AccessWorkspaceWrite, ApprovalMode: ApprovalModeAskHuman,
		})
		if err != nil {
			t.Fatal(err)
		}
		turn, err := svc.Send(context.Background(), SendInput{
			OperationID: "op_allow_race_s", WorkerID: w.ID, Prompt: "run",
		})
		if err != nil {
			t.Fatal(err)
		}

		reqCancel := <-reqCancelCh
		var reqID string
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if list := svc.ListHumanInteractions(); len(list) > 0 {
				reqID = list[0].RequestID
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if reqID == "" {
			t.Fatal("expected pending approval interaction")
		}

		// Hold an exclusive SQLite transaction on a second connection to pause SaveWorker.
		dbPath := filepath.Join(svc.home, "workers", "workers.db")
		blocker, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Close()
		tx, err := blocker.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`UPDATE workers SET updated_at = updated_at WHERE worker_id = ?`, w.ID); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}

		humanErrCh := make(chan error, 1)
		go func() {
			_, hErr := svc.DecideInteractionHuman(reqID, InteractionDecision{Decision: DecisionAllowSession})
			humanErrCh <- hErr
		}()
		// Give DecideInteractionHuman time to acquire s.mu and block in SaveWorker,
		// then cancel the native interaction context before releasing the DB lock.
		time.Sleep(60 * time.Millisecond)
		reqCancel()
		time.Sleep(40 * time.Millisecond)
		_ = tx.Rollback()

		humanErr := <-humanErrCh
		if _, _, _, err := svc.Wait(context.Background(), turn.ID, w.WorkspaceID, 3*time.Second); err != nil {
			t.Fatalf("Wait: %v", err)
		}
		wAfter, _, err := svc.Get(w.ID, w.WorkspaceID)
		if err != nil {
			t.Fatal(err)
		}
		decMu.Lock()
		gotNative := nativeDec
		decMu.Unlock()

		if humanErr != nil {
			if wAfter.SessionAutoApprove {
				t.Fatalf("DecideInteractionHuman returned error (%v) yet DB persisted SessionAutoApprove=true!", humanErr)
			}
		} else {
			if !wAfter.SessionAutoApprove {
				t.Fatalf("DecideInteractionHuman succeeded yet DB SessionAutoApprove=false")
			}
			if gotNative != DecisionAllowSession {
				t.Fatalf("DecideInteractionHuman succeeded and persisted true, but native received %q instead of allow_session", gotNative)
			}
		}
	})

	t.Run("cancel_before_human_decide_never_persists_session_grant", func(t *testing.T) {
		reqCancelCh := make(chan context.CancelFunc, 1)
		adapter := &interactiveFakeAdapter{
			harnessID: harnesses.HarnessCodex,
			run: func(ctx context.Context, spec TurnStartSpec, onEvent func(AdapterEvent)) TurnOutcome {
				reqCtx, reqCancel := context.WithCancel(ctx)
				reqCancelCh <- reqCancel
				defer reqCancel()
				_, _ = spec.OnInteraction(reqCtx, InteractionRequest{
					Kind: InteractionApproval, ToolName: "Bash", Command: "echo ok",
				})
				return TurnOutcome{State: TurnCancelled}
			},
		}
		svc, ws := setupInteractiveTestService(t, harnesses.HarnessCodex, adapter)
		w, err := svc.Create(context.Background(), CreateInput{
			OperationID: "op_allow_cancel_w", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws,
			AccessMode: harnesses.AccessWorkspaceWrite, ApprovalMode: ApprovalModeAskHuman,
		})
		if err != nil {
			t.Fatal(err)
		}
		turn, err := svc.Send(context.Background(), SendInput{
			OperationID: "op_allow_cancel_s", WorkerID: w.ID, Prompt: "run",
		})
		if err != nil {
			t.Fatal(err)
		}
		reqCancel := <-reqCancelCh
		var reqID string
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if list := svc.ListHumanInteractions(); len(list) > 0 {
				reqID = list[0].RequestID
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		reqCancel()
		_, _, _, _ = svc.Wait(context.Background(), turn.ID, w.WorkspaceID, 3*time.Second)

		if _, err := svc.DecideInteractionHuman(reqID, InteractionDecision{Decision: DecisionAllowSession}); err == nil {
			t.Fatal("expected error deciding already-cancelled interaction")
		}
		wAfter, _, err := svc.Get(w.ID, w.WorkspaceID)
		if err != nil || wAfter.SessionAutoApprove {
			t.Fatalf("expected SessionAutoApprove=false after cancelled interaction, got %+v err=%v", wAfter, err)
		}
	})
}
