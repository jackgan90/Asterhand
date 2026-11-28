package app

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/workers"
)

// Keep every serialized workers_* public data payload comfortably inside both
// automation.MaxResponse (64 KiB) and the MCP text projection cap (48 KiB)
// after outer response, receipt summary, and next_step wrappers.
const workerViewMaxPayloadJSONBytes = 44 << 10

func serializedJSONBytes(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

func appendUniqueField(list []string, name string) []string {
	for _, existing := range list {
		if existing == name {
			return list
		}
	}
	return append(list, name)
}

// minimalWorkerSummary constructs a strictly bounded Worker identity/state
// summary from an empty struct using an explicit allowlist so unbounded or
// newly added Worker fields (such as long role_hint or warnings) can never
// push fallback responses over budget.
func minimalWorkerSummary(w workers.Worker) workers.Worker {
	omitted := append([]string(nil), w.DetailsOmitted...)
	if w.RoleHint != "" {
		omitted = appendUniqueField(omitted, "role_hint")
	}
	if len(w.Warnings) > 0 {
		omitted = appendUniqueField(omitted, "warnings")
	}
	if w.WorkspaceRoot != "" {
		omitted = appendUniqueField(omitted, "workspace_root")
	}
	if w.WorktreeKey != "" {
		omitted = appendUniqueField(omitted, "worktree_key")
	}
	if w.Model != "" || w.NativeDefaultModel != "" {
		omitted = appendUniqueField(omitted, "model")
	}
	if w.NativeSessionID != "" {
		omitted = appendUniqueField(omitted, "native_session_id")
	}
	return workers.Worker{
		ID:                    w.ID,
		WorkspaceID:           w.WorkspaceID,
		Alias:                 w.Alias,
		HarnessID:             w.HarnessID,
		AccessMode:            w.AccessMode,
		ApprovalMode:          w.ApprovalMode,
		EffectiveApprovalMode: w.EffectiveApprovalMode,
		SessionAutoApprove:    w.SessionAutoApprove,
		ProtectionLevel:       w.ProtectionLevel,
		State:                 w.State,
		SessionEpoch:          w.SessionEpoch,
		ActiveTurnID:          w.ActiveTurnID,
		OperationID:           w.OperationID,
		CreatedAt:             w.CreatedAt,
		UpdatedAt:             w.UpdatedAt,
		ClosedAt:              w.ClosedAt,
		DetailsOmitted:        omitted,
	}
}

// minimalTurnSummary constructs a strictly bounded Turn routing/status summary
// from an empty struct using an explicit allowlist so heavy or future Turn
// fields can never push fallback responses over budget.
func minimalTurnSummary(t workers.Turn) workers.Turn {
	return workers.Turn{
		ID:                  t.ID,
		WorkerID:            t.WorkerID,
		WorkspaceID:         t.WorkspaceID,
		HarnessID:           t.HarnessID,
		OperationID:         t.OperationID,
		AccessMode:          t.AccessMode,
		State:               t.State,
		AnswerSource:        t.AnswerSource,
		AnswerAvailable:     t.AnswerAvailable,
		ResultBytes:         t.ResultBytes,
		ResultTruncated:     t.ResultTruncated,
		ErrorCode:           t.ErrorCode,
		LatestCommentarySeq: t.LatestCommentarySeq,
		StartedAt:           t.StartedAt,
		UpdatedAt:           t.UpdatedAt,
		FinishedAt:          t.FinishedAt,
	}
}

func minimalInteractionSummary(pi *workers.InteractionView, turnID string) *workers.InteractionView {
	if pi == nil {
		return nil
	}
	return &workers.InteractionView{
		RequestID:        pi.RequestID,
		Kind:             pi.Kind,
		TurnID:           pi.TurnID,
		WorkerID:         pi.WorkerID,
		VisibleToHuman:   pi.VisibleToHuman,
		ClaimedByCaller:  pi.ClaimedByCaller,
		DetailsTruncated: true,
		InspectHint:      fmt.Sprintf("Full interaction details omitted to fit response budget; inspect full details in Asterhand desktop/Web UI or via workers_wait(turn_id=%q, wait_seconds=1).", turnID),
	}
}

func compactWorkerHeavyFields(w workers.Worker) workers.Worker {
	cp := w
	omitted := append([]string(nil), cp.DetailsOmitted...)
	if cp.RoleHint != "" {
		cp.RoleHint = ""
		omitted = appendUniqueField(omitted, "role_hint")
	}
	if len(cp.Warnings) > 0 {
		cp.Warnings = nil
		omitted = appendUniqueField(omitted, "warnings")
	}
	cp.DetailsOmitted = omitted
	return cp
}

func fitWorkerViewBudget(w workers.Worker) workers.Worker {
	if serializedJSONBytes(w) <= workerViewMaxPayloadJSONBytes {
		return w
	}
	cp := compactWorkerHeavyFields(w)
	if serializedJSONBytes(cp) <= workerViewMaxPayloadJSONBytes {
		return cp
	}
	return minimalWorkerSummary(w)
}

func fitTurnViewBudget(t workers.Turn, maxBytes int) workers.Turn {
	if serializedJSONBytes(t) <= maxBytes {
		return t
	}
	cp := cloneWaitTurn(t)
	cp.PartialOutput = ""
	cp.BaselineVCS = nil
	cp.AfterVCS = nil
	cp.ErrorMessage = truncateWaitText(cp.ErrorMessage, 256)
	cp.Progress = truncateWaitText(cp.Progress, 120)
	cp.RevocationReason = truncateWaitText(cp.RevocationReason, 160)
	cp.WriteLockReason = truncateWaitText(cp.WriteLockReason, 160)
	if cp.PendingInteraction != nil {
		compactWaitInteraction(cp.PendingInteraction, cp.WorkerID, cp.ID, true)
	}
	if serializedJSONBytes(cp) <= maxBytes {
		return cp
	}
	return minimalTurnSummary(t)
}

func fitSteerViewBudget(v workerSteerView) workerSteerView {
	if serializedJSONBytes(v) <= workerViewMaxPayloadJSONBytes {
		return v
	}
	out := v
	out.Turn = fitTurnViewBudget(v.Turn, workerViewMaxPayloadJSONBytes-(2<<10))
	if serializedJSONBytes(out) <= workerViewMaxPayloadJSONBytes {
		return out
	}
	out.Turn = minimalTurnSummary(v.Turn)
	return out
}

func fitReplyViewBudget(v workerReplyView) workerReplyView {
	if serializedJSONBytes(v) <= workerViewMaxPayloadJSONBytes {
		return v
	}
	out := v
	out.Turn = fitTurnViewBudget(v.Turn, workerViewMaxPayloadJSONBytes-(2<<10))
	if serializedJSONBytes(out) <= workerViewMaxPayloadJSONBytes {
		return out
	}
	out.Turn = minimalTurnSummary(v.Turn)
	return out
}

func buildWorkerGetView(w workers.Worker, turns []workers.Turn) workerGetView {
	cloned := make([]workers.Turn, len(turns))
	for i, t := range turns {
		cloned[i] = cloneWaitTurn(t)
	}
	view := workerGetView{Worker: w, Turns: cloned}
	if serializedJSONBytes(view) <= workerViewMaxPayloadJSONBytes {
		return view
	}

	// The combined worker+turns view exceeded budget; try omitting heavy Worker
	// config fields (role_hint/warnings) before touching turn history so
	// partial_output and VCS details stay intact whenever config omission fits.
	if w.RoleHint != "" || len(w.Warnings) > 0 {
		withCompactWorker := view
		withCompactWorker.Worker = compactWorkerHeavyFields(w)
		if serializedJSONBytes(withCompactWorker) <= workerViewMaxPayloadJSONBytes {
			return withCompactWorker
		}
		if serializedJSONBytes(view)-serializedJSONBytes(withCompactWorker) > 1024 {
			view = withCompactWorker
		}
	}
	if serializedJSONBytes(view.Worker) > workerViewMaxPayloadJSONBytes {
		view.Worker = minimalWorkerSummary(w)
		if serializedJSONBytes(view) <= workerViewMaxPayloadJSONBytes {
			return view
		}
	}

	if len(view.Turns) > 0 {
		view.HistoryTruncated = true
		view.NextStep = buildWorkerGetCompactedNextStep(w, cloned)
	}
	for i := range view.Turns {
		view.Turns[i].PartialOutput = ""
	}
	if serializedJSONBytes(view) <= workerViewMaxPayloadJSONBytes {
		return view
	}
	for i := range view.Turns {
		if view.Turns[i].BaselineVCS != nil {
			view.Turns[i].BaselineVCS.ChangedFiles = nil
		}
		if view.Turns[i].AfterVCS != nil {
			view.Turns[i].AfterVCS.ChangedFiles = nil
		}
	}
	if serializedJSONBytes(view) <= workerViewMaxPayloadJSONBytes {
		return view
	}
	for i := range view.Turns {
		view.Turns[i].BaselineVCS = nil
		view.Turns[i].AfterVCS = nil
		view.Turns[i].ErrorMessage = truncateWaitText(view.Turns[i].ErrorMessage, 256)
		view.Turns[i].Progress = truncateWaitText(view.Turns[i].Progress, 120)
		view.Turns[i].RevocationReason = truncateWaitText(view.Turns[i].RevocationReason, 160)
		view.Turns[i].WriteLockReason = truncateWaitText(view.Turns[i].WriteLockReason, 160)
	}
	if serializedJSONBytes(view) <= workerViewMaxPayloadJSONBytes {
		return view
	}
	for _, dropQuestions := range []bool{false, true} {
		for i := range view.Turns {
			if pi := view.Turns[i].PendingInteraction; pi != nil {
				compactWaitInteraction(pi, w.ID, view.Turns[i].ID, dropQuestions)
				if pi.DetailsTruncated {
					pi.InspectHint = fmt.Sprintf("Full interaction details omitted to fit response budget; inspect full details in Asterhand desktop/Web UI or via workers_wait(turn_id=%q, wait_seconds=1).", view.Turns[i].ID)
				}
			}
		}
		if serializedJSONBytes(view) <= workerViewMaxPayloadJSONBytes {
			return view
		}
	}

	// Guaranteed allowlisted fallback constructed from scratch so no current
	// or future unbounded field on Worker or Turn can leave workers_get over budget.
	minTurns := make([]workers.Turn, len(turns))
	for i, t := range turns {
		minTurns[i] = minimalTurnSummary(t)
	}
	fallback := workerGetView{
		Worker:           minimalWorkerSummary(w),
		Turns:            minTurns,
		HistoryTruncated: len(minTurns) > 0,
		NextStep:         buildWorkerGetCompactedNextStep(w, turns),
	}
	for len(fallback.Turns) > 1 && serializedJSONBytes(fallback) > workerViewMaxPayloadJSONBytes {
		fallback.Turns = fallback.Turns[:len(fallback.Turns)-1]
	}
	return fallback
}

func buildWorkerGetCompactedNextStep(w workers.Worker, turns []workers.Turn) string {
	focusID := w.ActiveTurnID
	if focusID == "" && len(turns) > 0 {
		focusID = turns[0].ID
	}
	if focusID == "" {
		return ""
	}
	return fmt.Sprintf("Recent turn histories were compacted to fit the response budget while keeping worker identity and turn summaries. Call workers_wait(turn_id=%q, wait_seconds=1) (or pass any listed turn_id) to inspect a turn's summary, partial_output, and whether stored output is still available or expired.", focusID)
}

func buildWorkerListView(items []workers.Worker, dbHasMore bool, binding any, revision string) workerListView {
	applyCursor := func(v workerListView, delivered []workers.Worker, hasMore bool) workerListView {
		v.Items = delivered
		v.HasMore = hasMore
		if hasMore && len(delivered) > 0 {
			v.NextCursor = automation.EncodeCursor(binding, revision, workers.EncodeWorkerListAfter(delivered[len(delivered)-1]))
		} else {
			v.NextCursor = ""
		}
		return v
	}

	view := applyCursor(workerListView{}, items, dbHasMore)
	if serializedJSONBytes(view) <= workerViewMaxPayloadJSONBytes {
		return view
	}

	// Prefer keeping every requested worker on this page by omitting heavy
	// fields (such as role_hint/warnings) before reducing the item count.
	compacted := make([]workers.Worker, len(items))
	for i, w := range items {
		compacted[i] = compactWorkerHeavyFields(w)
	}
	view = applyCursor(workerListView{Truncated: true}, compacted, dbHasMore)
	if serializedJSONBytes(view) <= workerViewMaxPayloadJSONBytes {
		if view.NextCursor != "" {
			view.NextStep = fmt.Sprintf("More workers match this query; call workers_list(cursor=%q) with the same filters to read the next page.", view.NextCursor)
		}
		return view
	}

	minimal := make([]workers.Worker, len(items))
	for i, w := range items {
		minimal[i] = minimalWorkerSummary(w)
	}
	view = applyCursor(workerListView{Truncated: true}, minimal, dbHasMore)
	if serializedJSONBytes(view) <= workerViewMaxPayloadJSONBytes {
		if view.NextCursor != "" {
			view.NextStep = fmt.Sprintf("More workers match this query; call workers_list(cursor=%q) with the same filters to read the next page.", view.NextCursor)
		}
		return view
	}

	// Split page cleanly with an exact keyset cursor so callers can read all
	// remaining workers without narrowing filters.
	firstTooLarge := sort.Search(len(minimal), func(i int) bool {
		k := i + 1
		probe := applyCursor(workerListView{Truncated: true}, minimal[:k], true)
		probe.NextStep = fmt.Sprintf("Worker list page was shortened to fit the response budget; call workers_list(cursor=%q) with the same filters to read the remaining workers.", probe.NextCursor)
		return serializedJSONBytes(probe) > workerViewMaxPayloadJSONBytes
	})
	keep := firstTooLarge
	if keep < 1 && len(minimal) > 0 {
		keep = 1
	}
	out := applyCursor(workerListView{Truncated: true}, minimal[:keep], dbHasMore || keep < len(minimal))
	if out.NextCursor != "" {
		out.NextStep = fmt.Sprintf("Worker list page was shortened to fit the response budget; call workers_list(cursor=%q) with the same filters to read the remaining workers.", out.NextCursor)
	}
	return out
}

func buildWorkerLogsView(t workers.Turn, entries []workers.LogEntry, dbHasMore bool, backward bool) workerLogsView {
	finalize := func(slice []workers.LogEntry, hasMore, truncated bool) workerLogsView {
		v := workerLogsView{
			TurnID:    t.ID,
			WorkerID:  t.WorkerID,
			State:     t.State,
			Entries:   slice,
			HasMore:   hasMore,
			Truncated: truncated,
		}
		if hasMore && len(slice) > 0 {
			if backward {
				firstSeq := slice[0].Seq
				v.EarlierBeforeSeq = firstSeq
				v.NextStep = fmt.Sprintf("Earlier logs exist before seq %d; call workers_logs(turn_id=%q, tail=true, before_seq=%d) to page backward, or workers_logs(turn_id=%q, after_seq=0) to read forward from the start.", firstSeq, t.ID, firstSeq, t.ID)
			} else {
				lastSeq := slice[len(slice)-1].Seq
				v.NextAfterSeq = lastSeq
				v.NextStep = fmt.Sprintf("More logs are available; call workers_logs(turn_id=%q, after_seq=%d) to read the next page.", t.ID, lastSeq)
			}
		}
		return v
	}

	view := finalize(entries, dbHasMore, false)
	if len(entries) == 0 || serializedJSONBytes(view) <= workerViewMaxPayloadJSONBytes {
		return view
	}

	if !backward {
		firstTooLarge := sort.Search(len(entries), func(i int) bool {
			k := i + 1
			probe := finalize(entries[:k], dbHasMore || k < len(entries), true)
			return serializedJSONBytes(probe) > workerViewMaxPayloadJSONBytes
		})
		if firstTooLarge > 0 {
			return finalize(entries[:firstTooLarge], dbHasMore || firstTooLarge < len(entries), true)
		}
		more := dbHasMore || len(entries) > 1
		single := fitSingleLogEntry(finalize([]workers.LogEntry{entries[0]}, more, true), entries[0])
		return finalize([]workers.LogEntry{single}, more, true)
	}

	firstFitStart := sort.Search(len(entries), func(start int) bool {
		probe := finalize(entries[start:], dbHasMore || start > 0, true)
		return serializedJSONBytes(probe) <= workerViewMaxPayloadJSONBytes
	})
	if firstFitStart < len(entries) {
		return finalize(entries[firstFitStart:], dbHasMore || firstFitStart > 0, true)
	}
	more := dbHasMore || len(entries) > 1
	last := entries[len(entries)-1]
	single := fitSingleLogEntry(finalize([]workers.LogEntry{last}, more, true), last)
	return finalize([]workers.LogEntry{single}, more, true)
}

func fitSingleLogEntry(base workerLogsView, entry workers.LogEntry) workers.LogEntry {
	out := entry
	out.Truncated = true
	ends := waitUTF8StrictPrefixBoundaries(entry.Summary)
	if len(ends) > 0 {
		firstTooLarge := sort.Search(len(ends), func(i int) bool {
			cand := out
			cand.Summary = entry.Summary[:ends[i]] + "..."
			probe := base
			probe.Entries = []workers.LogEntry{cand}
			return serializedJSONBytes(probe) > workerViewMaxPayloadJSONBytes
		})
		if firstTooLarge > 0 {
			out.Summary = entry.Summary[:ends[firstTooLarge-1]] + "..."
			return out
		}
	}
	out.Summary = ""
	return out
}
