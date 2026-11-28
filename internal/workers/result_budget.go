package workers

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"unicode/utf8"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/harnesses"
)

// Keep serialized workers_result data comfortably inside both
// automation.MaxResponse (64 KiB) and the MCP text projection cap (48 KiB)
// after outer response / next_step wrappers.
const resultPageMaxPayloadJSONBytes = 46 << 10

type ResultPage struct {
	TurnID          string `json:"turn_id"`
	WorkerID        string `json:"worker_id"`
	State           string `json:"state"`
	AnswerAvailable bool   `json:"answer_available"`
	AnswerSource    string `json:"answer_source"`
	Partial         bool   `json:"partial,omitempty" description:"True when text in this page is retained partial output from an incomplete or non-success turn rather than a completed final answer"`
	PartialOutput   string `json:"partial_output,omitempty"`
	Text            string `json:"text,omitempty"`
	Offset          int    `json:"offset"`
	TotalBytes      int    `json:"total_bytes"`
	Complete        bool   `json:"complete"`
	Expired         bool   `json:"expired"`
	ResultTruncated bool   `json:"result_truncated,omitempty" description:"True when the stored final or partial text was truncated at the 2 MiB storage cap before persistence"`
	NextCursor      string `json:"next_cursor,omitempty"`
}

func canReadStoredResult(t Turn) (readable bool, partial bool) {
	if t.State == TurnCompleted && t.AnswerAvailable {
		return true, false
	}
	if IsTurnTerminal(t.State) && !t.AnswerAvailable && t.ResultBytes > 0 {
		return true, true
	}
	return false, false
}

func (s *Service) retainedPartialExpired(t Turn) (bool, error) {
	readable, isPartial := canReadStoredResult(t)
	if !readable || !isPartial {
		return false, nil
	}
	exists, err := s.store.HasResultText(t.ID)
	if err != nil {
		return false, err
	}
	return !exists, nil
}

// clampUTF8String returns the largest byte prefix length <= max that does not
// split a multi-byte UTF-8 rune, operating directly on s with zero allocation.
func clampUTF8String(s string, max int) int {
	if len(s) <= max {
		return len(s)
	}
	n := max
	for n > 0 && (s[n]&0xc0) == 0x80 {
		n--
	}
	return n
}

func resultPageJSONSize(p ResultPage) int {
	b, err := json.Marshal(p)
	if err != nil {
		return 0
	}
	return len(b)
}

func applyResultPageSlice(base ResultPage, rem string, take, offset, totalLen int) ResultPage {
	out := base
	out.Text = rem[:take]
	if offset+take >= totalLen {
		out.Complete = true
		out.NextCursor = ""
	} else {
		out.Complete = false
		out.NextCursor = strconv.Itoa(offset + take)
	}
	return out
}

// fitResultPageBudget bounds the serialized JSON size of page to maxBytes while
// keeping page.Text aligned to UTF-8 rune boundaries and advancing the cursor
// by the exact delivered byte count.
func fitResultPageBudget(page *ResultPage, rem string, take, offset, totalLen, maxBytes int) {
	cand := applyResultPageSlice(*page, rem, take, offset, totalLen)
	if resultPageJSONSize(cand) <= maxBytes {
		*page = cand
		return
	}

	ends := utf8CursorBoundaries(rem[:take], rem)
	if len(ends) > 0 && ends[len(ends)-1] == take {
		ends = ends[:len(ends)-1]
	}
	if len(ends) > 0 {
		firstTooLarge := sort.Search(len(ends), func(i int) bool {
			probe := applyResultPageSlice(*page, rem, ends[i], offset, totalLen)
			return resultPageJSONSize(probe) > maxBytes
		})
		if firstTooLarge > 0 {
			*page = applyResultPageSlice(*page, rem, ends[firstTooLarge-1], offset, totalLen)
			return
		}
	}

	// Always deliver at least one UTF-8 rune when rem is non-empty so callers
	// make strict forward progress even under degenerate base metadata.
	_, firstLen := utf8.DecodeRuneInString(rem)
	if firstLen <= 0 || firstLen > len(rem) {
		firstLen = len(rem)
	}
	*page = applyResultPageSlice(*page, rem, firstLen, offset, totalLen)
}

func fillResultPageFromRange(page *ResultPage, chunk string, offset, maxBytes, total int) error {
	page.TotalBytes = total
	if offset >= total || len(chunk) == 0 {
		page.Complete = true
		return nil
	}
	if !utf8.RuneStart(chunk[0]) {
		return automation.Fail("invalid_cursor", "Cursor offset does not align with a UTF-8 character boundary")
	}
	take := clampUTF8String(chunk, maxBytes)
	if take == 0 && len(chunk) > 0 {
		_, take = utf8.DecodeRuneInString(chunk)
	}
	fitResultPageBudget(page, chunk, take, offset, total, resultPageMaxPayloadJSONBytes)
	return nil
}

// ListPage returns a keyset-paginated slice of workers ordered by
// (updated_at DESC, worker_id DESC) alongside a query-scoped list revision.
// Passing a stale revision returns conflict so callers never observe a silent
// incomplete list when a worker's updated_at moves ahead of the cursor.
func (s *Service) ListPage(workspaceRoot, workspaceID string, includeClosed bool, expectedRevision, after string, limit int) ([]Worker, bool, string, error) {
	wsID := workspaceID
	if wsID == "" && workspaceRoot != "" {
		ws, _, err := s.harnesses.ResolveWorkspace(workspaceRoot, harnesses.AccessReadOnly)
		if err != nil {
			return nil, false, "", err
		}
		wsID = ws.ID
	}
	items, hasMore, rev, err := s.store.ListWorkersPage(wsID, includeClosed, expectedRevision, after, limit)
	if errors.Is(err, ErrInvalidCursor) {
		return nil, false, "", automation.Fail("invalid_cursor", "Invalid workers_list pagination cursor")
	}
	if errors.Is(err, ErrListCursorStale) {
		return nil, false, "", automation.Fail("conflict", "Worker list changed while paginating (a worker was created, updated, or closed); restart workers_list without cursor to read a consistent list")
	}
	return items, hasMore, rev, err
}

// ReadLogsBefore returns up to limit log entries with seq < beforeSeq (or the
// newest tail window when beforeSeq <= 0), ordered oldest-to-newest.
func (s *Service) ReadLogsBefore(turnID, expectedWorkspaceID string, beforeSeq int64, limit int) (Turn, []LogEntry, bool, error) {
	t, err := s.GetTurn(turnID, expectedWorkspaceID)
	if err != nil {
		return Turn{}, nil, false, err
	}
	entries, hasMore, err := s.store.ReadLogsTailBefore(t.ID, beforeSeq, limit)
	return t, entries, hasMore, err
}

// minimalWaitAnyItem constructs a strictly bounded WaitAnyItem from an empty
// struct using an explicit allowlist so extreme metadata or future fields can
// never push a workers_wait_any batch past budget.
func minimalWaitAnyItem(it WaitAnyItem) WaitAnyItem {
	var pi *InteractionView
	if it.PendingInteraction != nil {
		pi = &InteractionView{
			RequestID:        it.PendingInteraction.RequestID,
			Kind:             it.PendingInteraction.Kind,
			TurnID:           it.TurnID,
			WorkerID:         it.WorkerID,
			VisibleToHuman:   it.PendingInteraction.VisibleToHuman,
			ClaimedByCaller:  it.PendingInteraction.ClaimedByCaller,
			DetailsTruncated: true,
			InspectHint:      it.PendingInteraction.InspectHint,
		}
	}
	return WaitAnyItem{
		TurnID:                 it.TurnID,
		WorkerID:               it.WorkerID,
		WorkspaceID:            it.WorkspaceID,
		HarnessID:              it.HarnessID,
		State:                  it.State,
		AnswerAvailable:        it.AnswerAvailable,
		AnswerSource:           it.AnswerSource,
		Truncated:              it.Truncated,
		Expired:                it.Expired,
		ResultTruncated:        it.ResultTruncated,
		ResultBytes:            it.ResultBytes,
		NextCursor:             it.NextCursor,
		ElapsedSeconds:         it.ElapsedSeconds,
		IdleSeconds:            it.IdleSeconds,
		StreamEvents:           it.StreamEvents,
		StreamBytes:            it.StreamBytes,
		ErrorCode:              it.ErrorCode,
		LatestCommentarySeq:    it.LatestCommentarySeq,
		DeliveredCommentarySeq: it.DeliveredCommentarySeq,
		CommentariesTruncated:  it.CommentariesTruncated,
		PendingInteraction:     pi,
	}
}
