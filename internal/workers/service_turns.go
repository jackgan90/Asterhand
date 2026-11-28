package workers

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackgan90/Asterhand/internal/automation"
)

// promptPreviewMaxBytes bounds the single-line prompt preview kept in the
// 8 KiB turn summary so it survives the 30-day summary retention.
const promptPreviewMaxBytes = 240

const (
	TurnFilterAll       = "all"
	TurnFilterAttention = "attention"
)

// TurnListItem is the compact per-turn view used by the timeline list.
type TurnListItem struct {
	TurnID                 string     `json:"turn_id"`
	WorkerID               string     `json:"worker_id"`
	TurnSeq                int        `json:"turn_seq"`
	TurnSeqDerived         bool       `json:"turn_seq_derived,omitempty" description:"True when turn_seq was derived from start-time order because the turn predates stored sequence numbers"`
	State                  string     `json:"state"`
	PromptPreview          string     `json:"prompt_preview,omitempty"`
	PromptBytes            int        `json:"prompt_bytes,omitempty"`
	StartedAt              time.Time  `json:"started_at"`
	FinishedAt             *time.Time `json:"finished_at,omitempty"`
	ElapsedSeconds         int        `json:"elapsed_seconds"`
	AnswerAvailable        bool       `json:"answer_available"`
	ResultBytes            int        `json:"result_bytes"`
	ErrorCode              string     `json:"error_code,omitempty"`
	ErrorCodeTruncated     bool       `json:"error_code_truncated,omitempty" description:"True when error_code was shortened to fit the response budget"`
	ChangedFiles           int        `json:"changed_files,omitempty" description:"Number of unique changed paths in the post-turn worktree snapshot"`
	ChangedFilesLowerBound bool       `json:"changed_files_lower_bound,omitempty" description:"True when changed_files is a lower bound (collapsed untracked directory or ambiguous legacy snapshot)"`
}

// TurnSummary aggregates a worker's retained turns for statistics.
type TurnSummary struct {
	TurnCount      int            `json:"turn_count"`
	StateCounts    map[string]int `json:"state_counts,omitempty"`
	AttentionCount int            `json:"attention_count"`
	FirstStartedAt *time.Time     `json:"first_started_at,omitempty"`
	LastFinishedAt *time.Time     `json:"last_finished_at,omitempty"`
	TurnsSeconds   int            `json:"turns_seconds" description:"Sum of each retained turn's start-to-finish duration, including time waiting for approval or answers; running turns count up to now"`
	WallSeconds    int            `json:"wall_seconds" description:"Seconds from worker creation to close (or now when still open)"`
	Partial        bool           `json:"partial,omitempty" description:"True when older turns were pruned by retention, so counts and durations cover only retained turns"`
}

// promptPreview collapses whitespace into a single line and clamps it to
// promptPreviewMaxBytes on a UTF-8 boundary, appending an ellipsis when cut.
func promptPreview(prompt string) string {
	line := strings.Join(strings.Fields(prompt), " ")
	if len(line) <= promptPreviewMaxBytes {
		return line
	}
	const ellipsis = "…"
	return line[:clampUTF8String(line, promptPreviewMaxBytes-len(ellipsis))] + ellipsis
}

func turnFilterAttention(filter string) (bool, error) {
	switch strings.TrimSpace(filter) {
	case "", TurnFilterAll:
		return false, nil
	case TurnFilterAttention:
		return true, nil
	}
	return false, automation.Fail("invalid_parameters", "filter must be \"all\" or \"attention\"")
}

func isAttentionState(state string) bool {
	for _, st := range attentionTurnStates {
		if st == state {
			return true
		}
	}
	return false
}

func isActiveTurnState(state string) bool {
	for _, st := range activeTurnStates {
		if st == state {
			return true
		}
	}
	return false
}

// turnElapsedSeconds matches AggregateTurns: finished turns use finish-start,
// active turns run up to now, and unfinished non-active turns (such as
// reconciliation_required) freeze at their last recorded update.
func turnElapsedSeconds(t Turn, now time.Time) int {
	if t.StartedAt.IsZero() {
		return 0
	}
	var end time.Time
	switch {
	case t.FinishedAt != nil:
		end = *t.FinishedAt
	case isActiveTurnState(t.State):
		end = now
	case !t.UpdatedAt.IsZero():
		end = t.UpdatedAt
	default:
		return 0
	}
	if ms := end.UnixMilli() - t.StartedAt.UnixMilli(); ms > 0 {
		return int(ms / 1000)
	}
	return 0
}

func changedFileCount(v *VCSSnapshot) (int, bool) {
	if v == nil || v.VCS == "none" {
		return 0, false
	}
	collapsedDir := v.ChangedCountLowerBound
	if !collapsedDir {
		for _, f := range v.ChangedFiles {
			if isCollapsedPorcelainDir(f) {
				collapsedDir = true
				break
			}
		}
	}
	if v.ChangedCount > 0 {
		return v.ChangedCount, collapsedDir
	}
	if v.StagedCount == 0 && v.UnstagedCount == 0 && v.UntrackedCount == 0 && len(v.ChangedFiles) == 0 {
		return 0, false
	}
	if v.StagedCount == 0 || v.UnstagedCount == 0 {
		n := v.StagedCount + v.UnstagedCount + v.UntrackedCount
		if len(v.ChangedFiles) > n {
			n = len(v.ChangedFiles)
		}
		// Legacy v1 snapshots only sampled the first 32 paths; when entries were
		// omitted and untracked items exist, an omitted entry may be a collapsed
		// untracked directory ("dir/").
		lower := collapsedDir || (v.UntrackedCount > 0 && len(v.ChangedFiles) < n)
		return n, lower
	}
	trackedMin := v.StagedCount
	if v.UnstagedCount > trackedMin {
		trackedMin = v.UnstagedCount
	}
	minEntries := trackedMin + v.UntrackedCount
	if len(v.ChangedFiles) >= minEntries && len(v.ChangedFiles) > 0 && len(v.ChangedFiles) < 32 {
		return len(v.ChangedFiles), collapsedDir
	}
	lower := minEntries
	if len(v.ChangedFiles) > lower {
		lower = len(v.ChangedFiles)
	}
	return lower, true
}

// NewTurnListItem projects a turn into the compact timeline view.
func NewTurnListItem(t Turn, now time.Time) TurnListItem {
	changed, lowerBound := changedFileCount(t.AfterVCS)
	preview := t.PromptPreview
	if len(preview) > promptPreviewMaxBytes {
		preview = promptPreview(preview)
	}
	return TurnListItem{
		TurnID:                 t.ID,
		WorkerID:               t.WorkerID,
		TurnSeq:                t.TurnSeq,
		TurnSeqDerived:         t.TurnSeqDerived,
		State:                  t.State,
		PromptPreview:          preview,
		PromptBytes:            t.PromptBytes,
		StartedAt:              t.StartedAt,
		FinishedAt:             t.FinishedAt,
		ElapsedSeconds:         turnElapsedSeconds(t, now),
		AnswerAvailable:        t.AnswerAvailable,
		ResultBytes:            t.ResultBytes,
		ErrorCode:              t.ErrorCode,
		ChangedFiles:           changed,
		ChangedFilesLowerBound: lowerBound,
	}
}

// GetWorker loads a worker and verifies its optional expected workspace.
func (s *Service) GetWorker(workerID, expectedWorkspaceID string) (Worker, error) {
	w, err := s.store.GetWorker(workerID)
	if errors.Is(err, ErrNotFound) {
		return Worker{}, automation.Fail("not_found", "Worker not found")
	}
	if err != nil {
		return Worker{}, err
	}
	if expectedWorkspaceID != "" && w.WorkspaceID != expectedWorkspaceID {
		return Worker{}, automation.Fail("workspace_mismatch", "Worker does not belong to the expected workspace")
	}
	return w, nil
}

// fillDerivedTurnSeqs numbers legacy turns (stored before sequence numbers
// existed) by their chronological rank within the worker's full retained
// history in a single covering-index query.
func (s *Service) fillDerivedTurnSeqs(workerID string, turns []Turn) error {
	var missing []string
	for i := range turns {
		if turns[i].TurnSeq <= 0 {
			missing = append(missing, turns[i].ID)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	ranks, err := s.store.TurnRanks(workerID, missing)
	if err != nil {
		return err
	}
	for i := range turns {
		if turns[i].TurnSeq > 0 {
			continue
		}
		turns[i].TurnSeq = ranks[turns[i].ID]
		turns[i].TurnSeqDerived = true
	}
	return nil
}

// ListTurns returns one page of a worker's turns, newest first, after the
// given keyset position. nextAfter is empty when no older turns remain.
func (s *Service) ListTurns(workerID, expectedWorkspaceID, after string, limit int, filter string) ([]TurnListItem, bool, string, error) {
	attention, err := turnFilterAttention(filter)
	if err != nil {
		return nil, false, "", err
	}
	w, err := s.GetWorker(workerID, expectedWorkspaceID)
	if err != nil {
		return nil, false, "", err
	}
	turns, hasMore, err := s.store.ListTurnsPage(w.ID, after, limit, attention)
	if errors.Is(err, ErrInvalidCursor) {
		return nil, false, "", automation.Fail("invalid_cursor", "Invalid workers_turns pagination cursor")
	}
	if err != nil {
		return nil, false, "", err
	}
	next := ""
	if hasMore && len(turns) > 0 {
		last := turns[len(turns)-1]
		next = EncodeTurnListAfter(last.StartedAt, last.TurnSeq, last.ID)
	}
	if err := s.fillDerivedTurnSeqs(w.ID, turns); err != nil {
		return nil, false, "", err
	}
	now := s.now().UTC()
	items := make([]TurnListItem, len(turns))
	for i, t := range turns {
		items[i] = NewTurnListItem(t, now)
	}
	return items, hasMore, next, nil
}

// TurnListItemAfter returns the keyset position just after item, using the
// stored sequence number (0 for legacy turns whose number was derived).
func TurnListItemAfter(item TurnListItem) string {
	seq := item.TurnSeq
	if item.TurnSeqDerived {
		seq = 0
	}
	return EncodeTurnListAfter(item.StartedAt, seq, item.TurnID)
}

// TurnSummary aggregates counts and durations over the worker's retained turns.
func (s *Service) TurnSummary(w Worker) (TurnSummary, error) {
	now := s.now().UTC()
	rows, err := s.store.AggregateTurns(w.ID, now.UnixMilli())
	if err != nil {
		return TurnSummary{}, err
	}
	pruned, err := s.store.PrunedTurnCount(w.ID)
	if err != nil {
		return TurnSummary{}, err
	}
	sum := TurnSummary{StateCounts: map[string]int{}}
	var (
		minStarted, maxFinished, totalMillis int64
		maxSeq                               = w.LastTurnSeq
	)
	for _, r := range rows {
		sum.TurnCount += r.Count
		sum.StateCounts[r.State] += r.Count
		if isAttentionState(r.State) {
			sum.AttentionCount += r.Count
		}
		if r.MinStarted > 0 && (minStarted == 0 || r.MinStarted < minStarted) {
			minStarted = r.MinStarted
		}
		if r.MaxFinished > maxFinished {
			maxFinished = r.MaxFinished
		}
		if r.MaxSeq > maxSeq {
			maxSeq = r.MaxSeq
		}
		totalMillis += r.TotalMillis
	}
	if minStarted > 0 {
		ts := time.UnixMilli(minStarted).UTC()
		sum.FirstStartedAt = &ts
	}
	if maxFinished > 0 {
		ts := time.UnixMilli(maxFinished).UTC()
		sum.LastFinishedAt = &ts
	}
	sum.TurnsSeconds = int(totalMillis / 1000)
	end := now
	if w.ClosedAt != nil {
		end = *w.ClosedAt
	}
	if !w.CreatedAt.IsZero() {
		if sec := int(end.Sub(w.CreatedAt).Seconds()); sec > 0 {
			sum.WallSeconds = sec
		}
	}
	sum.Partial = pruned > 0 || maxSeq > sum.TurnCount
	if len(sum.StateCounts) == 0 {
		sum.StateCounts = nil
	}
	return sum, nil
}

// ReadPrompt pages through the caller's original prompt for a turn using the
// same UTF-8 aligned cursor semantics and JSON budget as ReadResult.
func (s *Service) ReadPrompt(turnID, expectedWorkspaceID string, offset, maxBytes int) (ResultPage, error) {
	t, err := s.GetTurn(turnID, expectedWorkspaceID)
	if err != nil {
		return ResultPage{}, err
	}
	if maxBytes <= 0 || maxBytes > 32<<10 {
		maxBytes = 24 << 10
	}
	if offset < 0 {
		return ResultPage{}, automation.Fail("invalid_cursor", "Negative prompt offset")
	}
	page := ResultPage{
		TurnID:          t.ID,
		WorkerID:        t.WorkerID,
		Part:            "prompt",
		State:           t.State,
		AnswerAvailable: t.AnswerAvailable,
		AnswerSource:    t.AnswerSource,
		Offset:          offset,
		TotalBytes:      t.PromptBytes,
	}
	if t.PromptBytes == 0 {
		page.Complete = true
		return page, nil
	}
	chunk, total, ok, err := s.store.ReadPromptRange(t.ID, offset, maxBytes+utf8.UTFMax)
	if err != nil {
		return ResultPage{}, err
	}
	if !ok {
		page.Expired = true
		page.Complete = true
		return page, nil
	}
	if err := fillResultPageFromRange(&page, chunk, offset, maxBytes, total); err != nil {
		return ResultPage{}, err
	}
	return page, nil
}
