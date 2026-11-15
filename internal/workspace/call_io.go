package workspace

import (
	"strings"
	"time"
	"unicode/utf8"
)

func (s *Service) enterCallIO(id, root, rel string, eff Effect, needWrite bool) (*call, *schedEntry, error) {
	s.mu.Lock()
	faultIgnoreID := s.sched.faults != nil && s.sched.faults.ignoreCallIdentity
	var c *call
	if faultIgnoreID {
		for _, anyCall := range s.callsByID {
			c = anyCall
			break
		}
	} else {
		c = s.callsByID[id]
	}
	s.mu.Unlock()
	if c == nil || c.entry == nil {
		return nil, nil, fail("invalid_call")
	}
	e := c.entry
	s.sched.mu.Lock()
	if e.phase != PhaseRunning && e.phase != PhasePreparing {
		reason := "invalid_call"
		if e.admissionClosed {
			reason = "cancelled"
		}
		s.sched.mu.Unlock()
		return nil, nil, fail(reason)
	}
	if e.admissionClosed {
		s.sched.mu.Unlock()
		return nil, nil, fail("cancelled")
	}
	if !ClaimCoversAccess(e.plan.Claims, e.plan.Effects, defaultRootID(root), rel, eff, needWrite) {
		e.violation = "undeclared_effect:" + string(eff) + ":" + pathKey(rel)
		e.admissionClosed = true
		e.phase = PhaseDraining
		s.sched.mu.Unlock()
		s.recordTrace(e.id, e.tool, "rejected", eff, defaultRootID(root), pathKey(rel), "violation")
		return nil, nil, fail("undeclared_effect")
	}
	e.inflight++
	s.sched.mu.Unlock()

	access := "S"
	if needWrite {
		access = "X"
	}
	s.recordTrace(e.id, e.tool, "io_enter", eff, defaultRootID(root), pathKey(rel), access)
	return c, e, nil
}

func (s *Service) leaveCallIO(e *schedEntry, eff Effect, root, rel string) {
	s.recordTrace(e.id, e.tool, "io_exit", eff, defaultRootID(root), pathKey(rel), "")
	s.sched.mu.Lock()
	if e.inflight > 0 {
		e.inflight--
	}
	if e.inflight == 0 {
		s.sched.broadcastLocked()
	}
	s.sched.mu.Unlock()
}

func (s *Service) Write(id, name string, data []byte, exclusive bool) (string, error) {
	rel, err := relative(name, false)
	if err != nil {
		return "", err
	}
	eff := EffectWrite
	if exclusive {
		eff = EffectCreate
	}
	c, e, err := s.enterCallIO(id, DefaultRoot, rel, eff, true)
	if err != nil {
		return "", err
	}
	defer s.leaveCallIO(e, eff, DefaultRoot, rel)

	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.held[pathKey(rel)]
	if h == nil {
		return "", fail("undeclared_target")
	}
	if exclusive && h.file != nil && !h.removed {
		return "exists", nil
	}
	if c.remove[pathKey(rel)] {
		return "", fail("undeclared_write")
	}
	if len(data) > MaxFileBytes || !utf8.Valid(data) {
		return "", fail("file_limit")
	}
	if c.written+int64(len(data)) > c.newBytes {
		return "", fail("batch_limit")
	}
	if err := h.pinAvailableParents(); err != nil {
		return "", err
	}
	for _, missing := range h.missing {
		mRel := strings.TrimPrefix(strings.ReplaceAll(missing, `\`, "/"), strings.ReplaceAll(s.root, `\`, "/")+"/")
		if !ClaimCoversAccess(e.plan.Claims, e.plan.Effects, DefaultRoot, mRel, EffectMkdir, true) {
			return "", fail("undeclared_effect")
		}
	}
	var oldSize int64
	newFiles := len(h.missing)
	if h.file != nil {
		info, statErr := h.file.Stat()
		if statErr != nil {
			return "", statErr
		}
		oldSize = info.Size()
	} else {
		newFiles++
	}
	growth := max(0, int64(len(data))-oldSize)
	if err := s.checkDynamicWriteQuota(c, growth, newFiles); err != nil {
		return "", err
	}
	s.fireIOHook(c.op.ID, c.op.Tool, eff, rel)
	if err := h.write(data); err != nil {
		return "", err
	}
	s.recordCallPublished(c, growth, newFiles)
	c.written += int64(len(data))
	return "created", nil
}

func (s *Service) Remove(id, name string) error {
	rel, err := relative(name, false)
	if err != nil {
		return err
	}
	c, e, err := s.enterCallIO(id, DefaultRoot, rel, EffectRemove, true)
	if err != nil {
		return err
	}
	defer s.leaveCallIO(e, EffectRemove, DefaultRoot, rel)

	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.held[pathKey(rel)]
	if h == nil {
		return fail("undeclared_target")
	}
	s.fireIOHook(c.op.ID, c.op.Tool, EffectRemove, rel)
	if err := h.remove(); err != nil {
		return err
	}
	s.bumpPublishGen()
	return nil
}

func (s *Service) Mkdir(id, name string) error {
	rel, err := relative(name, true)
	if err != nil {
		return err
	}
	s.mu.Lock()
	c := s.lookupActiveCallLocked(id)
	s.mu.Unlock()
	if c == nil || c.entry == nil {
		return fail("invalid_call")
	}
	e := c.entry
	existingAncestor := rel == "" || isExistingTargetAncestor(c, rel)
	s.sched.mu.Lock()
	if e.phase != PhaseRunning && e.phase != PhasePreparing {
		reason := "invalid_call"
		if e.admissionClosed {
			reason = "cancelled"
		}
		s.sched.mu.Unlock()
		return fail(reason)
	}
	if e.admissionClosed {
		s.sched.mu.Unlock()
		return fail("cancelled")
	}
	allowed := e.plan.Effects[EffectMkdir] && (existingAncestor || ClaimCoversAccess(e.plan.Claims, e.plan.Effects, DefaultRoot, rel, EffectMkdir, true))
	if !allowed {
		e.violation = "undeclared_effect:mkdir:" + pathKey(rel)
		e.admissionClosed = true
		e.phase = PhaseDraining
		s.sched.mu.Unlock()
		s.recordTrace(e.id, e.tool, "rejected", EffectMkdir, DefaultRoot, pathKey(rel), "violation")
		return fail("undeclared_effect")
	}
	e.inflight++
	s.sched.mu.Unlock()
	access := "X"
	if existingAncestor {
		access = "S"
	}
	s.recordTrace(e.id, e.tool, "io_enter", EffectMkdir, DefaultRoot, pathKey(rel), access)
	s.leaveCallIO(e, EffectMkdir, DefaultRoot, rel)
	return nil
}

func (s *Service) lookupActiveCallLocked(id string) *call {
	if s.sched.faults != nil && s.sched.faults.ignoreCallIdentity {
		for _, anyCall := range s.callsByID {
			return anyCall
		}
		return nil
	}
	return s.callsByID[id]
}

func isExistingTargetAncestor(c *call, rel string) bool {
	prefix := pathKey(rel) + "/"
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, target := range c.op.Changes {
		tk := pathKey(target.Path)
		if !strings.HasPrefix(tk, prefix) || c.remove[tk] {
			continue
		}
		h := c.held[tk]
		if h == nil {
			continue
		}
		isMissing := false
		for _, m := range h.missing {
			mRel := strings.ReplaceAll(m, `\`, "/")
			if strings.HasSuffix(strings.ToLower(mRel), "/"+pathKey(rel)) || strings.EqualFold(mRel, pathKey(rel)) {
				isMissing = true
				break
			}
		}
		if !isMissing {
			return true
		}
	}
	return false
}

func (s *Service) CancelCall(id, reason string) error {
	if reason == "" {
		reason = "cancelled"
	}
	s.mu.Lock()
	c := s.callsByID[id]
	s.sched.mu.Lock()
	e := s.sched.entries[id]
	if e == nil {
		s.sched.mu.Unlock()
		s.mu.Unlock()
		return nil
	}
	e.admissionClosed = true
	e.cancelReason = reason
	if !e.granted {
		s.sched.releaseEntryLocked(e)
		s.sched.mu.Unlock()
		s.releaseRegisteredSlotLocked(id, reason)
		s.mu.Unlock()
		return nil
	}
	e.phase = PhaseDraining
	s.sched.broadcastLocked()
	s.sched.mu.Unlock()
	if c != nil && c.op.Phase != PhaseDraining {
		s.sequence++
		c.op.Sequence = s.sequence
		c.op.Phase = PhaseDraining
		s.records[id] = publicOperation(c.op)
	}
	s.mu.Unlock()
	return nil
}

func (s *Service) CancelTurn(turn string) {
	if turn == "" {
		return
	}
	s.sched.mu.Lock()
	var ids []string
	for id, e := range s.sched.entries {
		if e.scope.Turn == turn && !e.system {
			ids = append(ids, id)
		}
	}
	s.sched.mu.Unlock()
	for _, id := range ids {
		_ = s.CancelCall(id, "cancelled")
	}
}

func (s *Service) End(id, state, detail string) (Operation, error) {
	if state != "completed" && state != "failed" && state != "cancelled" && state != "unknown" {
		return Operation{}, fail("invalid_state")
	}
	s.mu.Lock()
	c := s.callsByID[id]
	if c == nil {
		op, err := s.endUngrantedLocked(id, state, detail)
		s.mu.Unlock()
		return op, err
	}
	e := c.entry
	s.sched.mu.Lock()
	e.admissionClosed = true
	e.phase = PhaseDraining
	violation := e.violation
	s.sched.mu.Unlock()
	if c.op.Phase != PhaseDraining {
		s.sequence++
		c.op.Sequence = s.sequence
		c.op.Phase = PhaseDraining
		s.records[id] = publicOperation(c.op)
	}
	s.mu.Unlock()

	s.fireEndHook(id, "before_drain")
	s.sched.waitDrained(id)
	s.fireEndHook(id, "after_drain")

	observeAndFinalizeCall(c, violation, state, detail)

	s.mu.Lock()
	s.sequence++
	c.op.Sequence = s.sequence
	s.mu.Unlock()

	var persistErr error
	var rawBytes []byte
	if !c.ephemeral {
		rawBytes, persistErr = s.persist(c.op)
		if persistErr != nil {
			c.op.State = "unknown"
		}
	}
	out, persistErr := s.commitEndedCall(id, c, e, rawBytes, persistErr)
	s.recordTrace(id, e.tool, "released", "", DefaultRoot, "", c.op.State)
	return out, persistErr
}

func (s *Service) endUngrantedLocked(id, state, detail string) (Operation, error) {
	s.sched.mu.Lock()
	e := s.sched.entries[id]
	if e != nil && !e.granted {
		s.sched.releaseEntryLocked(e)
		s.sched.mu.Unlock()
		endReason := detail
		if endReason == "" {
			endReason = state
		}
		s.releaseRegisteredSlotLocked(id, endReason)
		if op := s.records[id]; op.ID != "" {
			return publicOperation(op), nil
		}
		return Operation{ID: id, State: state, Phase: PhaseTerminal}, nil
	}
	s.sched.mu.Unlock()
	if op, ok := s.records[id]; ok {
		return publicOperation(op), nil
	}
	return Operation{}, fail("not_found")
}

func observeAndFinalizeCall(c *call, violation, state, detail string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if violation != "" && state == "completed" {
		state = "failed"
		if detail == "" {
			detail = "undeclared_effect"
		}
	}
	for i := range c.op.Changes {
		change := &c.op.Changes[i]
		h := c.held[pathKey(change.Path)]
		after, conflict, obsErr := h.observe()
		change.After = after
		if obsErr != nil {
			state = "unknown"
		}
		if change.After != change.Before {
			change.State = "changed"
		}
		if conflict {
			change.State = "conflict"
		}
	}
	if len(detail) > 2048 {
		detail = detail[:2048]
	}
	c.op.State = state
	c.op.Phase = PhaseTerminal
	c.op.WaitReason = ""
	c.op.Finished = time.Now().UTC()
	c.op.Detail = detail
	closeCall(c)
	c.held = map[string]*heldFile{}
}

func (s *Service) commitEndedCall(id string, c *call, e *schedEntry, rawBytes []byte, persistErr error) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !c.ephemeral {
		if c.op.Sequence < s.sequence {
			s.sequence++
			c.op.Sequence = s.sequence
		}
		if persistErr == nil {
			s.journalSizes[id] = int64(len(rawBytes))
		}
		s.records[id] = publicOperation(c.op)
	}
	if c.op.State == "unknown" {
		s.blocked = true
		s.closeOtherModifyingAdmissionsLocked(id)
	}
	s.releaseCallReservationLocked(c)
	if c.pinned {
		s.unpinJournalLocked(id)
		c.pinned = false
	}
	delete(s.callsByID, id)
	if c.op.Scope.Turn == "" && e.plan.Modifying {
		s.publicMutating = false
	}
	s.sched.mu.Lock()
	s.sched.releaseEntryLocked(e)
	s.sched.mu.Unlock()
	if persistErr == nil && !c.ephemeral {
		persistErr = s.pruneLocked()
	}
	return publicOperation(c.op), persistErr
}

func (s *Service) closeOtherModifyingAdmissionsLocked(exceptID string) {
	s.sched.mu.Lock()
	defer s.sched.mu.Unlock()
	for id, e := range s.sched.entries {
		if id == exceptID || !e.plan.Modifying {
			continue
		}
		e.admissionClosed = true
		if e.cancelReason == "" {
			e.cancelReason = "reconciliation_required"
		}
		if e.granted {
			e.phase = PhaseDraining
		}
	}
	s.sched.broadcastLocked()
}

// RuntimeStopped is called only after the owned process tree has exited.
func (s *Service) RuntimeStopped() error {
	s.sched.mu.Lock()
	var ungranted []string
	for id, e := range s.sched.entries {
		if !e.granted {
			ungranted = append(ungranted, id)
		} else {
			e.inflight = 0
			e.admissionClosed = true
		}
	}
	s.sched.broadcastLocked()
	s.sched.mu.Unlock()

	for _, id := range ungranted {
		_ = s.CancelCall(id, "runtime_stopped")
	}

	s.mu.Lock()
	activeIDs := make([]string, 0, len(s.callsByID))
	for id, c := range s.callsByID {
		if !c.ephemeral {
			activeIDs = append(activeIDs, id)
		} else {
			closeCall(c)
			s.releaseCallReservationLocked(c)
			if c.pinned {
				s.unpinJournalLocked(id)
			}
			delete(s.callsByID, id)
			s.sched.mu.Lock()
			if c.entry != nil {
				s.sched.releaseEntryLocked(c.entry)
			}
			s.sched.mu.Unlock()
		}
	}
	s.mu.Unlock()

	var firstErr error
	for _, id := range activeIDs {
		if _, err := s.End(id, "unknown", "runtime_stopped"); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.mu.Lock()
	s.sched.mu.Lock()
	s.sched.entries = map[string]*schedEntry{}
	s.sched.batches = map[string]*schedBatch{}
	s.sched.activeCalls, s.sched.modifyCalls, s.sched.indexCalls = 0, 0, 0
	s.sched.broadcastLocked()
	s.sched.mu.Unlock()
	s.turn = ""
	s.publicMutating = false
	s.reservedActivityBytes = 0
	s.reservedVersionBytes = 0
	s.mu.Unlock()
	return firstErr
}

func (s *Service) simulateAbruptCrashForTest() {
	s.mu.Lock()
	for _, c := range s.callsByID {
		closeCall(c)
	}
	s.callsByID = map[string]*call{}
	s.turn = ""
	s.publicMutating = false
	s.mu.Unlock()
}
