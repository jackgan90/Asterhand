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
	ctx             context.Context
	rt              *runningTurn
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

func (pi *pendingInteraction) claim() bool {
	pi.mu.Lock()
	if pi.resolved {
		pi.mu.Unlock()
		return false
	}
	pi.resolved = true
	pi.mu.Unlock()
	pi.stopEscalationTimer()
	return true
}

func (pi *pendingInteraction) deliver(dec InteractionDecision) {
	pi.decisionCh <- dec
}

func (pi *pendingInteraction) resolve(dec InteractionDecision) bool {
	if !pi.claim() {
		return false
	}
	pi.deliver(dec)
	return true
}

func (pi *pendingInteraction) markResolved() bool {
	return pi.claim()
}

func hasSecretQuestion(questions []InteractionQuestion) bool {
	for _, q := range questions {
		if q.IsSecret {
			return true
		}
	}
	return false
}

func defaultInteractionProgress(view InteractionView) string {
	if view.Kind == InteractionInput {
		return "Waiting for input: " + truncateSummary(view.Summary, 120)
	}
	return "Waiting for human approval: " + truncateSummary(view.Summary, 120)
}

func resolvedInteractionProgress(kind, decision string) string {
	if kind == InteractionApproval {
		return fmt.Sprintf("Approval resolved (%s); resuming...", decision)
	}
	return "Input delivered; resuming..."
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

func (s *Service) pendingListForTurnLocked(turnID string) []*pendingInteraction {
	type item struct {
		pi   *pendingInteraction
		view InteractionView
	}
	var list []item
	for _, pi := range s.interactions {
		if pi.isResolved() {
			continue
		}
		v := pi.snapshot()
		if v.TurnID == turnID {
			list = append(list, item{pi: pi, view: v})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		pi := interactionPriority(list[i].view.Kind)
		pj := interactionPriority(list[j].view.Kind)
		if pi != pj {
			return pi < pj
		}
		if !list[i].view.CreatedAt.Equal(list[j].view.CreatedAt) {
			return list[i].view.CreatedAt.Before(list[j].view.CreatedAt)
		}
		return list[i].view.RequestID < list[j].view.RequestID
	})
	out := make([]*pendingInteraction, len(list))
	for i := range list {
		out[i] = list[i].pi
	}
	return out
}

func (s *Service) pendingForTurnLocked(turnID string) *InteractionView {
	list := s.pendingListForTurnLocked(turnID)
	if len(list) == 0 {
		return nil
	}
	v := list[0].snapshot()
	return &v
}

// syncTurnInteractionStateLocked derives the turn's waiting/running state and
// timeout pause window strictly from the entire unresolved interaction set for
// turnID. waitingProgress applies only when updating a waiting turn (e.g. defer);
// resumingProgress applies only when the last pending interaction resolves.
// Caller must hold s.mu.
func (s *Service) syncTurnInteractionStateLocked(turnID string, rt *runningTurn, waitingProgress, resumingProgress string, now time.Time, resumeRunning bool) {
	if rt == nil {
		rt = s.activeTurns[turnID]
	}
	remaining := s.pendingListForTurnLocked(turnID)
	if len(remaining) > 0 {
		if rt != nil && rt.waitingSince.IsZero() {
			rt.waitingSince = now
		}
		head := remaining[0].snapshot()
		nextState := TurnWaitingInput
		if head.Kind == InteractionApproval {
			nextState = TurnWaitingApproval
		}
		if cur, err := s.store.GetTurn(turnID); err == nil && !IsTurnTerminal(cur.State) && cur.State != TurnReconcile {
			prevState := cur.State
			prevReqID := ""
			if cur.PendingInteraction != nil {
				prevReqID = cur.PendingInteraction.RequestID
			}
			cur.State = nextState
			cur.PendingInteraction = &head
			if waitingProgress != "" {
				cur.Progress = waitingProgress
			} else if prevReqID != head.RequestID || prevState != nextState || cur.Progress == "" {
				cur.Progress = defaultInteractionProgress(head)
			}
			cur.UpdatedAt = now
			_ = s.store.SaveTurn(cur)
		}
		return
	}

	if rt != nil {
		if !rt.waitingSince.IsZero() {
			rt.pausedTotal += now.Sub(rt.waitingSince)
			rt.waitingSince = time.Time{}
		}
		rt.lastEventAt = now
	}
	if cur, err := s.store.GetTurn(turnID); err == nil && !IsTurnTerminal(cur.State) && cur.State != TurnReconcile {
		cur.PendingInteraction = nil
		if resumeRunning {
			cur.State = TurnRunning
			if resumingProgress != "" {
				cur.Progress = resumingProgress
			}
		}
		cur.UpdatedAt = now
		_ = s.store.SaveTurn(cur)
	}
}

// handleTurnInteraction is called by adapters (from a worker goroutine, never
// holding adapter stdio locks) whenever a native harness requests permission
// approval or user input.
func (s *Service) handleTurnInteraction(ctx context.Context, rt *runningTurn, w *Worker, t *Turn, req InteractionRequest) (InteractionDecision, error) {
	if req.Kind == "" {
		req.Kind = InteractionApproval
	}
	now := s.now().UTC()
	turnID := t.ID

	// Read latest worker flags (e.g. session_auto_approve toggled by human) into a local snapshot.
	effWorker := *w
	if curWorker, err := s.store.GetWorker(w.ID); err == nil {
		effWorker = curWorker
	}

	risk := EvaluateInteractionRisk(s.home, effWorker.WorkspaceRoot, effWorker.AccessMode, req)

	if req.Kind == InteractionApproval {
		if risk.HardDeny {
			s.appendInteractionLog(turnID, "approval_denied", fmt.Sprintf("Blocked escalation (%s): %s", req.ToolName, strings.Join(risk.Reasons, "; ")))
			return InteractionDecision{Decision: DecisionDeny, Message: strings.Join(risk.Reasons, "; "), Actor: "policy"}, nil
		}
		if effWorker.ApprovalMode == ApprovalModeStrictDeny {
			s.appendInteractionLog(turnID, "approval_denied", fmt.Sprintf("Strict-deny policy blocked approval request (%s)", firstNonEmptyStr(req.Summary, req.ToolName)))
			return InteractionDecision{Decision: DecisionCancelTurn, Message: "Approval rejected under strict_deny approval_mode", Actor: "policy"}, nil
		}
		if effWorker.SessionAutoApprove && !risk.HardRedline {
			s.appendInteractionLog(turnID, "approval_auto", fmt.Sprintf("Session auto-approved %s: %s", firstNonEmptyStr(req.ToolName, "action"), firstNonEmptyStr(req.Summary, truncateSummary(req.Command, 120))))
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

	isSecretInput := req.Kind == InteractionInput && hasSecretQuestion(req.Questions)
	visibleToHuman := req.Kind == InteractionApproval || isSecretInput

	view := InteractionView{
		RequestID:        reqID,
		Kind:             req.Kind,
		WorkerID:         effWorker.ID,
		WorkerAlias:      effWorker.Alias,
		WorkspaceID:      effWorker.WorkspaceID,
		WorkspaceRoot:    effWorker.WorkspaceRoot,
		HarnessID:        effWorker.HarnessID,
		TurnID:           turnID,
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
		VisibleToHuman:   visibleToHuman,
		CreatedAt:        now,
	}
	if len(risk.Reasons) > 0 && view.Reason == "" {
		view.Reason = strings.Join(risk.Reasons, "; ")
	}

	pi := &pendingInteraction{
		view:            view,
		ctx:             ctx,
		rt:              rt,
		decisionCh:      make(chan InteractionDecision, 1),
		escalateStop:    make(chan struct{}),
		escalateResetCh: make(chan time.Duration, 1),
	}

	waitState := TurnWaitingApproval
	progressMsg := defaultInteractionProgress(view)
	var grace time.Duration
	if req.Kind == InteractionInput {
		waitState = TurnWaitingInput
		if !isSecretInput {
			grace = s.budgets.InputCallerGrace
			if grace <= 0 {
				grace = 15 * time.Second
			}
			escAt := now.Add(grace)
			pi.view.EscalateAt = &escAt
		}
	}

	s.mu.Lock()
	s.interactions[reqID] = pi
	s.syncTurnInteractionStateLocked(turnID, rt, "", "", now, false)
	s.mu.Unlock()

	s.appendInteractionLog(turnID, waitState, progressMsg)
	s.notifyWaiters(turnID)
	s.notifyInteractionsChanged()

	if req.Kind == InteractionInput && !isSecretInput {
		s.startEscalationTimer(reqID, turnID, pi, rt, grace)
	}

	dec := s.awaitInteractionDecision(ctx, rt, reqID, turnID, req, pi)
	pi.stopEscalationTimer()

	s.appendInteractionLog(turnID, "interaction_resolved", formatDecisionLog(req, dec))
	s.notifyWaiters(turnID)
	s.notifyInteractionsChanged()
	return dec, nil
}

func (s *Service) startEscalationTimer(reqID, turnID string, pi *pendingInteraction, rt *runningTurn, grace time.Duration) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		timer := time.NewTimer(grace)
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				s.escalateInteractionToHuman(reqID, turnID, "")
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

func (s *Service) awaitInteractionDecision(ctx context.Context, rt *runningTurn, reqID, turnID string, req InteractionRequest, pi *pendingInteraction) InteractionDecision {
	waitTimeout := s.budgets.InteractionTimeout
	if waitTimeout <= 0 {
		waitTimeout = 30 * time.Minute
	}
	timeoutTimer := time.NewTimer(waitTimeout)
	defer timeoutTimer.Stop()

	var fallback InteractionDecision
	select {
	case dec := <-pi.decisionCh:
		return dec
	case <-rt.cancelCh:
		fallback = InteractionDecision{Decision: DecisionCancelTurn, Message: "Turn cancelled while waiting for interaction", Actor: "policy"}
	case <-ctx.Done():
		fallback = InteractionDecision{Decision: DecisionCancelTurn, Message: "Context cancelled while waiting for interaction", Actor: "policy"}
	case <-timeoutTimer.C:
		fallback = InteractionDecision{Decision: DecisionDeny, Message: "Timed out waiting for user response", Actor: "timeout"}
	}

	// Arbitrate fallback against DecideInteractionHuman / ReplyTurn under s.mu
	// so only one winner can claim pi and commit any associated session grant.
	now := s.now().UTC()
	s.mu.Lock()
	won := pi.claim()
	if won {
		delete(s.interactions, reqID)
		s.syncTurnInteractionStateLocked(turnID, rt, "", resolvedInteractionProgress(req.Kind, fallback.Decision), now, fallback.Decision != DecisionCancelTurn)
	}
	s.mu.Unlock()
	if won {
		return fallback
	}
	return <-pi.decisionCh
}

func (s *Service) markInputObservedByCaller(turnID, requestID string) {
	s.mu.Lock()
	var pi *pendingInteraction
	if requestID != "" {
		if cand, ok := s.interactions[requestID]; ok && !cand.isResolved() && cand.snapshot().TurnID == turnID {
			pi = cand
		}
	} else {
		for _, cand := range s.pendingListForTurnLocked(turnID) {
			if cand.snapshot().Kind == InteractionInput {
				pi = cand
				break
			}
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
	pi.mu.Unlock()

	pi.resetEscalationTimer(claimedGrace)
	s.mu.Lock()
	s.syncTurnInteractionStateLocked(turnID, nil, "", "", now, false)
	s.mu.Unlock()
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
	pi.mu.Unlock()
	pi.stopEscalationTimer()

	now := s.now().UTC()
	s.mu.Lock()
	s.syncTurnInteractionStateLocked(turnID, nil, "", "", now, false)
	s.mu.Unlock()
	s.notifyWaiters(turnID)
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
	if !ok {
		s.mu.Unlock()
		return InteractionView{}, automation.Fail("not_found", "Pending interaction not found or already resolved")
	}
	snap := pi.snapshot()
	dec.Actor = "human"
	if snap.Kind == InteractionApproval {
		switch dec.Decision {
		case DecisionAllowOnce, DecisionDeny, DecisionCancelTurn:
		case DecisionAllowSession:
			if !snap.AllowSessionOK || snap.HardRedline {
				s.mu.Unlock()
				return InteractionView{}, automation.Fail("forbidden", "Session auto-approve is not permitted for hard-redline requests")
			}
		default:
			s.mu.Unlock()
			return InteractionView{}, automation.Fail("invalid_parameters", "decision must be allow_once, allow_session, deny, or cancel_turn")
		}
	} else {
		switch dec.Decision {
		case DecisionAnswer, "":
			dec.Decision = DecisionAnswer
			if strings.TrimSpace(dec.Text) == "" && len(dec.Answers) == 0 {
				s.mu.Unlock()
				return InteractionView{}, automation.Fail("invalid_parameters", "answer text or answers map is required")
			}
		case DecisionCancelTurn:
		default:
			s.mu.Unlock()
			return InteractionView{}, automation.Fail("invalid_parameters", "decision must be answer or cancel_turn")
		}
	}
	if pi.isResolved() || (pi.ctx != nil && pi.ctx.Err() != nil) || (pi.rt != nil && (pi.rt.cancelled || pi.rt.revoked)) {
		s.mu.Unlock()
		return InteractionView{}, automation.Fail("conflict", "Interaction was already resolved or cancelled")
	}
	now := s.now().UTC()
	if dec.Decision == DecisionAllowSession && snap.Kind == InteractionApproval && !snap.HardRedline && snap.AllowSessionOK {
		cur, err := s.store.GetWorker(snap.WorkerID)
		if err != nil {
			s.mu.Unlock()
			return InteractionView{}, err
		}
		if cur.State == WorkerClosed {
			s.mu.Unlock()
			return InteractionView{}, automation.Fail("conflict", "Worker is closed")
		}
		cur.SessionAutoApprove = true
		cur.UpdatedAt = now
		if err := s.store.SaveWorker(cur); err != nil {
			s.mu.Unlock()
			return InteractionView{}, err
		}
	}
	if !pi.claim() {
		s.mu.Unlock()
		return InteractionView{}, automation.Fail("conflict", "Interaction was already resolved")
	}
	delete(s.interactions, requestID)
	s.syncTurnInteractionStateLocked(snap.TurnID, nil, "", resolvedInteractionProgress(snap.Kind, dec.Decision), now, dec.Decision != DecisionCancelTurn)
	s.mu.Unlock()

	pi.deliver(dec)
	s.notifyWaiters(snap.TurnID)
	s.notifyInteractionsChanged()
	return snap, nil
}

type ReplyInput struct {
	OperationID     string
	TurnID          string
	RequestID       string
	WorkspaceID     string
	Text            string
	Answers         map[string][]string
	EscalateToHuman bool
	DeferSeconds    int
	Note            string
}

type ReplyResult struct {
	Turn       Turn
	RequestID  string
	Escalated  bool
	Deferred   bool
	EscalateAt *time.Time
}

// ReplyTurn lets caller A answer a waiting_input question via workers_reply,
// defer escalation while verifying code, or explicitly escalate it to human UI
// with an optional verification note. Caller A is strictly forbidden from
// approving waiting_approval requests (EXEC-HARNESS-09) or answering secret inputs.
func (s *Service) ReplyTurn(in ReplyInput) (Turn, bool, error) {
	res, err := s.ReplyTurnWithReceipt(in)
	return res.Turn, res.Escalated, err
}

func (s *Service) ReplyTurnWithReceipt(in ReplyInput) (ReplyResult, error) {
	turnID := strings.TrimSpace(in.TurnID)
	if turnID == "" {
		return ReplyResult{}, automation.Fail("invalid_parameters", "turn_id is required")
	}
	t, err := s.store.GetTurn(turnID)
	if err != nil {
		return ReplyResult{}, automation.Fail("not_found", "Turn not found")
	}
	if in.WorkspaceID != "" && t.WorkspaceID != in.WorkspaceID {
		return ReplyResult{}, automation.Fail("workspace_mismatch", "Turn belongs to a different workspace")
	}
	reqID := strings.TrimSpace(in.RequestID)
	if reqID == "" {
		if t.State == TurnWaitingApproval {
			return ReplyResult{}, automation.Fail("forbidden", "Caller cannot approve waiting_approval requests; human confirmation is required in desktop or Web UI")
		}
		if t.State != TurnWaitingInput {
			return ReplyResult{}, automation.Fail("conflict", fmt.Sprintf("Turn is no longer waiting for input (current state: %s; it may have been answered already)", t.State))
		}
	} else if t.State != TurnWaitingInput && t.State != TurnWaitingApproval {
		return ReplyResult{}, automation.Fail("conflict", fmt.Sprintf("Turn is no longer waiting for input (current state: %s; it may have been answered already)", t.State))
	}

	s.mu.Lock()
	var pi *pendingInteraction
	if reqID != "" {
		if cand, ok := s.interactions[reqID]; ok && !cand.isResolved() && cand.snapshot().TurnID == turnID {
			pi = cand
		}
	} else {
		ordered := s.pendingListForTurnLocked(turnID)
		if len(ordered) > 0 {
			pi = ordered[0]
		}
	}
	s.mu.Unlock()
	if pi == nil {
		return ReplyResult{}, automation.Fail("not_found", "Pending input request not found")
	}
	snap := pi.snapshot()
	if snap.Kind != InteractionInput {
		return ReplyResult{}, automation.Fail("forbidden", "Caller cannot approve waiting_approval requests; human confirmation is required in desktop or Web UI")
	}
	if hasSecretQuestion(snap.Questions) {
		return ReplyResult{}, automation.Fail("forbidden", "Secret input questions require direct human entry in desktop or Web UI")
	}

	hasAnswer := strings.TrimSpace(in.Text) != "" || len(in.Answers) > 0
	if in.EscalateToHuman && in.DeferSeconds > 0 {
		return ReplyResult{}, automation.Fail("invalid_parameters", "Cannot combine escalate_to_human=true with defer_seconds")
	}
	if in.DeferSeconds > 0 && hasAnswer {
		return ReplyResult{}, automation.Fail("invalid_parameters", "Cannot combine defer_seconds with answer text/answers")
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
		return ReplyResult{Turn: updated, RequestID: snap.RequestID, Escalated: true}, nil
	}

	if in.DeferSeconds > 0 {
		if in.DeferSeconds < MinDeferSeconds || in.DeferSeconds > MaxDeferSeconds {
			return ReplyResult{}, automation.Fail("invalid_parameters", fmt.Sprintf("defer_seconds must be between %d and %d", MinDeferSeconds, MaxDeferSeconds))
		}
		now := s.now().UTC()
		dur := time.Duration(in.DeferSeconds) * time.Second
		nextAt := now.Add(dur)
		note := truncateSummary(strings.TrimSpace(in.Note), 1000)
		pi.mu.Lock()
		if pi.resolved {
			pi.mu.Unlock()
			return ReplyResult{}, automation.Fail("conflict", "Input request was already resolved")
		}
		if pi.view.VisibleToHuman {
			pi.mu.Unlock()
			return ReplyResult{}, automation.Fail("conflict", "Question has already been escalated to human UI")
		}
		pi.view.ClaimedByCaller = true
		pi.view.DeferCount++
		pi.view.EscalateAt = &nextAt
		if note != "" {
			pi.view.EscalationNote = note
		}
		pi.mu.Unlock()

		pi.resetEscalationTimer(dur)
		logMsg := fmt.Sprintf("Caller verifying question; deferred human escalation by %ds", in.DeferSeconds)
		if note != "" {
			logMsg += " (" + truncateSummary(note, 160) + ")"
		}
		s.mu.Lock()
		waitProg := ""
		if head := s.pendingForTurnLocked(turnID); head != nil && head.RequestID == snap.RequestID {
			waitProg = logMsg
		}
		s.syncTurnInteractionStateLocked(turnID, nil, waitProg, "", now, false)
		s.mu.Unlock()
		s.appendInteractionLog(turnID, "input_deferred", logMsg)
		s.notifyInteractionsChanged()
		updated, _ := s.store.GetTurn(turnID)
		updated.PendingInteraction = s.pendingForTurn(turnID)
		return ReplyResult{Turn: updated, RequestID: snap.RequestID, Deferred: true, EscalateAt: &nextAt}, nil
	}

	if !hasAnswer {
		return ReplyResult{}, automation.Fail("invalid_parameters", "Provide text/answers, defer_seconds, or set escalate_to_human=true")
	}
	dec := InteractionDecision{
		Decision: DecisionAnswer,
		Text:     strings.TrimSpace(in.Text),
		Answers:  in.Answers,
		Actor:    "caller",
	}
	now := s.now().UTC()
	s.mu.Lock()
	if !pi.claim() {
		s.mu.Unlock()
		return ReplyResult{}, automation.Fail("conflict", "Input request was already resolved")
	}
	delete(s.interactions, snap.RequestID)
	s.syncTurnInteractionStateLocked(turnID, nil, "", resolvedInteractionProgress(InteractionInput, DecisionAnswer), now, true)
	s.mu.Unlock()

	pi.deliver(dec)
	s.notifyWaiters(turnID)
	s.notifyInteractionsChanged()
	updated, _ := s.store.GetTurn(turnID)
	updated.PendingInteraction = s.pendingForTurn(turnID)
	return ReplyResult{Turn: updated, RequestID: snap.RequestID}, nil
}

// SetSessionAutoApproveHuman allows a human (Web UI) to enable or revoke the
// per-worker session auto-approve policy. Synchronized under s.mu with
// DecideInteractionHuman so revokes can never be overwritten by a late grant.
func (s *Service) SetSessionAutoApproveHuman(workerID string, enabled bool) (Worker, error) {
	id := strings.TrimSpace(workerID)
	var toClose *residentProc
	s.mu.Lock()
	w, err := s.store.GetWorker(id)
	if err != nil {
		s.mu.Unlock()
		return Worker{}, automation.Fail("not_found", "Worker not found")
	}
	if w.State == WorkerClosed {
		s.mu.Unlock()
		return Worker{}, automation.Fail("conflict", "Closed worker cannot be modified")
	}
	if enabled && w.AccessMode == harnesses.AccessReadOnly {
		s.mu.Unlock()
		return Worker{}, automation.Fail("forbidden", "read_only worker cannot enable session auto-approve")
	}
	w.SessionAutoApprove = enabled
	w.UpdatedAt = s.now().UTC()
	if err := s.store.SaveWorker(w); err != nil {
		s.mu.Unlock()
		return Worker{}, err
	}
	if !enabled {
		if _, busy := s.workerTurn[w.ID]; !busy {
			if rp, ok := s.residents[w.ID]; ok {
				toClose = rp
				delete(s.residents, w.ID)
			}
		}
	}
	s.mu.Unlock()
	if toClose != nil {
		if ad := s.adapters[toClose.harnessID]; ad != nil {
			_ = ad.CloseThread(toClose.handle)
		}
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
		if hasSecretQuestion(req.Questions) {
			return fmt.Sprintf("Input answered by %s (secret redacted)", firstNonEmptyStr(dec.Actor, "user"))
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
