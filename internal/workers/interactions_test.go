package workers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/harnesses"
)

func TestApprovalModeNormalizationAndDefaults(t *testing.T) {
	codexCaps, _ := harnesses.StaticCapabilities(harnesses.HarnessCodex)
	claudeCaps, _ := harnesses.StaticCapabilities(harnesses.HarnessClaudeCode)
	dshCaps, _ := harnesses.StaticCapabilities(harnesses.HarnessDeepSeek)

	if !codexCaps.SupportsAutoApproval || !claudeCaps.SupportsAutoApproval || dshCaps.SupportsAutoApproval {
		t.Fatalf("unexpected SupportsAutoApproval matrix: codex=%v claude=%v dsh=%v",
			codexCaps.SupportsAutoApproval, claudeCaps.SupportsAutoApproval, dshCaps.SupportsAutoApproval)
	}

	req, eff, err := NormalizeApprovalMode(harnesses.AccessWorkspaceWrite, "", codexCaps)
	if err != nil || req != ApprovalModeAuto || eff != ApprovalModeAuto {
		t.Fatalf("codex default: req=%q eff=%q err=%v", req, eff, err)
	}
	req, eff, err = NormalizeApprovalMode(harnesses.AccessWorkspaceWrite, "", claudeCaps)
	if err != nil || req != ApprovalModeAuto || eff != ApprovalModeAuto {
		t.Fatalf("claude default: req=%q eff=%q err=%v", req, eff, err)
	}
	req, eff, err = NormalizeApprovalMode(harnesses.AccessWorkspaceWrite, "", dshCaps)
	if err != nil || req != ApprovalModeAuto || eff != ApprovalModeAskHuman {
		t.Fatalf("deepseek default should fall back to ask_human: req=%q eff=%q err=%v", req, eff, err)
	}
	req, eff, err = NormalizeApprovalMode(harnesses.AccessWorkspaceWrite, ApprovalModeAskHuman, codexCaps)
	if err != nil || req != ApprovalModeAskHuman || eff != ApprovalModeAskHuman {
		t.Fatalf("explicit ask_human: req=%q eff=%q err=%v", req, eff, err)
	}
	req, eff, err = NormalizeApprovalMode(harnesses.AccessReadOnly, "", codexCaps)
	if err != nil || req != ApprovalModeDenyEscalation || eff != ApprovalModeDenyEscalation {
		t.Fatalf("read_only default: req=%q eff=%q err=%v", req, eff, err)
	}
	if _, _, err := NormalizeApprovalMode(harnesses.AccessReadOnly, ApprovalModeAskHuman, codexCaps); err == nil {
		t.Fatalf("expected error for ask_human on read_only")
	}
}

func TestHardRedlineRiskEvaluation(t *testing.T) {
	nodeHome := t.TempDir()
	ws := t.TempDir()
	outside := t.TempDir()

	// 1. read_only escalation is always hard-denied.
	r := EvaluateInteractionRisk(nodeHome, ws, harnesses.AccessReadOnly, InteractionRequest{
		Kind: InteractionApproval, ToolName: "pwsh", Command: "echo hi",
	})
	if !r.HardRedline || !r.HardDeny {
		t.Fatalf("expected read_only hard deny, got %+v", r)
	}

	// 2. Destructive OS commands and credential paths trigger HardRedline.
	for _, cmd := range []string{
		"shutdown /s /t 0",
		"reg delete HKLM\\Software\\Foo /f",
		"cat ~/.ssh/id_rsa",
		"type %USERPROFILE%\\.claude.json",
		"Remove-Item -Recurse -Force " + outside,
	} {
		r = EvaluateInteractionRisk(nodeHome, ws, harnesses.AccessWorkspaceWrite, InteractionRequest{
			Kind: InteractionApproval, ToolName: "Bash", Command: cmd,
		})
		if !r.HardRedline || r.RiskLevel != "critical" {
			t.Fatalf("expected hard redline for %q, got %+v", cmd, r)
		}
	}

	// 3. Targeting Asterhand node home triggers HardRedline.
	r = EvaluateInteractionRisk(nodeHome, ws, harnesses.AccessWorkspaceWrite, InteractionRequest{
		Kind: InteractionApproval, ToolName: "Write", Paths: []string{filepath.Join(nodeHome, "config.json")},
	})
	if !r.HardRedline {
		t.Fatalf("expected nodeHome target to trigger hard redline, got %+v", r)
	}

	// 4. Ordinary write outside workspace (non-sensitive, non-destructive) is high risk, NOT hard redline.
	r = EvaluateInteractionRisk(nodeHome, ws, harnesses.AccessWorkspaceWrite, InteractionRequest{
		Kind: InteractionApproval, ToolName: "Write", Paths: []string{filepath.Join(outside, "note.txt")},
	})
	if !r.OutsideWorkspace || r.RiskLevel != "high" || r.HardRedline {
		t.Fatalf("expected high-risk non-redline for ordinary outside path, got %+v", r)
	}
}

type interactiveFakeAdapter struct {
	harnessID string
	run       func(ctx context.Context, spec TurnStartSpec, onEvent func(AdapterEvent)) TurnOutcome
}

func (f *interactiveFakeAdapter) HarnessID() string { return f.harnessID }
func (f *interactiveFakeAdapter) OpenThread(_ context.Context, spec ThreadOpenSpec) (ThreadHandle, error) {
	return ThreadHandle{Key: spec.WorkerID + ":th1", NativeSessionID: "th1"}, nil
}
func (f *interactiveFakeAdapter) StartTurn(_ context.Context, _ ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
	ch := make(chan TurnOutcome, 1)
	go func() {
		ch <- f.run(context.Background(), spec, onEvent)
		close(ch)
	}()
	return "nt_1", ch, nil
}
func (f *interactiveFakeAdapter) InterruptTurn(_ context.Context, _ ThreadHandle, _ string) error {
	return nil
}
func (f *interactiveFakeAdapter) ForceKill(_ ThreadHandle) error { return nil }
func (f *interactiveFakeAdapter) InspectThreadTurns(_ context.Context, _ ThreadHandle) ([]NativeTurnRecord, error) {
	return nil, nil
}
func (f *interactiveFakeAdapter) CloseThread(_ ThreadHandle) error { return nil }

func setupInteractiveTestService(t *testing.T, harnessID string, adapter Adapter) (*Service, string) {
	t.Helper()
	home := t.TempDir()
	ws := t.TempDir()
	hs, err := harnesses.Open(home, "")
	if err != nil {
		t.Fatal(err)
	}
	fakeExe := filepath.Join(t.TempDir(), "fake.exe")
	if err := os.WriteFile(fakeExe, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := hs.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnessID,
		ManualPath:          &fakeExe,
		CollabWorkerEnabled: boolPtr(true),
		AllowWorkspaceWrite: boolPtr(true),
	}); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(home, hs, adapter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc, ws
}

func boolPtr(v bool) *bool { return &v }

func TestInteractionBridgeApprovalFlowsAndTimerPause(t *testing.T) {
	var seenDecisions []string
	var mu sync.Mutex
	adapter := &interactiveFakeAdapter{
		harnessID: harnesses.HarnessCodex,
		run: func(ctx context.Context, spec TurnStartSpec, onEvent func(AdapterEvent)) TurnOutcome {
			// 1) First approval: human denies once -> turn must CONTINUE, not die.
			d1, _ := spec.OnInteraction(ctx, InteractionRequest{
				Kind: InteractionApproval, ToolName: "Bash", Command: "npm install left-pad",
			})
			// 2) Second approval: human grants allow_session -> worker becomes session_auto_approve.
			d2, _ := spec.OnInteraction(ctx, InteractionRequest{
				Kind: InteractionApproval, ToolName: "Bash", Command: "mkdir dist",
			})
			// 3) Third safe approval: auto-approved silently by policy!
			d3, _ := spec.OnInteraction(ctx, InteractionRequest{
				Kind: InteractionApproval, ToolName: "Bash", Command: "echo ok > dist/a.txt",
			})
			// 4) Fourth hard-redline approval: MUST still ask human (never auto-approved)!
			d4, _ := spec.OnInteraction(ctx, InteractionRequest{
				Kind: InteractionApproval, ToolName: "Bash", Command: "shutdown /s /t 0",
			})
			mu.Lock()
			seenDecisions = []string{d1.Decision, d2.Decision, d3.Decision, d4.Decision}
			mu.Unlock()
			return TurnOutcome{
				State: TurnCompleted,
				Items: []NativeItem{{ItemID: "i1", Type: "agentMessage", Phase: "final_answer", Text: "all handled"}},
			}
		},
	}

	svc, ws := setupInteractiveTestService(t, harnesses.HarnessCodex, adapter)
	// Tight stall & wall-clock budgets to prove waiting_approval PAUSES both timers!
	svc.SetBudgets(Budgets{
		StallTimeout:       120 * time.Millisecond,
		WallClockTimeout:   250 * time.Millisecond,
		InteractionTimeout: 5 * time.Second,
	})

	w, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_c1", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws,
		AccessMode: harnesses.AccessWorkspaceWrite, ApprovalMode: ApprovalModeAskHuman,
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_s1", WorkerID: w.ID, Prompt: "do work",
	})
	if err != nil {
		t.Fatal(err)
	}

	waitForHumanReq := func() InteractionView {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			list := svc.ListHumanInteractions()
			if len(list) > 0 {
				return list[0]
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("timed out waiting for pending human interaction")
		return InteractionView{}
	}

	// Approval 1: sleep 300ms (> both StallTimeout 120ms and WallClockTimeout 250ms) to verify pause!
	r1 := waitForHumanReq()
	time.Sleep(300 * time.Millisecond)
	curTurn, _ := svc.GetTurn(turn.ID, "")
	if curTurn.State != TurnWaitingApproval {
		t.Fatalf("expected turn to remain waiting_approval during pause, got %s (%s)", curTurn.State, curTurn.ErrorCode)
	}
	// Caller A must be strictly forbidden from approving waiting_approval.
	if _, _, err := svc.ReplyTurn(ReplyInput{TurnID: turn.ID, Text: "approve"}); err == nil {
		t.Fatalf("caller ReplyTurn must be forbidden on waiting_approval")
	}
	// Idempotent decide: first wins, second fails.
	if _, err := svc.DecideInteractionHuman(r1.RequestID, InteractionDecision{Decision: DecisionDeny}); err != nil {
		t.Fatalf("decide r1 deny failed: %v", err)
	}
	if _, err := svc.DecideInteractionHuman(r1.RequestID, InteractionDecision{Decision: DecisionAllowOnce}); err == nil {
		t.Fatalf("expected duplicate decide on same request_id to fail")
	}

	// Approval 2: allow_session.
	r2 := waitForHumanReq()
	if !r2.AllowSessionOK {
		t.Fatalf("expected AllowSessionOK=true for safe command")
	}
	if _, err := svc.DecideInteractionHuman(r2.RequestID, InteractionDecision{Decision: DecisionAllowSession}); err != nil {
		t.Fatalf("decide r2 allow_session failed: %v", err)
	}

	// Approval 3 is auto-approved silently, so next human prompt MUST be Approval 4 (hard redline)!
	r4 := waitForHumanReq()
	if !r4.HardRedline || r4.AllowSessionOK || !strings.Contains(r4.Command, "shutdown") {
		t.Fatalf("expected hard-redline prompt for shutdown, got %+v", r4)
	}
	// Attempting allow_session on hard-redline request must be forbidden.
	if _, err := svc.DecideInteractionHuman(r4.RequestID, InteractionDecision{Decision: DecisionAllowSession}); err == nil {
		t.Fatalf("expected allow_session to be rejected on hard-redline request")
	}
	if _, err := svc.DecideInteractionHuman(r4.RequestID, InteractionDecision{Decision: DecisionDeny}); err != nil {
		t.Fatalf("decide r4 deny failed: %v", err)
	}

	doneTurn, ans, _, err := svc.Wait(context.Background(), turn.ID, "", 3*time.Second)
	if err != nil || doneTurn.State != TurnCompleted || ans != "all handled" {
		t.Fatalf("unexpected final turn: state=%s ans=%q err=%v", doneTurn.State, ans, err)
	}
	mu.Lock()
	got := append([]string(nil), seenDecisions...)
	mu.Unlock()
	want := []string{DecisionDeny, DecisionAllowSession, DecisionAllowOnce, DecisionDeny}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("decision[%d] = %q, want %q (all=%v)", i, got[i], want[i], got)
		}
	}
}

func TestInteractionBridgeUserInputCallerReplyAndAutoEscalation(t *testing.T) {
	var answers []string
	var mu sync.Mutex
	adapter := &interactiveFakeAdapter{
		harnessID: harnesses.HarnessClaudeCode,
		run: func(ctx context.Context, spec TurnStartSpec, _ func(AdapterEvent)) TurnOutcome {
			d1, _ := spec.OnInteraction(ctx, InteractionRequest{
				Kind:     InteractionInput,
				ToolName: "AskUserQuestion",
				Questions: []InteractionQuestion{{
					ID: "q_fw", Question: "Which test framework?",
					Options: []InteractionOption{{Label: "pytest"}, {Label: "unittest"}},
				}},
			})
			d2, _ := spec.OnInteraction(ctx, InteractionRequest{
				Kind:     InteractionInput,
				ToolName: "AskUserQuestion",
				Questions: []InteractionQuestion{{
					ID: "q_port", Question: "Which port should the server listen on?",
				}},
			})
			mu.Lock()
			answers = []string{d1.Text, d2.Text}
			mu.Unlock()
			return TurnOutcome{
				State: TurnCompleted,
				Items: []NativeItem{{ItemID: "i1", Type: "agentMessage", Phase: "final_answer", Text: "done"}},
			}
		},
	}

	svc, ws := setupInteractiveTestService(t, harnesses.HarnessClaudeCode, adapter)
	svc.SetBudgets(Budgets{InputCallerGrace: 80 * time.Millisecond})

	w, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_ic1", HarnessID: harnesses.HarnessClaudeCode, WorkspaceRoot: ws,
		AccessMode: harnesses.AccessReadOnly,
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_is1", WorkerID: w.ID, Prompt: "review",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Question 1: workers_wait wakes immediately for caller A before human grace expires.
	wt, _, _, err := svc.Wait(context.Background(), turn.ID, "", 2*time.Second)
	if err != nil || wt.State != TurnWaitingInput || wt.PendingInteraction == nil {
		t.Fatalf("expected immediate waiting_input wakeup, got state=%s pi=%+v err=%v", wt.State, wt.PendingInteraction, err)
	}
	if _, escalated, err := svc.ReplyTurn(ReplyInput{TurnID: turn.ID, Text: "pytest"}); err != nil || escalated {
		t.Fatalf("caller reply failed: escalated=%v err=%v", escalated, err)
	}

	// Question 2: caller does not answer -> auto-escalates to human after 80ms grace!
	deadline := time.Now().Add(2 * time.Second)
	var humanQ InteractionView
	for time.Now().Before(deadline) {
		list := svc.ListHumanInteractions()
		if len(list) > 0 && list[0].Kind == InteractionInput {
			humanQ = list[0]
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if humanQ.RequestID == "" {
		t.Fatalf("expected second question to auto-escalate to human")
	}
	if _, err := svc.DecideInteractionHuman(humanQ.RequestID, InteractionDecision{
		Decision: DecisionAnswer, Text: "8080",
	}); err != nil {
		t.Fatalf("human answer failed: %v", err)
	}

	doneTurn, _, _, err := svc.Wait(context.Background(), turn.ID, "", 3*time.Second)
	if err != nil || doneTurn.State != TurnCompleted {
		t.Fatalf("expected completed turn, got %s err=%v", doneTurn.State, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(answers) != 2 || answers[0] != "pytest" || answers[1] != "8080" {
		t.Fatalf("unexpected delivered answers: %v", answers)
	}
}

func TestCodexClaudeDeepSeekInteractionPayloads(t *testing.T) {
	// Codex policies matrix.
	sb, _, pol, rev := codexTurnPolicies(harnesses.AccessWorkspaceWrite, ApprovalModeAuto, false, `F:\proj`)
	if sb != "workspace-write" || pol != "on-request" || rev != "auto_review" {
		t.Fatalf("codex auto: %s %s %s", sb, pol, rev)
	}
	sb, _, pol, rev = codexTurnPolicies(harnesses.AccessWorkspaceWrite, ApprovalModeAskHuman, false, `F:\proj`)
	if sb != "workspace-write" || pol != "on-request" || rev != "user" {
		t.Fatalf("codex ask_human: %s %s %s", sb, pol, rev)
	}
	sb, _, pol, rev = codexTurnPolicies(harnesses.AccessWorkspaceWrite, ApprovalModeAskHuman, true, `F:\proj`)
	if sb != "danger-full-access" || pol != "untrusted" || rev != "user" {
		t.Fatalf("codex session_auto_approve: %s %s %s", sb, pol, rev)
	}

	// Claude AskUserQuestion answer payload.
	rawQ := json.RawMessage(`{"questions":[{"id":"q1","question":"Pick color","options":[{"label":"Blue"}]}]}`)
	ireq := parseClaudeInteractionRequest("AskUserQuestion", rawQ)
	resp, interrupt := buildClaudeInteractionReply("AskUserQuestion", rawQ, ireq, InteractionDecision{
		Decision: DecisionAnswer, Answers: map[string][]string{"q1": {"Blue"}},
	})
	if interrupt || resp["behavior"] != "allow" {
		t.Fatalf("unexpected claude question reply: %+v interrupt=%v", resp, interrupt)
	}

	// DeepSeek permission reply: request_permission with ONLY toolCallId falls back to cached tool_call rawInput.
	wsDir := t.TempDir()
	extDir := t.TempDir()
	cachedRaw := json.RawMessage(`{"command":"Remove-Item -Recurse -Force '` + filepath.ToSlash(extDir) + `'","justification":"delete outside dir"}`)
	rawDSOnlyID := json.RawMessage(`{"toolCall":{"toolCallId":"tc1"},"options":[{"optionId":"allow-once","kind":"allow_once"},{"optionId":"reject-once","kind":"reject_once"}]}`)
	dsReq, allowOpts, rejectOpts := parseDeepSeekPermissionRequest(rawDSOnlyID, cachedRaw, "pwsh", "command")
	if !strings.Contains(dsReq.Command, "Remove-Item -Recurse -Force") || dsReq.Reason != "delete outside dir" || len(allowOpts) != 1 || len(rejectOpts) != 1 {
		t.Fatalf("unexpected deepseek fallback parse: req=%+v allow=%v reject=%v", dsReq, allowOpts, rejectOpts)
	}
	risk := EvaluateInteractionRisk(t.TempDir(), wsDir, harnesses.AccessWorkspaceWrite, dsReq)
	if !risk.HardRedline || !risk.OutsideWorkspace || risk.RiskLevel != "critical" {
		t.Fatalf("expected deepseek cached recursive delete outside workspace to hit hard redline, got %+v", risk)
	}
	dsResp, cancel := buildDeepSeekPermissionReply(allowOpts, rejectOpts, InteractionDecision{Decision: DecisionDeny})
	if cancel {
		t.Fatalf("deny must not cancel deepseek turn")
	}
	if outcome, _ := dsResp["outcome"].(map[string]any); outcome["optionId"] != "reject-once" {
		t.Fatalf("expected reject-once, got %+v", dsResp)
	}
}

func TestFinalizeTurnPreservesConcurrentRevokeAndStrictDenyMode(t *testing.T) {
	gate := make(chan struct{})
	adapter := &interactiveFakeAdapter{
		harnessID: harnesses.HarnessClaudeCode,
		run: func(ctx context.Context, spec TurnStartSpec, onEvent func(AdapterEvent)) TurnOutcome {
			// Simulate Claude system(init) reporting acceptEdits under strict_deny.
			onEvent(AdapterEvent{Kind: "approval_mode", Summary: ApprovalModeAskHuman})
			<-gate
			return TurnOutcome{
				State: TurnCompleted,
				Items: []NativeItem{{ItemID: "i1", Type: "agentMessage", Phase: "final_answer", Text: "ok"}},
			}
		},
	}

	svc, ws := setupInteractiveTestService(t, harnesses.HarnessClaudeCode, adapter)
	w, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_rev_c1", HarnessID: harnesses.HarnessClaudeCode, WorkspaceRoot: ws,
		AccessMode: harnesses.AccessWorkspaceWrite, ApprovalMode: ApprovalModeStrictDeny,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Enable session_auto_approve BEFORE turn starts so turn's local Worker copy has true.
	if _, err := svc.SetSessionAutoApproveHuman(w.ID, true); err != nil {
		t.Fatal(err)
	}
	turn, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_rev_s1", WorkerID: w.ID, Prompt: "run",
	})
	if err != nil {
		t.Fatal(err)
	}
	// While turn is running, human revokes session_auto_approve via Web UI!
	if _, err := svc.SetSessionAutoApproveHuman(w.ID, false); err != nil {
		t.Fatal(err)
	}
	close(gate)

	doneTurn, _, _, err := svc.Wait(context.Background(), turn.ID, "", 3*time.Second)
	if err != nil || doneTurn.State != TurnCompleted {
		t.Fatalf("expected completed turn, got %s err=%v", doneTurn.State, err)
	}
	after, _, err := svc.Get(w.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if after.SessionAutoApprove {
		t.Fatalf("finalizeTurn clobbered concurrent session_auto_approve revoke back to true!")
	}
	if after.EffectiveApprovalMode != ApprovalModeStrictDeny {
		t.Fatalf("system(init) approval_mode overwrote strict_deny to %q!", after.EffectiveApprovalMode)
	}
}

func TestDecideAllowSessionPersistsSynchronouslyBeforeReturning(t *testing.T) {
	resumeGate := make(chan struct{})
	adapter := &interactiveFakeAdapter{
		harnessID: harnesses.HarnessCodex,
		run: func(ctx context.Context, spec TurnStartSpec, onEvent func(AdapterEvent)) TurnOutcome {
			dec, err := spec.OnInteraction(ctx, InteractionRequest{
				Kind:     InteractionApproval,
				ToolName: "commandExecution",
				Summary:  "Write file outside workspace",
				Command:  "pwsh -Command Set-Content outside.txt ok",
			})
			if err != nil || dec.Decision != DecisionAllowSession {
				return TurnOutcome{State: TurnFailed, ErrorMessage: "expected allow_session"}
			}
			<-resumeGate
			return TurnOutcome{
				State: TurnCompleted,
				Items: []NativeItem{{ItemID: "i1", Type: "agentMessage", Phase: "final_answer", Text: "done"}},
			}
		},
	}

	svc, ws := setupInteractiveTestService(t, harnesses.HarnessCodex, adapter)
	w, err := svc.Create(context.Background(), CreateInput{
		OperationID: "op_sync_c1", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws,
		AccessMode: harnesses.AccessWorkspaceWrite, ApprovalMode: ApprovalModeAskHuman,
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_sync_s1", WorkerID: w.ID, Prompt: "write outside",
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var reqID string
	for time.Now().Before(deadline) {
		if list := svc.ListHumanInteractions(); len(list) > 0 {
			reqID = list[0].RequestID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if reqID == "" {
		t.Fatalf("expected pending approval interaction")
	}
	if _, err := svc.DecideInteractionHuman(reqID, InteractionDecision{Decision: DecisionAllowSession}); err != nil {
		t.Fatalf("DecideInteractionHuman failed: %v", err)
	}
	// Immediately after DecideInteractionHuman returns (even while turn goroutine is still blocked),
	// persisted Worker MUST already report SessionAutoApprove == true so Web UI never sees a stale false!
	gotW, _, err := svc.Get(w.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !gotW.SessionAutoApprove {
		t.Fatalf("expected SessionAutoApprove to be persisted synchronously inside DecideInteractionHuman")
	}
	close(resumeGate)
	doneTurn, _, _, err := svc.Wait(context.Background(), turn.ID, "", 3*time.Second)
	if err != nil || doneTurn.State != TurnCompleted {
		t.Fatalf("expected completed turn, got %s err=%v", doneTurn.State, err)
	}
}
