package workspace

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"time"
	"unicode/utf8"
)

func (s *Service) Begin(req Begin) (Operation, error) {
	return s.BeginContext(context.Background(), req)
}

func (s *Service) BeginContext(ctx context.Context, req Begin) (Operation, error) {
	if !validStrictID(req.ID) || req.Fingerprint == "" || len(req.Tool) > 100 {
		return Operation{}, fail("invalid_request")
	}
	if len(req.Targets) > MaxTargets || req.NewBytes < 0 || req.NewBytes > MaxBatchBytes {
		return Operation{}, fail("batch_limit")
	}
	if err := validateProgress(req.Progress); err != nil {
		return Operation{}, err
	}
	summary, err := sanitizeSummary(req.Summary)
	if err != nil {
		return Operation{}, err
	}
	plan, err := PrepareTool(req.Tool, DescriptorInput{WorkspaceRoot: s.root, Targets: req.Targets, Path: req.Path, NewBytes: req.NewBytes})
	if err != nil {
		return Operation{}, err
	}
	if summary == "" {
		summary = plan.Summary
	}

	s.mu.Lock()
	if prior, ok := s.records[req.ID]; ok {
		op, err := s.checkExistingBeginLocked(req, prior)
		s.mu.Unlock()
		return op, err
	}
	if !s.enabled || req.Scope.Policy != s.policy || !platformSupported() {
		s.mu.Unlock()
		return Operation{}, fail("not_authorized")
	}
	if s.blocked {
		s.mu.Unlock()
		return Operation{}, fail("reconciliation_required")
	}
	isPublicMutation := req.Scope.Turn == "" && plan.Modifying
	if req.Scope.Turn != "" {
		if s.turn != req.Scope.Turn {
			s.mu.Unlock()
			return Operation{}, fail("busy")
		}
		if s.calls >= MaxCalls {
			s.mu.Unlock()
			return Operation{}, fail("call_limit")
		}
	} else if plan.Modifying {
		if s.turn != "" || s.publicMutating || len(s.callsByID) > 0 {
			s.mu.Unlock()
			return Operation{}, fail("busy")
		}
	}
	if err := s.pruneAndReserveActivityLocked(MaxOperationBytes); err != nil {
		s.mu.Unlock()
		return Operation{}, err
	}

	s.sched.mu.Lock()
	if s.sched.queuedCountLocked() >= s.sched.maxQueued {
		s.sched.mu.Unlock()
		s.mu.Unlock()
		return Operation{}, fail("queue_limit")
	}
	s.sched.applyClaimFaults(&plan)
	s.sched.nextTicket++
	entry := &schedEntry{
		id: req.ID, batch: "single-" + req.ID, order: 0, ticket: s.sched.nextTicket,
		scope: req.Scope, tool: req.Tool, inputFingerprint: req.Fingerprint, fingerprint: req.Fingerprint,
		plan: plan, newBytes: req.NewBytes, summary: summary, progress: req.Progress, phase: PhaseRegistered, sealed: true,
	}
	s.sched.entries[req.ID] = entry
	s.sched.mu.Unlock()

	if req.Scope.Turn != "" {
		s.calls++
	}
	if isPublicMutation {
		s.publicMutating = true
	}
	s.reservedActivityBytes += MaxOperationBytes
	s.sequence++
	op := Operation{
		ID: req.ID, Scope: req.Scope, Tool: req.Tool, Fingerprint: req.Fingerprint,
		Sequence: s.sequence, Started: time.Now().UTC(), State: "started", Phase: PhaseRegistered,
		Changes: []Change{}, Summary: summary, Progress: req.Progress,
	}
	s.records[req.ID] = publicOperation(op)
	s.mu.Unlock()

	res, err := s.Acquire(ctx, req.ID, req.Fingerprint)
	if err != nil && isPublicMutation {
		s.mu.Lock()
		s.publicMutating = false
		s.mu.Unlock()
	}
	return res, err
}

func (s *Service) checkExistingBeginLocked(req Begin, prior Operation) (Operation, error) {
	s.sched.mu.Lock()
	_, inSched := s.sched.entries[req.ID]
	s.sched.mu.Unlock()
	if prior.Fingerprint != req.Fingerprint || prior.Scope != req.Scope {
		return Operation{}, fail("identity_conflict")
	}
	if prior.State == "started" || inSched || s.callsByID[req.ID] != nil {
		return publicOperation(prior), fail("operation_pending")
	}
	return publicOperation(prior), nil
}

func (s *Service) Acquire(ctx context.Context, id, fingerprint string) (Operation, error) {
	s.mu.Lock()
	if c := s.callsByID[id]; c != nil {
		if fingerprint != "" && c.op.Fingerprint != fingerprint {
			s.mu.Unlock()
			return Operation{}, fail("fingerprint_mismatch")
		}
		op := publicOperation(c.op)
		s.mu.Unlock()
		return op, nil
	}
	if prior, ok := s.records[id]; ok && prior.State != "started" {
		if fingerprint != "" && prior.Fingerprint != fingerprint {
			s.mu.Unlock()
			return Operation{}, fail("fingerprint_mismatch")
		}
		s.mu.Unlock()
		if prior.State != "completed" {
			detail := prior.Detail
			if detail == "" {
				detail = prior.State
			}
			return publicOperation(prior), fail(detail)
		}
		return publicOperation(prior), nil
	}
	s.mu.Unlock()

	entry, err := s.sched.acquire(ctx, id, fingerprint, func(reason string) {
		s.mu.Lock()
		if op, ok := s.records[id]; ok && op.State == "started" && (op.Phase != PhaseQueued || op.WaitReason != reason) {
			s.sequence++
			op.Sequence = s.sequence
			op.Phase = PhaseQueued
			op.WaitReason = reason
			s.records[id] = op
		}
		s.mu.Unlock()
		s.recordTrace(id, "", "queued", "", DefaultRoot, reason, "")
	})
	if err != nil {
		s.mu.Lock()
		if Code(err) == "not_found" {
			if prior, ok := s.records[id]; ok && prior.State != "started" && prior.State != "completed" && prior.Detail != "" {
				if fingerprint != "" && prior.Fingerprint != fingerprint {
					err = fail("fingerprint_mismatch")
				} else {
					err = fail(prior.Detail)
				}
			}
		}
		s.sched.mu.Lock()
		_, stillQueued := s.sched.entries[id]
		s.sched.mu.Unlock()
		if !stillQueued {
			s.releaseRegisteredSlotLocked(id, Code(err))
		}
		s.mu.Unlock()
		return Operation{}, err
	}
	return s.prepareGrantedCall(entry)
}

func (s *Service) prepareGrantedCall(entry *schedEntry) (Operation, error) {
	s.mu.Lock()
	if existing := s.callsByID[entry.id]; existing != nil {
		op := publicOperation(existing.op)
		s.mu.Unlock()
		return op, nil
	}
	if !s.enabled || entry.scope.Policy != s.policy || !platformSupported() {
		s.abortGrantedBeforeCaptureLocked(entry, "not_authorized")
		s.mu.Unlock()
		return Operation{}, fail("not_authorized")
	}
	if s.blocked {
		s.abortGrantedBeforeCaptureLocked(entry, "reconciliation_required")
		s.mu.Unlock()
		return Operation{}, fail("reconciliation_required")
	}
	if err := s.pruneLocked(); err != nil {
		s.abortGrantedBeforeCaptureLocked(entry, Code(err))
		s.mu.Unlock()
		return Operation{}, err
	}
	baseOp := s.records[entry.id]
	if baseOp.ID == "" {
		s.sequence++
		baseOp = Operation{ID: entry.id, Scope: entry.scope, Tool: entry.tool, Fingerprint: entry.fingerprint, Sequence: s.sequence, Started: time.Now().UTC(), State: "started"}
	}
	baseOp.Tool = entry.tool
	baseOp.Fingerprint = entry.fingerprint
	baseOp.Summary = entry.summary
	baseOp.Progress = entry.progress
	baseOp.Phase = PhasePreparing
	baseOp.WaitReason = ""
	baseOp.Changes = []Change{}
	s.mu.Unlock()

	c := &call{
		op: baseOp, entry: entry, held: map[string]*heldFile{}, remove: map[string]bool{},
		newBytes: entry.newBytes, resActivity: MaxOperationBytes,
	}
	if len(entry.plan.Targets) > 0 {
		if err := s.capture(c, entry.plan); err != nil {
			closeCall(c)
			s.failBeforePublish(entry, Code(err))
			return Operation{}, err
		}
	}
	metadata, _ := json.Marshal(publicOperation(c.op))
	if len(metadata) > MaxOperationBytes-3*1024 {
		closeCall(c)
		s.failBeforePublish(entry, "activity_limit")
		return Operation{}, fail("activity_limit")
	}
	var snapshotLen int64
	if len(entry.plan.Targets) > 0 {
		snapshot, _ := json.Marshal(c.op)
		snapshotLen = int64(len(snapshot))
		growth := max(0, c.newBytes-c.oldBytes)
		if entry.plan.DeferInitialGrowthRes {
			growth = 0
		}
		if err := s.reserveForCall(c, growth, c.newFiles+len(c.op.ParentDirectories), snapshotLen); err != nil {
			closeCall(c)
			s.failBeforePublish(entry, Code(err))
			return Operation{}, err
		}
	}

	c.op.Phase = PhaseRunning
	rawBytes, err := s.persist(c.op)
	if err != nil {
		closeCall(c)
		s.mu.Lock()
		c.resActivity = 0
		s.releaseCallReservationLocked(c)
		s.mu.Unlock()
		s.failBeforePublish(entry, Code(err))
		return Operation{}, err
	}
	return s.publishPreparedCall(entry, c, rawBytes), nil
}

func (s *Service) publishPreparedCall(entry *schedEntry, c *call, rawBytes []byte) Operation {
	s.mu.Lock()
	s.sched.mu.Lock()
	if entry.admissionClosed {
		entry.phase = PhaseDraining
		c.op.Phase = PhaseDraining
	} else {
		entry.phase = PhaseRunning
	}
	s.sched.mu.Unlock()
	if s.reservedActivityBytes >= c.resActivity {
		s.reservedActivityBytes -= c.resActivity
	} else {
		s.reservedActivityBytes = 0
	}
	c.resActivity = 0
	if c.resVersion > 0 {
		if s.reservedVersionBytes >= c.resVersion {
			s.reservedVersionBytes -= c.resVersion
		} else {
			s.reservedVersionBytes = 0
		}
		c.resVersion = 0
	}
	s.sequence++
	c.op.Sequence = s.sequence
	s.journalSizes[c.op.ID] = int64(len(rawBytes))
	s.pinJournalLocked(c.op.ID)
	c.pinned = true
	delete(s.registered, c.op.ID)
	s.callsByID[c.op.ID] = c
	s.records[c.op.ID] = publicOperation(c.op)
	s.mu.Unlock()

	for _, claim := range entry.plan.Claims {
		s.recordTrace(entry.id, entry.tool, "granted", "", claim.Root, claim.Key, string(claim.Access))
	}
	return publicOperation(c.op)
}

func (s *Service) abortGrantedBeforeCaptureLocked(entry *schedEntry, reason string) {
	s.sched.mu.Lock()
	s.sched.releaseEntryLocked(entry)
	s.sched.mu.Unlock()
	s.releaseRegisteredSlotLocked(entry.id, reason)
}

func (s *Service) failBeforePublish(entry *schedEntry, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.abortGrantedBeforeCaptureLocked(entry, reason)
}

func (s *Service) capture(c *call, plan PreparedPlan) error {
	targets := append([]Target(nil), plan.Targets...)
	sort.Slice(targets, func(i, j int) bool { return pathKey(targets[i].Path) < pathKey(targets[j].Path) })
	var oldBytes int64
	parents := map[string]string{}
	writable := 0
	for _, target := range targets {
		name, err := relative(target.Path, false)
		if err != nil {
			return err
		}
		if _, exists := c.held[pathKey(name)]; exists {
			return fail("duplicate_target")
		}
		h, err := hold(s.root, name, true, true)
		if err != nil {
			return err
		}
		h.staging = filepath.Join(s.journal, "staging")
		c.held[pathKey(name)] = h
		c.remove[pathKey(name)] = target.Remove
		change := Change{Path: name, Before: MissingRevision, After: MissingRevision, State: "unchanged", Restorable: true}
		if h.file != nil {
			change.Content, err = h.bytes(MaxFileBytes)
			if err != nil {
				return err
			}
			if !utf8.Valid(change.Content) {
				return fail("not_utf8")
			}
			change.Before = revision(change.Content)
			change.After = change.Before
		}
		if target.Expected != "" && target.Expected != change.Before {
			return fail("revision_conflict")
		}
		oldBytes += int64(len(change.Content))
		if !target.Remove {
			writable++
			c.oldBytes += int64(len(change.Content))
			if h.file == nil {
				c.newFiles++
			}
			for _, parent := range h.missing {
				rel, _ := filepath.Rel(s.root, parent)
				parents[pathKey(rel)] = filepath.ToSlash(rel)
			}
		}
		c.op.Changes = append(c.op.Changes, change)
	}
	if writable == 0 {
		c.newBytes = 0
	} else if plan.AddSourceBytesToBound {
		c.newBytes += oldBytes
	} else if plan.AddOldBytesToBound {
		c.newBytes += c.oldBytes
	}
	for _, parent := range parents {
		c.op.ParentDirectories = append(c.op.ParentDirectories, parent)
	}
	sort.Strings(c.op.ParentDirectories)
	if oldBytes+c.newBytes > MaxBatchBytes {
		return fail("batch_limit")
	}
	return nil
}

func closeCall(c *call) {
	if c == nil {
		return
	}
	for _, h := range c.held {
		h.close()
	}
}
