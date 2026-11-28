package app

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"unicode/utf8"

	"github.com/jackgan90/Asterhand/internal/workers"
)

// Keep serialized workers_wait data comfortably inside both
// automation.MaxResponse (64 KiB) and the MCP text projection cap (48 KiB)
// after outer response / next_step wrappers.
const workerWaitMaxPayloadJSONBytes = 46 << 10

func buildWorkerWaitView(
	t workers.Turn,
	preview string,
	origTrunc bool,
	expired bool,
	commentaries []workers.CommentaryEntry,
	verbose bool,
	hasAfterSeq bool,
	afterSeq int64,
) workerWaitView {
	var fullTurn *workers.Turn
	if verbose {
		cp := cloneWaitTurn(t)
		fullTurn = &cp
	}
	var afterVCS *workers.VCSSnapshot
	if workers.IsTurnTerminal(t.State) {
		afterVCS = cloneWaitVCS(t.AfterVCS)
	}
	base := workerWaitView{
		Turn:                fullTurn,
		WorkerID:            t.WorkerID,
		TurnID:              t.ID,
		State:               t.State,
		Progress:            t.Progress,
		AnswerAvailable:     t.AnswerAvailable,
		AnswerSource:        t.AnswerSource,
		PartialOutput:       t.PartialOutput,
		Expired:             expired,
		ResultTruncated:     t.ResultTruncated,
		ResultBytes:         t.ResultBytes,
		ErrorCode:           t.ErrorCode,
		ErrorMessage:        t.ErrorMessage,
		AfterVCS:            afterVCS,
		ElapsedSeconds:      t.ElapsedSeconds,
		IdleSeconds:         t.IdleSeconds,
		StreamEvents:        t.StreamEvents,
		StreamBytes:         t.StreamBytes,
		LatestCommentarySeq: t.LatestCommentarySeq,
		PendingInteraction:  cloneWaitInteraction(t.PendingInteraction),
	}
	return fitWorkerWaitViewBudget(t, base, preview, origTrunc, commentaries, hasAfterSeq, afterSeq, workerWaitMaxPayloadJSONBytes)
}

func fitWorkerWaitViewBudget(
	t workers.Turn,
	base workerWaitView,
	preview string,
	origTrunc bool,
	commentaries []workers.CommentaryEntry,
	hasAfterSeq bool,
	afterSeq int64,
	maxBytes int,
) workerWaitView {
	// Fast path: full commentaries + full preview already fit within budget.
	full := base
	if len(commentaries) > 0 {
		full.Commentaries = commentaries
		full.CommentariesTruncated = false
		full.DeliveredCommentarySeq = waitInt64Ptr(commentaries[len(commentaries)-1].Seq)
	} else if hasAfterSeq && t.LatestCommentarySeq > 0 && afterSeq >= t.LatestCommentarySeq {
		full.DeliveredCommentarySeq = waitInt64Ptr(t.LatestCommentarySeq)
	}
	full = applyWaitViewAnswer(full, t, preview, origTrunc)
	if waitViewJSONSize(full) <= maxBytes {
		return full
	}

	// Budgeted path: start with empty answer preview (preserving exact cursor "0"
	// whenever answer text exists) and allocate budget to complete commentaries
	// in ascending seq order first, then assign remaining budget to answer preview.
	cur := base
	hasAnswer := len(preview) > 0 || origTrunc
	if len(commentaries) > 0 {
		cur.CommentariesTruncated = true
		cur.DeliveredCommentarySeq = waitInt64Ptr(commentaries[0].Seq - 1)
	} else if hasAfterSeq && t.LatestCommentarySeq > 0 && afterSeq >= t.LatestCommentarySeq {
		cur.DeliveredCommentarySeq = waitInt64Ptr(t.LatestCommentarySeq)
	}
	cur = applyWaitViewAnswer(cur, t, "", hasAnswer)
	if waitViewJSONSize(cur) > maxBytes {
		compactWaitViewBase(&cur, t, func(v workerWaitView) bool {
			return waitViewJSONSize(v) <= maxBytes
		}, true)
	}
	cur = fitWaitViewCommentaries(t, cur, commentaries, maxBytes)
	cur = fitWaitViewAnswer(t, cur, preview, origTrunc, maxBytes)
	return cur
}

func fitWaitViewCommentaries(t workers.Turn, base workerWaitView, commentaries []workers.CommentaryEntry, maxBytes int) workerWaitView {
	if len(commentaries) == 0 {
		return base
	}
	all := applyWaitViewCommentaries(base, t, commentaries, false, waitInt64Ptr(commentaries[len(commentaries)-1].Seq))
	if waitViewJSONSize(all) <= maxBytes {
		return all
	}
	firstFits := func(v workerWaitView) bool {
		cand := applyWaitViewCommentaries(v, t, commentaries[:1], len(commentaries) > 1, waitInt64Ptr(commentaries[0].Seq))
		return waitViewJSONSize(cand) <= maxBytes
	}
	if !firstFits(base) {
		// Compact secondary base metadata—and if still necessary, heavy
		// PendingInteraction fields—so every page delivers at least one
		// complete commentary and strictly advances delivered_commentary_seq.
		compactWaitViewBase(&base, t, firstFits, true)
		all = applyWaitViewCommentaries(base, t, commentaries, false, waitInt64Ptr(commentaries[len(commentaries)-1].Seq))
		if waitViewJSONSize(all) <= maxBytes {
			return all
		}
	}
	first := applyWaitViewCommentaries(base, t, commentaries[:1], len(commentaries) > 1, waitInt64Ptr(commentaries[0].Seq))
	if waitViewJSONSize(first) <= maxBytes {
		if len(commentaries) == 1 {
			return first
		}
		// Binary-search largest prefix count k in [1, len(commentaries)-1] that fits.
		maxPrefix := len(commentaries) - 1
		firstTooLarge := sort.Search(maxPrefix, func(i int) bool {
			k := i + 1
			cand := applyWaitViewCommentaries(base, t, commentaries[:k], true, waitInt64Ptr(commentaries[k-1].Seq))
			return waitViewJSONSize(cand) > maxBytes
		})
		if firstTooLarge <= 0 {
			return first
		}
		k := firstTooLarge
		return applyWaitViewCommentaries(base, t, commentaries[:k], true, waitInt64Ptr(commentaries[k-1].Seq))
	}

	// Fallback when even the first commentary cannot fit in full alongside base
	// metadata: deliver the largest UTF-8 prefix marked truncated=true while
	// keeping delivered_commentary_seq before it so callers never skip its tail.
	entry := commentaries[0]
	ends := waitUTF8StrictPrefixBoundaries(entry.Text)
	if len(ends) == 0 {
		return base
	}
	resumeBefore := waitInt64Ptr(entry.Seq - 1)
	firstTooLarge := sort.Search(len(ends), func(i int) bool {
		e := entry
		e.Text = entry.Text[:ends[i]]
		e.Truncated = true
		cand := applyWaitViewCommentaries(base, t, []workers.CommentaryEntry{e}, true, resumeBefore)
		return waitViewJSONSize(cand) > maxBytes
	})
	if firstTooLarge == 0 {
		return base
	}
	e := entry
	e.Text = entry.Text[:ends[firstTooLarge-1]]
	e.Truncated = true
	return applyWaitViewCommentaries(base, t, []workers.CommentaryEntry{e}, true, resumeBefore)
}

func fitWaitViewAnswer(t workers.Turn, base workerWaitView, preview string, origTrunc bool, maxBytes int) workerWaitView {
	if preview == "" {
		return base
	}
	full := applyWaitViewAnswer(base, t, preview, origTrunc)
	if waitViewJSONSize(full) <= maxBytes {
		return full
	}
	ends := waitUTF8StrictPrefixBoundaries(preview)
	if len(ends) == 0 {
		return base
	}
	firstTooLarge := sort.Search(len(ends), func(i int) bool {
		cand := applyWaitViewAnswer(base, t, preview[:ends[i]], true)
		return waitViewJSONSize(cand) > maxBytes
	})
	if firstTooLarge == 0 {
		return base
	}
	return applyWaitViewAnswer(base, t, preview[:ends[firstTooLarge-1]], true)
}

func compactWaitViewBase(view *workerWaitView, t workers.Turn, fits func(workerWaitView) bool, allowCompactInteraction bool) {
	check := func() bool {
		view.NextStep = buildWaitViewNextStep(t, *view)
		return fits(*view)
	}
	if check() {
		return
	}
	if view.AfterVCS != nil {
		view.AfterVCS.ChangedFiles = nil
	}
	if view.Turn != nil {
		if view.Turn.AfterVCS != nil {
			view.Turn.AfterVCS.ChangedFiles = nil
		}
		if view.Turn.BaselineVCS != nil {
			view.Turn.BaselineVCS.ChangedFiles = nil
		}
	}
	if check() {
		return
	}
	view.AfterVCS = nil
	if view.Turn != nil {
		view.Turn.AfterVCS = nil
		view.Turn.BaselineVCS = nil
	}
	if check() {
		return
	}
	view.PartialOutput = truncateWaitText(view.PartialOutput, 512)
	view.ErrorMessage = truncateWaitText(view.ErrorMessage, 256)
	view.Progress = truncateWaitText(view.Progress, 120)
	if view.Turn != nil {
		view.Turn.PartialOutput = truncateWaitText(view.Turn.PartialOutput, 512)
		view.Turn.ErrorMessage = truncateWaitText(view.Turn.ErrorMessage, 256)
		view.Turn.Progress = truncateWaitText(view.Turn.Progress, 120)
		view.Turn.PendingInteraction = nil
	}
	if check() {
		return
	}
	if allowCompactInteraction && view.PendingInteraction != nil {
		compactWaitInteraction(view.PendingInteraction, view.WorkerID, view.TurnID, false)
		if view.PendingInteraction.DetailsTruncated && t.LatestCommentarySeq > 0 {
			view.PendingInteraction.InspectHint = fmt.Sprintf("Full interaction details omitted to fit response budget alongside commentaries; inspect full details in Asterhand desktop/Web UI, via workers_wait(turn_id=%q, after_commentary_seq=%d, wait_seconds=1), or via workers_get(worker_id=%q).", view.TurnID, t.LatestCommentarySeq, view.WorkerID)
		}
		if check() {
			return
		}
		compactWaitInteraction(view.PendingInteraction, view.WorkerID, view.TurnID, true)
		if view.PendingInteraction.DetailsTruncated && t.LatestCommentarySeq > 0 {
			view.PendingInteraction.InspectHint = fmt.Sprintf("Full interaction details omitted to fit response budget alongside commentaries; inspect full details in Asterhand desktop/Web UI, via workers_wait(turn_id=%q, after_commentary_seq=%d, wait_seconds=1), or via workers_get(worker_id=%q).", view.TurnID, t.LatestCommentarySeq, view.WorkerID)
		}
		if check() {
			return
		}
	}
	if view.Turn != nil {
		view.Turn = nil
		if check() {
			return
		}
	}
	if !check() {
		*view = minimalWorkerWaitView(t, *view)
		check()
	}
}

func minimalWorkerWaitView(t workers.Turn, cur workerWaitView) workerWaitView {
	out := workerWaitView{
		WorkerID:               cur.WorkerID,
		TurnID:                 cur.TurnID,
		State:                  cur.State,
		AnswerAvailable:        cur.AnswerAvailable,
		AnswerSource:           cur.AnswerSource,
		Truncated:              cur.Truncated,
		Expired:                cur.Expired,
		ResultTruncated:        cur.ResultTruncated,
		ResultBytes:            cur.ResultBytes,
		NextCursor:             cur.NextCursor,
		ErrorCode:              cur.ErrorCode,
		ElapsedSeconds:         cur.ElapsedSeconds,
		IdleSeconds:            cur.IdleSeconds,
		StreamEvents:           cur.StreamEvents,
		StreamBytes:            cur.StreamBytes,
		LatestCommentarySeq:    cur.LatestCommentarySeq,
		DeliveredCommentarySeq: cur.DeliveredCommentarySeq,
		CommentariesTruncated:  cur.CommentariesTruncated,
		PendingInteraction:     minimalInteractionSummary(cur.PendingInteraction, cur.TurnID),
	}
	out.NextStep = buildWaitViewNextStep(t, out)
	return out
}

func applyWaitViewCommentaries(base workerWaitView, t workers.Turn, entries []workers.CommentaryEntry, truncated bool, deliveredSeq *int64) workerWaitView {
	out := base
	out.Commentaries = entries
	out.CommentariesTruncated = truncated
	out.DeliveredCommentarySeq = deliveredSeq
	out.NextStep = buildWaitViewNextStep(t, out)
	return out
}

func applyWaitViewAnswer(base workerWaitView, t workers.Turn, preview string, truncated bool) workerWaitView {
	out := base
	out.Answer = preview
	out.Truncated = truncated
	if truncated {
		out.NextCursor = strconv.Itoa(len(preview))
	} else {
		out.NextCursor = ""
	}
	out.NextStep = buildWaitViewNextStep(t, out)
	return out
}

func buildWaitViewNextStep(t workers.Turn, view workerWaitView) string {
	hasMoreComments := view.CommentariesTruncated && view.DeliveredCommentarySeq != nil
	switch t.State {
	case workers.TurnRunning, workers.TurnPrepared:
		if hasMoreComments {
			return fmt.Sprintf("Turn is still %s with paginated commentary updates (delivered_commentary_seq=%d, latest_commentary_seq=%d). Call workers_wait(turn_id=%q, after_commentary_seq=%d, wake_on_commentary=true) to read the remaining commentaries immediately.", t.State, *view.DeliveredCommentarySeq, t.LatestCommentarySeq, t.ID, *view.DeliveredCommentarySeq)
		}
		return workerWaitNextStep(t, view.Truncated, view.NextCursor, len(view.Commentaries))
	case workers.TurnCompleted:
		if view.Expired {
			if hasMoreComments {
				return fmt.Sprintf("Turn completed (stored %d-byte answer has expired under retention or capacity limits) with paginated commentaries (delivered_commentary_seq=%d, latest_commentary_seq=%d). Call workers_wait(turn_id=%q, after_commentary_seq=%d) to read the remaining commentaries.", t.ResultBytes, *view.DeliveredCommentarySeq, t.LatestCommentarySeq, t.ID, *view.DeliveredCommentarySeq)
			}
			return fmt.Sprintf("Turn completed and previously produced %d bytes of answer text, but the stored body has expired under retention or capacity limits.", t.ResultBytes)
		}
		if hasMoreComments && view.Truncated {
			return fmt.Sprintf("Turn completed with paginated commentaries (delivered_commentary_seq=%d, latest_commentary_seq=%d) and truncated preview. Call workers_wait(turn_id=%q, after_commentary_seq=%d) for remaining commentaries and workers_result(turn_id=%q, cursor=%q) for the rest of the answer.", *view.DeliveredCommentarySeq, t.LatestCommentarySeq, t.ID, *view.DeliveredCommentarySeq, t.ID, view.NextCursor)
		}
		if hasMoreComments {
			return fmt.Sprintf("Turn completed with paginated commentaries (delivered_commentary_seq=%d, latest_commentary_seq=%d). Call workers_wait(turn_id=%q, after_commentary_seq=%d) to read the remaining commentaries.", *view.DeliveredCommentarySeq, t.LatestCommentarySeq, t.ID, *view.DeliveredCommentarySeq)
		}
		step := workerWaitNextStep(t, view.Truncated, view.NextCursor, len(view.Commentaries))
		if view.ResultTruncated {
			note := "Note: the original final answer exceeded the 2 MiB storage cap and was truncated when saved."
			if step == "" {
				return note
			}
			return step + " " + note
		}
		return step
	default:
		step := workerWaitNextStepWithExpiry(t, view.Truncated, view.NextCursor, len(view.Commentaries), view.Expired)
		if hasMoreComments {
			extra := fmt.Sprintf("More commentaries remain (delivered_commentary_seq=%d, latest_commentary_seq=%d); call workers_wait(turn_id=%q, after_commentary_seq=%d, wake_on_commentary=true) to continue reading immediately.", *view.DeliveredCommentarySeq, t.LatestCommentarySeq, t.ID, *view.DeliveredCommentarySeq)
			if step == "" {
				return extra
			}
			return step + " " + extra
		}
		return step
	}
}

func waitViewJSONSize(v workerWaitView) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

func waitUTF8StrictPrefixBoundaries(s string) []int {
	ends := make([]int, 0, len(s))
	for pos := 0; pos < len(s); {
		_, size := utf8.DecodeRuneInString(s[pos:])
		if size <= 0 {
			break
		}
		pos += size
		if pos < len(s) && utf8.RuneStart(s[pos]) {
			ends = append(ends, pos)
		}
	}
	return ends
}

func waitInt64Ptr(v int64) *int64 {
	return &v
}

func truncateWaitText(s string, maxBytes int) string {
	if len(s) <= maxBytes || maxBytes <= 0 {
		return s
	}
	n := maxBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}

func cloneWaitTurn(t workers.Turn) workers.Turn {
	cp := t
	cp.BaselineVCS = cloneWaitVCS(t.BaselineVCS)
	cp.AfterVCS = cloneWaitVCS(t.AfterVCS)
	cp.PendingInteraction = cloneWaitInteraction(t.PendingInteraction)
	return cp
}

func cloneWaitVCS(v *workers.VCSSnapshot) *workers.VCSSnapshot {
	if v == nil {
		return nil
	}
	cp := *v
	if len(v.ChangedFiles) > 0 {
		cp.ChangedFiles = append([]string(nil), v.ChangedFiles...)
	}
	return &cp
}

func cloneWaitInteraction(v *workers.InteractionView) *workers.InteractionView {
	if v == nil {
		return nil
	}
	cp := *v
	if len(v.Paths) > 0 {
		cp.Paths = append([]string(nil), v.Paths...)
	}
	if len(v.Questions) > 0 {
		qs := make([]workers.InteractionQuestion, len(v.Questions))
		for i, q := range v.Questions {
			if len(q.Options) > 0 {
				q.Options = append([]workers.InteractionOption(nil), q.Options...)
			}
			qs[i] = q
		}
		cp.Questions = qs
	}
	if v.EscalateAt != nil {
		ts := *v.EscalateAt
		cp.EscalateAt = &ts
	}
	return &cp
}

func compactWaitInteraction(pi *workers.InteractionView, workerID, turnID string, dropQuestions bool) {
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
	if s := truncateWaitText(pi.Summary, 200); s != pi.Summary {
		pi.Summary = s
		changed = true
	}
	if r := truncateWaitText(pi.Reason, 200); r != pi.Reason {
		pi.Reason = r
		changed = true
	}
	if n := truncateWaitText(pi.EscalationNote, 200); n != pi.EscalationNote {
		pi.EscalationNote = n
		changed = true
	}
	if dropQuestions && len(pi.Questions) > 0 {
		pi.Questions = nil
		changed = true
	}
	if changed {
		pi.DetailsTruncated = true
		pi.InspectHint = fmt.Sprintf("Full interaction details omitted to fit response budget; inspect full details in Asterhand desktop/Web UI or via workers_get(worker_id=%q) for turn %q.", workerID, turnID)
	}
}
