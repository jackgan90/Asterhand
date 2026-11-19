package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackgan90/Asterhand/internal/platform/sandbox"
)

func (s *Service) SetHomeRoot(home string) {
	s.mu.Lock()
	s.homeRoot = home
	s.mu.Unlock()
}

func (s *Service) SetBundledNodeDir(dir string) {
	s.mu.Lock()
	s.bundledNodeDir = dir
	s.mu.Unlock()
}

func (s *Service) enterExecCall(id, expectedTool string, allowAwaiting bool) (*call, *schedEntry, Scope, string, string, string, error) {
	s.mu.Lock()
	if !s.enabled || !platformSupported() {
		s.mu.Unlock()
		return nil, nil, Scope{}, "", "", "", fail("not_authorized")
	}
	if s.blocked {
		s.mu.Unlock()
		return nil, nil, Scope{}, "", "", "", fail("reconciliation_required")
	}
	c := s.lookupActiveCallLocked(id)
	homeRoot := s.homeRoot
	nodeDir := s.bundledNodeDir
	s.mu.Unlock()
	if c == nil || c.entry == nil {
		return nil, nil, Scope{}, "", "", "", fail("invalid_call")
	}
	e := c.entry
	s.sched.mu.Lock()
	phaseOK := e.phase == PhaseRunning || e.phase == PhasePreparing || (allowAwaiting && e.phase == PhaseAwaitingApproval)
	if e.tool != expectedTool || !phaseOK || e.admissionClosed {
		reason := "invalid_call"
		if e.admissionClosed {
			reason = "cancelled"
		}
		s.sched.mu.Unlock()
		return nil, nil, Scope{}, "", "", "", fail(reason)
	}
	e.inflight++
	s.sched.mu.Unlock()
	return c, e, c.op.Scope, c.op.Summary, homeRoot, nodeDir, nil
}

func (s *Service) StartExec(ctx context.Context, req ExecStartRequest) (ExecResult, error) {
	if strings.TrimSpace(req.Command) == "" || len(req.Command) > MaxExecCommandBytes || len(req.EscalationReason) > MaxEscalationReasonInputBytes || len(req.ExternalPaths) > MaxExternalPaths {
		return ExecResult{}, fail("invalid_request")
	}
	if !s.exec.Supported() {
		return ExecResult{}, fail("unsupported_platform")
	}
	c, e, scope, summary, homeRoot, nodeDir, err := s.enterExecCall(req.CallID, "exec_command", false)
	if err != nil {
		return ExecResult{}, err
	}
	defer s.leaveCallIO(e, EffectWrite, DefaultRoot, "")
	if scope.Turn == "" {
		return ExecResult{}, fail("invalid_call")
	}
	decision, err := s.evaluateExecPolicy(scope.Conversation, summary, req, homeRoot, nodeDir)
	if err != nil {
		return ExecResult{}, err
	}
	switch decision.kind {
	case decisionDeny:
		s.updateCallApprovalRecord(c.op.ID, "", "", "denied", decision.code, nil)
		return ExecResult{Status: decision.code, EscalationDenied: true}, nil
	case decisionAsk:
		if ctx.Err() != nil {
			return ExecResult{}, fail("cancelled")
		}
		yieldDur, timeoutDur := clampExecDurations(req.YieldMs, req.TimeoutSec)
		return s.startApprovalTicket(c, e, scope, req, decision, homeRoot, nodeDir, yieldDur, timeoutDur)
	}
	if decision.approvalMode != "" {
		s.updateCallApprovalRecord(c.op.ID, "", "", decision.approvalMode, "", nil)
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
		return ExecResult{}, fail("cancelled")
	}
	yieldDur, timeoutDur := clampExecDurations(req.YieldMs, req.TimeoutSec)
	escalated := decision.kind == decisionAllowTier2
	meta := execApprovalMeta{approvalMode: decision.approvalMode}
	sess, err := s.spawnExecSession(c.op.ID, scope, summary, req, decision.externalDirs, escalated, decision.inv.resolvedExe, nil, true, homeRoot, nodeDir, timeoutDur, meta)
	if err != nil {
		if !escalated && Code(err) == "sensitive_path_denied" && isOutsideWorkspaceWorkdirCandidate(req.Workdir, s.root, s.exec.tempRoot, homeRoot) {
			return ExecResult{Status: "failed", Reason: "outside_workspace", EscalationAvailable: true}, nil
		}
		return ExecResult{}, err
	}
	res, keepActivity, err := s.runForegroundExecWait(ctx, e, sess, yieldDur)
	if keepActivity {
		reservedActivity = false
	}
	return res, err
}

func (s *Service) ensureExecQuotaAndActivity() error {
	s.mu.Lock()
	over := s.overQuota
	s.mu.Unlock()
	if over {
		return fail("workspace_limit")
	}
	if _, _, err := s.measureWorkspaceDisk(0, 0); err != nil {
		if Code(err) == "workspace_limit" {
			s.mu.Lock()
			s.overQuota = true
			s.mu.Unlock()
		}
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.pruneAndReserveActivityLocked(MaxOperationBytes); err != nil {
		return err
	}
	s.reservedActivityBytes += MaxOperationBytes
	return nil
}

func (s *Service) refundExecActivity() {
	s.mu.Lock()
	if s.reservedActivityBytes >= MaxOperationBytes {
		s.reservedActivityBytes -= MaxOperationBytes
	} else {
		s.reservedActivityBytes = 0
	}
	s.mu.Unlock()
}

func clampExecDurations(yieldMsPtr *int, timeoutSec int) (yieldDur, timeoutDur time.Duration) {
	yieldMs := DefaultExecYieldMs
	if yieldMsPtr != nil {
		yieldMs = *yieldMsPtr
	}
	if yieldMs < 0 {
		yieldMs = 0
	}
	if yieldMs > MaxExecYieldMs {
		yieldMs = MaxExecYieldMs
	}
	if timeoutSec <= 0 {
		timeoutSec = DefaultExecTimeoutSec
	}
	if timeoutSec > MaxExecTimeoutSec {
		timeoutSec = MaxExecTimeoutSec
	}
	return time.Duration(yieldMs) * time.Millisecond, time.Duration(timeoutSec) * time.Second
}

func (s *Service) spawnExecSession(callID string, scope Scope, summary string, req ExecStartRequest, externalDirs []string, escalated bool, expectedExe string, tk *approvalTicket, skipStaticCmdScan bool, homeRoot, nodeDir string, timeout time.Duration, approvalMeta execApprovalMeta) (*execSession, error) {
	if err := s.checkCallCancelled(callID, scope.Turn); err != nil {
		return nil, err
	}
	verifiedInv, lockedFiles, err := s.verifyAndLockPreSpawn(req, externalDirs, escalated, expectedExe, tk, skipStaticCmdScan, homeRoot, nodeDir)
	if err != nil {
		return nil, err
	}
	m := s.exec
	launchCtx, cancelLaunch := context.WithCancel(context.Background())
	sess, err := m.preRegisterSpawningSession(callID, scope, summary, req.Command, escalated, approvalMeta, lockedFiles, cancelLaunch)
	if err != nil {
		cancelLaunch()
		closeLockedHandles(lockedFiles)
		return nil, err
	}
	if err := s.checkCallCancelled(callID, scope.Turn); err != nil {
		cancelLaunch()
		m.rollbackSpawningSession(callID, sess)
		return nil, err
	}
	spec := buildExecSpawnSpec(s.root, m.tempRoot, homeRoot, nodeDir, sess.id, req, externalDirs, escalated, timeout, verifiedInv)
	spec.AdmitResume = func(resume func() error) error {
		return s.admitSessionResume(callID, scope.Turn, sess, resume)
	}
	m.cleanMu.RLock()
	// Background-capable exec sessions outlive the originating HTTP request; foreground
	// cancellation is enforced via launchCtx, admitSessionResume, and runForegroundExecWait.
	proc, err := m.backend.Start(launchCtx, spec)
	if err != nil {
		cancelLaunch()
		m.cleanMu.RUnlock()
		m.rollbackSpawningSession(callID, sess)
		var se *sandbox.Error
		if errors.As(err, &se) && se.Code != "" {
			return nil, fail(se.Code)
		}
		return nil, err
	}
	stopped, stopReason := m.attachLaunchedProc(callID, scope.Turn, sess, proc)
	m.cleanMu.RUnlock()
	if !stopped {
		if cerr := s.checkCallCancelled(callID, scope.Turn); cerr != nil {
			stopped, stopReason = true, Code(cerr)
			m.clearFgCall(callID)
		}
	}
	if stopped {
		abortStoppedSpawn(m, sess)
		return nil, fail(stopReason)
	}
	m.ensureWatchdog(scope.Turn)
	go s.waitDetachedSession(sess)
	return sess, nil
}

func abortStoppedSpawn(m *execManager, sess *execSession) {
	m.mu.Lock()
	reason := sess.stopReason
	m.mu.Unlock()
	if reason == "" {
		reason = "cancelled"
	}
	sess.proc.Terminate(reason)
	select {
	case <-sess.proc.Done():
	case <-time.After(3 * time.Second):
	}
	waitSessionProcessesStopped(sess.proc, 1500*time.Millisecond)
	sess.closeLockedFiles()
	m.mu.Lock()
	sess.exited = true
	sess.inFgWait = false
	m.releaseTurnSlotLocked(sess)
	close(sess.detachCh)
	m.broadcastSettledLocked()
	m.mu.Unlock()
}

func (s *Service) runForegroundExecWait(ctx context.Context, e *schedEntry, sess *execSession, yieldDur time.Duration) (ExecResult, bool, error) {
	if yieldDur == 0 {
		if ctx.Err() != nil {
			res, err := s.abortSyncExec(sess)
			return res, false, err
		}
		select {
		case <-sess.proc.Done():
			res, err := s.finishSyncExec(sess)
			return res, false, err
		case <-sess.fgCancelCh:
			res, err := s.abortSyncExec(sess)
			return res, false, err
		case <-ctx.Done():
			res, err := s.abortSyncExec(sess)
			return res, false, err
		default:
			if e.isAdmissionClosed(s.sched) {
				res, err := s.abortSyncExec(sess)
				return res, false, err
			}
			return s.detachExec(sess)
		}
	}
	timer := time.NewTimer(yieldDur)
	defer timer.Stop()
	select {
	case <-sess.proc.Done():
		res, err := s.finishSyncExec(sess)
		return res, false, err
	case <-sess.fgCancelCh:
		res, err := s.abortSyncExec(sess)
		return res, false, err
	case <-ctx.Done():
		res, err := s.abortSyncExec(sess)
		return res, false, err
	case <-timer.C:
		if e.isAdmissionClosed(s.sched) {
			res, err := s.abortSyncExec(sess)
			return res, false, err
		}
		return s.detachExec(sess)
	}
}

func waitSessionProcessesStopped(proc sandbox.SessionProcess, budget time.Duration) {
	if proc == nil {
		return
	}
	deadline := time.Now().Add(budget)
	for {
		stopped, err := proc.ProcessesStopped()
		if err != nil || stopped || time.Now().After(deadline) {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
}

func (s *Service) finishSyncExec(sess *execSession) (ExecResult, error) {
	waitSessionProcessesStopped(sess.proc, 1500*time.Millisecond)
	sess.closeLockedFiles()
	m := s.exec
	m.mu.Lock()
	delete(m.fgByCall, sess.callID)
	sess.exited = true
	sess.inFgWait = false
	m.releaseTurnSlotLocked(sess)
	stopReason := sess.stopReason
	meta := sess.approvalMetaLocked()
	close(sess.detachCh)
	m.broadcastSettledLocked()
	m.mu.Unlock()

	unsafe, _, recErr := s.reconcileAfterExec(sess.turn, false)
	m.mu.Lock()
	sess.unsafeLinks = unsafe
	m.mu.Unlock()
	res := buildExecResult(sess, stopReason, meta)
	if recErr != nil {
		return res, recErr
	}
	return res, nil
}

func (s *Service) abortSyncExec(sess *execSession) (ExecResult, error) {
	m := s.exec
	m.mu.Lock()
	if sess.stopReason == "" {
		sess.stopReason = "cancelled"
	}
	reason := sess.stopReason
	m.mu.Unlock()

	sess.proc.Terminate(reason)
	select {
	case <-sess.proc.Done():
	case <-time.After(3 * time.Second):
	}
	waitSessionProcessesStopped(sess.proc, 1500*time.Millisecond)
	sess.closeLockedFiles()

	m.mu.Lock()
	delete(m.fgByCall, sess.callID)
	sess.exited = true
	sess.inFgWait = false
	m.releaseTurnSlotLocked(sess)
	close(sess.detachCh)
	m.broadcastSettledLocked()
	m.mu.Unlock()

	_, _, _ = s.reconcileAfterExec(sess.turn, false)
	return ExecResult{}, fail(reason)
}

func (s *Service) detachExec(sess *execSession) (ExecResult, bool, error) {
	m := s.exec
	m.mu.Lock()
	stopping := m.turnStopped[sess.turn] != "" || sess.stopReason != ""
	m.mu.Unlock()
	if stopping {
		res, err := s.abortSyncExec(sess)
		return res, false, err
	}

	s.mu.Lock()
	s.sequence++
	bgOp := Operation{
		ID:          sess.id,
		Scope:       sess.scope,
		Tool:        "exec_command",
		Summary:     sess.summary,
		Fingerprint: "exec:" + sess.id,
		Sequence:    s.sequence,
		Started:     sess.started,
		State:       "started",
		Phase:       PhaseRunning,
		Changes:     []Change{},
	}
	s.mu.Unlock()

	raw, err := s.persist(bgOp)
	if err != nil {
		res, _ := s.abortSyncExec(sess)
		return res, false, err
	}

	s.mu.Lock()
	s.pinJournalLocked(sess.id)
	s.journalSizes[sess.id] = int64(len(raw))
	s.records[sess.id] = publicOperation(bgOp)
	s.mu.Unlock()

	slice := sess.proc.OutputSnapshot(0, MaxExecPollBytes)
	m.mu.Lock()
	delete(m.fgByCall, sess.callID)
	sess.detached = true
	sess.inFgWait = false
	sess.pollCursor = slice.NextOffset
	sess.activeDec = true
	meta := sess.approvalMetaLocked()
	s.sched.activeExec.Add(1)
	close(sess.detachCh)
	m.mu.Unlock()

	return ExecResult{
		Status:           "running",
		SessionID:        sess.id,
		Output:           slice.Output,
		NextOffset:       slice.NextOffset,
		TotalBytes:       slice.TotalBytes,
		Truncated:        slice.Truncated,
		Escalated:        sess.escalated,
		ApprovalMode:     meta.approvalMode,
		UserApproved:     meta.userApproved,
		ApprovalDecision: meta.approvalDecision,
		ApprovalWaitMs:   meta.approvalWaitMs,
	}, true, nil
}

func (s *Service) waitDetachedSession(sess *execSession) {
	<-sess.detachCh
	m := s.exec
	m.mu.Lock()
	detached := sess.detached
	m.mu.Unlock()
	if !detached {
		return
	}
	<-sess.proc.Done()
	waitSessionProcessesStopped(sess.proc, 1500*time.Millisecond)
	sess.closeLockedFiles()
	info := sess.proc.ExitInfo()

	m.mu.Lock()
	sess.exited = true
	m.releaseTurnSlotLocked(sess)
	stopReason := sess.stopReason
	m.mu.Unlock()

	unsafe, _, recErr := s.reconcileAfterExec(sess.turn, false)

	m.mu.Lock()
	sess.unsafeLinks = unsafe
	m.mu.Unlock()

	state, detail := classifyExecTerminal(info, stopReason, unsafe, recErr)
	s.persistDetachedTerminal(sess, state, detail)

	m.mu.Lock()
	sess.finalized = true
	if sess.activeDec {
		sess.activeDec = false
		if s.sched.activeExec.Add(-1) < 0 {
			s.sched.activeExec.Store(0)
		}
	}
	m.broadcastSettledLocked()
	m.mu.Unlock()
	s.sched.wake()
}

func (s *Service) persistDetachedTerminal(sess *execSession, state, detail string) {
	s.mu.Lock()
	op, exists := s.records[sess.id]
	if !exists {
		op = Operation{
			ID:          sess.id,
			Scope:       sess.scope,
			Tool:        "exec_command",
			Summary:     sess.summary,
			Fingerprint: "exec:" + sess.id,
			Started:     sess.started,
			Changes:     []Change{},
		}
	}
	if op.State != "started" && state != "unknown" {
		if s.reservedActivityBytes >= MaxOperationBytes {
			s.reservedActivityBytes -= MaxOperationBytes
		} else {
			s.reservedActivityBytes = 0
		}
		s.unpinJournalLocked(sess.id)
		_ = s.pruneLocked()
		s.mu.Unlock()
		return
	}
	op.State = state
	op.Phase = PhaseTerminal
	op.WaitReason = ""
	op.Detail = detail
	op.Finished = time.Now().UTC()
	s.sequence++
	op.Sequence = s.sequence
	s.mu.Unlock()

	raw, persistErr := s.persist(op)
	if persistErr != nil {
		op.State = "unknown"
	}

	s.mu.Lock()
	if cur, ok := s.records[sess.id]; !ok || cur.State == "started" || op.State == "unknown" {
		if op.Sequence < s.sequence {
			s.sequence++
			op.Sequence = s.sequence
		}
		if persistErr == nil {
			s.journalSizes[sess.id] = int64(len(raw))
		}
		s.records[sess.id] = publicOperation(op)
		if op.State == "unknown" {
			s.blocked = true
			s.closeOtherModifyingAdmissionsLocked(sess.id)
		}
	}
	if s.reservedActivityBytes >= MaxOperationBytes {
		s.reservedActivityBytes -= MaxOperationBytes
	} else {
		s.reservedActivityBytes = 0
	}
	s.unpinJournalLocked(sess.id)
	_ = s.pruneLocked()
	s.mu.Unlock()
}

func (s *Service) PollExec(ctx context.Context, req ExecPollRequest) (ExecResult, error) {
	_, e, scope, _, _, _, err := s.enterExecCall(req.CallID, "process_control", false)
	if err != nil {
		return ExecResult{}, err
	}
	defer s.leaveCallIO(e, EffectStat, DefaultRoot, "")

	sess, err := s.lookupTurnSession(req.SessionID, scope.Turn)
	if err != nil {
		return ExecResult{}, err
	}

	offset, advanceCursor := s.resolvePollOffset(sess, req.Offset)
	waitDur := clampPollWait(req.WaitMs)
	if waitDur > 0 {
		deadline := time.Now().Add(waitDur)
		for {
			cur := sess.proc.OutputSnapshot(offset, 1)
			select {
			case <-sess.proc.Done():
				goto doneWait
			default:
			}
			if cur.Output != "" {
				break
			}
			rem := time.Until(deadline)
			if rem <= 0 {
				break
			}
			step := min(rem, 40*time.Millisecond)
			timer := time.NewTimer(step)
			select {
			case <-sess.proc.Done():
				timer.Stop()
				goto doneWait
			case <-ctx.Done():
				timer.Stop()
				return ExecResult{}, fail("cancelled")
			case <-timer.C:
			}
		}
	}
doneWait:
	if sess.proc.ExitInfo().Exited {
		waitSessionProcessesStopped(sess.proc, 500*time.Millisecond)
		s.exec.mu.Lock()
		s.exec.releaseTurnSlotLocked(sess)
		s.exec.mu.Unlock()
	}
	return s.snapshotExecResult(sess, offset, req.Limit, advanceCursor), nil
}

func (s *Service) ControlExec(req ExecControlRequest) (ExecResult, error) {
	_, e, scope, _, _, _, err := s.enterExecCall(req.CallID, "process_control", false)
	if err != nil {
		return ExecResult{}, err
	}
	defer s.leaveCallIO(e, EffectStat, DefaultRoot, "")

	switch req.Action {
	case "list":
		return s.listTurnExecSessions(scope.Turn), nil
	case "status", "log":
		sess, err := s.lookupTurnSession(req.SessionID, scope.Turn)
		if err != nil {
			return ExecResult{}, err
		}
		if sess.proc.ExitInfo().Exited {
			waitSessionProcessesStopped(sess.proc, 500*time.Millisecond)
			s.exec.mu.Lock()
			s.exec.releaseTurnSlotLocked(sess)
			s.exec.mu.Unlock()
		}
		offset := max(int64(0), req.Offset)
		return s.snapshotExecResult(sess, offset, req.Limit, false), nil
	default:
		return ExecResult{}, fail("invalid_request")
	}
}

func (s *Service) KillExec(req ExecKillRequest) (ExecResult, error) {
	m := s.exec
	if m == nil {
		return ExecResult{}, fail("not_found")
	}
	reason := req.Reason
	if reason == "" {
		reason = "cancelled"
	}
	if req.SessionID != "" {
		_, e, scope, _, _, _, err := s.enterExecCall(req.CallID, "process_control", false)
		if err != nil {
			return ExecResult{}, err
		}
		defer s.leaveCallIO(e, EffectStat, DefaultRoot, "")

		sess, err := s.lookupTurnSession(req.SessionID, scope.Turn)
		if err != nil {
			return ExecResult{}, err
		}
		m.mu.Lock()
		if sess.stopReason == "" {
			sess.stopReason = reason
		}
		m.mu.Unlock()
		sess.fgCancelOnce.Do(func() { close(sess.fgCancelCh) })
		sess.proc.Terminate(reason)
		select {
		case <-sess.proc.Done():
			waitSessionProcessesStopped(sess.proc, 500*time.Millisecond)
			m.mu.Lock()
			m.releaseTurnSlotLocked(sess)
			m.mu.Unlock()
		case <-time.After(1500 * time.Millisecond):
		}
		return s.snapshotExecResult(sess, -1, MaxExecPollBytes, false), nil
	}
	s.mu.Lock()
	active := s.callsByID[req.CallID]
	s.mu.Unlock()
	if req.CallID != "" && active != nil {
		m.CancelForegroundCall(req.CallID, reason)
		return ExecResult{Status: "cancelled", Reason: reason}, nil
	}
	return ExecResult{}, fail("not_found")
}

func (s *Service) lookupTurnSession(sessionID, turn string) (*execSession, error) {
	if s.exec == nil || sessionID == "" || turn == "" {
		return nil, fail("not_found")
	}
	s.exec.mu.Lock()
	sess := s.exec.sessions[sessionID]
	hasProc := sess != nil && sess.turn == turn && sess.proc != nil
	s.exec.mu.Unlock()
	if !hasProc {
		return nil, fail("not_found")
	}
	return sess, nil
}

func (s *Service) listTurnExecSessions(turn string) ExecResult {
	s.exec.mu.Lock()
	var items []*execSession
	for _, sess := range s.exec.sessions {
		if sess.turn == turn && sess.proc != nil {
			items = append(items, sess)
		}
	}
	s.exec.mu.Unlock()
	out := make([]ExecSessionSummary, 0, len(items))
	for _, sess := range items {
		res := s.snapshotExecResult(sess, 0, 0, false)
		out = append(out, ExecSessionSummary{
			SessionID:    sess.id,
			Status:       res.Status,
			ExitCode:     res.ExitCode,
			DurationMs:   res.DurationMs,
			TotalBytes:   res.TotalBytes,
			Truncated:    res.Truncated,
			Escalated:    res.Escalated,
			ApprovalMode: res.ApprovalMode,
			TLSHint:      res.TLSHint,
			Warning:      res.Warning,
		})
	}
	sortExecSessionSummaries(out)
	return ExecResult{Status: "completed", Sessions: out}
}

func (s *Service) resolvePollOffset(sess *execSession, rawOffset *int64) (int64, bool) {
	if rawOffset != nil {
		return max(int64(0), *rawOffset), false
	}
	s.exec.mu.Lock()
	off := sess.pollCursor
	s.exec.mu.Unlock()
	return off, true
}

func clampPollWait(waitMs *int) time.Duration {
	if waitMs == nil {
		return MaxExecHttpPollMs * time.Millisecond
	}
	v := *waitMs
	if v < 0 {
		v = MaxExecHttpPollMs
	}
	if v > MaxExecHttpPollMs {
		v = MaxExecHttpPollMs
	}
	return time.Duration(v) * time.Millisecond
}

func (sess *execSession) approvalMetaLocked() execApprovalMeta {
	return execApprovalMeta{
		approvalMode:     sess.approvalMode,
		userApproved:     sess.userApproved,
		approvalDecision: sess.approvalDecision,
		approvalWaitMs:   sess.approvalWaitMs,
	}
}

func (s *Service) snapshotExecResult(sess *execSession, offset int64, limit int, advanceCursor bool) ExecResult {
	if limit <= 0 || limit > MaxExecPollBytes {
		limit = MaxExecPollBytes
	}
	s.exec.mu.Lock()
	proc := sess.proc
	stopReason := sess.stopReason
	unsafe := sess.unsafeLinks
	meta := sess.approvalMetaLocked()
	if advanceCursor || offset < 0 {
		offset = sess.pollCursor
	}
	s.exec.mu.Unlock()
	if proc == nil {
		return ExecResult{
			Status:           "running",
			SessionID:        sess.id,
			Escalated:        sess.escalated,
			ApprovalMode:     meta.approvalMode,
			UserApproved:     meta.userApproved,
			ApprovalDecision: meta.approvalDecision,
			ApprovalWaitMs:   meta.approvalWaitMs,
		}
	}

	slice := proc.OutputSnapshot(offset, limit)
	if advanceCursor {
		s.exec.mu.Lock()
		if slice.NextOffset > sess.pollCursor {
			sess.pollCursor = slice.NextOffset
		}
		s.exec.mu.Unlock()
	}
	res := buildExecResultWithSlice(sess, slice, stopReason, meta)
	if unsafe {
		res.Warning = "unsafe_links_detected"
	}
	return res
}

func buildExecResult(sess *execSession, stopReason string, meta execApprovalMeta) ExecResult {
	slice := sess.proc.OutputSnapshot(0, MaxExecPollBytes)
	res := buildExecResultWithSlice(sess, slice, stopReason, meta)
	if sess.unsafeLinks {
		res.Warning = "unsafe_links_detected"
	}
	return res
}

func buildExecResultWithSlice(sess *execSession, slice sandbox.OutputSlice, stopReason string, meta execApprovalMeta) ExecResult {
	info := sess.proc.ExitInfo()
	status := "running"
	reason := ""
	tlsHint := info.TLSHint
	escalationAvail := false
	if info.Exited {
		if tlsHint == "" && info.ExitCode != 0 {
			tlsHint = sandbox.DetectWindowsTLSHint(info.ExitCode, slice.Output)
		}
		escalationAvail = !sess.escalated && info.ExitCode != 0 && (tlsHint == "windows_schannel_restricted" || sessionIndicatesRestrictedAccess(sess, slice.Output, info.Reason))
		switch {
		case stopReason == "cancelled" || info.Reason == "cancelled":
			status, reason = "cancelled", "cancelled"
		case info.Reason != "":
			status, reason = "failed", info.Reason
		case stopReason != "":
			status, reason = "failed", stopReason
		case info.ExitCode != 0:
			status, reason = "failed", fmt.Sprintf("exit_code=%d", info.ExitCode)
		default:
			status = "completed"
		}
	}
	return ExecResult{
		Status:              status,
		SessionID:           sess.id,
		Output:              slice.Output,
		NextOffset:          slice.NextOffset,
		TotalBytes:          slice.TotalBytes,
		Truncated:           slice.Truncated,
		ExitCode:            info.ExitCode,
		DurationMs:          info.DurationMs,
		Reason:              reason,
		TLSHint:             tlsHint,
		Escalated:           sess.escalated,
		ApprovalMode:        meta.approvalMode,
		UserApproved:        meta.userApproved,
		ApprovalDecision:    meta.approvalDecision,
		ApprovalWaitMs:      meta.approvalWaitMs,
		EscalationAvailable: escalationAvail,
	}
}

func isOutsideWorkspaceWorkdirCandidate(rawWorkdir, wsRoot, tempRoot, homeRoot string) bool {
	trimmed := strings.TrimSpace(rawWorkdir)
	if trimmed == "" || trimmed == "." {
		return false
	}
	if _, bad := sandbox.PathTargetsProtected(trimmed, wsRoot, tempRoot, sandbox.DefaultProtectedDirs(homeRoot)); bad {
		return false
	}
	wsReal := sandbox.ResolveRealishPath(wsRoot)
	candReal := sandbox.ResolveRealishPath(trimmed)
	return candReal != "" && !sandbox.PathWithin(candReal, wsReal)
}

func sessionIndicatesRestrictedAccess(sess *execSession, output, reason string) bool {
	if outputIndicatesRestrictedAccess(output, reason) {
		return true
	}
	low := strings.ToLower(output + "\n" + reason)
	zhNotFound := string([]byte{0xe6, 0x9c, 0xaa, 0xe8, 0x83, 0xbd, 0xe6, 0x89, 0xbe, 0xe5, 0x88, 0xb0, 0xe6, 0x96, 0x87, 0xe4, 0xbb, 0xb6})
	if !strings.Contains(low, "could not find file") && !strings.Contains(low, zhNotFound) {
		return false
	}
	if sess == nil || sess.wsRoot == "" {
		return false
	}
	return len(sandbox.ExtractExternalPathCandidates(output, sess.wsRoot, sess.tempRoot)) > 0
}

func outputIndicatesRestrictedAccess(output, reason string) bool {
	low := strings.ToLower(output + "\n" + reason)
	for _, needle := range []string{
		"access is denied",
		"access to the path",
		"access_denied",
		"unauthorizedaccessexception",
		"permissionerror",
		"eperm",
		"eacces",
		string([]byte{0xe6, 0x8b, 0x92, 0xe7, 0xbb, 0x9d, 0xe8, 0xae, 0xbf, 0xe9, 0x97, 0xae}),
		string([]byte{0xe8, 0xae, 0xbf, 0xe9, 0x97, 0xae, 0xe8, 0xa2, 0xab, 0xe6, 0x8b, 0x92, 0xe7, 0xbb, 0x9d}),
		"cannot create item",
		"write-protected",
	} {
		if strings.Contains(low, needle) {
			return true
		}
	}
	return false
}

func classifyExecTerminal(info sandbox.ExitInfo, stopReason string, unsafe bool, recErr error) (string, string) {
	if stopReason == "runtime_stopped" {
		return "unknown", "runtime_stopped"
	}
	if recErr != nil {
		return "unknown", Code(recErr)
	}
	if info.Reason == "cancelled" || stopReason == "cancelled" {
		return "cancelled", "cancelled"
	}
	if info.Reason != "" {
		return "failed", info.Reason
	}
	if stopReason != "" {
		return "failed", stopReason
	}
	if info.ExitCode != 0 {
		return "failed", fmt.Sprintf("exit_code=%d", info.ExitCode)
	}
	if unsafe {
		return "failed", "unsafe_links_detected"
	}
	return "completed", ""
}

func (s *Service) reconcileAfterExec(turn string, forceTempClean bool) (unsafeLinks bool, overQuota bool, err error) {
	unsafe, linkErr := scanUnsafeWorkspaceLinks(s.root)
	if linkErr != nil {
		return false, false, linkErr
	}
	_, _, quotaErr := s.measureWorkspaceDisk(0, 0)
	overQ := Code(quotaErr) == "workspace_limit"
	if quotaErr != nil && !overQ {
		return unsafe, false, quotaErr
	}
	var cleanErr error
	if s.exec != nil && (forceTempClean || turn != "") {
		cleanErr = s.exec.cleanupTemp(turn, forceTempClean)
	}
	s.mu.Lock()
	s.publishGen++
	s.overQuota = overQ
	s.mu.Unlock()
	return unsafe, overQ, cleanErr
}

func (s *Service) refreshOverQuotaIfNeeded() {
	s.mu.Lock()
	over := s.overQuota
	s.mu.Unlock()
	if !over {
		return
	}
	_, _, err := s.measureWorkspaceDisk(0, 0)
	s.mu.Lock()
	s.overQuota = Code(err) == "workspace_limit"
	s.mu.Unlock()
}
