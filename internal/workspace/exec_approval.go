package workspace

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
)

type approvalTicket struct {
	id              string
	callID          string
	scope           Scope
	req             ExecStartRequest
	homeRoot        string
	nodeDir         string
	timeoutDur      time.Duration
	yieldDur        time.Duration
	externalDirs    []string
	scopeKind       string
	scopeTarget     string
	allFolders      []string
	inv             commandInvocation
	view            ApprovalView
	snapshot        ticketSnapshot
	createdAt       time.Time
	deadline        time.Time
	decidedAt       time.Time
	decideCh        chan struct{}
	decideOnce      sync.Once
	execDoneCh      chan struct{}
	execDoneOnce    sync.Once
	execRes         ExecResult
	execErr         error
	decision        string
	cancelReason    string
	waiters         int
	claimed         bool
	barrierReleased bool
	settled         bool
}

func (tk *approvalTicket) finishExecution(res ExecResult, err error) {
	tk.execRes = res
	tk.execErr = err
	tk.settled = true
	tk.execDoneOnce.Do(func() { close(tk.execDoneCh) })
}

func (m *execManager) cancelApprovalTicketLocked(tk *approvalTicket, reason string) bool {
	if tk == nil {
		return false
	}
	wakeSched := false
	if !tk.barrierReleased {
		tk.barrierReleased = true
		if m.svc.sched.awaitingApproval.Add(-1) < 0 {
			m.svc.sched.awaitingApproval.Store(0)
		}
		wakeSched = true
	}
	if tk.decision == "" && tk.cancelReason == "" {
		if reason == "" {
			reason = "cancelled"
		}
		tk.cancelReason = reason
		tk.view.Status = "cancelled"
		tk.decideOnce.Do(func() { close(tk.decideCh) })
	}
	if !tk.claimed && tk.waiters == 0 && !tk.settled {
		tk.claimed = true
		tk.finishExecution(ExecResult{}, fail(reason))
		m.broadcastSettledLocked()
	}
	return wakeSched
}

func (s *Service) startApprovalTicket(c *call, e *schedEntry, scope Scope, req ExecStartRequest, decision policyDecision, homeRoot, nodeDir string, yieldDur, timeoutDur time.Duration) (ExecResult, error) {
	snap, err := captureTicketSnapshot(s.root, req, decision.externalDirs, decision.inv)
	if err != nil {
		return ExecResult{}, err
	}
	preview := redactCommandSummary(req.Command)
	if len(preview) > 280 {
		preview = preview[:280] + "..."
	}
	now := time.Now().UTC()
	tk := &approvalTicket{
		id:           "ap_" + rand.Text(),
		callID:       c.op.ID,
		scope:        scope,
		req:          req,
		homeRoot:     homeRoot,
		nodeDir:      nodeDir,
		timeoutDur:   timeoutDur,
		yieldDur:     yieldDur,
		externalDirs: append([]string(nil), decision.externalDirs...),
		scopeKind:    decision.scopeKind,
		scopeTarget:  decision.scopeTarget,
		allFolders:   append([]string(nil), decision.allFolders...),
		inv:          decision.inv,
		snapshot:     snap,
		createdAt:    now,
		deadline:     now.Add(ApprovalWaitTimeout),
		decideCh:     make(chan struct{}),
		execDoneCh:   make(chan struct{}),
		view: ApprovalView{
			ID:             "",
			Status:         "pending",
			Kind:           decision.scopeKind,
			Target:         decision.scopeTarget,
			Targets:        append([]string(nil), decision.scopeTargets...),
			Reason:         decision.reason,
			CommandPreview: preview,
			CanSession:     decision.canSession,
			CanAlways:      decision.canAlways,
		},
	}
	tk.view.ID = tk.id

	m := s.exec
	m.mu.Lock()
	if m.turnStopped[scope.Turn] != "" {
		m.mu.Unlock()
		return ExecResult{}, fail("cancelled")
	}
	m.approvals[tk.id] = tk
	m.fgApprovalsByCall[tk.callID] = tk
	m.mu.Unlock()

	s.sched.mu.Lock()
	e.phase = PhaseAwaitingApproval
	e.waitReason = "approval_pending"
	s.sched.awaitingApproval.Add(1)
	s.sched.broadcastLocked()
	s.sched.mu.Unlock()

	s.updateCallApprovalRecord(c.op.ID, PhaseAwaitingApproval, "approval_pending", "pending", "", &tk.view)

	m.policyMu.RLock()
	notifyFn := m.onApprovalPending
	m.policyMu.RUnlock()
	if notifyFn != nil {
		notifyFn()
	}

	return ExecResult{
		Status:     "approval_required",
		ApprovalID: tk.id,
	}, nil
}

func (s *Service) updateCallApprovalRecord(callID, phase, waitReason, approvalMode, detail string, view *ApprovalView) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.callsByID[callID]
	if c == nil {
		return
	}
	s.sequence++
	c.op.Sequence = s.sequence
	if phase != "" {
		c.op.Phase = phase
	}
	c.op.WaitReason = waitReason
	if approvalMode != "" {
		c.op.ApprovalMode = approvalMode
	}
	if detail != "" {
		c.op.Detail = detail
	}
	if view != nil {
		cp := *view
		c.op.Approval = &cp
	}
	s.records[callID] = publicOperation(c.op)
}

func (s *Service) WaitApproval(ctx context.Context, req ExecApprovalWaitRequest) (ExecResult, error) {
	if strings.TrimSpace(req.ApprovalID) == "" {
		return ExecResult{}, fail("invalid_request")
	}
	c, e, _, summary, homeRoot, nodeDir, err := s.enterExecCall(req.CallID, "exec_command", true)
	if err != nil {
		return ExecResult{}, err
	}
	defer s.leaveCallIO(e, EffectWrite, DefaultRoot, "")

	m := s.exec
	m.mu.Lock()
	tk := m.approvals[req.ApprovalID]
	if tk == nil {
		tk = m.resolvedApprovals[req.ApprovalID]
	}
	if tk == nil || tk.callID != c.op.ID {
		m.mu.Unlock()
		return ExecResult{}, fail("not_found")
	}
	if tk.claimed {
		doneCh := tk.execDoneCh
		m.mu.Unlock()
		return waitClaimedTicketResult(ctx, tk, doneCh)
	}
	claim := s.waitAndClaimTicketLocked(ctx, m, tk, req.WaitMs)
	if !claim.claimed {
		return claim.res, claim.err
	}
	m.mu.Lock()
	afterClaimHook := m.onAfterClaimBeforeSpawn
	m.mu.Unlock()
	if afterClaimHook != nil {
		afterClaimHook(c.op.ID)
	}

	var res ExecResult
	var resErr error
	defer func() {
		m.mu.Lock()
		tk.finishExecution(res, resErr)
		m.broadcastSettledLocked()
		m.mu.Unlock()
	}()

	switch {
	case claim.expired:
		waitMs := approvalWaitDurationMs(tk.createdAt, tk.deadline)
		s.transitionApprovalToRunning(e, c.op.ID, "timeout", "approval_timeout", &claim.viewCopy)
		res = ExecResult{Status: "approval_timeout", ApprovalID: tk.id, Escalated: true, ApprovalMode: "timeout", ApprovalWaitMs: waitMs, EscalationDenied: true}
	case claim.cancelReason != "":
		s.transitionApprovalToRunning(e, c.op.ID, "cancelled", "", &claim.viewCopy)
		resErr = fail(claim.cancelReason)
	case claim.decision == "deny":
		waitMs := approvalWaitDurationMs(tk.createdAt, claim.decidedAt)
		s.transitionApprovalToRunning(e, c.op.ID, "denied", "denied_by_user", &claim.viewCopy)
		res = ExecResult{Status: "denied_by_user", ApprovalID: tk.id, Escalated: true, ApprovalMode: "denied", ApprovalDecision: "deny", ApprovalWaitMs: waitMs, EscalationDenied: true}
	default:
		res, resErr = s.spawnApprovedTicket(ctx, c, e, tk, claim.viewCopy, claim.decidedAt, summary, homeRoot, nodeDir)
	}
	return res, resErr
}

type ticketClaim struct {
	claimed      bool
	res          ExecResult
	err          error
	viewCopy     ApprovalView
	decision     string
	cancelReason string
	expired      bool
	decidedAt    time.Time
}

func (s *Service) waitAndClaimTicketLocked(ctx context.Context, m *execManager, tk *approvalTicket, waitMs *int) ticketClaim {
	tk.waiters++
	decideCh := tk.decideCh
	deadline := tk.deadline
	m.mu.Unlock()

	if tkDecision, cancelReason := readTicketResolution(m, tk, deadline); tkDecision == "" && cancelReason == "" {
		waitDur := clampApprovalWait(waitMs, deadline)
		if waitDur > 0 {
			timer := time.NewTimer(waitDur)
			select {
			case <-decideCh:
				timer.Stop()
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
	}

	m.mu.Lock()
	if tk.waiters > 0 {
		tk.waiters--
	}
	if tk.claimed {
		doneCh := tk.execDoneCh
		m.mu.Unlock()
		res, err := waitClaimedTicketResult(ctx, tk, doneCh)
		return ticketClaim{res: res, err: err}
	}
	decision := tk.decision
	cancelReason := tk.cancelReason
	expired := decision == "" && cancelReason == "" && !time.Now().Before(tk.deadline)
	viewCopy := tk.view
	if decision == "" && cancelReason == "" && !expired {
		m.mu.Unlock()
		if ctx.Err() != nil {
			return ticketClaim{err: fail("cancelled")}
		}
		return ticketClaim{res: ExecResult{Status: "approval_required", ApprovalID: tk.id}}
	}
	tk.claimed = true
	wakeSched := false
	if !tk.barrierReleased {
		tk.barrierReleased = true
		if s.sched.awaitingApproval.Add(-1) < 0 {
			s.sched.awaitingApproval.Store(0)
		}
		wakeSched = true
	}
	if expired && tk.view.Status == "pending" {
		tk.view.Status = "expired"
		viewCopy = tk.view
	}
	delete(m.approvals, tk.id)
	delete(m.fgApprovalsByCall, tk.callID)
	m.resolvedApprovals[tk.id] = tk
	m.mu.Unlock()
	if wakeSched {
		s.sched.wake()
	}
	return ticketClaim{
		claimed:      true,
		viewCopy:     viewCopy,
		decision:     decision,
		cancelReason: cancelReason,
		expired:      expired,
		decidedAt:    tk.decidedAt,
	}
}

func waitClaimedTicketResult(ctx context.Context, tk *approvalTicket, doneCh <-chan struct{}) (ExecResult, error) {
	select {
	case <-doneCh:
		return tk.execRes, tk.execErr
	case <-ctx.Done():
		return ExecResult{}, fail("cancelled")
	}
}

func approvalWaitDurationMs(start, end time.Time) int64 {
	if start.IsZero() || end.IsZero() {
		return 0
	}
	return max(int64(0), end.Sub(start).Milliseconds())
}

func decisionToApprovalMode(decision string) string {
	switch decision {
	case "allow_session":
		return "session_allow"
	case "allow_always":
		return "rule_allow"
	default:
		return "once"
	}
}

func (s *Service) spawnApprovedTicket(ctx context.Context, c *call, e *schedEntry, tk *approvalTicket, viewCopy ApprovalView, decidedAt time.Time, summary, homeRoot, nodeDir string) (ExecResult, error) {
	if err := s.checkCallCancelled(c.op.ID, tk.scope.Turn); err != nil {
		viewCopy.Status = "cancelled"
		s.transitionApprovalToRunning(e, c.op.ID, "cancelled", "", &viewCopy)
		return ExecResult{}, err
	}
	if err := s.ensureExecQuotaAndActivity(); err != nil {
		return ExecResult{}, err
	}
	reservedActivity := true
	defer func() {
		if reservedActivity {
			s.refundExecActivity()
		}
	}()
	if ctx.Err() != nil {
		viewCopy.Status = "cancelled"
		s.transitionApprovalToRunning(e, c.op.ID, "cancelled", "", &viewCopy)
		return ExecResult{}, fail("cancelled")
	}
	extDirs := ensureNonNilStrings(tk.externalDirs)
	modeTag := decisionToApprovalMode(tk.decision)
	meta := execApprovalMeta{
		approvalMode:     modeTag,
		userApproved:     true,
		approvalDecision: tk.decision,
		approvalWaitMs:   approvalWaitDurationMs(tk.createdAt, decidedAt),
	}
	sess, err := s.spawnExecSession(c.op.ID, tk.scope, summary, tk.req, extDirs, true, tk.snapshot.exePath, tk, false, homeRoot, nodeDir, tk.timeoutDur, meta)
	if err != nil {
		switch Code(err) {
		case "approval_tampered":
			viewCopy.Status = "cancelled"
			s.transitionApprovalToRunning(e, c.op.ID, "tampered", "approval_tampered", &viewCopy)
		case "cancelled", "runtime_stopped":
			viewCopy.Status = "cancelled"
			s.transitionApprovalToRunning(e, c.op.ID, "cancelled", "", &viewCopy)
		}
		return ExecResult{}, err
	}
	if err := s.checkCallCancelled(c.op.ID, tk.scope.Turn); err != nil {
		_, _ = s.abortSyncExec(sess)
		viewCopy.Status = "cancelled"
		s.transitionApprovalToRunning(e, c.op.ID, "cancelled", "", &viewCopy)
		return ExecResult{}, err
	}
	var trustPersistErr error
	if tk.decision == "allow_session" || tk.decision == "allow_always" {
		rulesToAdd := buildApprovalTrustRules(tk, time.Now().UTC().Format(time.RFC3339))
		trustPersistErr = s.applyApprovedTrust(tk.scope.Conversation, tk.scopeKind, tk.decision, rulesToAdd)
	}
	if trustPersistErr != nil {
		modeTag = "once"
		s.exec.mu.Lock()
		sess.approvalMode = "once"
		s.exec.mu.Unlock()
	}
	viewCopy.Status = "approved"
	s.transitionApprovalToRunning(e, c.op.ID, modeTag, "", &viewCopy)

	res, keepActivity, err := s.runForegroundExecWait(ctx, e, sess, tk.yieldDur)
	if keepActivity {
		reservedActivity = false
	}
	if trustPersistErr != nil {
		code := Code(trustPersistErr)
		res.RulePersistError = code
		msg := "trust_persist_failed:" + code
		if res.Warning == "" {
			res.Warning = msg
		} else {
			res.Warning += ";" + msg
		}
	}
	return res, err
}

func (s *Service) transitionApprovalToRunning(e *schedEntry, callID, approvalMode, detail string, view *ApprovalView) {
	s.sched.mu.Lock()
	if e.phase == PhaseAwaitingApproval {
		e.phase = PhaseRunning
		e.waitReason = ""
	}
	s.sched.broadcastLocked()
	s.sched.mu.Unlock()
	s.updateCallApprovalRecord(callID, PhaseRunning, "", approvalMode, detail, view)
}

func (s *Service) ApproveTool(conversationID, approvalID, decision string) error {
	_, err := s.ApproveToolCall(conversationID, approvalID, decision)
	return err
}

func (s *Service) ApproveToolCall(conversationID, approvalID, decision string) (string, error) {
	if s == nil || s.exec == nil {
		return "", fail("unsupported_platform")
	}
	if decision != "allow_once" && decision != "allow_session" && decision != "allow_always" && decision != "deny" {
		return "", fail("invalid_request")
	}
	m := s.exec
	m.mu.Lock()
	tk := m.approvals[approvalID]
	if tk == nil || tk.scope.Conversation != conversationID {
		m.mu.Unlock()
		return "", fail("not_found")
	}
	if tk.decision != "" || tk.cancelReason != "" || tk.claimed || tk.settled {
		m.mu.Unlock()
		return "", fail("already_resolved")
	}
	if decision == "allow_session" && !tk.view.CanSession {
		m.mu.Unlock()
		return "", fail("invalid_request")
	}
	if decision == "allow_always" && !tk.view.CanAlways {
		m.mu.Unlock()
		return "", fail("broad_scope_denied")
	}
	wakeSched := false
	if !time.Now().Before(tk.deadline) {
		tk.view.Status = "expired"
		if !tk.barrierReleased {
			tk.barrierReleased = true
			if s.sched.awaitingApproval.Add(-1) < 0 {
				s.sched.awaitingApproval.Store(0)
			}
			wakeSched = true
		}
		if tk.waiters == 0 && !tk.claimed {
			tk.claimed = true
			tk.finishExecution(ExecResult{Status: "approval_timeout", ApprovalID: tk.id, Escalated: true, ApprovalMode: "timeout", ApprovalWaitMs: approvalWaitDurationMs(tk.createdAt, tk.deadline), EscalationDenied: true}, nil)
			m.broadcastSettledLocked()
		}
		tk.decideOnce.Do(func() { close(tk.decideCh) })
		m.mu.Unlock()
		if wakeSched {
			s.sched.wake()
		}
		return "", fail("approval_timeout")
	}
	tk.decision = decision
	tk.decidedAt = time.Now()
	approvalMode := "once"
	if decision == "deny" {
		tk.view.Status = "denied"
		approvalMode = "denied"
	} else {
		tk.view.Status = "approved"
		if decision == "allow_session" {
			approvalMode = "session_allow"
		} else if decision == "allow_always" {
			approvalMode = "rule_allow"
		}
	}
	if !tk.barrierReleased {
		tk.barrierReleased = true
		if s.sched.awaitingApproval.Add(-1) < 0 {
			s.sched.awaitingApproval.Store(0)
		}
		wakeSched = true
	}
	viewCopy := tk.view
	callID := tk.callID
	tk.decideOnce.Do(func() { close(tk.decideCh) })
	m.mu.Unlock()

	if wakeSched {
		s.sched.wake()
	}
	s.updateCallApprovalRecord(callID, "", "", approvalMode, "", &viewCopy)
	return callID, nil
}

func (s *Service) applyApprovedTrust(convID, scopeKind, decision string, rulesToAdd []config.TrustRule) error {
	m := s.exec
	switch decision {
	case "allow_session":
		m.policyMu.Lock()
		if scopeKind == "workspace_escalated" {
			m.sessionWorkspaceEscalated[convID] = true
		} else {
			existing := m.sessionRules[convID]
			for _, rule := range rulesToAdd {
				if !trustRuleExists(existing, rule) {
					existing = append(existing, rule)
				}
			}
			m.sessionRules[convID] = existing
		}
		m.policyMu.Unlock()
	case "allow_always":
		if len(rulesToAdd) == 0 {
			return nil
		}
		m.policyMu.RLock()
		persistFn := m.persistTrustFn
		m.policyMu.RUnlock()
		if persistFn != nil {
			return persistFn(rulesToAdd)
		}
		m.policyMu.Lock()
		for _, rule := range rulesToAdd {
			if !trustRuleExists(m.trustRules, rule) {
				m.trustRules = append(m.trustRules, rule)
			}
		}
		m.policyMu.Unlock()
	}
	return nil
}

func buildApprovalTrustRules(tk *approvalTicket, createdAt string) []config.TrustRule {
	if tk.scopeKind == "folder" && len(tk.allFolders) > 0 {
		rules := make([]config.TrustRule, 0, len(tk.allFolders))
		for _, folder := range tk.allFolders {
			rules = append(rules, config.TrustRule{
				ID:        "tr_" + rand.Text(),
				Kind:      "folder",
				Pattern:   folder,
				Action:    "allow",
				Label:     folder,
				CreatedAt: createdAt,
			})
		}
		return rules
	}
	if tk.scopeKind == "command" && tk.inv.resolvedExe != "" {
		label := filepath.Base(tk.inv.resolvedExe)
		if dir := filepath.Dir(tk.inv.resolvedExe); dir != "" && dir != "." {
			label = label + " (" + dir + ")"
		}
		return []config.TrustRule{{
			ID:        "tr_" + rand.Text(),
			Kind:      "command",
			Pattern:   tk.inv.resolvedExe,
			Action:    "allow",
			Label:     label,
			CreatedAt: createdAt,
		}}
	}
	return nil
}

// PendingApprovals returns a bounded, Sequence-sorted list of active calls awaiting
// human approval in the given conversation (and optional turn), independent of activity paging.
func (s *Service) PendingApprovals(conversation, turn string) []Operation {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []Operation
	for _, c := range s.callsByID {
		op := c.op
		if conversation != "" && op.Scope.Conversation != conversation {
			continue
		}
		if turn != "" && op.Scope.Turn != turn {
			continue
		}
		if op.State != "started" || op.Phase != PhaseAwaitingApproval || op.Approval == nil || op.Approval.Status != "pending" {
			continue
		}
		pub := publicOperation(op)
		pub.Changes = nil
		items = append(items, pub)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Sequence < items[j].Sequence })
	return items
}

func readTicketResolution(m *execManager, tk *approvalTicket, deadline time.Time) (string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if tk.decision != "" || tk.cancelReason != "" {
		return tk.decision, tk.cancelReason
	}
	if !time.Now().Before(deadline) {
		return "timeout", ""
	}
	return "", ""
}

func clampApprovalWait(waitPtr *int, deadline time.Time) time.Duration {
	ms := MaxExecHttpPollMs
	if waitPtr != nil {
		ms = *waitPtr
	}
	if ms < 0 {
		ms = 0
	}
	if ms > MaxExecHttpPollMs {
		ms = MaxExecHttpPollMs
	}
	dur := time.Duration(ms) * time.Millisecond
	rem := time.Until(deadline)
	if rem < 0 {
		return 0
	}
	if rem < dur {
		return rem
	}
	return dur
}
