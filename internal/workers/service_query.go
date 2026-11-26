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

func (s *Service) List(workspaceRoot, workspaceID string, includeClosed bool, limit int) ([]Worker, error) {
	wsID := workspaceID
	if wsID == "" && strings.TrimSpace(workspaceRoot) != "" {
		ws, _, err := s.harnesses.ResolveWorkspace(workspaceRoot, harnesses.AccessReadOnly)
		if err != nil {
			return nil, err
		}
		wsID = ws.ID
	}
	return s.store.ListWorkers(wsID, includeClosed, limit)
}

func (s *Service) Get(workerID, expectedWorkspaceID string) (Worker, []Turn, error) {
	w, err := s.store.GetWorker(workerID)
	if errors.Is(err, ErrNotFound) {
		return Worker{}, nil, automation.Fail("not_found", "Worker not found")
	}
	if err != nil {
		return Worker{}, nil, err
	}
	if expectedWorkspaceID != "" && w.WorkspaceID != expectedWorkspaceID {
		return Worker{}, nil, automation.Fail("workspace_mismatch", "Worker does not belong to the expected workspace")
	}
	turns, err := s.store.ListTurnsByWorker(w.ID, 10)
	return w, turns, err
}

func (s *Service) GetTurn(turnID, expectedWorkspaceID string) (Turn, error) {
	t, err := s.store.GetTurn(turnID)
	if errors.Is(err, ErrNotFound) {
		return Turn{}, automation.Fail("not_found", "Turn not found")
	}
	if err != nil {
		return Turn{}, err
	}
	if expectedWorkspaceID != "" && t.WorkspaceID != expectedWorkspaceID {
		return Turn{}, automation.Fail("workspace_mismatch", "Turn does not belong to the expected workspace")
	}
	s.populateTurnActivity(&t)
	if t.State == TurnWaitingApproval || t.State == TurnWaitingInput {
		t.PendingInteraction = s.pendingForTurn(t.ID)
	}
	return t, nil
}

func (s *Service) populateTurnActivity(t *Turn) {
	now := s.now().UTC()
	if t.FinishedAt != nil {
		if sec := int(t.FinishedAt.Sub(t.StartedAt).Seconds()); sec >= 0 {
			t.ElapsedSeconds = sec
		}
		return
	}
	if !t.StartedAt.IsZero() {
		if sec := int(now.Sub(t.StartedAt).Seconds()); sec >= 0 {
			t.ElapsedSeconds = sec
		}
	}
	s.mu.Lock()
	if rt, ok := s.activeTurns[t.ID]; ok {
		if !rt.lastEventAt.IsZero() {
			if idle := int(now.Sub(rt.lastEventAt).Seconds()); idle >= 0 {
				t.IdleSeconds = idle
			}
		}
		t.StreamEvents = rt.streamEvents
		t.StreamBytes = rt.streamBytes
		t.LatestCommentarySeq = rt.commentarySeq
	} else if hist := s.turnCommentaries[t.ID]; len(hist) > 0 {
		t.LatestCommentarySeq = hist[len(hist)-1].Seq
	}
	s.mu.Unlock()
}

func (s *Service) recordCommentary(rt *runningTurn, text string, now time.Time) int64 {
	txt := truncateSummary(strings.TrimSpace(text), maxCommentaryBytes)
	if txt == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rt.commentarySeq++
	entry := CommentaryEntry{
		Seq:       rt.commentarySeq,
		Timestamp: now,
		Text:      txt,
	}
	rt.commentaries = append(rt.commentaries, entry)
	if len(rt.commentaries) > maxBufferedComments {
		rt.commentaries = append([]CommentaryEntry(nil), rt.commentaries[len(rt.commentaries)-maxBufferedComments:]...)
	}
	return rt.commentarySeq
}

func (s *Service) archiveTurnCommentaries(rt *runningTurn, finalAnswer string) {
	if rt == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(rt.commentaries) == 0 {
		return
	}
	cp := append([]CommentaryEntry(nil), rt.commentaries...)
	if normFinal := truncateSummary(strings.TrimSpace(finalAnswer), maxCommentaryBytes); normFinal != "" && len(cp) > 0 && cp[len(cp)-1].Text == normFinal {
		cp = cp[:len(cp)-1]
	}
	if len(cp) == 0 {
		return
	}
	if _, exists := s.turnCommentaries[rt.turnID]; !exists {
		s.turnCommentaryOrder = append(s.turnCommentaryOrder, rt.turnID)
		if len(s.turnCommentaryOrder) > 64 {
			evict := s.turnCommentaryOrder[0]
			s.turnCommentaryOrder = s.turnCommentaryOrder[1:]
			delete(s.turnCommentaries, evict)
		}
	}
	s.turnCommentaries[rt.turnID] = cp
}

func (s *Service) selectCommentaries(turnID string, afterSeq int64, defaultTail int) ([]CommentaryEntry, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var src []CommentaryEntry
	if rt, ok := s.activeTurns[turnID]; ok {
		src = rt.commentaries
	} else {
		src = s.turnCommentaries[turnID]
	}
	if len(src) == 0 {
		return nil, 0
	}
	latest := src[len(src)-1].Seq
	if afterSeq > 0 {
		var out []CommentaryEntry
		for _, e := range src {
			if e.Seq > afterSeq {
				out = append(out, e)
			}
		}
		return out, latest
	}
	if defaultTail <= 0 || len(src) <= defaultTail {
		return append([]CommentaryEntry(nil), src...), latest
	}
	return append([]CommentaryEntry(nil), src[len(src)-defaultTail:]...), latest
}

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
