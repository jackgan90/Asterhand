package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/jackgan90/Asterhand/internal/config"
)

type call struct {
	mu             sync.Mutex
	op             Operation
	entry          *schedEntry
	held           map[string]*heldFile
	newBytes       int64
	written        int64
	remove         map[string]bool
	oldBytes       int64
	newFiles       int
	unsettledBytes int64
	unsettledFiles int
	resVersion     int64
	resActivity    int64
	pinned         bool
	ephemeral      bool
}

type Service struct {
	mu                    sync.Mutex
	root                  string
	journal               string
	id                    string
	policy                string
	enabled               bool
	turn                  string
	scope                 Scope
	calls                 int
	publicMutating        bool
	callsByID             map[string]*call
	registered            map[string]bool
	sched                 *scheduler
	sequence              int64
	records               map[string]Operation
	journalSizes          map[string]int64
	pinnedJournals        map[string]int
	publishGen            uint64
	reservedVersionBytes  int64
	reservedActivityBytes int64
	blocked               bool

	traceMu      sync.Mutex
	traceEnabled bool
	traceLimit   int
	traceSeq     int64
	trace        []TraceEvent
	ioHook       func(callID, tool string, eff Effect, relPath string)
	endHook      func(callID, stage string)
}

func Open(root, journal, id string) (*Service, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	for _, p := range []string{root, journal, filepath.Join(journal, "staging")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			return nil, err
		}
	}
	orphans, err := os.ReadDir(filepath.Join(journal, "staging"))
	if err != nil {
		return nil, err
	}
	for _, item := range orphans {
		if item.Type().IsRegular() && (strings.HasSuffix(item.Name(), ".old") || strings.HasSuffix(item.Name(), ".new")) {
			if err := os.Remove(filepath.Join(journal, "staging", item.Name())); err != nil {
				return nil, err
			}
		}
	}
	s := &Service{
		root:           root,
		journal:        journal,
		id:             id,
		callsByID:      map[string]*call{},
		registered:     map[string]bool{},
		sched:          newScheduler(),
		records:        map[string]Operation{},
		journalSizes:   map[string]int64{},
		pinnedJournals: map[string]int{},
	}
	entries, err := os.ReadDir(journal)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		full := filepath.Join(journal, entry.Name())
		data, err := os.ReadFile(full)
		var op Operation
		if err != nil || json.Unmarshal(data, &op) != nil || !validID(op.ID) {
			return nil, fail("journal_corrupt")
		}
		if op.State == "started" {
			op.State = "unknown"
			op.Phase = PhaseTerminal
			op.WaitReason = ""
		}
		if op.State == "unknown" {
			s.blocked = true
		}
		s.records[op.ID] = publicOperation(op)
		s.journalSizes[op.ID] = int64(len(data))
		s.sequence = max(s.sequence, op.Sequence)
	}
	return s, nil
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 160 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func validStrictID(id string) bool {
	if len(id) == 0 || len(id) > 160 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (s *Service) ID() string   { return s.id }
func (s *Service) Root() string { return s.root }

func (s *Service) busyLocked() bool {
	s.sched.mu.Lock()
	active := s.sched.hasNonTerminalLocked()
	s.sched.mu.Unlock()
	return active || len(s.callsByID) > 0 || s.turn != "" || s.publicMutating
}

func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := Status{
		ID: s.id, Supported: platformSupported(), Authorized: s.enabled, Policy: s.policy,
		Ready: platformSupported() && s.enabled && !s.blocked, Busy: s.busyLocked(), Enforcement: "restricted_service",
		Capabilities: []string{"files.read", "files.mutate", "files.discover", "memory.search", "progress", "artifacts.read", "versions.restore"},
		Limits: map[string]int64{
			"file_bytes": MaxFileBytes, "batch_bytes": MaxBatchBytes, "batch_targets": MaxTargets,
			"workspace_bytes": MaxWorkspaceBytes, "workspace_files": MaxWorkspaceFiles, "turn_calls": MaxCalls,
			"page_bytes": PageBytes, "active_calls": MaxActiveCalls, "modifying_calls": MaxModifyingCalls, "queued_calls": MaxQueuedCalls,
		},
	}
	if !state.Supported {
		state.Reason = "unsupported_platform"
	} else if !state.Authorized {
		state.Reason = "not_authorized"
	} else if s.blocked {
		state.Reason = "reconciliation_required"
	}
	return state
}

func (s *Service) Configure(enabled bool, policy string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busyLocked() {
		return fail("busy")
	}
	s.enabled, s.policy = enabled, policy
	return nil
}

func (s *Service) AcquireTurn(turn, policy string) error {
	return s.AcquireTurnScope(Scope{Turn: turn, Policy: policy})
}

func (s *Service) AcquireTurnScope(scope Scope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	turn := scope.Turn
	if !s.enabled || scope.Policy != s.policy || !platformSupported() {
		return fail("not_authorized")
	}
	if s.blocked {
		return fail("reconciliation_required")
	}
	if s.turn == turn && turn != "" {
		return nil
	}
	if !validStrictID(turn) || s.turn != "" || s.publicMutating || len(s.callsByID) > 0 {
		return fail("busy")
	}
	s.turn, s.scope, s.calls = turn, scope, 0
	return nil
}

// ReleaseTurn requires a trusted adapter's proof of native termination.
func (s *Service) ReleaseTurn(turn string, stopped bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turn != turn {
		return nil
	}
	s.sched.mu.Lock()
	hasActive := false
	for _, e := range s.sched.entries {
		if !e.system && e.scope.Turn == turn && e.phase != PhaseTerminal {
			hasActive = true
			break
		}
	}
	s.sched.mu.Unlock()
	if !stopped || hasActive || len(s.callsByID) > 0 {
		return fail("cleanup_pending")
	}
	s.sched.mu.Lock()
	for id, b := range s.sched.batches {
		if b.scope.Turn == turn {
			delete(s.sched.batches, id)
		}
	}
	s.sched.mu.Unlock()
	s.turn = ""
	return nil
}

func validateProgress(steps []Step) error {
	if len(steps) > 20 {
		return fail("progress_limit")
	}
	for _, step := range steps {
		if len(step.Text) > 200 || !utf8.ValidString(step.Text) || (step.Status != "pending" && step.Status != "in_progress" && step.Status != "completed") {
			return fail("progress_limit")
		}
	}
	return nil
}

func (s *Service) Register(req Register) (Operation, error) {
	if !validStrictID(req.ID) || !validStrictID(req.Batch) || req.InputFingerprint == "" || len(req.Tool) > 100 {
		return Operation{}, fail("invalid_request")
	}
	if _, ok := LookupDescriptor(req.Tool); !ok {
		return Operation{}, fail("unsupported_tool")
	}
	if err := validateProgress(req.Progress); err != nil {
		return Operation{}, err
	}
	summary, err := sanitizeSummary(req.Summary)
	if err != nil {
		return Operation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prior, ok := s.records[req.ID]; ok {
		if prior.Scope != req.Scope {
			return Operation{}, fail("identity_conflict")
		}
		return publicOperation(prior), nil
	}
	if !s.enabled || req.Scope.Policy != s.policy || !platformSupported() {
		return Operation{}, fail("not_authorized")
	}
	if s.blocked {
		return Operation{}, fail("reconciliation_required")
	}
	if s.turn == "" || req.Scope.Turn != s.turn {
		return Operation{}, fail("busy")
	}
	if s.calls >= MaxCalls {
		return Operation{}, fail("call_limit")
	}
	if err := s.pruneAndReserveActivityLocked(MaxOperationBytes); err != nil {
		return Operation{}, err
	}
	s.sched.mu.Lock()
	if existing, ok := s.sched.entries[req.ID]; ok {
		if existing.inputFingerprint != req.InputFingerprint || existing.scope != req.Scope {
			s.sched.mu.Unlock()
			return Operation{}, fail("identity_conflict")
		}
		s.sched.mu.Unlock()
		return s.records[req.ID], nil
	}
	if s.sched.queuedCountLocked() >= s.sched.maxQueued {
		s.sched.mu.Unlock()
		return Operation{}, fail("queue_limit")
	}
	b := s.sched.batches[req.Batch]
	if b == nil {
		b = &schedBatch{id: req.Batch, scope: req.Scope}
		s.sched.batches[req.Batch] = b
	} else if b.sealed || b.scope != req.Scope {
		s.sched.mu.Unlock()
		return Operation{}, fail("invalid_batch")
	}
	s.sched.nextTicket++
	entry := &schedEntry{
		id: req.ID, batch: req.Batch, order: req.Order, ticket: s.sched.nextTicket,
		scope: req.Scope, tool: req.Tool, inputFingerprint: req.InputFingerprint,
		newBytes: req.NewBytes, summary: summary, progress: req.Progress, phase: PhaseRegistered, waitReason: "batch_seal",
	}
	s.sched.entries[req.ID] = entry
	b.ids = append(b.ids, req.ID)
	s.sched.mu.Unlock()

	s.calls++
	s.registered[req.ID] = true
	s.reservedActivityBytes += MaxOperationBytes
	s.sequence++
	op := Operation{
		ID: req.ID, Scope: req.Scope, Tool: req.Tool, Fingerprint: req.InputFingerprint,
		Sequence: s.sequence, Started: time.Now().UTC(), State: "started", Phase: PhaseRegistered,
		WaitReason: "batch_seal", Changes: []Change{}, Summary: summary, Progress: req.Progress,
	}
	s.records[req.ID] = publicOperation(op)
	s.recordTrace(req.ID, req.Tool, "registered", "", DefaultRoot, "", "")
	return publicOperation(op), nil
}

func (s *Service) SealBatch(req SealBatch) error {
	if !validStrictID(req.Batch) || len(req.Items) == 0 {
		return fail("invalid_batch")
	}
	byID, alreadySealed, err := s.validateBatchSealHeader(req)
	if err != nil || alreadySealed {
		return err
	}
	plans, itemFailures := s.prepareSealPlans(req.Items)
	return s.commitSealedBatch(req.Batch, byID, plans, itemFailures)
}

func (s *Service) validateBatchSealHeader(req SealBatch) (map[string]SealItem, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || req.Scope.Policy != s.policy || !platformSupported() {
		return nil, false, fail("not_authorized")
	}
	if s.blocked {
		s.abortBatchLocked(req.Batch, "reconciliation_required")
		return nil, false, fail("reconciliation_required")
	}
	s.sched.mu.Lock()
	b := s.sched.batches[req.Batch]
	if b == nil || b.scope != req.Scope {
		s.sched.mu.Unlock()
		return nil, false, fail("invalid_batch")
	}
	if b.sealed {
		s.sched.mu.Unlock()
		return nil, true, nil
	}
	byID := make(map[string]SealItem, len(req.Items))
	for _, item := range req.Items {
		if _, dup := byID[item.ID]; dup {
			s.sched.mu.Unlock()
			s.abortBatchLocked(req.Batch, "invalid_batch")
			return nil, false, fail("invalid_batch")
		}
		byID[item.ID] = item
	}
	if len(byID) != len(b.ids) {
		s.sched.mu.Unlock()
		s.abortBatchLocked(req.Batch, "invalid_batch")
		return nil, false, fail("invalid_batch")
	}
	s.sched.mu.Unlock()
	return byID, false, nil
}

func resolveBatchSummary(itemSummary, planSummary, existing string) string {
	if s, err := sanitizeSummary(itemSummary); err == nil && s != "" {
		return s
	}
	if planSummary != "" {
		return planSummary
	}
	return existing
}

func (s *Service) prepareSealPlans(items []SealItem) (map[string]PreparedPlan, map[string]string) {
	plans := make(map[string]PreparedPlan, len(items))
	itemFailures := make(map[string]string)
	for _, item := range items {
		if item.Aborted {
			continue
		}
		if item.Fingerprint == "" || item.NewBytes < 0 || item.NewBytes > MaxBatchBytes || len(item.Targets) > MaxTargets {
			itemFailures[item.ID] = "batch_limit"
			continue
		}
		if err := validateProgress(item.Progress); err != nil {
			itemFailures[item.ID] = Code(err)
			continue
		}
		if _, err := sanitizeSummary(item.Summary); err != nil {
			itemFailures[item.ID] = Code(err)
			continue
		}
		plan, err := PrepareTool(item.Tool, DescriptorInput{WorkspaceRoot: s.root, Targets: item.Targets, Path: item.Path, NewBytes: item.NewBytes})
		if err != nil {
			itemFailures[item.ID] = Code(err)
			continue
		}
		plans[item.ID] = plan
	}
	return plans, itemFailures
}

func hasPriorBatchMutation(plan PreparedPlan, priorMutated map[string]bool) bool {
	if plan.Semantic != SemanticFileWrite {
		return false
	}
	for _, t := range plan.Targets {
		if priorMutated[pathKey(t.Path)] {
			return true
		}
	}
	return false
}

func (s *Service) commitSealedBatch(batchID string, byID map[string]SealItem, plans map[string]PreparedPlan, itemFailures map[string]string) error {
	type rejectedItem struct{ id, tool, fp, summary, reason string }
	var rejected []rejectedItem

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sched.mu.Lock()
	b := s.sched.batches[batchID]
	if b == nil {
		s.sched.mu.Unlock()
		return fail("invalid_batch")
	}
	if b.sealed {
		s.sched.mu.Unlock()
		return nil
	}
	priorMutated := make(map[string]bool, len(b.ids))
	for _, id := range b.ids {
		e := s.sched.entries[id]
		item := byID[id]
		if e == nil {
			continue
		}
		if failReason, bad := itemFailures[id]; bad {
			s.sched.releaseEntryLocked(e)
			rejected = append(rejected, rejectedItem{id: id, tool: item.Tool, fp: item.Fingerprint, summary: resolveBatchSummary(item.Summary, "", ""), reason: failReason})
			continue
		}
		if item.Aborted || e.admissionClosed {
			reason := item.Reason
			if reason == "" {
				reason = e.cancelReason
			}
			s.sched.releaseEntryLocked(e)
			rejected = append(rejected, rejectedItem{id: id, tool: item.Tool, fp: item.Fingerprint, summary: resolveBatchSummary(item.Summary, "", ""), reason: reason})
			continue
		}
		plan := plans[id]
		if hasPriorBatchMutation(plan, priorMutated) {
			s.sched.releaseEntryLocked(e)
			rejected = append(rejected, rejectedItem{id: id, tool: item.Tool, fp: item.Fingerprint, summary: resolveBatchSummary(item.Summary, plan.Summary, e.summary), reason: "conflicting_batch_write"})
			s.recordTrace(id, item.Tool, "rejected", "", DefaultRoot, "conflicting_batch_write", "")
			continue
		}
		if plan.Modifying {
			for _, t := range plan.Targets {
				priorMutated[pathKey(t.Path)] = true
			}
		}
		s.sched.applyClaimFaults(&plan)
		sum := resolveBatchSummary(item.Summary, plan.Summary, e.summary)
		e.tool, e.plan, e.fingerprint, e.newBytes, e.summary, e.progress = item.Tool, plan, item.Fingerprint, item.NewBytes, sum, item.Progress
		e.sealed, e.waitReason = true, ""
		if op, ok := s.records[id]; ok {
			s.sequence++
			op.Sequence = s.sequence
			op.Tool, op.Fingerprint, op.Summary, op.Progress, op.WaitReason = item.Tool, item.Fingerprint, sum, item.Progress, ""
			s.records[id] = op
		}
		s.recordTrace(id, item.Tool, "sealed", "", DefaultRoot, "", "")
	}
	b.sealed = true
	s.sched.pruneBatchIfEmptyLocked(batchID, b)
	s.sched.broadcastLocked()
	s.sched.mu.Unlock()
	for _, r := range rejected {
		if op, ok := s.records[r.id]; ok && op.State == "started" {
			if r.tool != "" {
				op.Tool = r.tool
			}
			if r.fp != "" {
				op.Fingerprint = r.fp
			}
			if r.summary != "" {
				op.Summary = r.summary
			}
			s.records[r.id] = op
		}
		s.releaseRegisteredSlotLocked(r.id, r.reason)
	}
	return nil
}

func (s *Service) abortBatchLocked(batchID, reason string) {
	s.sched.mu.Lock()
	b := s.sched.batches[batchID]
	if b == nil {
		s.sched.mu.Unlock()
		return
	}
	ids := append([]string(nil), b.ids...)
	delete(s.sched.batches, batchID)
	for _, id := range ids {
		if e := s.sched.entries[id]; e != nil && !e.granted {
			s.sched.releaseEntryLocked(e)
		}
	}
	s.sched.mu.Unlock()
	for _, id := range ids {
		s.releaseRegisteredSlotLocked(id, reason)
	}
}

func (s *Service) releaseRegisteredSlotLocked(id, reason string) {
	if s.reservedActivityBytes >= MaxOperationBytes {
		s.reservedActivityBytes -= MaxOperationBytes
	} else {
		s.reservedActivityBytes = 0
	}
	wasRegistered := s.registered[id]
	delete(s.registered, id)
	if op, ok := s.records[id]; ok && op.State == "started" && s.callsByID[id] == nil {
		if !wasRegistered {
			delete(s.records, id)
			return
		}
		if reason == "" {
			reason = "cancelled"
		}
		if reason == "cancelled" || reason == "runtime_stopped" {
			op.State = "cancelled"
		} else {
			op.State = "failed"
		}
		s.sequence++
		op.Sequence = s.sequence
		op.Phase = PhaseTerminal
		op.WaitReason = ""
		op.Detail = reason
		op.Finished = time.Now().UTC()
		if rawBytes, err := s.persist(op); err == nil {
			s.journalSizes[id] = int64(len(rawBytes))
		}
		s.records[id] = publicOperation(op)
	}
}

func (s *Service) persist(op Operation) ([]byte, error) {
	data, err := json.Marshal(op)
	if err != nil {
		return nil, err
	}
	return data, config.AtomicFile(filepath.Join(s.journal, op.ID+".json"), data, 0600)
}

func (s *Service) EnableTrace(limit int) {
	s.traceMu.Lock()
	defer s.traceMu.Unlock()
	s.traceEnabled = limit > 0
	s.traceLimit = limit
	s.traceSeq = 0
	s.trace = nil
}

func (s *Service) TraceEvents() []TraceEvent {
	s.traceMu.Lock()
	defer s.traceMu.Unlock()
	return append([]TraceEvent(nil), s.trace...)
}

func (s *Service) SetIOHook(fn func(callID, tool string, eff Effect, relPath string)) {
	s.traceMu.Lock()
	s.ioHook = fn
	s.traceMu.Unlock()
}

func (s *Service) SetEndHook(fn func(callID, stage string)) {
	s.traceMu.Lock()
	s.endHook = fn
	s.traceMu.Unlock()
}

func (s *Service) recordTrace(callID, tool, stage string, eff Effect, root, key, access string) {
	s.traceMu.Lock()
	defer s.traceMu.Unlock()
	if !s.traceEnabled {
		return
	}
	seq := atomic.AddInt64(&s.traceSeq, 1)
	if len(s.trace) < s.traceLimit {
		s.trace = append(s.trace, TraceEvent{Seq: seq, CallID: callID, Tool: tool, Stage: stage, Effect: string(eff), Root: root, Key: key, Access: access})
	}
}

func (s *Service) fireIOHook(callID, tool string, eff Effect, relPath string) {
	s.traceMu.Lock()
	fn := s.ioHook
	s.traceMu.Unlock()
	if fn != nil {
		fn(callID, tool, eff, relPath)
	}
}

func (s *Service) fireEndHook(callID, stage string) {
	s.traceMu.Lock()
	fn := s.endHook
	s.traceMu.Unlock()
	if fn != nil {
		fn(callID, stage)
	}
}
