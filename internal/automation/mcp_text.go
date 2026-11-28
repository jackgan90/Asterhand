package automation

import (
	"encoding/json"
	"fmt"
	"strings"
)

const maxMCPTextBytes = 48 << 10

// formatMCPText builds a bounded, model-readable text projection alongside
// StructuredContent so clients that only forward content[].text to the LLM
// still receive operation IDs, receipts, worker/turn identifiers, review text,
// and next-step continuation guidance.
func formatMCPText(opName string, response Response) string {
	if response.Error != nil {
		var b strings.Builder
		b.WriteString(response.Error.Code)
		b.WriteString(": ")
		b.WriteString(response.Error.Message)
		if response.ID != "" {
			b.WriteString(" (operation_id=")
			b.WriteString(response.ID)
			b.WriteString(")")
		}
		return clampUTF8(b.String(), maxMCPTextBytes)
	}
	if !needsRichMCPText(opName) {
		if response.Receipt != nil {
			return response.Receipt.State
		}
		return "Completed"
	}
	payload := map[string]any{"operation": opName}
	if response.ID != "" {
		payload["operation_id"] = response.ID
	}
	if response.Receipt != nil {
		payload["receipt_state"] = response.Receipt.State
		if response.Receipt.Accepted {
			payload["accepted"] = true
		}
		if len(response.Receipt.Result) > 0 {
			var summary any
			if json.Unmarshal(response.Receipt.Result, &summary) == nil {
				payload["receipt_summary"] = summary
			}
		}
	}
	if response.Data != nil {
		payload["data"] = response.Data
	}
	dataMap, hasMap := toStringMap(response.Data)
	hasEmbeddedNextStep := false
	if hasMap {
		if ns, _ := dataMap["next_step"].(string); ns != "" {
			hasEmbeddedNextStep = true
		}
	}
	if !hasEmbeddedNextStep {
		if hint := nextStepHint(opName, response); hint != "" {
			payload["next_step"] = hint
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "Completed"
	}
	if len(raw) <= maxMCPTextBytes {
		return string(raw)
	}
	// Preserve essential identification/pagination metadata when trimming oversized data.
	delete(payload, "data")
	payload["text_truncated"] = true
	if hasMap {
		if compact := compactOverBudgetData(opName, dataMap); len(compact) > 0 {
			payload["data"] = compact
		}
	}
	if hint := overBudgetNextStep(opName, dataMap); hint != "" {
		payload["next_step"] = hint
	}
	raw, err = json.Marshal(payload)
	if err != nil {
		return "Completed"
	}
	return clampUTF8(string(raw), maxMCPTextBytes)
}

func compactOverBudgetData(opName string, dataMap map[string]any) map[string]any {
	if opName == "workers_get" {
		return compactWorkersGetOverBudget(dataMap)
	}
	keys := []string{"turn_id", "worker_id", "workspace_id", "alias", "state", "truncated", "expired", "result_truncated", "result_bytes", "next_cursor", "has_more", "next_after_seq", "earlier_before_seq", "triggered_turn_id", "error_code", "latest_commentary_seq", "delivered_commentary_seq", "commentaries_truncated", "details_omitted", "request_id", "delivery_mode"}
	if opName == "workers_result" {
		// Do not advertise next_cursor when the page text was omitted; preserve offset
		// so text-only clients retry the same page with a smaller max_bytes.
		keys = []string{"turn_id", "worker_id", "part", "state", "offset", "total_bytes", "complete", "expired", "partial", "result_truncated"}
	} else if opName == "workers_turns" {
		// Do not advertise next_cursor when items were omitted; preserve request
		// cursor so text-only clients retry this same page with a smaller limit.
		keys = []string{"worker_id", "workspace_id", "filter", "cursor", "has_more", "truncated"}
	}
	compact := map[string]any{}
	for _, k := range keys {
		if v, ok := dataMap[k]; ok && v != nil && v != "" {
			compact[k] = v
		}
	}
	return compact
}

func toMapSlice(v any) []map[string]any {
	switch s := v.(type) {
	case []map[string]any:
		return s
	case []any:
		out := make([]map[string]any, 0, len(s))
		for _, item := range s {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

func compactWorkersGetOverBudget(dataMap map[string]any) map[string]any {
	compact := map[string]any{}
	if summary, ok := dataMap["summary"].(map[string]any); ok && len(summary) > 0 {
		compact["summary"] = summary
	}
	if more, ok := dataMap["turns_has_more"].(bool); ok && more {
		compact["turns_has_more"] = true
	}
	if w, ok := dataMap["worker"].(map[string]any); ok {
		cw := map[string]any{}
		for _, k := range []string{"worker_id", "workspace_id", "alias", "harness_id", "state", "active_turn_id"} {
			if v, exists := w[k]; exists && v != nil && v != "" {
				cw[k] = v
			}
		}
		if len(cw) > 0 {
			compact["worker"] = cw
		}
	}
	if rawTurns := toMapSlice(dataMap["turns"]); len(rawTurns) > 0 {
		const maxCompactTurns = 50
		limit := len(rawTurns)
		if limit > maxCompactTurns {
			limit = maxCompactTurns
			compact["turns_total"] = len(rawTurns)
		}
		turns := make([]map[string]any, 0, limit)
		for _, tm := range rawTurns[:limit] {
			ct := map[string]any{}
			for _, k := range []string{"turn_id", "state", "error_code"} {
				if v, exists := tm[k]; exists && v != nil && v != "" {
					ct[k] = v
				}
			}
			if avail, ok := tm["answer_available"].(bool); ok {
				ct["answer_available"] = avail
			}
			if rb := readInt64Value(tm["result_bytes"]); rb > 0 {
				ct["result_bytes"] = rb
			}
			if rt, ok := tm["result_truncated"].(bool); ok && rt {
				ct["result_truncated"] = true
			}
			if len(ct) > 0 {
				turns = append(turns, ct)
			}
		}
		if len(turns) > 0 {
			compact["turns"] = turns
		}
	}
	return compact
}

func workersGetFocusTurnID(dataMap map[string]any) string {
	if w, ok := dataMap["worker"].(map[string]any); ok {
		if active, _ := w["active_turn_id"].(string); active != "" {
			return active
		}
	}
	for _, tm := range toMapSlice(dataMap["turns"]) {
		if tid, _ := tm["turn_id"].(string); tid != "" {
			return tid
		}
	}
	return ""
}

func overBudgetNextStep(opName string, dataMap map[string]any) string {
	switch opName {
	case "workers_result":
		tid, _ := dataMap["turn_id"].(string)
		off := readInt64Value(dataMap["offset"])
		if tid != "" {
			if part, _ := dataMap["part"].(string); part == "prompt" {
				return fmt.Sprintf("Text projection exceeded single-message budget; retry this page with a smaller slice via workers_result(turn_id=%q, part=\"prompt\", cursor=\"%d\", max_bytes=8192).", tid, off)
			}
			return fmt.Sprintf("Text projection exceeded single-message budget; retry this page with a smaller slice via workers_result(turn_id=%q, cursor=\"%d\", max_bytes=8192).", tid, off)
		}
		return "Text projection exceeded single-message budget; retry this page with a smaller max_bytes."
	case "workers_get":
		if tid := workersGetFocusTurnID(dataMap); tid != "" {
			return fmt.Sprintf("Text projection exceeded single-message budget; omitted verbose turn histories while keeping worker and recent turn summaries. Call workers_wait(turn_id=%q, wait_seconds=1) (or pass any listed turn_id) to inspect a turn's summary and whether stored output is still available or expired.", tid)
		}
		return "Text projection exceeded single-message budget; omitted verbose worker details. Call workers_wait(turn_id=..., wait_seconds=1) to inspect a specific turn's summary and whether stored output is still available or expired."
	case "workers_wait":
		if exp, _ := dataMap["expired"].(bool); exp {
			return "Text projection exceeded single-message budget; stored body has expired under retention or capacity limits."
		}
		tid, _ := dataMap["turn_id"].(string)
		cur, _ := dataMap["next_cursor"].(string)
		if tid != "" && cur != "" {
			return fmt.Sprintf("Text projection exceeded single-message budget; call workers_result(turn_id=%q, cursor=%q) to read paginated output.", tid, cur)
		}
		if tid != "" {
			return fmt.Sprintf("Text projection exceeded single-message budget; call workers_result(turn_id=%q, cursor=\"0\") to read paginated output.", tid)
		}
		return "Text projection exceeded single-message budget; read full body with workers_result using cursor pagination."
	case "workers_wait_any":
		if trig, _ := dataMap["triggered_turn_id"].(string); trig != "" {
			return fmt.Sprintf("Text projection exceeded single-message budget; inspect turn %q via workers_wait(turn_id=%q) or workers_result(turn_id=%q, cursor=\"0\").", trig, trig, trig)
		}
		return "Text projection exceeded single-message budget; inspect individual turns via workers_wait(turn_id=...) or workers_result(turn_id=..., cursor=\"0\")."
	case "workers_list":
		if cur, _ := dataMap["next_cursor"].(string); cur != "" {
			return fmt.Sprintf("Text projection exceeded single-message budget; call workers_list(cursor=%q) with a smaller limit to read remaining workers.", cur)
		}
		return "Text projection exceeded single-message budget; call workers_list with a smaller limit or workspace_id filter."
	case "workers_turns":
		wid, _ := dataMap["worker_id"].(string)
		ws, _ := dataMap["workspace_id"].(string)
		flt, _ := dataMap["filter"].(string)
		cur, _ := dataMap["cursor"].(string)
		retryLimit := 10
		if items := toMapSlice(dataMap["items"]); len(items) > 0 {
			if half := len(items) / 2; half < retryLimit {
				if half < 1 {
					retryLimit = 1
				} else {
					retryLimit = half
				}
			}
		}
		if wid != "" {
			args := fmt.Sprintf("worker_id=%q", wid)
			if ws != "" {
				args += fmt.Sprintf(", workspace_id=%q", ws)
			}
			if flt != "" {
				args += fmt.Sprintf(", filter=%q", flt)
			}
			if cur != "" {
				args += fmt.Sprintf(", cursor=%q", cur)
			}
			return fmt.Sprintf("Text projection exceeded single-message budget; retry workers_turns(%s, limit=%d) with a smaller limit.", args, retryLimit)
		}
		return "Text projection exceeded single-message budget; retry workers_turns with a smaller limit."
	case "workers_logs":
		tid, _ := dataMap["turn_id"].(string)
		if nextSeq := readInt64Value(dataMap["next_after_seq"]); tid != "" && nextSeq > 0 {
			return fmt.Sprintf("Text projection exceeded single-message budget; call workers_logs(turn_id=%q, after_seq=%d, limit=10).", tid, nextSeq)
		}
		if beforeSeq := readInt64Value(dataMap["earlier_before_seq"]); tid != "" && beforeSeq > 0 {
			return fmt.Sprintf("Text projection exceeded single-message budget; call workers_logs(turn_id=%q, tail=true, before_seq=%d, limit=10).", tid, beforeSeq)
		}
		if tid != "" {
			return fmt.Sprintf("Text projection exceeded single-message budget; retry workers_logs(turn_id=%q, limit=10) with a smaller limit.", tid)
		}
		return "Text projection exceeded single-message budget; retry workers_logs with a smaller limit."
	}
	return ""
}

func needsRichMCPText(opName string) bool {
	if opName == "operations_new_id" || opName == "operations_get" || opName == "capabilities" {
		return true
	}
	return strings.HasPrefix(opName, "workers_") || strings.HasPrefix(opName, "harnesses_")
}

func nextStepHint(opName string, response Response) string {
	dataMap, ok := toStringMap(response.Data)
	if !ok {
		return ""
	}
	if ns, _ := dataMap["next_step"].(string); ns != "" {
		return ns
	}
	switch opName {
	case "operations_new_id":
		if id, _ := dataMap["operation_id"].(string); id != "" {
			return fmt.Sprintf("Pass operation_id=%q to the next write tool; reuse unchanged if disconnected.", id)
		}
	case "workers_send":
		if tid, _ := dataMap["turn_id"].(string); tid != "" {
			return fmt.Sprintf("Turn accepted. Call workers_wait(turn_id=%q) in this conversation until terminal (or run the cli_path from capabilities with `wait %s` in a background shell for long turns).", tid, tid)
		}
	case "workers_wait", "workers_get":
		state, _ := dataMap["state"].(string)
		tid, _ := dataMap["turn_id"].(string)
		switch state {
		case "running", "queued":
			if ev, ok := dataMap["stream_events"].(float64); ok && ev > 0 {
				elapsed, _ := dataMap["elapsed_seconds"].(float64)
				idle, _ := dataMap["idle_seconds"].(float64)
				return fmt.Sprintf("Turn is still %s (elapsed=%ds, idle=%ds, stream_events=%d). Continue calling workers_wait(turn_id=%q).", state, int(elapsed), int(idle), int64(ev), tid)
			}
			return fmt.Sprintf("Turn is still %s. Continue calling workers_wait(turn_id=%q).", state, tid)
		case "waiting_approval":
			return fmt.Sprintf("Turn is waiting for human permission approval in Asterhand desktop/Web UI. Caller cannot approve on the user's behalf; do not cancel—inform the user if helpful and continue calling workers_wait(turn_id=%q).", tid)
		case "waiting_input":
			if pi, ok := dataMap["pending_interaction"].(map[string]any); ok {
				if vis, _ := pi["visible_to_human"].(bool); vis {
					return fmt.Sprintf("Turn is waiting for human input in Asterhand desktop/Web UI (already escalated). Continue calling workers_wait(turn_id=%q).", tid)
				}
			}
			return fmt.Sprintf("Worker asked a business question (see pending_interaction.questions). Answer via workers_reply(turn_id=%q, text=...) or escalate via workers_reply(turn_id=%q, escalate_to_human=true).", tid, tid)
		case "completed":
			if trunc, _ := dataMap["truncated"].(bool); trunc {
				if cur, _ := dataMap["next_cursor"].(string); cur != "" {
					return fmt.Sprintf("Turn completed with truncated preview. Call workers_result(turn_id=%q, cursor=%q) to read the rest.", tid, cur)
				}
				return fmt.Sprintf("Turn completed with truncated preview. Call workers_result(turn_id=%q) with cursor to read the rest.", tid)
			}
			if avail, ok := dataMap["answer_available"].(bool); ok && !avail {
				return fmt.Sprintf("Turn completed without a candidate final answer; inspect workers_logs(turn_id=%q).", tid)
			}
		case "blocked_unsupported_approval", "blocked_unsupported_user_input":
			return appendChangedFilesNote("Worker requested an unsupported interactive approval or user question and stopped cleanly; report the reason to the user.", dataMap)
		case "interrupted", "cancelled", "cancelled_authorization_revoked", "failed":
			return appendChangedFilesNote(fmt.Sprintf("Turn ended with non-success state %q (partial output is not a completed review); inspect workers_logs(turn_id=%q).", state, tid), dataMap)
		case "reconciliation_required":
			return fmt.Sprintf("Turn requires reconciliation before continuing; do not resend automatically. Inspect workers_get or workers_logs(turn_id=%q).", tid)
		}
	case "workers_wait_any":
		if trig, _ := dataMap["triggered_turn_id"].(string); trig != "" {
			return fmt.Sprintf("Turn %q requires attention (completed, waiting_input, or terminal/reconciliation). Inspect its state/preview, call workers_reply if waiting_input or workers_result if truncated, and continue monitoring remaining active turns with workers_wait_any.", trig)
		}
		return "All watched turns are still active (or waiting on human approval). Continue calling workers_wait_any with the active turn_ids."
	case "workers_steer":
		return "Steering message injected into the running turn. Continue calling workers_wait to observe worker progress and response."
	case "workers_reply":
		if esc, _ := dataMap["escalated"].(bool); esc {
			return "Question escalated to the human desktop/Web prompt. Continue calling workers_wait until the human responds."
		}
		if def, _ := dataMap["deferred"].(bool); def {
			return "Human escalation deferred while you verify. Call workers_reply with your answer (or escalate_to_human=true, or defer_seconds again) before escalate_at."
		}
		return "Reply delivered to worker. Continue calling workers_wait until the turn finishes."
	case "workers_result":
		part, _ := dataMap["part"].(string)
		isPartial, _ := dataMap["partial"].(bool)
		if next, _ := dataMap["next_cursor"].(string); next != "" {
			tid, _ := dataMap["turn_id"].(string)
			if part == "prompt" {
				return fmt.Sprintf("More prompt text available. Call workers_result(turn_id=%q, part=\"prompt\", cursor=%q).", tid, next)
			}
			if isPartial {
				return fmt.Sprintf("More partial output available (not a completed final answer). Call workers_result(turn_id=%q, cursor=%q).", tid, next)
			}
			return fmt.Sprintf("More result text available. Call workers_result(turn_id=%q, cursor=%q).", tid, next)
		}
		if rt, _ := dataMap["result_truncated"].(bool); rt {
			if isPartial {
				return "Reached end of retained partial output (not a completed final answer); the original partial output exceeded the 2 MiB storage cap and was truncated when saved."
			}
			return "Reached end of retained result text; the original final answer exceeded the 2 MiB storage cap and was truncated when saved."
		}
	}
	return ""
}

func toStringMap(v any) (map[string]any, bool) {
	if v == nil {
		return nil, false
	}
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil, false
	}
	return m, true
}

func clampUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	n := maxBytes
	for n > 0 && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n]
}

func appendChangedFilesNote(base string, dataMap map[string]any) string {
	afterVCS, _ := dataMap["after_vcs"].(map[string]any)
	if afterVCS == nil {
		turnObj, _ := dataMap["turn"].(map[string]any)
		afterVCS, _ = turnObj["after_vcs"].(map[string]any)
	}
	filesAny, _ := afterVCS["changed_files"].([]any)
	if len(filesAny) == 0 {
		return base
	}
	var names []string
	for i, f := range filesAny {
		if i >= 8 {
			names = append(names, fmt.Sprintf("... (+%d more)", len(filesAny)-8))
			break
		}
		if s, ok := f.(string); ok && s != "" {
			names = append(names, s)
		}
	}
	if len(names) == 0 {
		return base
	}
	return base + " Worktree currently has changed files on disk: " + strings.Join(names, ", ") + "."
}
