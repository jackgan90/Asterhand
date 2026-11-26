package workers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackgan90/Asterhand/internal/harnesses"
)

func (s *Service) markInterruptedOnStartup() error {
	pending, err := s.store.ListNonTerminalTurns()
	if err != nil {
		return err
	}
	now := s.now().UTC()
	for _, t := range pending {
		if t.State == TurnReconcile {
			if err := s.restoreRetainedWriteLockOnStartup(t, now); err != nil {
				return err
			}
			continue
		}
		t.State = TurnReconcile
		t.PendingInteraction = nil
		t.UpdatedAt = now
		if t.ErrorCode == "" {
			t.ErrorCode = "node_restarted"
			t.ErrorMessage = "Node restarted while turn was in progress; reconcile or manually resolve before sending new turns"
		}
		if t.AccessMode == harnesses.AccessWorkspaceWrite {
			t.WriteLockRetained = true
			if t.WriteLockReason == "" {
				t.WriteLockReason = t.ErrorCode
			}
		}
		var workerToSave *Worker
		if w, err := s.store.GetWorker(t.WorkerID); err == nil && w.State != WorkerClosed {
			w.State = WorkerReconcile
			w.ActiveTurnID = t.ID
			w.UpdatedAt = now
			workerToSave = &w
		}
		if err := s.store.SaveTurnAndWorker(t, workerToSave); err != nil {
			return err
		}
		if err := s.restoreRetainedWriteLockOnStartup(t, now); err != nil {
			return err
		}
	}
	s.store.PruneExpiredDetails(now)
	return nil
}

func (s *Service) restoreRetainedWriteLockOnStartup(t Turn, now time.Time) error {
	if t.AccessMode != harnesses.AccessWorkspaceWrite {
		return nil
	}
	if !t.WriteLockRetained && t.ErrorCode != "process_stop_unconfirmed" {
		return nil
	}
	root := strings.TrimSpace(t.WriteLockRoot)
	key := strings.TrimSpace(t.WriteLockKey)
	if root == "" || key == "" {
		if w, err := s.store.GetWorker(t.WorkerID); err == nil {
			if root == "" {
				root = w.WorkspaceRoot
			}
			if key == "" {
				key = w.WorktreeKey
			}
		}
	}
	// Preserve the dispatch-time WriteLockKey as an immutable fact of this turn;
	// only backfill from live git topology when legacy records have no key.
	if key == "" && root != "" {
		if liveKey, _ := harnesses.WorktreeKey(root); liveKey != "" {
			key = liveKey
		}
	}
	if root == "" && key == "" {
		return nil
	}
	s.mu.Lock()
	s.writeLocks[t.ID] = worktreeLock{
		turnID:        t.ID,
		workerID:      t.WorkerID,
		workspaceRoot: root,
		worktreeKey:   key,
		retained:      true,
	}
	s.mu.Unlock()
	if !t.WriteLockRetained || t.WriteLockRoot != root || t.WriteLockKey != key || t.WriteLockReason == "" {
		t.WriteLockRetained = true
		t.WriteLockRoot = root
		t.WriteLockKey = key
		if t.WriteLockReason == "" {
			t.WriteLockReason = t.ErrorCode
		}
		t.UpdatedAt = now
		if err := s.store.SaveTurn(t); err != nil {
			return err
		}
	}
	return nil
}

// BeginStop integrates into App.RequestStop(confirmed). Active worker turns
// block unconfirmed stop; idle resident adapter processes do not.
func (s *Service) BeginStop(confirmed bool, next func() bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !confirmed && len(s.activeTurns) > 0 {
		return false
	}
	if next != nil && !next() {
		return false
	}
	s.stopping = true
	return true
}

func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.stopping = true
	turns := make([]*runningTurn, 0, len(s.activeTurns))
	for _, rt := range s.activeTurns {
		rt.cancelled = true
		turns = append(turns, rt)
	}
	s.mu.Unlock()

	s.reaperOnce.Do(func() { close(s.reaperStop) })

	for _, rt := range turns {
		rt.signalCancel()
	}

	waitDone := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(waitDone)
	}()
	drained := false
	var closeTimeoutErr error
	select {
	case <-waitDone:
		drained = true
	case <-ctx.Done():
		closeTimeoutErr = ctx.Err()
		for _, rt := range turns {
			rt.signalForceKill()
		}
		// Prefer letting the turn runner arbitrate ForceKill and persist
		// reconciliation_required / retained write locks; fall back to direct
		// ForceKill if an adapter ignores InterruptTurn cancellation.
		select {
		case <-waitDone:
			drained = true
		case <-time.After(300 * time.Millisecond):
			s.forceKillActiveTurnsOnClose()
			select {
			case <-waitDone:
				drained = true
			case <-time.After(2 * time.Second):
			}
		}
	}

	s.mu.Lock()
	residents := make([]*residentProc, 0, len(s.residents))
	for k, rp := range s.residents {
		residents = append(residents, rp)
		delete(s.residents, k)
	}
	s.closed = true
	s.mu.Unlock()

	for _, rp := range residents {
		if ad := s.adapters[rp.harnessID]; ad != nil {
			_ = ad.CloseThread(rp.handle)
		}
	}

	var errs []error
	s.mu.Lock()
	for _, rt := range turns {
		if rt.stopErr != nil {
			errs = append(errs, rt.stopErr)
		} else if rt.forceKillErr != nil {
			errs = append(errs, rt.forceKillErr)
		}
		if rt.persistErr != nil {
			errs = append(errs, rt.persistErr)
		}
	}
	s.mu.Unlock()

	if !drained {
		// Never close the database while background goroutines may still touch it.
		go func() {
			<-waitDone
			_ = s.store.Close()
		}()
		if closeTimeoutErr == nil {
			closeTimeoutErr = context.DeadlineExceeded
		}
		errs = append(errs, fmt.Errorf("workers service close timed out waiting for active goroutines: %w", closeTimeoutErr))
		return errors.Join(errs...)
	}
	if storeErr := s.store.Close(); storeErr != nil {
		errs = append(errs, storeErr)
	}
	return errors.Join(errs...)
}

func (s *Service) forceKillActiveTurnsOnClose() {
	type killTarget struct {
		rt     *runningTurn
		ad     Adapter
		handle ThreadHandle
	}
	var targets []killTarget
	s.mu.Lock()
	for _, rt := range s.activeTurns {
		if rt.handle != nil {
			if ad := s.adapters[rt.harnessID]; ad != nil {
				targets = append(targets, killTarget{rt: rt, ad: ad, handle: *rt.handle})
			}
		}
	}
	s.mu.Unlock()
	for _, kt := range targets {
		if kErr := kt.ad.ForceKill(kt.handle); kErr != nil {
			s.mu.Lock()
			if kt.rt.forceKillErr == nil {
				kt.rt.forceKillErr = kErr
			}
			s.mu.Unlock()
		}
	}
}

func (s *Service) acquireHandle(ctx context.Context, adapter Adapter, w Worker, exe, ownerTurnID string, forReconcile bool) (ThreadHandle, error) {
	if err := ctx.Err(); err != nil {
		return ThreadHandle{}, err
	}
	s.mu.Lock()
	if rp, ok := s.residents[w.ID]; ok {
		rp.lastUsedAt = s.now().UTC()
		h := rp.handle
		s.mu.Unlock()
		return h, nil
	}
	if len(s.residents) >= s.budgets.MaxResidentProcs {
		var oldestKey string
		var oldestTime time.Time
		for k, rp := range s.residents {
			if _, active := s.workerTurn[k]; active {
				continue
			}
			if oldestKey == "" || rp.lastUsedAt.Before(oldestTime) {
				oldestKey = k
				oldestTime = rp.lastUsedAt
			}
		}
		if oldestKey != "" {
			evicted := s.residents[oldestKey]
			delete(s.residents, oldestKey)
			s.mu.Unlock()
			if ad := s.adapters[evicted.harnessID]; ad != nil {
				_ = ad.CloseThread(evicted.handle)
			}
		} else {
			s.mu.Unlock()
		}
	} else {
		s.mu.Unlock()
	}

	h, err := adapter.OpenThread(ctx, ThreadOpenSpec{
		WorkerID:                     w.ID,
		Executable:                   exe,
		WorkspaceRoot:                w.WorkspaceRoot,
		AccessMode:                   w.AccessMode,
		ApprovalMode:                 w.EffectiveApprovalMode,
		SessionAutoApprove:           w.SessionAutoApprove,
		Model:                        w.Model,
		ReasoningEffort:              w.ReasoningEffort,
		NativeDefaultsCaptured:       w.NativeDefaultsCaptured,
		NativeDefaultModel:           w.NativeDefaultModel,
		NativeDefaultReasoningEffort: w.NativeDefaultReasoningEffort,
		RoleHint:                     w.RoleHint,
		NativeSessionID:              w.NativeSessionID,
	})
	if err != nil {
		return ThreadHandle{}, err
	}
	s.mu.Lock()
	chosen, redundant, regErr := s.registerResidentLocked(ctx, w, h, ownerTurnID, forReconcile)
	s.mu.Unlock()
	if redundant != nil {
		_ = adapter.CloseThread(*redundant)
	}
	if regErr != nil {
		return ThreadHandle{}, regErr
	}
	return chosen, nil
}

func (s *Service) registerResidentLocked(ctx context.Context, w Worker, h ThreadHandle, ownerTurnID string, forReconcile bool) (ThreadHandle, *ThreadHandle, error) {
	if ctx.Err() != nil {
		return ThreadHandle{}, &h, ctx.Err()
	}
	if s.stopping || s.closed {
		return ThreadHandle{}, &h, errors.New("service stopping")
	}
	now := s.now().UTC()
	if curW, err := s.store.GetWorker(w.ID); err != nil || curW.State == WorkerClosed {
		return ThreadHandle{}, &h, errTurnStateStale
	}
	if forReconcile {
		if activeTID, busy := s.workerTurn[w.ID]; busy && activeTID != ownerTurnID {
			return ThreadHandle{}, &h, errTurnStateStale
		}
		if curTurn, err := s.store.GetTurn(ownerTurnID); err != nil || curTurn.State != TurnReconcile {
			return ThreadHandle{}, &h, errTurnStateStale
		}
	} else if activeTID, ok := s.workerTurn[w.ID]; !ok || activeTID != ownerTurnID {
		return ThreadHandle{}, &h, errTurnStateStale
	}
	if existing, exists := s.residents[w.ID]; exists {
		existing.lastUsedAt = now
		return existing.handle, &h, nil
	}
	s.nextResidentID++
	h.instanceID = s.nextResidentID
	s.residents[w.ID] = &residentProc{
		workerID:   w.ID,
		harnessID:  w.HarnessID,
		handle:     h,
		lastUsedAt: now,
	}
	return h, nil, nil
}

func (s *Service) closeDetachedResident(rp *residentProc) {
	if rp == nil {
		return
	}
	if ad := s.adapters[rp.harnessID]; ad != nil {
		_ = ad.CloseThread(rp.handle)
	}
}

func (s *Service) evictResidentByHandle(h ThreadHandle) {
	s.mu.Lock()
	for k, rp := range s.residents {
		if sameHandle(rp.handle, h) {
			delete(s.residents, k)
		}
	}
	s.mu.Unlock()
}

func (s *Service) idleReaperLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.reaperStop:
			return
		case <-ticker.C:
			s.reapIdleResidents()
		}
	}
}

func (s *Service) reapIdleResidents() {
	now := s.now().UTC()
	var toClose []*residentProc
	s.mu.Lock()
	for k, rp := range s.residents {
		if _, active := s.workerTurn[k]; active {
			continue
		}
		if now.Sub(rp.lastUsedAt) >= s.budgets.IdleRecycleTimeout {
			toClose = append(toClose, rp)
			delete(s.residents, k)
		}
	}
	s.mu.Unlock()
	for _, rp := range toClose {
		if ad := s.adapters[rp.harnessID]; ad != nil {
			_ = ad.CloseThread(rp.handle)
		}
	}
}

// RevokeWorkspace aborts pending or in-flight turns whose workspace was removed
// or whose access_mode now exceeds newMaxAccess.
func (s *Service) RevokeWorkspace(workspaceID, newMaxAccess string) {
	newRank := harnesses.AccessModeRank(newMaxAccess)
	reason := "Workspace authorization was revoked"
	if newRank > 0 {
		reason = "Workspace access mode was downgraded below turn requirement"
	}
	s.mu.Lock()
	targets := []*runningTurn{}
	for _, rt := range s.activeTurns {
		if rt.workspaceID == workspaceID && (newRank == 0 || harnesses.AccessModeRank(rt.accessMode) > newRank) {
			rt.revoked = true
			rt.cancelled = true
			rt.revokeReason = reason
			targets = append(targets, rt)
		}
	}
	s.mu.Unlock()
	for _, rt := range targets {
		rt.signalCancel()
	}
}

// RevokeWorkerHarness aborts pending or in-flight turns running on harnessID.
func (s *Service) RevokeWorkerHarness(harnessID string) {
	reason := "Worker harness collaboration authorization was disabled"
	s.mu.Lock()
	targets := []*runningTurn{}
	for _, rt := range s.activeTurns {
		if rt.harnessID == harnessID {
			rt.revoked = true
			rt.cancelled = true
			rt.revokeReason = reason
			targets = append(targets, rt)
		}
	}
	s.mu.Unlock()
	for _, rt := range targets {
		rt.signalCancel()
	}
}

// RevokeWorkerWriteHarness aborts only workspace_write turns on harnessID when
// write permission is disabled while read_only worker collaboration remains on.
func (s *Service) RevokeWorkerWriteHarness(harnessID string) {
	reason := "Worker harness workspace_write authorization was disabled"
	s.mu.Lock()
	targets := []*runningTurn{}
	for _, rt := range s.activeTurns {
		if rt.harnessID == harnessID && rt.accessMode == harnesses.AccessWorkspaceWrite {
			rt.revoked = true
			rt.cancelled = true
			rt.revokeReason = reason
			targets = append(targets, rt)
		}
	}
	s.mu.Unlock()
	for _, rt := range targets {
		rt.signalCancel()
	}
}
