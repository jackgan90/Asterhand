package workers

import (
	"context"
	"errors"
	"strings"
	"time"
)

type turnStartResult struct {
	nativeID string
	doneCh   <-chan TurnOutcome
	err      error
}

func (s *Service) runTurn(rt *runningTurn, w Worker, t Turn, exe, prompt string) {
	defer s.wg.Done()
	defer close(rt.doneCh)
	defer s.releaseTurnLocks(rt)

	adapter := s.adapters[w.HarnessID]
	if adapter == nil {
		s.finalizeTurn(rt, &w, &t, TurnFailed, nil, "unsupported_worker", "No adapter available")
		return
	}

	t.BaselineVCS = CaptureVCSSnapshot(context.Background(), w.WorkspaceRoot)
	_ = s.store.SaveTurn(t)

	// Window 1 check: authorization revoked before native thread/turn start.
	if revoked, reason := s.checkRevoked(rt); revoked {
		s.finalizeRevoked(rt, &w, &t, reason, "")
		return
	}

	handle, err := s.acquireHandle(context.Background(), adapter, w, exe)
	if err != nil {
		code := "adapter_start_failed"
		if strings.Contains(err.Error(), "native_session_unrecoverable") {
			code = "native_session_unrecoverable"
		}
		s.finalizeTurn(rt, &w, &t, TurnFailed, nil, code, err.Error())
		return
	}
	s.mu.Lock()
	rt.handle = &handle
	s.mu.Unlock()

	if w.NativeSessionID == "" && handle.NativeSessionID != "" {
		now := s.now().UTC()
		w.NativeSessionID = handle.NativeSessionID
		t.NativeSessionID = handle.NativeSessionID
		w.UpdatedAt = now
		t.UpdatedAt = now
		s.updateWorkerState(w.ID, func(cur *Worker) {
			cur.NativeSessionID = handle.NativeSessionID
			cur.UpdatedAt = now
		})
		_ = s.store.SaveTurn(t)
	}

	// Re-check Window 1 immediately before issuing native turn/start.
	if revoked, reason := s.checkRevoked(rt); revoked {
		s.finalizeRevoked(rt, &w, &t, reason, "")
		return
	}

	sr, ok := s.awaitTurnStart(rt, &w, &t, adapter, handle, prompt)
	if !ok {
		return
	}
	s.awaitTurnExecution(rt, &w, &t, adapter, handle, sr)
}

func (s *Service) awaitTurnStart(rt *runningTurn, w *Worker, t *Turn, adapter Adapter, handle ThreadHandle, prompt string) (turnStartResult, bool) {
	onEvent := func(ev AdapterEvent) {
		now := s.now().UTC()
		sz := ev.Bytes
		if sz <= 0 {
			sz = len(ev.Summary)
		}
		s.mu.Lock()
		rt.lastEventAt = now
		rt.streamEvents++
		if sz > 0 {
			rt.streamBytes += int64(sz)
		}
		s.mu.Unlock()
		if ev.Kind != "delta" && ev.Kind != "progress" {
			_, _ = s.store.AppendLog(t.ID, LogEntry{
				Timestamp: now,
				Kind:      ev.Kind,
				ItemID:    ev.ItemID,
				Phase:     ev.Phase,
				Summary:   ev.Summary,
			})
		}
		switch ev.Kind {
		case "approval_mode":
			// Only reflect runtime fallback when worker was created with approval_mode="auto";
			// never let runtime init overwrite explicit strict_deny or read_only deny_escalation.
			if mode := strings.TrimSpace(ev.Summary); mode != "" && w.ApprovalMode == ApprovalModeAuto && w.EffectiveApprovalMode != mode {
				w.EffectiveApprovalMode = mode
				w.UpdatedAt = now
				s.updateWorkerState(w.ID, func(cur *Worker) {
					if cur.ApprovalMode == ApprovalModeAuto {
						cur.EffectiveApprovalMode = mode
						cur.UpdatedAt = now
					}
				})
			}
		case "progress", "commentary", "command", "edit", "mcp", "search", "plan", "tool", "system", "approval_auto":
			if ev.Summary != "" {
				t.Progress = ev.Summary
				t.UpdatedAt = now
				_ = s.store.SaveTurn(*t)
				s.notifyWaiters(t.ID)
			}
		}
	}

	onInteraction := func(ictx context.Context, req InteractionRequest) (InteractionDecision, error) {
		return s.handleTurnInteraction(ictx, rt, w, t, req)
	}

	// Window 2: turn/start is in-flight before nativeTurnID returns.
	startCtx, cancelStart := context.WithTimeout(context.Background(), 30*time.Second)
	startCh := make(chan turnStartResult, 1)
	go func() {
		nid, dch, sErr := adapter.StartTurn(startCtx, handle, TurnStartSpec{
			ThreadID:            handle.NativeSessionID,
			WorkspaceRoot:       w.WorkspaceRoot,
			AccessMode:          w.AccessMode,
			ApprovalMode:        w.EffectiveApprovalMode,
			SessionAutoApprove:  w.SessionAutoApprove,
			Prompt:              prompt,
			ClientCorrelationID: t.ClientCorrelationID,
			Model:               t.Model,
			ReasoningEffort:     t.ReasoningEffort,
			OnInteraction:       onInteraction,
		}, onEvent)
		startCh <- turnStartResult{nativeID: nid, doneCh: dch, err: sErr}
	}()

	var sr turnStartResult
	select {
	case sr = <-startCh:
		cancelStart()
	case <-rt.cancelCh:
		s.handleCancelDuringStart(rt, w, t, adapter, handle, startCh, cancelStart)
		return turnStartResult{}, false
	}

	if sr.err != nil {
		s.evictResident(w.ID)
		_ = adapter.ForceKill(handle)
		if errors.Is(sr.err, ErrPreStartFailed) {
			code := "adapter_start_failed"
			if strings.Contains(sr.err.Error(), "native_session_unrecoverable") {
				code = "native_session_unrecoverable"
			}
			s.finalizeTurn(rt, w, t, TurnFailed, nil, code, sr.err.Error())
			return turnStartResult{}, false
		}
		s.enterReconciliation(w, t, "turn_start_unacknowledged", sr.err.Error())
		return turnStartResult{}, false
	}
	if revoked, reason := s.checkRevoked(rt); revoked {
		t.NativeTurnID = sr.nativeID
		t.RevocationRequested = true
		t.RevocationReason = reason
		_ = s.store.SaveTurn(*t)
		_ = adapter.InterruptTurn(context.Background(), handle, sr.nativeID)
		s.waitStopOrKill(adapter, handle, sr.doneCh)
		s.finalizeRevoked(rt, w, t, reason, sr.nativeID)
		return turnStartResult{}, false
	}
	return sr, true
}

func (s *Service) handleCancelDuringStart(
	rt *runningTurn,
	w *Worker,
	t *Turn,
	adapter Adapter,
	handle ThreadHandle,
	startCh <-chan turnStartResult,
	cancelStart context.CancelFunc,
) {
	revoked, reason := s.checkRevoked(rt)
	if revoked {
		t.RevocationRequested = true
		t.RevocationReason = reason
		t.UpdatedAt = s.now().UTC()
		_ = s.store.SaveTurn(*t)
	}
	grace := time.NewTimer(minDuration(s.budgets.InterruptGrace, 2*time.Second))
	select {
	case sr := <-startCh:
		grace.Stop()
		cancelStart()
		if sr.err == nil && sr.nativeID != "" {
			t.NativeTurnID = sr.nativeID
			_ = s.store.SaveTurn(*t)
			_ = adapter.InterruptTurn(context.Background(), handle, sr.nativeID)
			s.waitStopOrKill(adapter, handle, sr.doneCh)
		} else {
			_ = adapter.ForceKill(handle)
			s.evictResident(w.ID)
		}
	case <-grace.C:
		cancelStart()
		_ = adapter.ForceKill(handle)
		s.evictResident(w.ID)
		go func() {
			late := <-startCh
			if late.err == nil && late.nativeID != "" {
				if cur, err := s.store.GetTurn(t.ID); err == nil && cur.NativeTurnID == "" {
					cur.NativeTurnID = late.nativeID
					cur.UpdatedAt = s.now().UTC()
					_ = s.store.SaveTurn(cur)
				}
			}
		}()
	}
	if revoked {
		s.finalizeRevoked(rt, w, t, reason, t.NativeTurnID)
	} else {
		s.finalizeTurn(rt, w, t, TurnCancelled, nil, "cancelled", "Turn cancelled before native acknowledgment")
	}
}

func (s *Service) awaitTurnExecution(rt *runningTurn, w *Worker, t *Turn, adapter Adapter, handle ThreadHandle, sr turnStartResult) {
	now := s.now().UTC()
	s.mu.Lock()
	rt.nativeTurnID = sr.nativeID
	rt.execStarted = now
	rt.lastEventAt = now
	s.mu.Unlock()
	t.NativeTurnID = sr.nativeID
	if t.State != TurnWaitingApproval && t.State != TurnWaitingInput {
		t.State = TurnRunning
	}
	t.UpdatedAt = now
	_ = s.store.SaveTurn(*t)
	s.notifyWaiters(t.ID)

	wallDur := rt.wallClock
	if wallDur <= 0 {
		wallDur = s.budgets.WallClockTimeout
	}
	tickInterval := minDuration(minDuration(s.budgets.StallTimeout/4+20*time.Millisecond, wallDur/4+20*time.Millisecond), 1*time.Second)
	if tickInterval < 20*time.Millisecond {
		tickInterval = 20 * time.Millisecond
	}
	stallTick := time.NewTicker(tickInterval)
	defer stallTick.Stop()

	for {
		select {
		case outcome := <-sr.doneCh:
			if revoked, reason := s.checkRevoked(rt); revoked {
				s.finalizeRevoked(rt, w, t, reason, outcome.NativeTurnID)
				return
			}
			s.finalizeTurn(rt, w, t, outcome.State, outcome.Items, outcome.ErrorCode, outcome.ErrorMessage)
			return

		case <-rt.cancelCh:
			revoked, reason := s.checkRevoked(rt)
			if revoked {
				t.RevocationRequested = true
				t.RevocationReason = reason
				_ = s.store.SaveTurn(*t)
			}
			_ = adapter.InterruptTurn(context.Background(), handle, sr.nativeID)
			outcome, ok := s.waitStopOrKill(adapter, handle, sr.doneCh)
			if revoked {
				s.finalizeRevoked(rt, w, t, reason, sr.nativeID)
			} else if ok && outcome.State != TurnCompleted {
				s.finalizeTurn(rt, w, t, TurnCancelled, outcome.Items, "cancelled", "Cancelled by caller or node shutdown")
			} else {
				s.finalizeTurn(rt, w, t, TurnCancelled, nil, "cancelled", "Cancelled by caller or node shutdown")
			}
			return

		case <-stallTick.C:
			curNow := s.now().UTC()
			s.mu.Lock()
			paused := !rt.waitingSince.IsZero()
			activeElapsed := curNow.Sub(rt.execStarted) - rt.pausedTotal
			idleFor := curNow.Sub(rt.lastEventAt)
			s.mu.Unlock()
			if paused {
				continue
			}
			if activeElapsed >= wallDur {
				_ = adapter.InterruptTurn(context.Background(), handle, sr.nativeID)
				outcome, _ := s.waitStopOrKill(adapter, handle, sr.doneCh)
				s.finalizeTurn(rt, w, t, TurnInterrupted, outcome.Items, "wall_clock_timeout", "Turn exceeded wall-clock execution budget")
				return
			}
			if idleFor >= s.budgets.StallTimeout {
				_ = adapter.InterruptTurn(context.Background(), handle, sr.nativeID)
				outcome, _ := s.waitStopOrKill(adapter, handle, sr.doneCh)
				s.finalizeTurn(rt, w, t, TurnInterrupted, outcome.Items, "stall_timeout", "Worker stopped emitting events before completion")
				return
			}
		}
	}
}

func (s *Service) waitStopOrKill(adapter Adapter, handle ThreadHandle, doneCh <-chan TurnOutcome) (TurnOutcome, bool) {
	if doneCh != nil {
		timer := time.NewTimer(s.budgets.InterruptGrace)
		defer timer.Stop()
		select {
		case out := <-doneCh:
			return out, true
		case <-timer.C:
		}
	}
	_ = adapter.ForceKill(handle)
	s.evictResidentByHandle(handle.Key)
	return TurnOutcome{}, false
}

func (s *Service) finalizeRevoked(rt *runningTurn, w *Worker, t *Turn, reason, nativeTurnID string) {
	now := s.now().UTC()
	t.RevocationRequested = true
	t.RevocationReason = reason
	if nativeTurnID != "" && t.NativeTurnID == "" {
		t.NativeTurnID = nativeTurnID
	}
	t.State = TurnCancelledRevoked
	t.AnswerAvailable = false
	t.AnswerSource = AnswerSourceNone
	t.ErrorCode = "authorization_revoked"
	t.ErrorMessage = reason
	t.PendingInteraction = nil
	t.AfterVCS = CaptureVCSSnapshot(context.Background(), w.WorkspaceRoot)
	t.UpdatedAt = now
	t.FinishedAt = &now
	_ = s.store.SaveTurn(*t)

	w.State = WorkerIdle
	w.ActiveTurnID = ""
	w.UpdatedAt = now
	s.updateWorkerState(w.ID, func(cur *Worker) {
		cur.State = WorkerIdle
		cur.ActiveTurnID = ""
		cur.UpdatedAt = now
	})
	s.evictAndCloseResident(w.ID)
	s.notifyWaiters(t.ID)
}

func (s *Service) finalizeTurn(rt *runningTurn, w *Worker, t *Turn, state string, items []NativeItem, errCode, errMsg string) {
	now := s.now().UTC()
	if state == "" {
		state = TurnFailed
	}
	answer, source, avail, partial := SelectFinalAnswer(state, items)
	t.State = state
	t.PendingInteraction = nil
	t.AnswerSource = source
	t.AnswerAvailable = avail
	t.PartialOutput = truncateSummary(partial, 4096)
	t.ErrorCode = errCode
	t.ErrorMessage = errMsg
	if avail && answer != "" {
		if state == TurnCompleted {
			t.Progress = truncateSummary(answer, 160)
		}
		n, _, err := s.store.SaveResultText(t.ID, answer, now)
		if err == nil {
			t.ResultBytes = n
		}
	}
	t.AfterVCS = CaptureVCSSnapshot(context.Background(), w.WorkspaceRoot)
	t.UpdatedAt = now
	t.FinishedAt = &now
	_ = s.store.SaveTurn(*t)

	w.State = WorkerIdle
	w.ActiveTurnID = ""
	w.UpdatedAt = now
	s.updateWorkerState(w.ID, func(cur *Worker) {
		cur.State = WorkerIdle
		cur.ActiveTurnID = ""
		cur.UpdatedAt = now
	})
	if state == TurnCompleted {
		s.touchResident(w.ID, now)
	} else {
		s.evictAndCloseResident(w.ID)
	}
	s.notifyWaiters(t.ID)
}

func (s *Service) enterReconciliation(w *Worker, t *Turn, errCode, errMsg string) {
	now := s.now().UTC()
	t.State = TurnReconcile
	t.AnswerAvailable = false
	t.AnswerSource = AnswerSourceNone
	t.ErrorCode = errCode
	t.ErrorMessage = errMsg
	t.UpdatedAt = now
	_ = s.store.SaveTurn(*t)

	w.State = WorkerReconcile
	w.ActiveTurnID = t.ID
	w.UpdatedAt = now
	s.updateWorkerState(w.ID, func(cur *Worker) {
		cur.State = WorkerReconcile
		cur.ActiveTurnID = t.ID
		cur.UpdatedAt = now
	})
	s.notifyWaiters(t.ID)
}

// updateWorkerState reloads the latest persisted Worker row before applying
// narrow lifecycle mutations so concurrent human changes (e.g. revoking
// session_auto_approve via Web UI) are never clobbered by a stale turn snapshot.
func (s *Service) updateWorkerState(workerID string, mutate func(*Worker)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.store.GetWorker(workerID)
	if err != nil {
		return
	}
	mutate(&cur)
	_ = s.store.SaveWorker(cur)
}

func (s *Service) acquireHandle(ctx context.Context, adapter Adapter, w Worker, exe string) (ThreadHandle, error) {
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
		WorkerID:           w.ID,
		Executable:         exe,
		WorkspaceRoot:      w.WorkspaceRoot,
		AccessMode:         w.AccessMode,
		ApprovalMode:       w.EffectiveApprovalMode,
		SessionAutoApprove: w.SessionAutoApprove,
		Model:              w.Model,
		ReasoningEffort:    w.ReasoningEffort,
		RoleHint:           w.RoleHint,
		NativeSessionID:    w.NativeSessionID,
	})
	if err != nil {
		return ThreadHandle{}, err
	}
	s.mu.Lock()
	s.residents[w.ID] = &residentProc{
		workerID:   w.ID,
		harnessID:  w.HarnessID,
		handle:     h,
		lastUsedAt: s.now().UTC(),
	}
	s.mu.Unlock()
	return h, nil
}

func (s *Service) touchResident(workerID string, now time.Time) {
	s.mu.Lock()
	if rp, ok := s.residents[workerID]; ok {
		rp.lastUsedAt = now
	}
	s.mu.Unlock()
}

func (s *Service) evictResident(workerID string) {
	s.mu.Lock()
	delete(s.residents, workerID)
	s.mu.Unlock()
}

func (s *Service) evictAndCloseResident(workerID string) {
	s.mu.Lock()
	rp, ok := s.residents[workerID]
	if ok {
		delete(s.residents, workerID)
	}
	s.mu.Unlock()
	if ok && rp != nil {
		if ad := s.adapters[rp.harnessID]; ad != nil {
			_ = ad.CloseThread(rp.handle)
		}
	}
}

func (s *Service) evictResidentByHandle(key string) {
	s.mu.Lock()
	for k, rp := range s.residents {
		if rp.handle.Key == key {
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

func composePrompt(prompt string, artifacts []string, gitRef, diffBase, outputContract string) string {
	if len(artifacts) == 0 && gitRef == "" && diffBase == "" && outputContract == "" {
		return prompt
	}
	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString("\n\n---\nStructured Handoff Context:")
	if len(artifacts) > 0 {
		b.WriteString("\nReferenced workspace files:")
		for _, p := range artifacts {
			b.WriteString("\n- ")
			b.WriteString(p)
		}
	}
	if gitRef != "" {
		b.WriteString("\nTarget git ref: ")
		b.WriteString(gitRef)
	}
	if diffBase != "" {
		b.WriteString("\nDiff base: ")
		b.WriteString(diffBase)
	}
	if outputContract != "" {
		b.WriteString("\nExpected output contract: ")
		b.WriteString(outputContract)
	}
	return b.String()
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
