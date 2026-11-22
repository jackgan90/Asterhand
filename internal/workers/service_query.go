package workers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/harnesses"
)

func (s *Service) Wait(ctx context.Context, turnID, expectedWorkspaceID string, waitDur time.Duration) (Turn, string, bool, error) {
	if waitDur <= 0 || waitDur > automation.MaxWait {
		waitDur = automation.MaxWait
	}
	t, err := s.GetTurn(turnID, expectedWorkspaceID)
	if err != nil {
		return Turn{}, "", false, err
	}
	if IsTurnTerminal(t.State) || t.State == TurnReconcile {
		preview, trunc := s.readPreview(t)
		return t, preview, trunc, nil
	}
	ch := s.subscribeWaiter(turnID)
	defer s.unsubscribeWaiter(turnID, ch)

	t, err = s.GetTurn(turnID, expectedWorkspaceID)
	if err != nil {
		return Turn{}, "", false, err
	}
	if IsTurnTerminal(t.State) || t.State == TurnReconcile {
		preview, trunc := s.readPreview(t)
		return t, preview, trunc, nil
	}

	timer := time.NewTimer(waitDur)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			t, _ = s.GetTurn(turnID, expectedWorkspaceID)
			preview, trunc := s.readPreview(t)
			return t, preview, trunc, nil
		case <-timer.C:
			t, _ = s.GetTurn(turnID, expectedWorkspaceID)
			preview, trunc := s.readPreview(t)
			return t, preview, trunc, nil
		case <-ch:
			t, err = s.GetTurn(turnID, expectedWorkspaceID)
			if err != nil {
				return Turn{}, "", false, err
			}
			if IsTurnTerminal(t.State) || t.State == TurnReconcile {
				preview, trunc := s.readPreview(t)
				return t, preview, trunc, nil
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
		if triggered == "" && (IsTurnTerminal(t.State) || t.State == TurnReconcile) {
			triggered = t.ID
		}
		preview, trunc := s.readPreviewCapped(t, 4<<10)
		items = append(items, WaitAnyItem{
			TurnID:          t.ID,
			WorkerID:        t.WorkerID,
			WorkspaceID:     t.WorkspaceID,
			HarnessID:       t.HarnessID,
			State:           t.State,
			Progress:        t.Progress,
			AnswerAvailable: t.AnswerAvailable,
			AnswerSource:    t.AnswerSource,
			AnswerPreview:   preview,
			Truncated:       trunc,
			ResultBytes:     t.ResultBytes,
			ElapsedSeconds:  t.ElapsedSeconds,
			IdleSeconds:     t.IdleSeconds,
			StreamEvents:    t.StreamEvents,
			StreamBytes:     t.StreamBytes,
			ErrorCode:       t.ErrorCode,
			ErrorMessage:    t.ErrorMessage,
			AfterVCS:        t.AfterVCS,
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
			if IsTurnTerminal(t.State) || t.State == TurnReconcile {
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
	handle, err := s.acquireHandle(ctx, adapter, w, exe)
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

	if t.RevocationRequested {
		t.State = TurnCancelledRevoked
		t.AnswerAvailable = false
		t.AnswerSource = AnswerSourceNone
		t.ErrorCode = "authorization_revoked"
		if t.RevocationReason != "" {
			t.ErrorMessage = t.RevocationReason
		}
		t.UpdatedAt = now
		t.FinishedAt = &now
		_ = s.store.SaveTurn(t)
		if w.ActiveTurnID == t.ID {
			w.State = WorkerIdle
			w.ActiveTurnID = ""
			w.UpdatedAt = now
			_ = s.store.SaveWorker(w)
		}
		return t, nil
	}

	if !IsTurnTerminal(matched.Status) {
		t.UpdatedAt = now
		_ = s.store.SaveTurn(t)
		return t, nil
	}
	s.finalizeTurn(nil, &w, &t, matched.Status, matched.Items, "", "")
	return t, nil
}

// ResolveTurn lets an operator explicitly close a reconciliation_required turn
// with an audit reason without faking success, unblocking the worker.
func (s *Service) ResolveTurn(turnID, actor, reason string) (Turn, error) {
	if strings.TrimSpace(reason) == "" {
		return Turn{}, automation.Fail("invalid_parameters", "Resolution reason is required")
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
	t.State = TurnFailed
	t.AnswerAvailable = false
	t.AnswerSource = AnswerSourceNone
	t.ErrorCode = "reconciled_manually_closed"
	t.ErrorMessage = fmt.Sprintf("Resolved by %s: %s", actor, strings.TrimSpace(reason))
	t.UpdatedAt = now
	t.FinishedAt = &now
	if err := s.store.SaveTurn(t); err != nil {
		return Turn{}, err
	}
	if w, err := s.store.GetWorker(t.WorkerID); err == nil && w.ActiveTurnID == t.ID && w.State == WorkerReconcile {
		w.State = WorkerIdle
		w.ActiveTurnID = ""
		w.UpdatedAt = now
		_ = s.store.SaveWorker(w)
	}
	s.notifyWaiters(t.ID)
	return t, nil
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
	switch opName {
	case "workers_create":
		w, err := s.store.WorkerByOperationID(opID)
		if errors.Is(err, ErrNotFound) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		return map[string]any{"worker_id": w.ID, "workspace_id": w.WorkspaceID, "alias": w.Alias, "state": w.State}, true, nil
	case "workers_send":
		t, err := s.store.TurnByOperationID(opID)
		if errors.Is(err, ErrNotFound) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		return map[string]any{"worker_id": t.WorkerID, "turn_id": t.ID, "workspace_id": t.WorkspaceID, "state": t.State}, true, nil
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

func (s *Service) checkRevoked(rt *runningTurn) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return rt.revoked, rt.revokeReason
}

func (s *Service) releaseTurnLocks(rt *runningTurn) {
	s.mu.Lock()
	delete(s.activeTurns, rt.turnID)
	if s.workerTurn[rt.workerID] == rt.turnID {
		delete(s.workerTurn, rt.workerID)
	}
	if s.writeLocks[rt.worktreeKey] == rt.turnID {
		delete(s.writeLocks, rt.worktreeKey)
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
