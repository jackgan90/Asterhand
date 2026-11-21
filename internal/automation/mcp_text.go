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
func formatMCPText(opName string, underscore bool, response Response) string {
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
	payload := map[string]any{"operation": mcpToolName(opName, underscore)}
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
	if hint := nextStepHint(opName, underscore, response); hint != "" {
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
	if opName == "workers.wait" || opName == "workers.result" || opName == "workers.get" {
		payload["next_step"] = "Text projection exceeded single-message budget; read full body with " + mcpToolName("workers.result", underscore) + " using cursor pagination."
	}
	raw, err = json.Marshal(payload)
	if err != nil {
		return "Completed"
	}
	return clampUTF8(string(raw), maxMCPTextBytes)
}

func needsRichMCPText(opName string) bool {
	if opName == "operations.new_id" || opName == "operations.get" || opName == "capabilities" {
		return true
	}
	return strings.HasPrefix(opName, "workers.") || strings.HasPrefix(opName, "harnesses.")
}

func nextStepHint(opName string, underscore bool, response Response) string {
	dataMap, ok := toStringMap(response.Data)
	if !ok {
		return ""
	}
	n := func(op string) string { return mcpToolName(op, underscore) }
	switch opName {
	case "operations.new_id":
		if id, _ := dataMap["operation_id"].(string); id != "" {
			return fmt.Sprintf("Pass operation_id=%q to the next write tool; reuse unchanged if disconnected.", id)
		}
	case "workers.send":
		if tid, _ := dataMap["turn_id"].(string); tid != "" {
			return fmt.Sprintf("Turn accepted. Call %s(turn_id=%q) in this conversation until terminal.", n("workers.wait"), tid)
		}
	case "workers.wait", "workers.get":
		state, _ := dataMap["state"].(string)
		tid, _ := dataMap["turn_id"].(string)
		switch state {
		case "running", "queued":
			return fmt.Sprintf("Turn is still %s. Continue calling %s(turn_id=%q).", state, n("workers.wait"), tid)
		case "completed":
			if trunc, _ := dataMap["truncated"].(bool); trunc {
				return fmt.Sprintf("Turn completed with truncated preview. Call %s(turn_id=%q) with cursor to read the rest.", n("workers.result"), tid)
			}
			if avail, ok := dataMap["answer_available"].(bool); ok && !avail {
				return fmt.Sprintf("Turn completed without a candidate final answer; inspect %s(turn_id=%q).", n("workers.logs"), tid)
			}
		case "blocked_unsupported_approval", "blocked_unsupported_user_input":
			return "Worker requested an unsupported interactive approval or user question and stopped cleanly; report the reason to the user."
		case "interrupted", "cancelled", "cancelled_authorization_revoked", "failed":
			return fmt.Sprintf("Turn ended with non-success state %q (partial output is not a completed review); inspect %s(turn_id=%q).", state, n("workers.logs"), tid)
		case "reconciliation_required":
			return fmt.Sprintf("Turn requires reconciliation before continuing; do not resend automatically. Inspect %s or %s(turn_id=%q).", n("workers.get"), n("workers.logs"), tid)
		}
	case "workers.result":
		if next, _ := dataMap["next_cursor"].(string); next != "" {
			tid, _ := dataMap["turn_id"].(string)
			return fmt.Sprintf("More result text available; call %s(turn_id=%q, cursor=%q).", n("workers.result"), tid, next)
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
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, false
	}
	return m, true
}

func clampUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	if maxBytes <= 0 {
		return ""
	}
	// Back off to a valid UTF-8 rune boundary.
	n := maxBytes
	for n > 0 && (s[n]&0xc0) == 0x80 {
		n--
	}
	return s[:n]
}
