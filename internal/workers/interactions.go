package workers

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/harnesses"
	"github.com/jackgan90/Asterhand/internal/model"
)

type pendingInteraction struct {
	mu              sync.Mutex
	view            InteractionView
	resolved        bool
	decisionCh      chan InteractionDecision
	escalateStop    chan struct{}
	escalateResetCh chan time.Duration
	escalateOnce    sync.Once
}

func (pi *pendingInteraction) resetEscalationTimer(d time.Duration) {
	if pi.escalateResetCh == nil {
		return
	}
	select {
	case pi.escalateResetCh <- d:
	default:
		select {
		case <-pi.escalateResetCh:
		default:
		}
		select {
		case pi.escalateResetCh <- d:
		default:
		}
	}
}

func (pi *pendingInteraction) stopEscalationTimer() {
	pi.escalateOnce.Do(func() {
		if pi.escalateStop != nil {
			close(pi.escalateStop)
		}
	})
}

func (pi *pendingInteraction) resolve(dec InteractionDecision) bool {
	pi.mu.Lock()
	if pi.resolved {
		pi.mu.Unlock()
		return false
	}
	pi.resolved = true
	pi.mu.Unlock()
	pi.stopEscalationTimer()
	pi.decisionCh <- dec
	return true
}

func (pi *pendingInteraction) snapshot() InteractionView {
	pi.mu.Lock()
	defer pi.mu.Unlock()
	v := pi.view
	if len(v.Paths) > 0 {
		v.Paths = append([]string(nil), v.Paths...)
	}
	if len(v.Questions) > 0 {
		qs := make([]InteractionQuestion, len(v.Questions))
		for i, q := range v.Questions {
			if len(q.Options) > 0 {
				q.Options = append([]InteractionOption(nil), q.Options...)
			}
			qs[i] = q
		}
		v.Questions = qs
	}
	return v
}

func (s *Service) SetInteractionsObserver(fn func([]InteractionView)) {
	s.mu.Lock()
	s.onInteractionsChanged = fn
	s.mu.Unlock()
}

func (s *Service) notifyInteractionsChanged() {
	s.mu.Lock()
	fn := s.onInteractionsChanged
	s.mu.Unlock()
	if fn != nil {
		fn(s.ListHumanInteractions())
	}
}

// ListHumanInteractions returns all currently pending interactions visible to
// human UIs (desktop bottom-right action center and Web panel), ordered with
// approvals ahead of questions, oldest first within each category.
func (pi *pendingInteraction) isResolved() bool {
	pi.mu.Lock()
	defer pi.mu.Unlock()
	return pi.resolved
}

func (s *Service) ListHumanInteractions() []InteractionView {
	s.mu.Lock()
	out := make([]InteractionView, 0, len(s.interactions))
	for _, pi := range s.interactions {
		if pi.isResolved() {
			continue
		}
		v := pi.snapshot()
		if v.VisibleToHuman {
			out = append(out, v)
		}
	}
	s.mu.Unlock()
	sortInteractions(out)
	return out
}

// ListAllPendingInteractions returns all pending interactions (including input
// questions currently in caller-A grace window).
func (s *Service) ListAllPendingInteractions() []InteractionView {
	s.mu.Lock()
	out := make([]InteractionView, 0, len(s.interactions))
	for _, pi := range s.interactions {
		if pi.isResolved() {
			continue
		}
		out = append(out, pi.snapshot())
	}
	s.mu.Unlock()
	sortInteractions(out)
	return out
}

func sortInteractions(items []InteractionView) {
	sort.Slice(items, func(i, j int) bool {
		pi := interactionPriority(items[i].Kind)
		pj := interactionPriority(items[j].Kind)
		if pi != pj {
			return pi < pj
		}
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		}
		return items[i].RequestID < items[j].RequestID
	})
}

func interactionPriority(kind string) int {
	if kind == InteractionApproval {
		return 0
	}
	return 1
}

func (s *Service) pendingForTurnLocked(turnID string) *InteractionView {
	for _, pi := range s.interactions {
		if pi.isResolved() {
			continue
		}
		v := pi.snapshot()
		if v.TurnID == turnID {
			return &v
		}
	}
	return nil
}

// handleTurnInteraction is called by adapters (from a worker goroutine, never
// holding adapter stdio locks) whenever a native harness requests permission
// approval or user input.
func (s *Service) handleTurnInteraction(ctx context.Context, rt *runningTurn, w *Worker, t *Turn, req InteractionRequest) (InteractionDecision, error) {
	if req.Kind == "" {
		req.Kind = InteractionApproval
	}
	now := s.now().UTC()

	// Refresh latest worker flags (e.g. session_auto_approve toggled by human).
	curWorker, err := s.store.GetWorker(w.ID)
	if err == nil {
		w.SessionAutoApprove = curWorker.SessionAutoApprove
		w.ApprovalMode = curWorker.ApprovalMode
		w.EffectiveApprovalMode = curWorker.EffectiveApprovalMode
	}

	risk := EvaluateInteractionRisk(s.home, w.WorkspaceRoot, w.AccessMode, req)

	if req.Kind == InteractionApproval {
		if risk.HardDeny {
			s.appendInteractionLog(t.ID, "approval_denied", fmt.Sprintf("Blocked escalation (%s): %s", req.ToolName, strings.Join(risk.Reasons, "; ")))
			return InteractionDecision{Decision: DecisionDeny, Message: strings.Join(risk.Reasons, "; "), Actor: "policy"}, nil
		}
		if w.ApprovalMode == ApprovalModeStrictDeny {
			s.appendInteractionLog(t.ID, "approval_denied", fmt.Sprintf("Strict-deny policy blocked approval request (%s)", firstNonEmptyStr(req.Summary, req.ToolName)))
			return InteractionDecision{Decision: DecisionCancelTurn, Message: "Approval rejected under strict_deny approval_mode", Actor: "policy"}, nil
		}
		if w.SessionAutoApprove && !risk.HardRedline {
			s.appendInteractionLog(t.ID, "approval_auto", fmt.Sprintf("Session auto-approved %s: %s", firstNonEmptyStr(req.ToolName, "action"), firstNonEmptyStr(req.Summary, truncateSummary(req.Command, 120))))
			return InteractionDecision{Decision: DecisionAllowOnce, Actor: "policy"}, nil
		}
	}

	reqID := "wir_" + model.ID()
	summary := strings.TrimSpace(req.Summary)
	if summary == "" {
		if req.Kind == InteractionInput && len(req.Questions) > 0 {
			summary = truncateSummary(req.Questions[0].Question, 160)
		} else {
			summary = firstNonEmptyStr(truncateSummary(req.Command, 160), req.ToolName, "Interactive request")
		}
	}

	view := InteractionView{
		RequestID:        reqID,
		Kind:             req.Kind,
		WorkerID:         w.ID,
		WorkerAlias:      w.Alias,
		WorkspaceID:      w.WorkspaceID,
		WorkspaceRoot:    w.WorkspaceRoot,
		HarnessID:        w.HarnessID,
		TurnID:           t.ID,
		ToolName:         req.ToolName,
		Summary:          summary,
		Command:          req.Command,
		Paths:            append([]string(nil), req.Paths...),
		Reason:           strings.TrimSpace(req.Reason),
		RiskLevel:        risk.RiskLevel,
		HardRedline:      risk.HardRedline,
		OutsideWorkspace: risk.OutsideWorkspace,
		AllowSessionOK:   req.Kind == InteractionApproval && !risk.HardRedline,
		Questions:        req.Questions,
		VisibleToHuman:   req.Kind == InteractionApproval,
		CreatedAt:        now,
	}
	if len(risk.Reasons) > 0 && view.Reason == "" {
		view.Reason = strings.Join(risk.Reasons, "; ")
	}

	pi := &pendingInteraction{
		view:            view,
		decisionCh:      make(chan InteractionDecision, 1),
		escalateStop:    make(chan struct{}),
		escalateResetCh: make(chan time.Duration, 1),
	}

	waitState := TurnWaitingApproval
	progressMsg := "Waiting for human approval: " + truncateSummary(summary, 120)
	if req.Kind == InteractionInput {
		waitState = TurnWaitingInput
		progressMsg = "Waiting for input: " + truncateSummary(summary, 120)
		grace := s.budgets.InputCallerGrace
		if grace <= 0 {
			grace = 15 * time.Second
		}
		escAt := now.Add(grace)
		pi.view.EscalateAt = &escAt
		view.EscalateAt = &escAt
	}

	s.mu.Lock()
	s.interactions[reqID] = pi
	rt.waitingSince = now
	s.mu.Unlock()

	t.State = waitState
	t.Progress = progressMsg
	t.PendingInteraction = &view
	t.UpdatedAt = now
	_ = s.store.SaveTurn(*t)
	s.appendInteractionLog(t.ID, waitState, progressMsg)
	s.notifyWaiters(t.ID)

	if req.Kind == InteractionApproval {
		s.notifyInteractionsChanged()
	} else {
		grace := s.budgets.InputCallerGrace
		if grace <= 0 {
			grace = 15 * time.Second
		}
		s.notifyInteractionsChanged()
		go func() {
			timer := time.NewTimer(grace)
			defer timer.Stop()
			for {
				select {
				case <-timer.C:
					s.escalateInteractionToHuman(reqID, t.ID, "")
					return
				case newDur := <-pi.escalateResetCh:
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(newDur)
				case <-pi.escalateStop:
					return
				case <-rt.cancelCh:
					return
				}
			}
		}()
	}

	waitTimeout := s.budgets.InteractionTimeout
	if waitTimeout <= 0 {
		waitTimeout = 30 * time.Minute
	}
	timeoutTimer := time.NewTimer(waitTimeout)
	defer timeoutTimer.Stop()

	var dec InteractionDecision
	select {
	case dec = <-pi.decisionCh:
	case <-rt.cancelCh:
		dec = InteractionDecision{Decision: DecisionCancelTurn, Message: "Turn cancelled while waiting for interaction", Actor: "policy"}
	case <-ctx.Done():
		dec = InteractionDecision{Decision: DecisionCancelTurn, Message: "Context cancelled while waiting for interaction", Actor: "policy"}
	case <-timeoutTimer.C:
		dec = InteractionDecision{Decision: DecisionDeny, Message: "Timed out waiting for user response", Actor: "timeout"}
	}
	pi.stopEscalationTimer()

	resumedAt := s.now().UTC()
	s.mu.Lock()
	delete(s.interactions, reqID)
	if !rt.waitingSince.IsZero() {
		rt.pausedTotal += resumedAt.Sub(rt.waitingSince)
		rt.waitingSince = time.Time{}
	}
	rt.lastEventAt = resumedAt
	s.mu.Unlock()

	if dec.Decision == DecisionAllowSession && req.Kind == InteractionApproval && !risk.HardRedline {
		s.updateWorkerState(w.ID, func(cur *Worker) {
			cur.SessionAutoApprove = true
			cur.UpdatedAt = resumedAt
		})
		w.SessionAutoApprove = true
	}

	t.PendingInteraction = nil
	if dec.Decision != DecisionCancelTurn {
		t.State = TurnRunning
		if req.Kind == InteractionApproval {
			t.Progress = fmt.Sprintf("Approval resolved (%s); resuming...", dec.Decision)
		} else {
			t.Progress = "Input delivered; resuming..."
		}
		t.UpdatedAt = resumedAt
		_ = s.store.SaveTurn(*t)
	}
	s.appendInteractionLog(t.ID, "interaction_resolved", formatDecisionLog(req, dec))
	s.notifyWaiters(t.ID)
	s.notifyInteractionsChanged()
	return dec, nil
}

func (s *Service) markInputObservedByCaller(turnID string) {
	s.mu.Lock()
	var pi *pendingInteraction
	for _, cand := range s.interactions {
		if cand.snapshot().TurnID == turnID {
			pi = cand
			break
		}
	}
	s.mu.Unlock()
	if pi == nil {
		return
	}

	claimedGrace := s.budgets.InputClaimedGrace
	if claimedGrace <= 0 {
		base := s.budgets.InputCallerGrace
		if base <= 0 {
			base = 15 * time.Second
		}
		if base >= time.Second {
			claimedGrace = 30 * time.Second
		} else {
			claimedGrace = 2 * base
		}
	}

	now := s.now().UTC()
	nextAt := now.Add(claimedGrace)
	pi.mu.Lock()
	if pi.resolved || pi.view.Kind != InteractionInput || pi.view.VisibleToHuman || pi.view.ClaimedByCaller {
		pi.mu.Unlock()
		return
	}
	pi.view.ClaimedByCaller = true
	pi.view.EscalateAt = &nextAt
	snap := pi.view
	pi.mu.Unlock()

	pi.resetEscalationTimer(claimedGrace)
	if t, err := s.store.GetTurn(turnID); err == nil && t.State == TurnWaitingInput {
		t.PendingInteraction = &snap
		t.UpdatedAt = now
		_ = s.store.SaveTurn(t)
	}
	s.notifyInteractionsChanged()
}

func (s *Service) escalateInteractionToHuman(requestID, turnID, note string) bool {
	s.mu.Lock()
	pi, ok := s.interactions[requestID]
	s.mu.Unlock()
	if !ok {
		return false
	}
	pi.mu.Lock()
	if pi.resolved || pi.view.VisibleToHuman {
		pi.mu.Unlock()
		return false
	}
	pi.view.VisibleToHuman = true
	pi.view.EscalateAt = nil
	if trimmed := truncateSummary(strings.TrimSpace(note), 1000); trimmed != "" {
		pi.view.EscalationNote = trimmed
	}
	snap := pi.view
	pi.mu.Unlock()
	pi.stopEscalationTimer()

	if t, err := s.store.GetTurn(turnID); err == nil && t.State == TurnWaitingInput {
		t.PendingInteraction = &snap
		t.UpdatedAt = s.now().UTC()
		_ = s.store.SaveTurn(t)
		s.notifyWaiters(turnID)
	}
	s.notifyInteractionsChanged()
	return true
}

// DecideInteractionHuman handles a human approval or question response from
// either the desktop bottom-right interactive popup or the Web panel.
// Idempotent by request_id: only the first caller wins.
func (s *Service) DecideInteractionHuman(requestID string, dec InteractionDecision) (InteractionView, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return InteractionView{}, automation.Fail("invalid_parameters", "request_id is required")
	}
	s.mu.Lock()
	pi, ok := s.interactions[requestID]
	s.mu.Unlock()
	if !ok {
		return InteractionView{}, automation.Fail("not_found", "Pending interaction not found or already resolved")
	}
	snap := pi.snapshot()
	dec.Actor = "human"
	if snap.Kind == InteractionApproval {
		switch dec.Decision {
		case DecisionAllowOnce, DecisionDeny, DecisionCancelTurn:
		case DecisionAllowSession:
			if !snap.AllowSessionOK || snap.HardRedline {
				return InteractionView{}, automation.Fail("forbidden", "Session auto-approve is not permitted for hard-redline requests")
			}
		default:
			return InteractionView{}, automation.Fail("invalid_parameters", "decision must be allow_once, allow_session, deny, or cancel_turn")
		}
	} else {
		switch dec.Decision {
		case DecisionAnswer, "":
			dec.Decision = DecisionAnswer
			if strings.TrimSpace(dec.Text) == "" && len(dec.Answers) == 0 {
				return InteractionView{}, automation.Fail("invalid_parameters", "answer text or answers map is required")
			}
		case DecisionCancelTurn:
		default:
			return InteractionView{}, automation.Fail("invalid_parameters", "decision must be answer or cancel_turn")
		}
	}
	if !pi.resolve(dec) {
		return InteractionView{}, automation.Fail("conflict", "Interaction was already resolved")
	}
	if dec.Decision == DecisionAllowSession && snap.Kind == InteractionApproval && !snap.HardRedline && snap.AllowSessionOK {
		now := s.now().UTC()
		s.updateWorkerState(snap.WorkerID, func(cur *Worker) {
			cur.SessionAutoApprove = true
			cur.UpdatedAt = now
		})
	}
	s.mu.Lock()
	delete(s.interactions, requestID)
	s.mu.Unlock()
	s.notifyInteractionsChanged()
	return snap, nil
}

type ReplyInput struct {
	OperationID     string
	TurnID          string
	WorkspaceID     string
	Text            string
	Answers         map[string][]string
	EscalateToHuman bool
	DeferSeconds    int
	Note            string
}

// ReplyTurn lets caller A answer a waiting_input question via workers_reply,
// defer escalation while verifying code, or explicitly escalate it to human UI
// with an optional verification note. Caller A is strictly forbidden from
// approving waiting_approval requests (EXEC-HARNESS-09).
func (s *Service) ReplyTurn(in ReplyInput) (Turn, bool, error) {
	turnID := strings.TrimSpace(in.TurnID)
	if turnID == "" {
		return Turn{}, false, automation.Fail("invalid_parameters", "turn_id is required")
	}
	t, err := s.store.GetTurn(turnID)
	if err != nil {
		return Turn{}, false, automation.Fail("not_found", "Turn not found")
	}
	if in.WorkspaceID != "" && t.WorkspaceID != in.WorkspaceID {
		return Turn{}, false, automation.Fail("workspace_mismatch", "Turn belongs to a different workspace")
	}
	if t.State == TurnWaitingApproval {
		return Turn{}, false, automation.Fail("forbidden", "Caller cannot approve waiting_approval requests; human confirmation is required in desktop or Web UI")
	}
	if t.State != TurnWaitingInput {
		return Turn{}, false, automation.Fail("conflict", fmt.Sprintf("Turn is no longer waiting for input (current state: %s; it may have been answered already)", t.State))
	}

	s.mu.Lock()
	var pi *pendingInteraction
	for _, cand := range s.interactions {
		if cand.snapshot().TurnID == turnID {
			pi = cand
			break
		}
	}
	s.mu.Unlock()
	if pi == nil {
		return Turn{}, false, automation.Fail("not_found", "Pending input request not found")
	}
	snap := pi.snapshot()
	if snap.Kind != InteractionInput {
		return Turn{}, false, automation.Fail("forbidden", "Caller cannot approve waiting_approval requests; human confirmation is required in desktop or Web UI")
	}

	hasAnswer := strings.TrimSpace(in.Text) != "" || len(in.Answers) > 0
	if in.EscalateToHuman && in.DeferSeconds > 0 {
		return Turn{}, false, automation.Fail("invalid_parameters", "Cannot combine escalate_to_human=true with defer_seconds")
	}
	if in.DeferSeconds > 0 && hasAnswer {
		return Turn{}, false, automation.Fail("invalid_parameters", "Cannot combine defer_seconds with answer text/answers")
	}

	if in.EscalateToHuman {
		note := truncateSummary(strings.TrimSpace(in.Note), 1000)
		s.escalateInteractionToHuman(snap.RequestID, turnID, note)
		logMsg := "Caller escalated question to human"
		if note != "" {
			logMsg += ": " + truncateSummary(note, 200)
		}
		s.appendInteractionLog(turnID, "input_escalated", logMsg)
		updated, _ := s.store.GetTurn(turnID)
		updated.PendingInteraction = s.pendingForTurn(turnID)
		return updated, true, nil
	}

	if in.DeferSeconds > 0 {
		if in.DeferSeconds < MinDeferSeconds || in.DeferSeconds > MaxDeferSeconds {
			return Turn{}, false, automation.Fail("invalid_parameters", fmt.Sprintf("defer_seconds must be between %d and %d", MinDeferSeconds, MaxDeferSeconds))
		}
		now := s.now().UTC()
		dur := time.Duration(in.DeferSeconds) * time.Second
		nextAt := now.Add(dur)
		note := truncateSummary(strings.TrimSpace(in.Note), 1000)
		pi.mu.Lock()
		if pi.resolved {
			pi.mu.Unlock()
			return Turn{}, false, automation.Fail("conflict", "Input request was already resolved")
		}
		if pi.view.VisibleToHuman {
			pi.mu.Unlock()
			return Turn{}, false, automation.Fail("conflict", "Question has already been escalated to human UI")
		}
		pi.view.ClaimedByCaller = true
		pi.view.DeferCount++
		pi.view.EscalateAt = &nextAt
		if note != "" {
			pi.view.EscalationNote = note
		}
		updatedSnap := pi.view
		pi.mu.Unlock()

		pi.resetEscalationTimer(dur)
		logMsg := fmt.Sprintf("Caller verifying question; deferred human escalation by %ds", in.DeferSeconds)
		if note != "" {
			logMsg += " (" + truncateSummary(note, 160) + ")"
		}
		if curT, gErr := s.store.GetTurn(turnID); gErr == nil && curT.State == TurnWaitingInput {
			curT.PendingInteraction = &updatedSnap
			curT.Progress = logMsg
			curT.UpdatedAt = now
			_ = s.store.SaveTurn(curT)
		}
		s.appendInteractionLog(turnID, "input_deferred", logMsg)
		s.notifyInteractionsChanged()
		updated, _ := s.store.GetTurn(turnID)
		updated.PendingInteraction = s.pendingForTurn(turnID)
		return updated, false, nil
	}

	if !hasAnswer {
		return Turn{}, false, automation.Fail("invalid_parameters", "Provide text/answers, defer_seconds, or set escalate_to_human=true")
	}
	if !pi.resolve(InteractionDecision{
		Decision: DecisionAnswer,
		Text:     strings.TrimSpace(in.Text),
		Answers:  in.Answers,
		Actor:    "caller",
	}) {
		return Turn{}, false, automation.Fail("conflict", "Input request was already resolved")
	}
	s.mu.Lock()
	delete(s.interactions, snap.RequestID)
	s.mu.Unlock()
	s.notifyInteractionsChanged()
	updated, _ := s.store.GetTurn(turnID)
	return updated, false, nil
}

// SetSessionAutoApproveHuman allows a human (Web UI) to enable or revoke the
// per-worker session auto-approve policy. Never exposed to caller A MCP.
func (s *Service) SetSessionAutoApproveHuman(workerID string, enabled bool) (Worker, error) {
	w, err := s.store.GetWorker(strings.TrimSpace(workerID))
	if err != nil {
		return Worker{}, automation.Fail("not_found", "Worker not found")
	}
	if w.State == WorkerClosed {
		return Worker{}, automation.Fail("conflict", "Closed worker cannot be modified")
	}
	if enabled && w.AccessMode == harnesses.AccessReadOnly {
		return Worker{}, automation.Fail("forbidden", "read_only worker cannot enable session auto-approve")
	}
	w.SessionAutoApprove = enabled
	w.UpdatedAt = s.now().UTC()
	if err := s.store.SaveWorker(w); err != nil {
		return Worker{}, err
	}
	return w, nil
}

func (s *Service) pendingForTurn(turnID string) *InteractionView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingForTurnLocked(turnID)
}

func (s *Service) appendInteractionLog(turnID, kind, summary string) {
	_, _ = s.store.AppendLog(turnID, LogEntry{
		Timestamp: s.now().UTC(),
		Kind:      kind,
		Summary:   truncateSummary(summary, 500),
	})
}

func formatDecisionLog(req InteractionRequest, dec InteractionDecision) string {
	if req.Kind == InteractionInput {
		if dec.Decision == DecisionCancelTurn {
			return fmt.Sprintf("Input cancelled (%s)", dec.Actor)
		}
		ans := strings.TrimSpace(dec.Text)
		if ans == "" && len(dec.Answers) > 0 {
			for _, vals := range dec.Answers {
				if len(vals) > 0 {
					ans = strings.Join(vals, ", ")
					break
				}
			}
		}
		return fmt.Sprintf("Input answered by %s: %s", firstNonEmptyStr(dec.Actor, "user"), truncateSummary(ans, 140))
	}
	return fmt.Sprintf("Approval %s by %s (%s)", dec.Decision, firstNonEmptyStr(dec.Actor, "user"), firstNonEmptyStr(req.ToolName, "action"))
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
