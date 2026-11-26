package workers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/harnesses"
)

// ReconcileTurn attempts exact correlation recovery via client_correlation_id.
// Never guesses by prompt equality; if revocation_requested was persisted, late
// native completion only appends audit facts and never promotes to completed.
func (s *Service) ReconcileTurn(ctx context.Context, turnID string) (Turn, error) {
	if _, _, fErr := s.flushPendingReconcile(turnID); fErr != nil {
		return Turn{}, fErr
	}
	t, err := s.store.GetTurn(turnID)
	if errors.Is(err, ErrNotFound) {
		return Turn{}, automation.Fail("not_found", "Turn not found")
	}
	if err != nil {
		return Turn{}, err
	}
	if t.State != TurnReconcile {
		return t, nil
	}
	w, err := s.store.GetWorker(t.WorkerID)
	if err != nil {
		return Turn{}, err
	}
	if w.NativeSessionID == "" {
		return t, nil
	}
	adapter := s.adapters[w.HarnessID]
	if adapter == nil {
		return t, nil
	}
	_, exe := s.harnesses.IsWorkerEnabled(w.HarnessID)
	handle, err := s.acquireHandle(ctx, adapter, w, exe, t.ID, true)
	if errors.Is(err, errTurnStateStale) {
		if latest, gErr := s.store.GetTurn(turnID); gErr == nil {
			return latest, nil
		}
		return t, nil
	}
	if err != nil {
		return t, nil
	}
	nativeTurns, err := adapter.InspectThreadTurns(ctx, handle)
	if err != nil {
		return t, nil
	}
	var matches []NativeTurnRecord
	for _, nt := range nativeTurns {
		for _, it := range nt.Items {
			if it.Type == "userMessage" && it.ClientID != "" && it.ClientID == t.ClientCorrelationID {
				matches = append(matches, nt)
				break
			}
		}
	}
	if len(matches) != 1 {
		return t, nil
	}
	matched := matches[0]
	now := s.now().UTC()
	t.NativeTurnID = matched.NativeTurnID

	// Never auto-finalize or release write locks while the native turn is still
	// non-terminal (even when revocation was requested): only record the
	// confirmed native_turn_id and keep reconciliation_required + retained locks.
	if !IsTurnTerminal(matched.Status) {
		curTurn, _, _, err := s.commitTurnAndWorker(t.ID, "", residentActionNone, requireReconcileState, func(cur *Turn) {
			cur.NativeTurnID = matched.NativeTurnID
			cur.UpdatedAt = now
		}, nil)
		if errors.Is(err, errTurnStateStale) {
			return curTurn, nil
		}
		if err != nil {
			return Turn{}, err
		}
		return curTurn, nil
	}

	if t.RevocationRequested {
		curTurn, _, evicted, err := s.commitTurnAndWorker(t.ID, w.ID, residentActionEvict, requireReconcileState, func(cur *Turn) {
			cur.NativeTurnID = matched.NativeTurnID
			cur.State = TurnCancelledRevoked
			cur.AnswerAvailable = false
			cur.AnswerSource = AnswerSourceNone
			cur.ErrorCode = "authorization_revoked"
			if cur.RevocationReason != "" {
				cur.ErrorMessage = cur.RevocationReason
			}
			cur.WriteLockRetained = false
			cur.WriteLockRoot = ""
			cur.WriteLockKey = ""
			cur.WriteLockReason = ""
			cur.UpdatedAt = now
			cur.FinishedAt = &now
		}, func(cur *Worker) bool {
			if cur.ActiveTurnID != t.ID {
				return false
			}
			cur.State = WorkerIdle
			cur.ActiveTurnID = ""
			cur.UpdatedAt = now
			return true
		})
		if errors.Is(err, errTurnStateStale) {
			return curTurn, nil
		}
		if err != nil {
			return Turn{}, err
		}
		s.releaseRetainedWriteLock(t.ID)
		s.closeDetachedResident(evicted)
		s.notifyWaiters(t.ID)
		return curTurn, nil
	}

	applied, err := s.finalizeTurn(nil, &w, &t, matched.Status, matched.Items, "", "")
	if err != nil {
		return Turn{}, err
	}
	if applied {
		s.releaseRetainedWriteLock(t.ID)
	}
	return t, nil
}

// ResolveTurn lets an operator explicitly close a reconciliation_required turn
// with an audit reason without faking success, unblocking the worker.
func (s *Service) ResolveTurn(turnID, actor, reason string) (Turn, error) {
	if strings.TrimSpace(reason) == "" {
		return Turn{}, automation.Fail("invalid_parameters", "Resolution reason is required")
	}
	if _, _, fErr := s.flushPendingReconcile(turnID); fErr != nil {
		return Turn{}, fErr
	}
	t, err := s.store.GetTurn(turnID)
	if errors.Is(err, ErrNotFound) {
		return Turn{}, automation.Fail("not_found", "Turn not found")
	}
	if err != nil {
		return Turn{}, err
	}
	if t.State != TurnReconcile {
		return Turn{}, automation.Fail("conflict", "Only reconciliation_required turns can be manually resolved")
	}
	now := s.now().UTC()
	curTurn, _, _, err := s.commitTurnAndWorker(turnID, t.WorkerID, residentActionNone, func(cur Turn) error {
		if cur.State != TurnReconcile {
			return automation.Fail("conflict", "Only reconciliation_required turns can be manually resolved")
		}
		return nil
	}, func(cur *Turn) {
		cur.State = TurnFailed
		cur.AnswerAvailable = false
		cur.AnswerSource = AnswerSourceNone
		cur.ErrorCode = "reconciled_manually_closed"
		cur.ErrorMessage = fmt.Sprintf("Resolved by %s: %s", actor, strings.TrimSpace(reason))
		cur.WriteLockRetained = false
		cur.WriteLockRoot = ""
		cur.WriteLockKey = ""
		cur.WriteLockReason = ""
		cur.UpdatedAt = now
		cur.FinishedAt = &now
	}, func(cur *Worker) bool {
		if cur.ActiveTurnID != turnID || cur.State != WorkerReconcile {
			return false
		}
		cur.State = WorkerIdle
		cur.ActiveTurnID = ""
		cur.UpdatedAt = now
		return true
	})
	if err != nil {
		return Turn{}, err
	}
	s.releaseRetainedWriteLock(curTurn.ID)
	s.notifyWaiters(curTurn.ID)
	return curTurn, nil
}

// OperationLookup supports reconcileAutomationOperation when a crash occurred
// after a worker/turn record was persisted and before the public receipt finished.
func (s *Service) OperationLookup(opName, opID string) (any, bool, error) {
	if opID == "" {
		return nil, false, nil
	}
	switch opName {
	case "workers_create":
		ws, err := s.store.ListWorkersByOperationID(opID, 2)
		if err != nil || len(ws) != 1 {
			return nil, false, err
		}
		return workerOpSummary(ws[0]), true, nil
	case "workers_send":
		ts, err := s.store.ListTurnsByOperationID(opID, 2)
		if err != nil || len(ts) != 1 {
			return nil, false, err
		}
		return turnOpSummary(ts[0]), true, nil
	default:
		return nil, false, nil
	}
}

// LegacyOperationLookup reconciles a pre-migration worker/turn row keyed by
// bare operation_id ONLY when the domain record itself carries durable
// ownership proof matching actor and target.
func (s *Service) LegacyOperationLookup(opName, rawOpID, actor, target string) (any, bool, error) {
	if rawOpID == "" {
		return nil, false, nil
	}
	switch opName {
	case "workers_create":
		ws, err := s.store.ListWorkersByOperationID(rawOpID, 2)
		if err != nil || len(ws) != 1 || !legacyWorkerMatchesActor(ws[0], actor, target) {
			return nil, false, err
		}
		return workerOpSummary(ws[0]), true, nil
	case "workers_send":
		ts, err := s.store.ListTurnsByOperationID(rawOpID, 2)
		if err != nil || len(ts) != 1 || !legacyTurnMatchesActor(ts[0], actor, target) {
			return nil, false, err
		}
		return turnOpSummary(ts[0]), true, nil
	default:
		return nil, false, nil
	}
}

func (s *Service) recordPersistFailure(rt *runningTurn, w *Worker, t *Turn, errCode, errMsg string, retainLock bool, lockRoot, lockKey, nativeTurnID string, revoked bool, revokeReason string, persistErr error) {
	if rt == nil {
		return
	}
	shouldRetain := retainLock && w.AccessMode == harnesses.AccessWorkspaceWrite
	if lockRoot == "" {
		lockRoot = rt.workspaceRoot
	}
	if lockKey == "" {
		lockKey = rt.worktreeKey
	}
	s.recordTurnPersistErr(rt, persistErr)
	if shouldRetain {
		s.retainWriteLock(rt.turnID)
	}
	s.mu.Lock()
	s.pendingReconcile[t.ID] = pendingReconcileInfo{
		turnID: t.ID, workerID: w.ID, errCode: errCode, errMsg: errMsg,
		retainLock: shouldRetain,
		lockRoot: lockRoot, lockKey: lockKey, nativeTurnID: nativeTurnID,
		revoked: revoked, revokeReason: revokeReason,
	}
	s.mu.Unlock()
}

func (s *Service) hasPendingOrRetainedForWorkerLocked(workerID string) bool {
	for _, pr := range s.pendingReconcile {
		if pr.workerID == workerID {
			return true
		}
	}
	for _, lk := range s.writeLocks {
		if lk.workerID == workerID && lk.retained {
			return true
		}
	}
	return false
}

func (s *Service) flushPendingReconcile(turnID string) (Turn, bool, error) {
	now := s.now().UTC()
	s.mu.Lock()
	pr, ok := s.pendingReconcile[turnID]
	if !ok {
		s.mu.Unlock()
		return Turn{}, false, nil
	}
	curTurn, _, _, err := s.commitTurnAndWorkerLocked(pr.turnID, pr.workerID, residentActionNone, func(cur Turn) error {
		if IsTurnTerminal(cur.State) || cur.State == TurnReconcile {
			return errTurnStateStale
		}
		return nil
	}, func(cur *Turn) {
		cur.State = TurnReconcile
		cur.PendingInteraction = nil
		cur.AnswerAvailable = false
		cur.AnswerSource = AnswerSourceNone
		cur.ErrorCode = pr.errCode
		cur.ErrorMessage = pr.errMsg
		if pr.nativeTurnID != "" && cur.NativeTurnID == "" {
			cur.NativeTurnID = pr.nativeTurnID
		}
		if pr.revoked {
			cur.RevocationRequested = true
			cur.RevocationReason = pr.revokeReason
		}
		if pr.retainLock && cur.AccessMode == harnesses.AccessWorkspaceWrite {
			cur.WriteLockRetained = true
			if cur.WriteLockRoot == "" {
				cur.WriteLockRoot = pr.lockRoot
			}
			if cur.WriteLockKey == "" {
				cur.WriteLockKey = pr.lockKey
			}
			cur.WriteLockReason = pr.errCode
		} else {
			cur.WriteLockRetained = false
			cur.WriteLockRoot = ""
			cur.WriteLockKey = ""
			cur.WriteLockReason = ""
		}
		cur.UpdatedAt = now
	}, func(cur *Worker) bool {
		if cur.State == WorkerClosed || (cur.ActiveTurnID != pr.turnID && cur.ActiveTurnID != "") {
			return false
		}
		cur.State = WorkerReconcile
		cur.ActiveTurnID = pr.turnID
		cur.UpdatedAt = now
		return true
	})
	if errors.Is(err, errTurnStateStale) {
		delete(s.pendingReconcile, turnID)
		s.mu.Unlock()
		return curTurn, true, nil
	}
	if err != nil {
		s.mu.Unlock()
		return Turn{}, true, err
	}
	delete(s.pendingReconcile, turnID)
	s.mu.Unlock()
	s.notifyWaiters(turnID)
	return curTurn, true, nil
}

func requireReconcileState(cur Turn) error {
	if cur.State != TurnReconcile {
		return errTurnStateStale
	}
	return nil
}

func (s *Service) recordTurnPersistErr(rt *runningTurn, err error) {
	if rt == nil || err == nil {
		return
	}
	s.mu.Lock()
	if rt.persistErr == nil {
		rt.persistErr = err
	}
	s.mu.Unlock()
}

func (s *Service) commitTurnAndWorker(
	turnID, workerID string,
	resAction residentCommitAction,
	checkTurn func(Turn) error,
	mutateTurn func(*Turn),
	mutateWorker func(*Worker) bool,
) (Turn, bool, *residentProc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commitTurnAndWorkerLocked(turnID, workerID, resAction, checkTurn, mutateTurn, mutateWorker)
}

func (s *Service) commitTurnAndWorkerLocked(
	turnID, workerID string,
	resAction residentCommitAction,
	checkTurn func(Turn) error,
	mutateTurn func(*Turn),
	mutateWorker func(*Worker) bool,
) (Turn, bool, *residentProc, error) {
	curTurn, err := s.store.GetTurn(turnID)
	if err != nil {
		return Turn{}, false, nil, err
	}
	if checkTurn != nil {
		if err := checkTurn(curTurn); err != nil {
			return curTurn, false, nil, err
		}
	}
	mutateTurn(&curTurn)
	var workerPtr *Worker
	workerUpdated := false
	if workerID != "" && mutateWorker != nil {
		curWorker, wErr := s.store.GetWorker(workerID)
		if wErr != nil {
			return Turn{}, false, nil, wErr
		}
		if mutateWorker(&curWorker) {
			workerPtr = &curWorker
			workerUpdated = true
		}
	}
	if err := s.store.SaveTurnAndWorker(curTurn, workerPtr); err != nil {
		return Turn{}, false, nil, err
	}
	if IsTurnTerminal(curTurn.State) && !curTurn.WriteLockRetained {
		if lk, ok := s.writeLocks[turnID]; ok && lk.retained {
			delete(s.writeLocks, turnID)
		}
	}
	var evicted *residentProc
	if workerUpdated && resAction != residentActionNone {
		active, hasActive := s.workerTurn[workerID]
		if !hasActive || active == "" || active == turnID {
			if rp, ok := s.residents[workerID]; ok {
				switch resAction {
				case residentActionTouch:
					rp.lastUsedAt = curTurn.UpdatedAt
				case residentActionEvict:
					evicted = rp
					delete(s.residents, workerID)
				}
			}
		}
	}
	return curTurn, workerUpdated, evicted, nil
}

func (s *Service) retainWriteLock(turnID string) {
	s.mu.Lock()
	if lk, ok := s.writeLocks[turnID]; ok {
		lk.retained = true
		s.writeLocks[turnID] = lk
	}
	s.mu.Unlock()
}

func (s *Service) releaseRetainedWriteLock(turnID string) {
	s.mu.Lock()
	delete(s.writeLocks, turnID)
	s.mu.Unlock()
}

func (s *Service) updateTurnState(turnID string, mutate func(*Turn)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.store.GetTurn(turnID)
	if err != nil {
		return err
	}
	mutate(&cur)
	return s.store.SaveTurn(cur)
}

// updateWorkerState reloads the latest persisted Worker row before applying
// narrow lifecycle mutations so concurrent human changes (e.g. revoking
// session_auto_approve via Web UI) are never clobbered by a stale turn snapshot.
func (s *Service) updateWorkerState(workerID string, mutate func(*Worker)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.store.GetWorker(workerID)
	if err != nil {
		return err
	}
	mutate(&cur)
	return s.store.SaveWorker(cur)
}
