package workers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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

func (s *Service) selectCommentaries(turnID string, afterSeq int64, hasAfterSeq bool, defaultTail int) ([]CommentaryEntry, int64) {
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
	if hasAfterSeq || afterSeq > 0 {
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
	if opts.WakeOnCommentary && t.LatestCommentarySeq > opts.AfterCommentarySeq {
		return true
	}
	return false
}

func (s *Service) Wait(ctx context.Context, turnID, expectedWorkspaceID string, waitDur time.Duration) (Turn, string, bool, error) {
	res, err := s.WaitSnapshot(ctx, turnID, expectedWorkspaceID, WaitOptions{WaitDuration: waitDur})
	return res.Turn, res.Preview, res.Truncated, err
}

func (s *Service) WaitWithOptions(ctx context.Context, turnID, expectedWorkspaceID string, opts WaitOptions) (Turn, string, bool, []CommentaryEntry, error) {
	res, err := s.WaitSnapshot(ctx, turnID, expectedWorkspaceID, opts)
	return res.Turn, res.Preview, res.Truncated, res.Commentaries, err
}

func (s *Service) WaitSnapshot(ctx context.Context, turnID, expectedWorkspaceID string, opts WaitOptions) (WaitResult, error) {
	waitDur := opts.WaitDuration
	if waitDur <= 0 || waitDur > automation.MaxWait {
		waitDur = automation.MaxWait
	}
	finish := func(t Turn) (WaitResult, error) {
		if t.State == TurnWaitingInput && t.PendingInteraction != nil && !t.PendingInteraction.VisibleToHuman {
			s.markInputObservedByCaller(t.ID, t.PendingInteraction.RequestID)
			if refreshed, gErr := s.GetTurn(t.ID, expectedWorkspaceID); gErr == nil {
				t = refreshed
			}
		}
		preview, trunc, expired, err := s.readPreview(t)
		if err != nil {
			return WaitResult{}, err
		}
		comments, latestSeq := s.selectCommentaries(t.ID, opts.AfterCommentarySeq, opts.HasAfterCommentarySeq, 2)
		if latestSeq > t.LatestCommentarySeq {
			t.LatestCommentarySeq = latestSeq
		}
		return WaitResult{
			Turn:         t,
			Preview:      preview,
			Truncated:    trunc,
			Expired:      expired,
			Commentaries: comments,
		}, nil
	}

	t, err := s.GetTurn(turnID, expectedWorkspaceID)
	if err != nil {
		return WaitResult{}, err
	}
	if shouldWakeCallerWithOpts(t, opts) {
		return finish(t)
	}
	ch := s.subscribeWaiter(turnID)
	defer s.unsubscribeWaiter(turnID, ch)

	t, err = s.GetTurn(turnID, expectedWorkspaceID)
	if err != nil {
		return WaitResult{}, err
	}
	if shouldWakeCallerWithOpts(t, opts) {
		return finish(t)
	}

	timer := time.NewTimer(waitDur)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			latest, gErr := s.GetTurn(turnID, expectedWorkspaceID)
			if gErr != nil {
				return WaitResult{}, gErr
			}
			return finish(latest)
		case <-timer.C:
			latest, gErr := s.GetTurn(turnID, expectedWorkspaceID)
			if gErr != nil {
				return WaitResult{}, gErr
			}
			return finish(latest)
		case <-ch:
			t, err = s.GetTurn(turnID, expectedWorkspaceID)
			if err != nil {
				return WaitResult{}, err
			}
			if shouldWakeCallerWithOpts(t, opts) {
				return finish(t)
			}
		}
	}
}

func (s *Service) readPreviewCapped(t Turn, capBytes int) (string, bool, bool, error) {
	if !t.AnswerAvailable || t.State != TurnCompleted {
		expired, err := s.retainedPartialExpired(t)
		return "", false, expired, err
	}
	chunk, total, ok, err := s.store.ReadResultRange(t.ID, 0, capBytes+utf8.UTFMax)
	if err != nil {
		return "", false, false, err
	}
	if !ok {
		return "", false, t.ResultBytes > 0, nil
	}
	if total <= capBytes {
		return chunk, false, false, nil
	}
	return chunk[:clampUTF8String(chunk, capBytes)], true, false, nil
}

func (s *Service) readPreview(t Turn) (string, bool, bool, error) {
	return s.readPreviewCapped(t, 24<<10)
}

const (
	waitAnyPerItemPreviewBytes = 4 << 10
	// Keep serialized workers_wait_any data comfortably inside both
	// automation.MaxResponse (64 KiB) and the MCP text projection cap (48 KiB)
	// after outer response / next_step wrappers.
	waitAnyMaxPayloadJSONBytes = 46 << 10
)

type waitAnyEnvelope struct {
	TriggeredTurnID string        `json:"triggered_turn_id,omitempty"`
	Items           []WaitAnyItem `json:"items"`
}

type waitAnyCandidate struct {
	previewChunk string
	totalBytes   int
	comments     []CommentaryEntry
}

type waitAnySlot struct {
	idx       int
	isPreview bool
	baseSize  int
	fullCap   string
	fullItem  WaitAnyItem
	fullNeed  int
}

func (s *Service) readCompletedResultPreview(t Turn, capBytes int) (string, int, bool, error) {
	if !t.AnswerAvailable || t.State != TurnCompleted {
		expired, err := s.retainedPartialExpired(t)
		return "", 0, expired, err
	}
	chunk, total, ok, err := s.store.ReadResultRange(t.ID, 0, capBytes+utf8.UTFMax)
	if err != nil {
		return "", 0, false, err
	}
	if !ok {
		return "", 0, t.ResultBytes > 0, nil
	}
	return chunk, total, false, nil
}

func cloneVCSSnapshot(v *VCSSnapshot) *VCSSnapshot {
	if v == nil {
		return nil
	}
	cp := *v
	if len(v.ChangedFiles) > 0 {
		cp.ChangedFiles = append([]string(nil), v.ChangedFiles...)
	}
	return &cp
}

func cloneInteractionView(v *InteractionView) *InteractionView {
	if v == nil {
		return nil
	}
	cp := *v
	if len(v.Paths) > 0 {
		cp.Paths = append([]string(nil), v.Paths...)
	}
	if len(v.Questions) > 0 {
		qs := make([]InteractionQuestion, len(v.Questions))
		for i, q := range v.Questions {
			if len(q.Options) > 0 {
				q.Options = append([]InteractionOption(nil), q.Options...)
			}
			qs[i] = q
		}
		cp.Questions = qs
	}
	if v.EscalateAt != nil {
		t := *v.EscalateAt
		cp.EscalateAt = &t
	}
	return &cp
}

func int64Ptr(v int64) *int64 {
	return &v
}

func interactionInspectHint(workerID, turnID, kind string) string {
	if kind == InteractionInput {
		return fmt.Sprintf("Full input details omitted to fit batch budget; call workers_wait(turn_id=%q, wait_seconds=1) or workers_get(worker_id=%q) to inspect all questions before replying.", turnID, workerID)
	}
	return fmt.Sprintf("Full approval details omitted to fit batch budget; call workers_get(worker_id=%q) or workers_wait(turn_id=%q, wait_seconds=1) to inspect full command/paths.", workerID, turnID)
}

func compactInteractionDetails(pi *InteractionView, workerID, turnID string, dropQuestions bool) {
	if pi == nil {
		return
	}
	changed := false
	if pi.Command != "" {
		pi.Command = ""
		changed = true
	}
	if len(pi.Paths) > 0 {
		pi.Paths = nil
		changed = true
	}
	if s := truncateSummary(pi.Summary, 200); s != pi.Summary {
		pi.Summary = s
		changed = true
	}
	if r := truncateSummary(pi.Reason, 200); r != pi.Reason {
		pi.Reason = r
		changed = true
	}
	if n := truncateSummary(pi.EscalationNote, 200); n != pi.EscalationNote {
		pi.EscalationNote = n
		changed = true
	}
	if dropQuestions && len(pi.Questions) > 0 {
		pi.Questions = nil
		changed = true
	}
	if changed {
		pi.DetailsTruncated = true
		pi.InspectHint = interactionInspectHint(workerID, turnID, pi.Kind)
	}
}

func waitAnyJSONSize(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

func applyWaitAnyPreview(base WaitAnyItem, totalBytes int, preview string) WaitAnyItem {
	out := base
	out.AnswerPreview = preview
	if len(preview) < totalBytes {
		out.Truncated = true
		out.NextCursor = strconv.Itoa(len(preview))
	} else {
		out.Truncated = false
		out.NextCursor = ""
	}
	return out
}

func utf8CursorBoundaries(prefix, fullText string) []int {
	ends := make([]int, 0, len(prefix))
	for pos := 0; pos < len(prefix); {
		_, size := utf8.DecodeRuneInString(prefix[pos:])
		if size <= 0 {
			break
		}
		pos += size
		if pos == len(fullText) || (pos < len(fullText) && utf8.RuneStart(fullText[pos])) {
			ends = append(ends, pos)
		}
	}
	return ends
}

func compactWaitAnyBaseItems(triggered string, items []WaitAnyItem, maxBytes int) int {
	size := func() int {
		return waitAnyJSONSize(waitAnyEnvelope{TriggeredTurnID: triggered, Items: items})
	}
	used := size()
	if used <= maxBytes {
		return used
	}
	apply := func(includeTriggered bool, fn func(*WaitAnyItem)) bool {
		for i := range items {
			if !includeTriggered && items[i].TurnID == triggered {
				continue
			}
			fn(&items[i])
		}
		used = size()
		return used <= maxBytes
	}
	if apply(false, func(it *WaitAnyItem) {
		if it.AfterVCS != nil {
			it.AfterVCS.ChangedFiles = nil
		}
	}) || apply(true, func(it *WaitAnyItem) {
		if it.AfterVCS != nil {
			it.AfterVCS.ChangedFiles = nil
		}
	}) || apply(false, func(it *WaitAnyItem) { it.AfterVCS = nil }) ||
		apply(true, func(it *WaitAnyItem) { it.AfterVCS = nil }) ||
		apply(true, func(it *WaitAnyItem) {
			it.ErrorMessage = truncateSummary(it.ErrorMessage, 256)
			it.Progress = truncateSummary(it.Progress, 120)
		}) {
		return used
	}
	// Compact heavy PendingInteraction payloads while preserving turn state,
	// request_id, kind, handler flags (visible_to_human/claimed_by_caller),
	// bounded summary, and single-turn inspection guidance.
	if apply(false, func(it *WaitAnyItem) {
		if it.PendingInteraction != nil && it.PendingInteraction.Kind == InteractionApproval {
			compactInteractionDetails(it.PendingInteraction, it.WorkerID, it.TurnID, true)
		}
	}) || apply(false, func(it *WaitAnyItem) {
		if it.PendingInteraction != nil && it.PendingInteraction.Kind != InteractionApproval {
			compactInteractionDetails(it.PendingInteraction, it.WorkerID, it.TurnID, false)
		}
	}) || apply(true, func(it *WaitAnyItem) {
		if it.PendingInteraction != nil {
			compactInteractionDetails(it.PendingInteraction, it.WorkerID, it.TurnID, it.PendingInteraction.Kind == InteractionApproval)
		}
	}) || apply(false, func(it *WaitAnyItem) {
		compactInteractionDetails(it.PendingInteraction, it.WorkerID, it.TurnID, true)
	}) || apply(true, func(it *WaitAnyItem) {
		compactInteractionDetails(it.PendingInteraction, it.WorkerID, it.TurnID, true)
	}) {
		return used
	}
	if apply(false, func(it *WaitAnyItem) {
		it.ErrorMessage = ""
		it.Progress = ""
	}) {
		return used
	}
	apply(true, func(it *WaitAnyItem) {
		if it.PendingInteraction != nil {
			it.PendingInteraction.WorkspaceRoot = ""
			it.PendingInteraction.WorkerAlias = ""
		}
	})
	if used > maxBytes {
		for i := range items {
			items[i] = minimalWaitAnyItem(items[i])
		}
		used = size()
	}
	return used
}

func fitWaitAnyPreviewSlot(base WaitAnyItem, previewChunk string, totalBytes int, fullCap string, baseSize, alloc int) (WaitAnyItem, int) {
	if alloc <= 0 || fullCap == "" {
		return base, 0
	}
	ends := utf8CursorBoundaries(fullCap, previewChunk)
	if len(fullCap) == totalBytes && len(ends) > 0 && ends[len(ends)-1] == totalBytes {
		ends = ends[:len(ends)-1]
	}
	if len(ends) == 0 {
		return base, 0
	}
	firstTooLarge := sort.Search(len(ends), func(i int) bool {
		cand := applyWaitAnyPreview(base, totalBytes, fullCap[:ends[i]])
		return waitAnyJSONSize(cand)-baseSize > alloc
	})
	if firstTooLarge == 0 {
		return base, 0
	}
	chosen := applyWaitAnyPreview(base, totalBytes, fullCap[:ends[firstTooLarge-1]])
	return chosen, waitAnyJSONSize(chosen) - baseSize
}

func fitWaitAnyCommentariesSlot(base WaitAnyItem, comments []CommentaryEntry, baseSize, alloc int) (WaitAnyItem, int) {
	if alloc <= 0 || len(comments) == 0 {
		return base, 0
	}
	for start := 0; start < len(comments); start++ {
		cand := base
		cand.Commentaries = comments[start:]
		if start == 0 {
			cand.CommentariesTruncated = false
			cand.DeliveredCommentarySeq = int64Ptr(comments[len(comments)-1].Seq)
		}
		if delta := waitAnyJSONSize(cand) - baseSize; delta <= alloc {
			return cand, delta
		}
	}
	latest := comments[len(comments)-1]
	ends := utf8CursorBoundaries(latest.Text, latest.Text)
	if len(ends) > 0 && ends[len(ends)-1] == len(latest.Text) {
		ends = ends[:len(ends)-1]
	}
	if len(ends) == 0 {
		return base, 0
	}
	firstTooLarge := sort.Search(len(ends), func(i int) bool {
		entry := latest
		entry.Text = latest.Text[:ends[i]]
		entry.Truncated = true
		cand := base
		cand.Commentaries = []CommentaryEntry{entry}
		return waitAnyJSONSize(cand)-baseSize > alloc
	})
	if firstTooLarge == 0 {
		return base, 0
	}
	entry := latest
	entry.Text = latest.Text[:ends[firstTooLarge-1]]
	entry.Truncated = true
	cand := base
	cand.Commentaries = []CommentaryEntry{entry}
	return cand, waitAnyJSONSize(cand) - baseSize
}

func buildWaitAnyTierSlots(items []WaitAnyItem, candidates []waitAnyCandidate, primary bool) []waitAnySlot {
	var slots []waitAnySlot
	for i := range items {
		if primary {
			if candidates[i].totalBytes > 0 {
				fullCap := candidates[i].previewChunk
				if candidates[i].totalBytes > waitAnyPerItemPreviewBytes {
					fullCap = fullCap[:clampUTF8String(fullCap, waitAnyPerItemPreviewBytes)]
				}
				baseSize := waitAnyJSONSize(items[i])
				fullItem := applyWaitAnyPreview(items[i], candidates[i].totalBytes, fullCap)
				slots = append(slots, waitAnySlot{
					idx:       i,
					isPreview: true,
					baseSize:  baseSize,
					fullCap:   fullCap,
					fullItem:  fullItem,
					fullNeed:  waitAnyJSONSize(fullItem) - baseSize,
				})
			} else if !IsTurnTerminal(items[i].State) && len(candidates[i].comments) > 0 {
				baseSize := waitAnyJSONSize(items[i])
				fullItem := items[i]
				fullItem.Commentaries = candidates[i].comments
				fullItem.CommentariesTruncated = false
				fullItem.DeliveredCommentarySeq = int64Ptr(candidates[i].comments[len(candidates[i].comments)-1].Seq)
				slots = append(slots, waitAnySlot{
					idx:      i,
					baseSize: baseSize,
					fullItem: fullItem,
					fullNeed: waitAnyJSONSize(fullItem) - baseSize,
				})
			}
		} else if IsTurnTerminal(items[i].State) && len(candidates[i].comments) > 0 {
			baseSize := waitAnyJSONSize(items[i])
			fullItem := items[i]
			fullItem.Commentaries = candidates[i].comments
			fullItem.CommentariesTruncated = false
			fullItem.DeliveredCommentarySeq = int64Ptr(candidates[i].comments[len(candidates[i].comments)-1].Seq)
			slots = append(slots, waitAnySlot{
				idx:      i,
				baseSize: baseSize,
				fullItem: fullItem,
				fullNeed: waitAnyJSONSize(fullItem) - baseSize,
			})
		}
	}
	return slots
}

func allocateWaitAnyTier(triggered string, items []WaitAnyItem, candidates []waitAnyCandidate, rem int, primary bool) int {
	pending := buildWaitAnyTierSlots(items, candidates, primary)
	for len(pending) > 0 {
		share := 0
		if rem > 0 {
			share = rem / len(pending)
		}
		progressed := false
		next := pending[:0]
		for _, sl := range pending {
			if sl.fullNeed <= share {
				items[sl.idx] = sl.fullItem
				rem -= sl.fullNeed
				progressed = true
			} else {
				next = append(next, sl)
			}
		}
		pending = next
		if !progressed {
			break
		}
	}
	if triggered != "" && len(pending) > 1 {
		for i := 1; i < len(pending); i++ {
			if items[pending[i].idx].TurnID == triggered {
				trigSlot := pending[i]
				copy(pending[1:i+1], pending[0:i])
				pending[0] = trigSlot
				break
			}
		}
	}
	for pos, sl := range pending {
		if rem <= 0 {
			break
		}
		alloc := rem / (len(pending) - pos)
		if sl.fullNeed <= alloc {
			items[sl.idx] = sl.fullItem
			rem -= sl.fullNeed
			continue
		}
		var updated WaitAnyItem
		var delta int
		if sl.isPreview {
			updated, delta = fitWaitAnyPreviewSlot(items[sl.idx], candidates[sl.idx].previewChunk, candidates[sl.idx].totalBytes, sl.fullCap, sl.baseSize, alloc)
		} else {
			updated, delta = fitWaitAnyCommentariesSlot(items[sl.idx], candidates[sl.idx].comments, sl.baseSize, alloc)
		}
		if delta <= alloc {
			items[sl.idx] = updated
			rem -= delta
		}
	}
	return rem
}

func fitWaitAnyBudget(triggered string, items []WaitAnyItem, candidates []waitAnyCandidate, maxBytes int) {
	for i := range items {
		if len(candidates[i].comments) > 0 && len(items[i].Commentaries) == 0 {
			items[i].CommentariesTruncated = true
			items[i].DeliveredCommentarySeq = int64Ptr(candidates[i].comments[0].Seq - 1)
		}
	}
	used := compactWaitAnyBaseItems(triggered, items, maxBytes)
	rem := allocateWaitAnyTier(triggered, items, candidates, maxBytes-used, true)
	if rem > 0 {
		allocateWaitAnyTier(triggered, items, candidates, rem, false)
	}
}

func (s *Service) buildWaitAnyItems(turnIDs []string, expectedWorkspaceID string) ([]WaitAnyItem, string, error) {
	items := make([]WaitAnyItem, 0, len(turnIDs))
	candidates := make([]waitAnyCandidate, 0, len(turnIDs))
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
		previewChunk, totalBytes, expired, err := s.readCompletedResultPreview(t, waitAnyPerItemPreviewBytes)
		if err != nil {
			return nil, "", err
		}
		comments, latestSeq := s.selectCommentaries(t.ID, 0, false, 2)
		if latestSeq > t.LatestCommentarySeq {
			t.LatestCommentarySeq = latestSeq
		}
		trunc := totalBytes > 0
		nextCursor := ""
		if trunc {
			nextCursor = "0"
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
			Truncated:           trunc,
			Expired:             expired,
			ResultTruncated:     t.ResultTruncated,
			ResultBytes:         t.ResultBytes,
			NextCursor:          nextCursor,
			ElapsedSeconds:      t.ElapsedSeconds,
			IdleSeconds:         t.IdleSeconds,
			StreamEvents:        t.StreamEvents,
			StreamBytes:         t.StreamBytes,
			ErrorCode:           t.ErrorCode,
			ErrorMessage:        t.ErrorMessage,
			AfterVCS:            cloneVCSSnapshot(t.AfterVCS),
			PendingInteraction:  cloneInteractionView(t.PendingInteraction),
			LatestCommentarySeq: t.LatestCommentarySeq,
		})
		candidates = append(candidates, waitAnyCandidate{
			previewChunk: previewChunk,
			totalBytes:   totalBytes,
			comments:     comments,
		})
	}
	fitWaitAnyBudget(triggered, items, candidates, waitAnyMaxPayloadJSONBytes)
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
	readable, isPartial := canReadStoredResult(t)
	page := ResultPage{
		TurnID:          t.ID,
		WorkerID:        t.WorkerID,
		State:           t.State,
		AnswerAvailable: t.AnswerAvailable,
		AnswerSource:    t.AnswerSource,
		Partial:         isPartial,
		PartialOutput:   t.PartialOutput,
		Offset:          offset,
		TotalBytes:      t.ResultBytes,
		ResultTruncated: t.ResultTruncated,
	}
	if !readable {
		page.Complete = true
		return page, nil
	}
	chunk, total, ok, err := s.store.ReadResultRange(t.ID, offset, maxBytes+utf8.UTFMax)
	if err != nil {
		return ResultPage{}, err
	}
	if !ok {
		page.Expired = t.ResultBytes > 0
		page.Complete = true
		return page, nil
	}
	if err := fillResultPageFromRange(&page, chunk, offset, maxBytes, total); err != nil {
		return ResultPage{}, err
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
