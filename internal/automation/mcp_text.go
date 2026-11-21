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
	if hint := nextStepHint(opName, response); hint != "" {
		payload["next_step"] = hint
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "Completed"
	}
	if len(raw) <= maxMCPTextBytes {
		return string(raw)
	}
	// Preserve essential metadata and truncate only the verbose data payload.
	delete(payload, "data")
	payload["text_truncated"] = true
	if opName == "workers_wait" || opName == "workers_result" || opName == "workers_get" {
		payload["next_step"] = "Text projection exceeded single-message budget; read full body with workers_result using cursor pagination."
	}
	raw, err = json.Marshal(payload)
	if err != nil {
		return "Completed"
	}
	return clampUTF8(string(raw), maxMCPTextBytes)
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
	switch opName {
	case "operations_new_id":
		if id, _ := dataMap["operation_id"].(string); id != "" {
			return fmt.Sprintf("Pass operation_id=%q to the next write tool; reuse unchanged if disconnected.", id)
		}
	case "workers_send":
		if tid, _ := dataMap["turn_id"].(string); tid != "" {
			return fmt.Sprintf("Turn accepted. Call workers_wait(turn_id=%q) in this conversation until terminal.", tid)
		}
	case "workers_wait", "workers_get":
		state, _ := dataMap["state"].(string)
		tid, _ := dataMap["turn_id"].(string)
		switch state {
		case "running", "queued":
			return fmt.Sprintf("Turn is still %s. Continue calling workers_wait(turn_id=%q).", state, tid)
		case "completed":
			if trunc, _ := dataMap["truncated"].(bool); trunc {
				return fmt.Sprintf("Turn completed with truncated preview. Call workers_result(turn_id=%q) with cursor to read the rest.", tid)
			}
			if avail, ok := dataMap["answer_available"].(bool); ok && !avail {
				return fmt.Sprintf("Turn completed without a candidate final answer; inspect workers_logs(turn_id=%q).", tid)
			}
		case "blocked_unsupported_approval", "blocked_unsupported_user_input":
			return "Worker requested an unsupported interactive approval or user question and stopped cleanly; report the reason to the user."
		case "interrupted", "cancelled", "cancelled_authorization_revoked", "failed":
			return fmt.Sprintf("Turn ended with non-success state %q (partial output is not a completed review); inspect workers_logs(turn_id=%q).", state, tid)
		case "reconciliation_required":
			return fmt.Sprintf("Turn requires reconciliation before continuing; do not resend automatically. Inspect workers_get or workers_logs(turn_id=%q).", tid)
		}
	case "workers_result":
		if next, _ := dataMap["next_cursor"].(string); next != "" {
			tid, _ := dataMap["turn_id"].(string)
			return fmt.Sprintf("More result text available. Call workers_result(turn_id=%q, cursor=%q).", tid, next)
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
