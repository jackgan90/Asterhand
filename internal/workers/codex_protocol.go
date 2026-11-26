package workers

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type codexRPCEnvelope struct {
	ID     *json.RawMessage `json:"id"`
	Method string           `json:"method"`
	Params json.RawMessage  `json:"params"`
	Result json.RawMessage  `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type codexCollabMode struct {
	Mode     string `json:"mode"`
	Settings struct {
		Model                 string  `json:"model"`
		ReasoningEffort       *string `json:"reasoning_effort"`
		DeveloperInstructions *string `json:"developer_instructions"`
	} `json:"settings"`
}

type codexThreadOpenResult struct {
	threadID       string
	model          string
	effort         string
	collabMode     string
	collabDevInstr *string
}

type codexTurnConfigPlan struct {
	wantModel      string
	wantEffort     string
	resetModel     string
	collabMode     string
	collabDevInstr any
	useCollabReset bool
	sendModel      bool
	sendEffort     bool
}

type codexAgentDeltaNotification struct {
	TurnID string `json:"turnId"`
	ItemID string `json:"itemId"`
	Delta  string `json:"delta"`
}

type codexTurnCompletedNotification struct {
	Turn struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"turn"`
}

func buildCodexThreadOpenRequest(spec ThreadOpenSpec, sandbox, approvalPolicy, reviewer string) (string, map[string]any, bool) {
	freshThread := spec.NativeSessionID == ""
	params := map[string]any{
		"cwd":               spec.WorkspaceRoot,
		"sandbox":           sandbox,
		"approvalPolicy":    approvalPolicy,
		"approvalsReviewer": reviewer,
	}
	if spec.Model != "" {
		params["model"] = spec.Model
	}
	if !freshThread {
		params["threadId"] = spec.NativeSessionID
		return "thread/resume", params, false
	}
	if spec.RoleHint != "" {
		params["developerInstructions"] = spec.RoleHint
	}
	return "thread/start", params, true
}

func parseCodexThreadOpenResponse(raw json.RawMessage, freshThread bool) (codexThreadOpenResult, error) {
	out := codexThreadOpenResult{collabMode: "default"}
	if !freshThread {
		var res struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
			Model             string           `json:"model"`
			ReasoningEffort   *string          `json:"reasoningEffort"`
			CollaborationMode *codexCollabMode `json:"collaborationMode"`
		}
		if err := json.Unmarshal(raw, &res); err != nil || res.Thread.ID == "" {
			return codexThreadOpenResult{}, errors.New("native_session_unrecoverable: invalid thread/resume response")
		}
		out.threadID = res.Thread.ID
		out.model = strings.TrimSpace(res.Model)
		if res.ReasoningEffort != nil {
			out.effort = strings.ToLower(strings.TrimSpace(*res.ReasoningEffort))
		}
		if res.CollaborationMode != nil {
			if m := strings.TrimSpace(res.CollaborationMode.Mode); m != "" {
				out.collabMode = m
			}
			if res.CollaborationMode.Settings.DeveloperInstructions != nil {
				v := *res.CollaborationMode.Settings.DeveloperInstructions
				out.collabDevInstr = &v
			}
		}
		return out, nil
	}
	var res struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model           string  `json:"model"`
		ReasoningEffort *string `json:"reasoningEffort"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || res.Thread.ID == "" {
		return codexThreadOpenResult{}, errors.New("codex thread/start returned empty thread id")
	}
	out.threadID = res.Thread.ID
	out.model = strings.TrimSpace(res.Model)
	if res.ReasoningEffort != nil {
		out.effort = strings.ToLower(strings.TrimSpace(*res.ReasoningEffort))
	}
	return out, nil
}

func buildCodexTurnStartParams(threadID string, spec TurnStartSpec, plan codexTurnConfigPlan) map[string]any {
	_, sandboxPolicy, approvalPolicy, reviewer := codexTurnPolicies(spec.AccessMode, spec.ApprovalMode, spec.SessionAutoApprove, spec.WorkspaceRoot)
	params := map[string]any{
		"threadId":            threadID,
		"cwd":                 spec.WorkspaceRoot,
		"approvalPolicy":      approvalPolicy,
		"approvalsReviewer":   reviewer,
		"sandboxPolicy":       sandboxPolicy,
		"clientUserMessageId": spec.ClientCorrelationID,
		"input": []map[string]any{
			{"type": "text", "text": spec.Prompt},
		},
	}
	if plan.useCollabReset {
		// In codex-cli 0.171.0, omitting or sending effort:null preserves the
		// previous turn's effort override. CollaborationMode resets
		// reasoning_effort to null while preserving active mode & instructions.
		params["collaborationMode"] = map[string]any{
			"mode": plan.collabMode,
			"settings": map[string]any{
				"model":                  plan.resetModel,
				"reasoning_effort":       nil,
				"developer_instructions": plan.collabDevInstr,
			},
		}
	} else {
		if plan.sendModel {
			params["model"] = plan.wantModel
		}
		if plan.sendEffort {
			params["effort"] = plan.wantEffort
		}
	}
	return params
}

func parseCodexTurnStartResponse(raw json.RawMessage) (string, error) {
	var res struct {
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || res.Turn.ID == "" {
		return "", errors.New("codex turn/start returned empty turn id")
	}
	return res.Turn.ID, nil
}

func parseCodexInspectTurnsResponse(raw json.RawMessage) ([]NativeTurnRecord, error) {
	var resp struct {
		Data []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Items  []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				ClientID string `json:"clientId"`
				Phase    string `json:"phase"`
				Text     string `json:"text"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	out := make([]NativeTurnRecord, 0, len(resp.Data))
	for _, t := range resp.Data {
		rec := NativeTurnRecord{
			NativeTurnID: t.ID,
			Status:       mapCodexTurnStatus(t.Status),
		}
		for _, it := range t.Items {
			rec.Items = append(rec.Items, NativeItem{
				ItemID:   it.ID,
				Type:     it.Type,
				ClientID: it.ClientID,
				Phase:    it.Phase,
				Text:     it.Text,
			})
		}
		out = append(out, rec)
	}
	return out, nil
}

func parseCodexGuardianWarning(params json.RawMessage) (string, bool) {
	var p struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(params, &p) != nil || strings.TrimSpace(p.Message) == "" {
		return "", false
	}
	return truncateSummary(p.Message, 240), true
}

func parseCodexAutoApprovalReview(params json.RawMessage) (string, bool) {
	var p struct {
		Review struct {
			Status    string `json:"status"`
			RiskLevel string `json:"riskLevel"`
			Rationale string `json:"rationale"`
		} `json:"review"`
	}
	if json.Unmarshal(params, &p) != nil {
		return "", false
	}
	msg := fmt.Sprintf("Codex Guardian review %s", firstNonEmptyStr(p.Review.Status, "completed"))
	if p.Review.RiskLevel != "" {
		msg += " (risk: " + p.Review.RiskLevel + ")"
	}
	if strings.TrimSpace(p.Review.Rationale) != "" {
		msg += ": " + truncateSummary(p.Review.Rationale, 160)
	}
	return msg, true
}

func buildCodexCompletedNativeItem(item codexThreadItem) (NativeItem, bool) {
	isAsyncQuestion := item.Type == "agentMessage" && len(item.Questions) > 0
	phase := item.Phase
	if isAsyncQuestion {
		phase = "commentary"
	}
	return NativeItem{
		ItemID:   item.ID,
		Type:     item.Type,
		ClientID: item.ClientID,
		Phase:    phase,
		Text:     item.Text,
	}, isAsyncQuestion
}

func mapCodexTurnStatus(native string) string {
	switch native {
	case "completed":
		return TurnCompleted
	case "interrupted":
		return TurnInterrupted
	case "failed":
		return TurnFailed
	default:
		return TurnRunning
	}
}

func truncateSummary(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:clampUTF8Bytes([]byte(s), max)] + "..."
}

type codexItemNotification struct {
	TurnID string          `json:"turnId"`
	Item   codexThreadItem `json:"item"`
}

type codexThreadItem struct {
	ID             string               `json:"id"`
	Type           string               `json:"type"`
	ClientID       string               `json:"clientId"`
	Phase          string               `json:"phase"`
	Delivery       string               `json:"delivery"`
	Questions      []codexAsyncQuestion `json:"questions"`
	Text           string               `json:"text"`
	Command        string               `json:"command"`
	Status         string               `json:"status"`
	ExitCode       *int                 `json:"exitCode"`
	DurationMs     *int64               `json:"durationMs"`
	Server         string               `json:"server"`
	Tool           string               `json:"tool"`
	Query          string               `json:"query"`
	Path           string               `json:"path"`
	Summary        []string             `json:"summary"`
	CommandActions []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Path    string `json:"path"`
		Query   string `json:"query"`
	} `json:"commandActions"`
	Changes []struct {
		Path string `json:"path"`
	} `json:"changes"`
}

func compactOneLine(s string, max int) string {
	fields := strings.Fields(s)
	return truncateSummary(strings.Join(fields, " "), max)
}

func summarizeCodexItem(it codexThreadItem) (kind, summary string, emit bool) {
	switch it.Type {
	case "userMessage", "hookPrompt":
		return "", "", false
	case "agentMessage":
		k := "commentary"
		if it.Phase != "" {
			k = it.Phase
		}
		txt := compactOneLine(it.Text, 240)
		if txt == "" {
			return "", "", false
		}
		return k, txt, true
	case "commandExecution":
		desc := summarizeCommandActions(it)
		if desc == "" {
			desc = compactOneLine(it.Command, 180)
		}
		if desc == "" {
			desc = "commandExecution"
		}
		meta := []string{}
		if it.ExitCode != nil {
			meta = append(meta, fmt.Sprintf("exit %d", *it.ExitCode))
		} else if it.Status != "" && it.Status != "completed" {
			meta = append(meta, it.Status)
		}
		if it.DurationMs != nil && *it.DurationMs > 0 {
			meta = append(meta, fmt.Sprintf("%dms", *it.DurationMs))
		}
		if len(meta) > 0 {
			desc += " (" + strings.Join(meta, ", ") + ")"
		}
		return "command", desc, true
	case "fileChange":
		paths := make([]string, 0, len(it.Changes))
		for _, ch := range it.Changes {
			if p := strings.TrimSpace(ch.Path); p != "" {
				paths = append(paths, p)
			}
		}
		desc := "fileChange"
		if len(paths) > 0 {
			if len(paths) > 4 {
				desc = fmt.Sprintf("%s +%d", strings.Join(paths[:4], ", "), len(paths)-4)
			} else {
				desc = strings.Join(paths, ", ")
			}
		}
		if it.Status != "" && it.Status != "completed" {
			desc += " (" + it.Status + ")"
		}
		return "edit", desc, true
	case "mcpToolCall":
		name := strings.Trim(it.Server+"/"+it.Tool, "/")
		if name == "" {
			name = "mcpToolCall"
		}
		if it.Status != "" && it.Status != "completed" {
			name += " (" + it.Status + ")"
		}
		return "mcp", name, true
	case "webSearch":
		q := compactOneLine(it.Query, 180)
		if q == "" {
			q = "webSearch"
		}
		return "search", q, true
	case "reasoning":
		parts := []string{}
		for _, s := range it.Summary {
			if t := compactOneLine(s, 140); t != "" {
				parts = append(parts, t)
			}
		}
		if len(parts) == 0 {
			return "", "", false
		}
		return "reasoning", truncateSummary(strings.Join(parts, " / "), 240), true
	case "plan":
		txt := compactOneLine(it.Text, 240)
		if txt == "" {
			return "", "", false
		}
		return "plan", txt, true
	default:
		desc := it.Type
		if it.Path != "" {
			desc += ": " + it.Path
		}
		return "tool", desc, desc != ""
	}
}

func summarizeCommandActions(it codexThreadItem) string {
	if len(it.CommandActions) == 0 {
		return ""
	}
	parts := make([]string, 0, len(it.CommandActions))
	for _, act := range it.CommandActions {
		switch act.Type {
		case "read":
			if act.Path != "" {
				parts = append(parts, "read "+act.Path)
			}
		case "listFiles":
			if act.Path != "" {
				parts = append(parts, "list "+act.Path)
			} else {
				parts = append(parts, "list files")
			}
		case "search":
			s := "search"
			if act.Query != "" {
				s += " " + compactOneLine(act.Query, 60)
			}
			if act.Path != "" {
				s += " in " + act.Path
			}
			parts = append(parts, s)
		default:
			// Unknown action: let raw command fallback provide exact details.
			return ""
		}
	}
	if len(parts) == 0 {
		return ""
	}
	if len(parts) > 3 {
		return strings.Join(parts[:3], "; ") + fmt.Sprintf(" (+%d)", len(parts)-3)
	}
	return strings.Join(parts, "; ")
}
