package workers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackgan90/Asterhand/internal/harnesses"
)

type turnStartResult struct {
	nativeID string
	doneCh   <-chan TurnOutcome
	err      error
}

func (s *Service) runTurn(rt *runningTurn, w Worker, t Turn, exe, prompt string) {
	defer s.wg.Done()
	defer close(rt.doneCh)
	defer func() {
		if rt.forceKillCancel != nil {
			rt.forceKillCancel()
		}
	}()
	defer rt.signalCancel()
	defer s.releaseTurnLocks(rt)

	adapter := s.adapters[w.HarnessID]
	if adapter == nil {
		s.finalizeTurn(rt, &w, &t, TurnFailed, nil, "unsupported_worker", "No adapter available")
		return
	}

	if cancelled, revoked, reason := s.checkCancelOrRevoked(rt); cancelled {
		if revoked {
			s.finalizeRevoked(rt, &w, &t, reason, "")
		} else {
			s.finalizeTurn(rt, &w, &t, TurnCancelled, nil, "cancelled", "Turn cancelled before start")
		}
		return
	}

	t.BaselineVCS = CaptureVCSSnapshot(rt.turnCtx, w.WorkspaceRoot)
	s.updateTurnState(t.ID, func(cur *Turn) {
		cur.BaselineVCS = t.BaselineVCS
	})

	// Window 1 check: authorization revoked or turn cancelled before OpenThread.
	if cancelled, revoked, reason := s.checkCancelOrRevoked(rt); cancelled {
		if revoked {
			s.finalizeRevoked(rt, &w, &t, reason, "")
		} else {
			s.finalizeTurn(rt, &w, &t, TurnCancelled, nil, "cancelled", "Turn cancelled before start")
		}
		return
	}

	handle, err := s.acquireHandle(rt.turnCtx, adapter, w, exe, t.ID, false)
	if err != nil {
		if cancelled, revoked, reason := s.checkCancelOrRevoked(rt); cancelled {
			if revoked {
				s.finalizeRevoked(rt, &w, &t, reason, "")
			} else {
				s.finalizeTurn(rt, &w, &t, TurnCancelled, nil, "cancelled", "Turn cancelled during startup")
			}
			return
		}
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
		s.updateTurnState(t.ID, func(cur *Turn) {
			cur.NativeSessionID = handle.NativeSessionID
			cur.UpdatedAt = now
		})
	}

	// Re-check Window 1 immediately before issuing native turn/start.
	if cancelled, revoked, reason := s.checkCancelOrRevoked(rt); cancelled {
		if revoked {
			s.finalizeRevoked(rt, &w, &t, reason, "")
		} else {
			s.finalizeTurn(rt, &w, &t, TurnCancelled, nil, "cancelled", "Turn cancelled before dispatch")
		}
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
		case "commentary":
			fullText := ""
			if ev.Item != nil {
				fullText = strings.TrimSpace(ev.Item.Text)
			}
			if fullText == "" {
				fullText = strings.TrimSpace(ev.Summary)
			}
			seq := s.recordCommentary(rt, fullText, now)
			prog := firstNonEmptyStr(ev.Summary, truncateSummary(fullText, 160))
			if seq > 0 || prog != "" {
				s.updateTurnState(t.ID, func(cur *Turn) {
					if seq > cur.LatestCommentarySeq {
						cur.LatestCommentarySeq = seq
					}
					if prog != "" && cur.State != TurnWaitingApproval && cur.State != TurnWaitingInput {
						cur.Progress = prog
					}
					cur.UpdatedAt = now
				})
				s.notifyWaiters(t.ID)
			}
		case "progress", "command", "edit", "mcp", "search", "plan", "tool", "system", "approval_auto":
			if ev.Summary != "" {
				s.updateTurnState(t.ID, func(cur *Turn) {
					if cur.State != TurnWaitingApproval && cur.State != TurnWaitingInput {
						cur.Progress = ev.Summary
					}
					cur.UpdatedAt = now
				})
				s.notifyWaiters(t.ID)
			}
		}
	}

	onInteraction := func(ictx context.Context, req InteractionRequest) (InteractionDecision, error) {
		return s.handleTurnInteraction(ictx, rt, w, t, req)
	}

	// Final synchronous arbitration before launching StartTurn: if cancel or
	// revocation already happened, never dispatch to the adapter.
	if cancelled, revoked, reason := s.checkCancelOrRevoked(rt); cancelled {
		if revoked {
			s.finalizeRevoked(rt, w, t, reason, "")
		} else {
			s.finalizeTurn(rt, w, t, TurnCancelled, nil, "cancelled", "Turn cancelled before dispatch")
		}
		return turnStartResult{}, false
	}

	// Window 2: turn/start is in-flight before nativeTurnID returns.
	startCtx, cancelStart := context.WithTimeout(rt.turnCtx, 30*time.Second)
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
		s.evictResidentByHandle(handle)
		killErr := adapter.ForceKill(handle)
		if killErr != nil {
			s.recordTurnStopErr(rt, killErr)
			_ = s.enterReconciliationRetained(rt, w, t, "process_stop_unconfirmed", fmt.Sprintf("Turn start failed (%v) and process termination could not be confirmed: %v", sr.err, killErr))
			return turnStartResult{}, false
		}
		if errors.Is(sr.err, ErrPreStartFailed) {
			if cancelled, revoked, reason := s.checkCancelOrRevoked(rt); cancelled {
				if revoked {
					_, _ = s.finalizeRevoked(rt, w, t, reason, "")
				} else {
					_, _ = s.finalizeTurn(rt, w, t, TurnCancelled, nil, "cancelled", "Turn cancelled before dispatch")
				}
				return turnStartResult{}, false
			}
			code := "adapter_start_failed"
			if strings.Contains(sr.err.Error(), "native_session_unrecoverable") {
				code = "native_session_unrecoverable"
			}
			_, _ = s.finalizeTurn(rt, w, t, TurnFailed, nil, code, sr.err.Error())
			return turnStartResult{}, false
		}
		_ = s.enterReconciliation(rt, w, t, "turn_start_unacknowledged", sr.err.Error())
		return turnStartResult{}, false
	}
	if cancelled, revoked, reason := s.checkCancelOrRevoked(rt); cancelled {
		t.NativeTurnID = sr.nativeID
		if revoked {
			t.RevocationRequested = true
			t.RevocationReason = reason
		}
		_ = s.updateTurnState(t.ID, func(cur *Turn) {
			cur.NativeTurnID = sr.nativeID
			if revoked {
				cur.RevocationRequested = true
				cur.RevocationReason = reason
			}
		})
		_ = adapter.InterruptTurn(rt.forceKillCtx, handle, sr.nativeID)
		_, _, killErr := s.waitStopOrKill(rt, adapter, handle, sr.doneCh)
		if killErr != nil {
			s.recordTurnStopErr(rt, killErr)
			_ = s.enterReconciliationRetained(rt, w, t, "process_stop_unconfirmed", fmt.Sprintf("Unable to confirm worker process stopped after cancellation: %v", killErr))
			return turnStartResult{}, false
		}
		if revoked {
			_, _ = s.finalizeRevoked(rt, w, t, reason, sr.nativeID)
		} else {
			_, _ = s.finalizeTurn(rt, w, t, TurnCancelled, nil, "cancelled", "Cancelled by caller or node shutdown")
		}
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
		now := s.now().UTC()
		_ = s.updateTurnState(t.ID, func(cur *Turn) {
			cur.RevocationRequested = true
			cur.RevocationReason = reason
			cur.UpdatedAt = now
		})
	}
	var killErr error
	grace := time.NewTimer(minDuration(s.budgets.InterruptGrace, 2*time.Second))
	select {
	case sr := <-startCh:
		grace.Stop()
		cancelStart()
		if sr.err == nil && sr.nativeID != "" {
			t.NativeTurnID = sr.nativeID
			_ = s.updateTurnState(t.ID, func(cur *Turn) {
				cur.NativeTurnID = sr.nativeID
			})
			_ = adapter.InterruptTurn(rt.forceKillCtx, handle, sr.nativeID)
			_, _, killErr = s.waitStopOrKill(rt, adapter, handle, sr.doneCh)
		} else {
			killErr = adapter.ForceKill(handle)
			s.evictResidentByHandle(handle)
		}
	case <-rt.forceKillCh:
		grace.Stop()
		cancelStart()
		killErr = adapter.ForceKill(handle)
		s.evictResidentByHandle(handle)
		s.watchLateStartResult(t.ID, startCh)
	case <-grace.C:
		cancelStart()
		killErr = adapter.ForceKill(handle)
		s.evictResidentByHandle(handle)
		s.watchLateStartResult(t.ID, startCh)
	}
	if killErr != nil {
		s.recordTurnStopErr(rt, killErr)
		_ = s.enterReconciliationRetained(rt, w, t, "process_stop_unconfirmed", fmt.Sprintf("Unable to confirm worker process stopped during start cancellation: %v", killErr))
		return
	}
	if revoked {
		_, _ = s.finalizeRevoked(rt, w, t, reason, t.NativeTurnID)
	} else {
		_, _ = s.finalizeTurn(rt, w, t, TurnCancelled, nil, "cancelled", "Turn cancelled before native acknowledgment")
	}
}

func (s *Service) watchLateStartResult(turnID string, startCh <-chan turnStartResult) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		late := <-startCh
		if late.err == nil && late.nativeID != "" {
			_ = s.updateTurnState(turnID, func(cur *Turn) {
				if cur.NativeTurnID == "" {
					cur.NativeTurnID = late.nativeID
					cur.UpdatedAt = s.now().UTC()
				}
			})
		}
	}()
}

func (s *Service) awaitTurnExecution(rt *runningTurn, w *Worker, t *Turn, adapter Adapter, handle ThreadHandle, sr turnStartResult) {
	now := s.now().UTC()
	s.mu.Lock()
	rt.nativeTurnID = sr.nativeID
	rt.execStarted = now
	rt.lastEventAt = now
	s.mu.Unlock()
	t.NativeTurnID = sr.nativeID
	s.updateTurnState(t.ID, func(cur *Turn) {
		cur.NativeTurnID = sr.nativeID
		if cur.State != TurnWaitingApproval && cur.State != TurnWaitingInput && !IsTurnTerminal(cur.State) && cur.State != TurnReconcile {
			cur.State = TurnRunning
		}
		cur.UpdatedAt = now
	})
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
			stopErr := outcome.StopErr
			if stopErr == nil {
				s.mu.Lock()
				stopErr = rt.forceKillErr
				s.mu.Unlock()
			}
			if outcome.LocalClose && stopErr == nil {
				stopErr = adapter.ForceKill(handle)
			}
			if outcome.LocalClose || stopErr != nil {
				s.evictResidentByHandle(handle)
			}
			if stopErr != nil {
				s.recordTurnStopErr(rt, stopErr)
				_ = s.enterReconciliationRetained(rt, w, t, "process_stop_unconfirmed", fmt.Sprintf("Unable to confirm worker process stopped: %v", stopErr))
				return
			}
			if revoked, reason := s.checkRevoked(rt); revoked {
				_, _ = s.finalizeRevoked(rt, w, t, reason, outcome.NativeTurnID)
				return
			}
			_, _ = s.finalizeTurn(rt, w, t, outcome.State, outcome.Items, outcome.ErrorCode, outcome.ErrorMessage)
			return

		case <-rt.cancelCh:
			revoked, reason := s.checkRevoked(rt)
			if revoked {
				t.RevocationRequested = true
				t.RevocationReason = reason
				_ = s.updateTurnState(t.ID, func(cur *Turn) {
					cur.RevocationRequested = true
					cur.RevocationReason = reason
				})
			}
			_ = adapter.InterruptTurn(rt.forceKillCtx, handle, sr.nativeID)
			outcome, ok, killErr := s.waitStopOrKill(rt, adapter, handle, sr.doneCh)
			if killErr != nil {
				s.recordTurnStopErr(rt, killErr)
				_ = s.enterReconciliationRetained(rt, w, t, "process_stop_unconfirmed", fmt.Sprintf("Unable to confirm worker process stopped after cancellation: %v", killErr))
				return
			}
			if revoked {
				_, _ = s.finalizeRevoked(rt, w, t, reason, sr.nativeID)
			} else if ok && outcome.State != TurnCompleted {
				_, _ = s.finalizeTurn(rt, w, t, TurnCancelled, outcome.Items, "cancelled", "Cancelled by caller or node shutdown")
			} else {
				_, _ = s.finalizeTurn(rt, w, t, TurnCancelled, nil, "cancelled", "Cancelled by caller or node shutdown")
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
				_ = adapter.InterruptTurn(rt.forceKillCtx, handle, sr.nativeID)
				outcome, _, killErr := s.waitStopOrKill(rt, adapter, handle, sr.doneCh)
				if killErr != nil {
					s.recordTurnStopErr(rt, killErr)
					_ = s.enterReconciliationRetained(rt, w, t, "process_stop_unconfirmed", fmt.Sprintf("Wall-clock timeout reached and worker termination could not be confirmed: %v", killErr))
					return
				}
				_, _ = s.finalizeTurn(rt, w, t, TurnInterrupted, outcome.Items, "wall_clock_timeout", "Turn exceeded wall-clock execution budget")
				return
			}
			if idleFor >= s.budgets.StallTimeout {
				_ = adapter.InterruptTurn(rt.forceKillCtx, handle, sr.nativeID)
				outcome, _, killErr := s.waitStopOrKill(rt, adapter, handle, sr.doneCh)
				if killErr != nil {
					s.recordTurnStopErr(rt, killErr)
					_ = s.enterReconciliationRetained(rt, w, t, "process_stop_unconfirmed", fmt.Sprintf("Stall timeout reached and worker termination could not be confirmed: %v", killErr))
					return
				}
				_, _ = s.finalizeTurn(rt, w, t, TurnInterrupted, outcome.Items, "stall_timeout", "Worker stopped emitting events before completion")
				return
			}
		}
	}
}

func (s *Service) waitStopOrKill(rt *runningTurn, adapter Adapter, handle ThreadHandle, doneCh <-chan TurnOutcome) (TurnOutcome, bool, error) {
	var forceCh <-chan struct{}
	if rt != nil {
		forceCh = rt.forceKillCh
	}
	if doneCh != nil {
		timer := time.NewTimer(s.budgets.InterruptGrace)
		defer timer.Stop()
		select {
		case out := <-doneCh:
			var fkErr error
			if rt != nil {
				s.mu.Lock()
				fkErr = rt.forceKillErr
				s.mu.Unlock()
			}
			if fkErr != nil {
				s.evictResidentByHandle(handle)
				return out, false, fkErr
			}
			if out.StopErr != nil {
				s.evictResidentByHandle(handle)
				return out, false, out.StopErr
			}
			if out.LocalClose {
				killErr := adapter.ForceKill(handle)
				s.evictResidentByHandle(handle)
				if killErr != nil {
					return out, false, killErr
				}
			}
			return out, true, nil
		case <-forceCh:
		case <-timer.C:
		}
	}
	killErr := adapter.ForceKill(handle)
	s.evictResidentByHandle(handle)
	if killErr == nil && rt != nil {
		s.mu.Lock()
		killErr = rt.forceKillErr
		s.mu.Unlock()
	}
	return TurnOutcome{}, false, killErr
}

func (s *Service) recordTurnStopErr(rt *runningTurn, err error) {
	if rt == nil || err == nil {
		return
	}
	s.mu.Lock()
	if rt.stopErr == nil {
		rt.stopErr = err
	}
	s.mu.Unlock()
}

func (s *Service) finalizeRevoked(rt *runningTurn, w *Worker, t *Turn, reason, nativeTurnID string) (bool, error) {
	now := s.now().UTC()
	afterVCS := CaptureVCSSnapshot(context.Background(), w.WorkspaceRoot)
	var latestSeq int64
	if rt != nil {
		s.mu.Lock()
		latestSeq = rt.commentarySeq
		s.mu.Unlock()
	}
	s.archiveTurnCommentaries(rt, "")
	var checkTurn func(Turn) error
	if rt == nil {
		checkTurn = requireReconcileState
	}
	curTurn, workerUpdated, evicted, err := s.commitTurnAndWorker(t.ID, w.ID, residentActionEvict, checkTurn, func(cur *Turn) {
		cur.RevocationRequested = true
		cur.RevocationReason = reason
		if nativeTurnID != "" && cur.NativeTurnID == "" {
			cur.NativeTurnID = nativeTurnID
		} else if t.NativeTurnID != "" && cur.NativeTurnID == "" {
			cur.NativeTurnID = t.NativeTurnID
		}
		if latestSeq > cur.LatestCommentarySeq {
			cur.LatestCommentarySeq = latestSeq
		}
		cur.State = TurnCancelledRevoked
		cur.AnswerAvailable = false
		cur.AnswerSource = AnswerSourceNone
		cur.ErrorCode = "authorization_revoked"
		cur.ErrorMessage = reason
		cur.PendingInteraction = nil
		cur.WriteLockRetained = false
		cur.WriteLockRoot = ""
		cur.WriteLockKey = ""
		cur.WriteLockReason = ""
		cur.AfterVCS = afterVCS
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
	if err != nil {
		if errors.Is(err, errTurnStateStale) {
			*t = curTurn
			return false, nil
		}
		s.recordPersistFailure(rt, w, t, "state_persist_failed", fmt.Sprintf("Failed to persist revoked state: %v", err), true, "", "", nativeTurnID, true, reason, err)
		return false, err
	}
	*t = curTurn
	if workerUpdated {
		w.State = WorkerIdle
		w.ActiveTurnID = ""
		w.UpdatedAt = now
	}
	s.closeDetachedResident(evicted)
	s.notifyWaiters(t.ID)
	return true, nil
}

func (s *Service) finalizeTurn(rt *runningTurn, w *Worker, t *Turn, state string, items []NativeItem, errCode, errMsg string) (bool, error) {
	now := s.now().UTC()
	if state == "" {
		state = TurnFailed
	}
	if rt == nil {
		cur, err := s.store.GetTurn(t.ID)
		if err != nil {
			return false, err
		}
		if cur.State != TurnReconcile {
			*t = cur
			return false, nil
		}
	}
	answer, source, avail, partial := SelectFinalAnswer(state, items)
	var resultBytes int
	if avail && answer != "" {
		n, _, err := s.store.SaveResultText(t.ID, answer, now)
		if err != nil {
			s.recordPersistFailure(rt, w, t, "state_persist_failed", fmt.Sprintf("Failed to persist turn result: %v", err), true, "", "", t.NativeTurnID, false, "", err)
			return false, err
		}
		resultBytes = n
	}
	afterVCS := CaptureVCSSnapshot(context.Background(), w.WorkspaceRoot)
	var latestSeq int64
	if rt != nil {
		s.mu.Lock()
		latestSeq = rt.commentarySeq
		s.mu.Unlock()
	}
	archivedAnswer := ""
	if state == TurnCompleted && avail {
		archivedAnswer = answer
	}
	s.archiveTurnCommentaries(rt, archivedAnswer)

	var checkTurn func(Turn) error
	if rt == nil {
		checkTurn = requireReconcileState
	}
	resAction := residentActionEvict
	if state == TurnCompleted {
		resAction = residentActionTouch
	}
	curTurn, workerUpdated, evicted, err := s.commitTurnAndWorker(t.ID, w.ID, resAction, checkTurn, func(cur *Turn) {
		cur.State = state
		cur.PendingInteraction = nil
		cur.AnswerSource = source
		cur.AnswerAvailable = avail
		cur.PartialOutput = truncateSummary(partial, 4096)
		cur.ErrorCode = errCode
		cur.ErrorMessage = errMsg
		if t.NativeTurnID != "" && cur.NativeTurnID == "" {
			cur.NativeTurnID = t.NativeTurnID
		}
		if latestSeq > cur.LatestCommentarySeq {
			cur.LatestCommentarySeq = latestSeq
		}
		if avail && answer != "" {
			if state == TurnCompleted {
				cur.Progress = truncateSummary(answer, 160)
			}
			cur.ResultBytes = resultBytes
		}
		cur.WriteLockRetained = false
		cur.WriteLockRoot = ""
		cur.WriteLockKey = ""
		cur.WriteLockReason = ""
		cur.AfterVCS = afterVCS
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
	if err != nil {
		if errors.Is(err, errTurnStateStale) {
			*t = curTurn
			return false, nil
		}
		s.recordPersistFailure(rt, w, t, "state_persist_failed", fmt.Sprintf("Failed to persist terminal state (%s): %v", state, err), true, "", "", t.NativeTurnID, false, "", err)
		return false, err
	}
	*t = curTurn
	if workerUpdated {
		w.State = WorkerIdle
		w.ActiveTurnID = ""
		w.UpdatedAt = now
	}
	s.closeDetachedResident(evicted)
	s.notifyWaiters(t.ID)
	return true, nil
}

func (s *Service) enterReconciliation(rt *runningTurn, w *Worker, t *Turn, errCode, errMsg string) error {
	return s.enterReconciliationInternal(rt, w, t, errCode, errMsg, false)
}

func (s *Service) enterReconciliationRetained(rt *runningTurn, w *Worker, t *Turn, errCode, errMsg string) error {
	return s.enterReconciliationInternal(rt, w, t, errCode, errMsg, true)
}

func (s *Service) enterReconciliationInternal(rt *runningTurn, w *Worker, t *Turn, errCode, errMsg string, retainLock bool) error {
	now := s.now().UTC()
	lockRoot := ""
	lockKey := ""
	if retainLock && w.AccessMode == harnesses.AccessWorkspaceWrite {
		lockRoot = w.WorkspaceRoot
		lockKey = w.WorktreeKey
		if rt != nil {
			if rt.workspaceRoot != "" {
				lockRoot = rt.workspaceRoot
			}
			if rt.worktreeKey != "" {
				lockKey = rt.worktreeKey
			}
			s.retainWriteLock(rt.turnID)
		}
	}
	curTurn, workerUpdated, _, err := s.commitTurnAndWorker(t.ID, w.ID, residentActionNone, nil, func(cur *Turn) {
		cur.State = TurnReconcile
		cur.PendingInteraction = nil
		cur.AnswerAvailable = false
		cur.AnswerSource = AnswerSourceNone
		cur.ErrorCode = errCode
		cur.ErrorMessage = errMsg
		if t.NativeTurnID != "" && cur.NativeTurnID == "" {
			cur.NativeTurnID = t.NativeTurnID
		}
		if t.RevocationRequested {
			cur.RevocationRequested = true
			cur.RevocationReason = t.RevocationReason
		}
		if retainLock && cur.AccessMode == harnesses.AccessWorkspaceWrite {
			cur.WriteLockRetained = true
			if cur.WriteLockRoot == "" {
				cur.WriteLockRoot = lockRoot
			}
			if cur.WriteLockKey == "" {
				cur.WriteLockKey = lockKey
			}
			cur.WriteLockReason = errCode
		} else {
			cur.WriteLockRetained = false
			cur.WriteLockRoot = ""
			cur.WriteLockKey = ""
			cur.WriteLockReason = ""
		}
		cur.UpdatedAt = now
	}, func(cur *Worker) bool {
		if cur.ActiveTurnID != t.ID && cur.ActiveTurnID != "" {
			return false
		}
		cur.State = WorkerReconcile
		cur.ActiveTurnID = t.ID
		cur.UpdatedAt = now
		return true
	})
	if err != nil {
		s.recordPersistFailure(rt, w, t, errCode, errMsg, retainLock, lockRoot, lockKey, t.NativeTurnID, t.RevocationRequested, t.RevocationReason, err)
		return err
	}
	*t = curTurn
	if workerUpdated {
		w.State = WorkerReconcile
		w.ActiveTurnID = t.ID
		w.UpdatedAt = now
	}
	s.notifyWaiters(t.ID)
	return nil
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
