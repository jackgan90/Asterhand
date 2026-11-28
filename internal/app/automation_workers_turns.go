package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/workers"
)

func formatWorkersTurnsCallArgs(workerID, workspaceID, filter, cursor string) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("worker_id=%q", workerID))
	if strings.TrimSpace(workspaceID) != "" {
		parts = append(parts, fmt.Sprintf("workspace_id=%q", strings.TrimSpace(workspaceID)))
	}
	if f := strings.TrimSpace(filter); f != "" && f != workers.TurnFilterAll {
		parts = append(parts, fmt.Sprintf("filter=%q", f))
	}
	if cursor != "" {
		parts = append(parts, fmt.Sprintf("cursor=%q", cursor))
	}
	return strings.Join(parts, ", ")
}

// withWorkerGetSummary attaches the retained-history summary to a budgeted
// workers_get view and points callers at workers_turns when the recent list
// does not cover every retained turn.
func withWorkerGetSummary(view workerGetView, workerID, requestedWorkspaceID string, summary workers.TurnSummary) workerGetView {
	view.Summary = &summary
	view.TurnsHasMore = summary.TurnCount > len(view.Turns)
	if view.TurnsHasMore && view.NextStep == "" {
		view.NextStep = fmt.Sprintf(
			"Listing the %d most recent of %d retained turns; call workers_turns(%s) to page through the full turn history.",
			len(view.Turns), summary.TurnCount, formatWorkersTurnsCallArgs(workerID, requestedWorkspaceID, "", ""),
		)
	}
	return view
}

const maxCompactedTurnErrorCodeBytes = 1024

// buildWorkerTurnsView fits a timeline page into the shared worker response
// budget, shortening multi-item pages first (preserving full item fields),
// compacting oversized non-identity fields when a single item alone exceeds
// budget, and issuing an exact keyset cursor matching the cursor binding.
func buildWorkerTurnsView(workerID, workspaceID, filter, requestCursor string, items []workers.TurnListItem, dbHasMore bool, binding any) workerTurnsView {
	normWS := strings.TrimSpace(workspaceID)
	normFilter := strings.TrimSpace(filter)
	if normFilter == workers.TurnFilterAll {
		normFilter = ""
	}
	reqCur := strings.TrimSpace(requestCursor)
	finalize := func(delivered []workers.TurnListItem, hasMore, truncated bool) workerTurnsView {
		v := workerTurnsView{
			WorkerID:    workerID,
			WorkspaceID: normWS,
			Filter:      normFilter,
			Cursor:      reqCur,
			Items:       delivered,
			HasMore:     hasMore,
			Truncated:   truncated,
		}
		if hasMore && len(delivered) > 0 {
			last := delivered[len(delivered)-1]
			v.NextCursor = automation.EncodeCursor(binding, "", workers.TurnListItemAfter(last))
			v.NextStep = fmt.Sprintf("Older turns exist; call workers_turns(%s) to read the next page.", formatWorkersTurnsCallArgs(workerID, normWS, filter, v.NextCursor))
		}
		return v
	}
	view := finalize(items, dbHasMore, false)
	if len(items) == 0 || serializedJSONBytes(view) <= workerViewMaxPayloadJSONBytes {
		return view
	}
	firstTooLarge := sort.Search(len(items), func(i int) bool {
		k := i + 1
		return serializedJSONBytes(finalize(items[:k], dbHasMore || k < len(items), true)) > workerViewMaxPayloadJSONBytes
	})
	if firstTooLarge > 0 {
		return finalize(items[:firstTooLarge], dbHasMore || firstTooLarge < len(items), true)
	}
	more := dbHasMore || len(items) > 1
	single := fitSingleTurnListItem(finalize(nil, more, true), items[0])
	return finalize([]workers.TurnListItem{single}, more, true)
}

func fitSingleTurnListItem(base workerTurnsView, item workers.TurnListItem) workers.TurnListItem {
	out := item
	if len(out.PromptPreview) > 240 {
		out.PromptPreview = truncateWaitText(out.PromptPreview, 237)
	}
	if len(out.ErrorCode) > maxCompactedTurnErrorCodeBytes {
		out.ErrorCode = truncateWaitText(out.ErrorCode, maxCompactedTurnErrorCodeBytes)
		out.ErrorCodeTruncated = true
	}
	probe := base
	probe.Items = []workers.TurnListItem{out}
	if serializedJSONBytes(probe) <= workerViewMaxPayloadJSONBytes {
		return out
	}
	return workers.TurnListItem{
		TurnID:                 item.TurnID,
		WorkerID:               item.WorkerID,
		TurnSeq:                item.TurnSeq,
		TurnSeqDerived:         item.TurnSeqDerived,
		State:                  item.State,
		PromptBytes:            item.PromptBytes,
		StartedAt:              item.StartedAt,
		FinishedAt:             item.FinishedAt,
		ElapsedSeconds:         item.ElapsedSeconds,
		AnswerAvailable:        item.AnswerAvailable,
		ResultBytes:            item.ResultBytes,
		ErrorCodeTruncated:     item.ErrorCode != "",
		ChangedFiles:           item.ChangedFiles,
		ChangedFilesLowerBound: item.ChangedFilesLowerBound,
	}
}
