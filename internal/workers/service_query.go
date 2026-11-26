package workers

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/harnesses"
)

func shouldWakeCaller(t Turn) bool {
	if IsTurnTerminal(t.State) || t.State == TurnReconcile {
		return true
	}
	if t.State == TurnWaitingInput {
		return t.PendingInteraction != nil && !t.PendingInteraction.VisibleToHuman
	}
	return false
}

func shouldWakeCallerWithOpts(t Turn, opts WaitOptions) bool {
	if shouldWakeCaller(t) {
		return true
	}
	if opts.WakeOnCommentary && t.State == TurnRunning && t.LatestCommentarySeq > opts.AfterCommentarySeq {
		return true
	}
	return false
}

func (s *Service) Wait(ctx context.Context, turnID, expectedWorkspaceID string, waitDur time.Duration) (Turn, string, bool, error) {
	t, preview, trunc, _, err := s.WaitWithOptions(ctx, turnID, expectedWorkspaceID, WaitOptions{WaitDuration: waitDur})
	return t, preview, trunc, err
}

func (s *Service) WaitWithOptions(ctx context.Context, turnID, expectedWorkspaceID string, opts WaitOptions) (Turn, string, bool, []CommentaryEntry, error) {
	waitDur := opts.WaitDuration
	if waitDur <= 0 || waitDur > automation.MaxWait {
		waitDur = automation.MaxWait
	}
	finish := func(t Turn) (Turn, string, bool, []CommentaryEntry, error) {
		if t.State == TurnWaitingInput && t.PendingInteraction != nil && !t.PendingInteraction.VisibleToHuman {
			s.markInputObservedByCaller(t.ID, t.PendingInteraction.RequestID)
			if refreshed, gErr := s.GetTurn(t.ID, expectedWorkspaceID); gErr == nil {
				t = refreshed
			}
		}
		preview, trunc := s.readPreview(t)
		comments, latestSeq := s.selectCommentaries(t.ID, opts.AfterCommentarySeq, 2)
		if latestSeq > t.LatestCommentarySeq {
			t.LatestCommentarySeq = latestSeq
		}
		return t, preview, trunc, comments, nil
	}

	t, err := s.GetTurn(turnID, expectedWorkspaceID)
	if err != nil {
		return Turn{}, "", false, nil, err
	}
	if shouldWakeCallerWithOpts(t, opts) {
		return finish(t)
	}
	ch := s.subscribeWaiter(turnID)
	defer s.unsubscribeWaiter(turnID, ch)

	t, err = s.GetTurn(turnID, expectedWorkspaceID)
	if err != nil {
		return Turn{}, "", false, nil, err
	}
	if shouldWakeCallerWithOpts(t, opts) {
		return finish(t)
	}

	timer := time.NewTimer(waitDur)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			t, _ = s.GetTurn(turnID, expectedWorkspaceID)
			return finish(t)
		case <-timer.C:
			t, _ = s.GetTurn(turnID, expectedWorkspaceID)
			return finish(t)
		case <-ch:
			t, err = s.GetTurn(turnID, expectedWorkspaceID)
			if err != nil {
				return Turn{}, "", false, nil, err
			}
			if shouldWakeCallerWithOpts(t, opts) {
				return finish(t)
			}
		}
	}
}

func (s *Service) readPreviewCapped(t Turn, capBytes int) (string, bool) {
	if !t.AnswerAvailable || t.State != TurnCompleted {
		return "", false
	}
	full, ok, err := s.store.ReadResultText(t.ID)
	if err != nil || !ok {
		return "", false
	}
	if len(full) <= capBytes {
		return full, false
	}
	return full[:clampUTF8Bytes([]byte(full), capBytes)], true
}

func (s *Service) readPreview(t Turn) (string, bool) {
	return s.readPreviewCapped(t, 24<<10)
}

func (s *Service) buildWaitAnyItems(turnIDs []string, expectedWorkspaceID string) ([]WaitAnyItem, string, error) {
	items := make([]WaitAnyItem, 0, len(turnIDs))
	triggered := ""
	for _, id := range turnIDs {
		t, err := s.GetTurn(id, expectedWorkspaceID)
		if err != nil {
			return nil, "", err
		}
		if triggered == "" && shouldWakeCaller(t) {
			triggered = t.ID
		}
		if t.State == TurnWaitingInput && t.PendingInteraction != nil && !t.PendingInteraction.VisibleToHuman {
			s.markInputObservedByCaller(t.ID, t.PendingInteraction.RequestID)
			if refreshed, gErr := s.GetTurn(t.ID, expectedWorkspaceID); gErr == nil {
				t = refreshed
			}
		}
		preview, trunc := s.readPreviewCapped(t, 4<<10)
		nextCursor := ""
		if trunc {
			nextCursor = strconv.Itoa(len(preview))
		}
		comments, latestSeq := s.selectCommentaries(t.ID, 0, 2)
		if latestSeq > t.LatestCommentarySeq {
			t.LatestCommentarySeq = latestSeq
		}
		items = append(items, WaitAnyItem{
			TurnID:              t.ID,
			WorkerID:            t.WorkerID,
			WorkspaceID:         t.WorkspaceID,
			HarnessID:           t.HarnessID,
			State:               t.State,
			Progress:            t.Progress,
			AnswerAvailable:     t.AnswerAvailable,
			AnswerSource:        t.AnswerSource,
			AnswerPreview:       preview,
			Truncated:           trunc,
			ResultBytes:         t.ResultBytes,
			NextCursor:          nextCursor,
			ElapsedSeconds:      t.ElapsedSeconds,
			IdleSeconds:         t.IdleSeconds,
			StreamEvents:        t.StreamEvents,
			StreamBytes:         t.StreamBytes,
			ErrorCode:           t.ErrorCode,
			ErrorMessage:        t.ErrorMessage,
			AfterVCS:            t.AfterVCS,
			PendingInteraction:  t.PendingInteraction,
			LatestCommentarySeq: t.LatestCommentarySeq,
			Commentaries:        comments,
		})
	}
	return items, triggered, nil
}

// WaitAny monitors up to 16 turns concurrently in a single bounded call and
// returns immediately when any watched turn reaches a terminal or reconciliation
// state, or when waitDur expires.
func (s *Service) WaitAny(ctx context.Context, turnIDs []string, expectedWorkspaceID string, waitDur time.Duration) ([]WaitAnyItem, string, error) {
	if waitDur <= 0 || waitDur > automation.MaxWait {
		waitDur = automation.MaxWait
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(turnIDs))
	for _, raw := range turnIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			return nil, "", automation.Fail("invalid_parameters", "turn_ids entries must be non-empty")
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 || len(ids) > 16 {
		return nil, "", automation.Fail("invalid_parameters", "turn_ids must contain between 1 and 16 turn identifiers")
	}

	items, triggered, err := s.buildWaitAnyItems(ids, expectedWorkspaceID)
	if err != nil || triggered != "" {
		return items, triggered, err
	}

	type sub struct {
		id string
		ch chan struct{}
	}
	subs := make([]sub, 0, len(ids))
	for _, id := range ids {
		subs = append(subs, sub{id: id, ch: s.subscribeWaiter(id)})
	}
	stopFwd := make(chan struct{})
	defer func() {
		close(stopFwd)
		for _, sb := range subs {
			s.unsubscribeWaiter(sb.id, sb.ch)
		}
	}()

	items, triggered, err = s.buildWaitAnyItems(ids, expectedWorkspaceID)
	if err != nil || triggered != "" {
		return items, triggered, err
	}

	anyCh := make(chan string, len(subs))
	for _, sb := range subs {
		go func(id string, ch chan struct{}) {
			for {
				select {
				case <-stopFwd:
					return
				case <-ch:
					select {
					case anyCh <- id:
					default:
					}
				}
			}
		}(sb.id, sb.ch)
	}

	timer := time.NewTimer(waitDur)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return s.buildWaitAnyItems(ids, expectedWorkspaceID)
		case <-timer.C:
			return s.buildWaitAnyItems(ids, expectedWorkspaceID)
		case id := <-anyCh:
			t, gErr := s.GetTurn(id, expectedWorkspaceID)
			if gErr != nil {
				return nil, "", gErr
			}
			if shouldWakeCaller(t) {
				return s.buildWaitAnyItems(ids, expectedWorkspaceID)
			}
		}
	}
}

type ResultPage struct {
	TurnID          string `json:"turn_id"`
	WorkerID        string `json:"worker_id"`
	State           string `json:"state"`
	AnswerAvailable bool   `json:"answer_available"`
	AnswerSource    string `json:"answer_source"`
	PartialOutput   string `json:"partial_output,omitempty"`
	Text            string `json:"text,omitempty"`
	Offset          int    `json:"offset"`
	TotalBytes      int    `json:"total_bytes"`
	Complete        bool   `json:"complete"`
	Expired         bool   `json:"expired"`
	NextCursor      string `json:"next_cursor,omitempty"`
}

func (s *Service) ReadResult(turnID, expectedWorkspaceID string, offset, maxBytes int) (ResultPage, error) {
	t, err := s.GetTurn(turnID, expectedWorkspaceID)
	if err != nil {
		return ResultPage{}, err
	}
	if maxBytes <= 0 || maxBytes > 32<<10 {
		maxBytes = 24 << 10
	}
	if offset < 0 {
		return ResultPage{}, automation.Fail("invalid_cursor", "Negative result offset")
	}
	page := ResultPage{
		TurnID:          t.ID,
		WorkerID:        t.WorkerID,
		State:           t.State,
		AnswerAvailable: t.AnswerAvailable,
		AnswerSource:    t.AnswerSource,
		PartialOutput:   t.PartialOutput,
		Offset:          offset,
		TotalBytes:      t.ResultBytes,
	}
	if t.State != TurnCompleted || !t.AnswerAvailable {
		page.Complete = true
		return page, nil
	}
	full, ok, err := s.store.ReadResultText(t.ID)
	if err != nil {
		return ResultPage{}, err
	}
	if !ok {
		page.Expired = t.ResultBytes > 0
		page.Complete = true
		return page, nil
	}
	page.TotalBytes = len(full)
	if offset >= len(full) {
		page.Complete = true
		return page, nil
	}
	if !utf8.RuneStart(full[offset]) {
		return ResultPage{}, automation.Fail("invalid_cursor", "Cursor offset does not align with a UTF-8 character boundary")
	}
	rem := full[offset:]
	take := clampUTF8Bytes([]byte(rem), maxBytes)
	if take == 0 && len(rem) > 0 {
		_, take = utf8.DecodeRuneInString(rem)
	}
	page.Text = rem[:take]
	if offset+take >= len(full) {
		page.Complete = true
	} else {
		page.NextCursor = fmt.Sprintf("%d", offset+take)
	}
	return page, nil
}

func (s *Service) ReadLogs(turnID, expectedWorkspaceID string, afterSeq int64, limit int) (Turn, []LogEntry, bool, error) {
	t, err := s.GetTurn(turnID, expectedWorkspaceID)
	if err != nil {
		return Turn{}, nil, false, err
	}
	entries, hasMore, err := s.store.ReadLogs(t.ID, afterSeq, limit)
	return t, entries, hasMore, err
}

func (s *Service) ReadLogsTail(turnID, expectedWorkspaceID string, limit int) (Turn, []LogEntry, bool, error) {
	t, err := s.GetTurn(turnID, expectedWorkspaceID)
	if err != nil {
		return Turn{}, nil, false, err
	}
	entries, hasMore, err := s.store.ReadLogsTail(t.ID, limit)
	return t, entries, hasMore, err
}

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

func (s *Service) ActiveTurnsSnapshot() []Turn {
	s.mu.Lock()
	ids := make([]string, 0, len(s.activeTurns))
	for id := range s.activeTurns {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	out := []Turn{}
	for _, id := range ids {
		if t, err := s.store.GetTurn(id); err == nil {
			out = append(out, t)
		}
	}
	if pending, err := s.store.ListNonTerminalTurns(); err == nil {
		seen := map[string]bool{}
		for _, t := range out {
			seen[t.ID] = true
		}
		for _, t := range pending {
			if !seen[t.ID] {
				out = append(out, t)
			}
		}
	}
	return out
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

func (s *Service) checkRevoked(rt *runningTurn) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return rt.revoked, rt.revokeReason
}

func (s *Service) checkCancelOrRevoked(rt *runningTurn) (cancelled bool, revoked bool, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rt.revoked {
		return true, true, rt.revokeReason
	}
	if rt.cancelled || (rt.turnCtx != nil && rt.turnCtx.Err() != nil) {
		return true, false, ""
	}
	return false, false, ""
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

func (s *Service) releaseTurnLocks(rt *runningTurn) {
	s.mu.Lock()
	delete(s.activeTurns, rt.turnID)
	if s.workerTurn[rt.workerID] == rt.turnID {
		delete(s.workerTurn, rt.workerID)
	}
	if lk, ok := s.writeLocks[rt.turnID]; ok && !lk.retained {
		delete(s.writeLocks, rt.turnID)
	}
	s.mu.Unlock()
}

func (s *Service) subscribeWaiter(turnID string) chan struct{} {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.waiters[turnID] = append(s.waiters[turnID], ch)
	s.mu.Unlock()
	return ch
}

func (s *Service) unsubscribeWaiter(turnID string, target chan struct{}) {
	s.mu.Lock()
	list := s.waiters[turnID]
	for i, ch := range list {
		if ch == target {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(s.waiters, turnID)
	} else {
		s.waiters[turnID] = list
	}
	s.mu.Unlock()
}

func (s *Service) notifyWaiters(turnID string) {
	s.mu.Lock()
	list := append([]chan struct{}(nil), s.waiters[turnID]...)
	s.mu.Unlock()
	for _, ch := range list {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// SteerTurn injects mid-turn guidance into an actively running turn when the
// underlying harness adapter implements SteerAdapter (e.g. Codex turn/steer,
// Claude Code stream-json stdin). Never silently cancels or restarts turns on
// harnesses without native steering support.
func (s *Service) SteerTurn(ctx context.Context, in SteerInput) (Turn, error) {
	turnID := strings.TrimSpace(in.TurnID)
	if turnID == "" {
		return Turn{}, automation.Fail("invalid_parameters", "turn_id is required")
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return Turn{}, automation.Fail("invalid_parameters", "text is required")
	}
	if len(text) > maxSteerBytes {
		return Turn{}, automation.Fail("invalid_parameters", fmt.Sprintf("text exceeds %d bytes limit", maxSteerBytes))
	}

	t, err := s.GetTurn(turnID, in.WorkspaceID)
	if err != nil {
		return Turn{}, err
	}
	switch t.State {
	case TurnWaitingApproval:
		return Turn{}, automation.Fail("turn_not_running", "Turn is waiting for human permission approval; wait for approval to finish before steering")
	case TurnWaitingInput:
		return Turn{}, automation.Fail("turn_not_running", "Turn is waiting for input; use workers_reply instead of workers_steer")
	case TurnRunning:
	default:
		return Turn{}, automation.Fail("turn_not_running", fmt.Sprintf("Turn is in %s state; wait or use workers_send to start a new turn instead", t.State))
	}

	s.mu.Lock()
	rt, ok := s.activeTurns[turnID]
	var adapter Adapter
	var handle ThreadHandle
	var nativeTurnID string
	if ok && rt != nil && rt.handle != nil {
		adapter = s.adapters[rt.harnessID]
		handle = *rt.handle
		nativeTurnID = rt.nativeTurnID
	}
	s.mu.Unlock()

	if !ok || rt == nil || adapter == nil || nativeTurnID == "" {
		return Turn{}, automation.Fail("turn_not_running", "Turn is not actively executing yet; wait briefly and retry")
	}
	steerer, ok := adapter.(SteerAdapter)
	if !ok {
		return Turn{}, automation.Fail("unsupported_steer", fmt.Sprintf("Harness %s does not support mid-turn steering; wait for this turn to finish (or cancel it) and call workers_send", t.HarnessID))
	}

	steerCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := steerer.SteerTurn(steerCtx, handle, nativeTurnID, text); err != nil {
		return Turn{}, automation.Fail("steer_failed", fmt.Sprintf("Failed to steer running turn: %v", err))
	}

	now := s.now().UTC()
	s.mu.Lock()
	if cur, exists := s.activeTurns[turnID]; exists {
		cur.lastEventAt = now
	}
	s.mu.Unlock()

	_, _ = s.store.AppendLog(turnID, LogEntry{
		Timestamp: now,
		Kind:      "steer",
		Summary:   truncateSummary("Steer: "+text, 500),
	})
	if latest, gErr := s.store.GetTurn(turnID); gErr == nil && latest.State == TurnRunning {
		latest.Progress = "Steer delivered: " + truncateSummary(text, 120)
		latest.UpdatedAt = now
		_ = s.store.SaveTurn(latest)
		t = latest
	}
	s.populateTurnActivity(&t)
	s.notifyWaiters(turnID)
	return t, nil
}
