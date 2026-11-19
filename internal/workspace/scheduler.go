package workspace

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
)

const (
	PhaseRegistered       = "registered"
	PhaseQueued           = "queued"
	PhaseAwaitingApproval = "awaiting_approval"
	PhasePreparing        = "preparing"
	PhaseRunning          = "running"
	PhaseDraining         = "draining"
	PhaseTerminal         = "terminal"
)

type schedBatch struct {
	id     string
	scope  Scope
	sealed bool
	ids    []string
}

type schedEntry struct {
	id               string
	batch            string
	order            int
	ticket           int64
	scope            Scope
	tool             string
	summary          string
	inputFingerprint string
	fingerprint      string
	plan             PreparedPlan
	newBytes         int64
	progress         []Step
	phase            string
	waitReason       string
	granted          bool
	sealed           bool
	admissionClosed  bool
	inflight         int
	cancelReason     string
	violation        string
	system           bool
}

// mutationFaultHooks lets isolated mutation tests inject single-point bugs without
// touching production behavior when nil.
type mutationFaultHooks struct {
	downgradeWriteToShared   bool
	treatSharedExclusiveOK   bool
	omitMissingParentClaims  bool
	ignoreCallIdentity       bool
	releaseBeforeChildDrain  bool
	nonAtomicQuotaCheck      bool
	skipPinActiveJournals    bool
	ignoreBatchOrderFairness bool
	forceGlobalSerialize     bool
}

type scheduler struct {
	mu               sync.Mutex
	notify           chan struct{}
	nextTicket       int64
	entries          map[string]*schedEntry
	batches          map[string]*schedBatch
	activeCalls      int
	modifyCalls      int
	indexCalls       int
	maxActive        int
	maxModifying     int
	maxIndex         int
	maxQueued        int
	activeExec       atomic.Int32
	awaitingApproval atomic.Int32
	faults           *mutationFaultHooks
}

func (sc *scheduler) wake() {
	sc.mu.Lock()
	sc.broadcastLocked()
	sc.mu.Unlock()
}

func (e *schedEntry) isAdmissionClosed(sc *scheduler) bool {
	if sc == nil || e == nil {
		return false
	}
	sc.mu.Lock()
	closed := e.admissionClosed
	sc.mu.Unlock()
	return closed
}

func newScheduler() *scheduler {
	return &scheduler{
		notify:       make(chan struct{}),
		entries:      map[string]*schedEntry{},
		batches:      map[string]*schedBatch{},
		maxActive:    MaxActiveCalls,
		maxModifying: MaxModifyingCalls,
		maxIndex:     MaxIndexCalls,
		maxQueued:    MaxQueuedCalls,
	}
}

func (sc *scheduler) broadcastLocked() {
	close(sc.notify)
	sc.notify = make(chan struct{})
}

func (sc *scheduler) queuedCountLocked() int {
	n := 0
	for _, e := range sc.entries {
		if e.phase == PhaseRegistered || e.phase == PhaseQueued {
			n++
		}
	}
	return n
}

func (sc *scheduler) hasNonTerminalLocked() bool {
	for _, e := range sc.entries {
		if e.phase != PhaseTerminal {
			return true
		}
	}
	return false
}

func (sc *scheduler) activeIDsLocked() []string {
	var ids []string
	for id, e := range sc.entries {
		if e.phase != PhaseTerminal {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func (sc *scheduler) applyClaimFaults(plan *PreparedPlan) {
	if sc.faults == nil {
		return
	}
	if sc.faults.omitMissingParentClaims && len(plan.Targets) > 0 {
		targetSet := map[string]bool{}
		for _, t := range plan.Targets {
			targetSet[pathKey(t.Path)] = true
		}
		filtered := plan.Claims[:0]
		for _, c := range plan.Claims {
			if c.Domain == DomainFS && !targetSet[c.Key] {
				continue
			}
			filtered = append(filtered, c)
		}
		plan.Claims = filtered
	}
	if sc.faults.downgradeWriteToShared {
		for i := range plan.Claims {
			if plan.Claims[i].Domain == DomainFS {
				plan.Claims[i].Access = AccessShared
			}
		}
	}
}

func (sc *scheduler) claimsConflictLocked(a, b []ResourceClaim) bool {
	if sc.faults != nil && sc.faults.forceGlobalSerialize {
		return true
	}
	for _, ca := range a {
		for _, cb := range b {
			if sc.faults != nil && sc.faults.treatSharedExclusiveOK && ca.Access != cb.Access {
				continue
			}
			if ClaimsConflict(ca, cb) {
				return true
			}
		}
	}
	return false
}

func (sc *scheduler) canGrantLocked(e *schedEntry) (bool, string) {
	if !e.sealed {
		return false, "batch_seal"
	}
	if sc.faults != nil && sc.faults.forceGlobalSerialize && sc.activeCalls > 0 {
		return false, "active_limit"
	}
	if sc.activeCalls >= sc.maxActive {
		return false, "active_limit"
	}
	if e.plan.Modifying && sc.modifyCalls >= sc.maxModifying {
		return false, "modifying_limit"
	}
	if e.plan.Modifying && e.tool != "exec_command" {
		if sc.activeExec.Load() > 0 {
			return false, "exec_active"
		}
		if sc.awaitingApproval.Load() > 0 {
			return false, "approval_pending"
		}
	}
	if e.plan.Indexing && sc.indexCalls >= sc.maxIndex {
		return false, "index_limit"
	}
	for _, other := range sc.entries {
		if other.id == e.id || other.phase == PhaseTerminal {
			continue
		}
		if other.granted {
			if sc.claimsConflictLocked(other.plan.Claims, e.plan.Claims) {
				return false, "resource_conflict"
			}
			continue
		}
		if sc.faults != nil && sc.faults.ignoreBatchOrderFairness {
			continue
		}
		if other.ticket < e.ticket {
			if !other.sealed {
				return false, "batch_seal"
			}
			if sc.claimsConflictLocked(other.plan.Claims, e.plan.Claims) {
				return false, "resource_conflict"
			}
		}
	}
	return true, ""
}

func (sc *scheduler) grantLocked(e *schedEntry) {
	e.granted = true
	e.waitReason = ""
	e.phase = PhasePreparing
	sc.activeCalls++
	if e.plan.Modifying {
		sc.modifyCalls++
	}
	if e.plan.Indexing {
		sc.indexCalls++
	}
}

func (sc *scheduler) releaseEntryLocked(e *schedEntry) {
	if e.granted {
		e.granted = false
		sc.activeCalls--
		if e.plan.Modifying {
			sc.modifyCalls--
		}
		if e.plan.Indexing {
			sc.indexCalls--
		}
	}
	e.phase = PhaseTerminal
	e.waitReason = ""
	delete(sc.entries, e.id)
	if b := sc.batches[e.batch]; b != nil && b.sealed {
		sc.pruneBatchIfEmptyLocked(e.batch, b)
	}
	sc.broadcastLocked()
}

func (sc *scheduler) pruneBatchIfEmptyLocked(batchID string, b *schedBatch) {
	for _, id := range b.ids {
		if _, ok := sc.entries[id]; ok {
			return
		}
	}
	delete(sc.batches, batchID)
}

func (sc *scheduler) acquire(ctx context.Context, id, fingerprint string, onWait func(reason string)) (*schedEntry, error) {
	for {
		sc.mu.Lock()
		e, ok := sc.entries[id]
		if !ok {
			sc.mu.Unlock()
			return nil, fail("not_found")
		}
		if e.sealed && fingerprint != "" && e.fingerprint != "" && e.fingerprint != fingerprint {
			sc.mu.Unlock()
			return nil, fail("fingerprint_mismatch")
		}
		if e.granted {
			sc.mu.Unlock()
			return e, nil
		}
		if e.admissionClosed {
			reason := e.cancelReason
			sc.releaseEntryLocked(e)
			sc.mu.Unlock()
			if reason == "" {
				reason = "cancelled"
			}
			return nil, fail(reason)
		}
		okGrant, reason := sc.canGrantLocked(e)
		if okGrant {
			sc.grantLocked(e)
			sc.mu.Unlock()
			return e, nil
		}
		changed := e.phase != PhaseQueued || e.waitReason != reason
		e.phase = PhaseQueued
		e.waitReason = reason
		waitCh := sc.notify
		sc.mu.Unlock()
		if changed && onWait != nil {
			onWait(reason)
		}
		select {
		case <-waitCh:
		case <-ctx.Done():
			sc.mu.Lock()
			if cur, exists := sc.entries[id]; exists && !cur.granted {
				sc.releaseEntryLocked(cur)
			}
			sc.mu.Unlock()
			return nil, fail("cancelled")
		}
	}
}

func (sc *scheduler) waitDrained(id string) {
	if sc.faults != nil && sc.faults.releaseBeforeChildDrain {
		return
	}
	for {
		sc.mu.Lock()
		e, ok := sc.entries[id]
		if !ok || e.inflight == 0 {
			sc.mu.Unlock()
			return
		}
		waitCh := sc.notify
		sc.mu.Unlock()
		<-waitCh
	}
}
