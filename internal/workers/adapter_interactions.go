package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackgan90/Asterhand/internal/harnesses"
)

func codexTurnPolicies(accessMode, approvalMode string, sessionAutoApprove bool, workspaceRoot string) (sandbox string, sandboxPolicy map[string]any, approvalPolicy, reviewer string) {
	if accessMode != harnesses.AccessWorkspaceWrite {
		return "read-only",
			map[string]any{"type": "readOnly", "networkAccess": false},
			"never",
			"user"
	}
	if sessionAutoApprove {
		// Codex app-server contract: danger-full-access + untrusted + user
		// routes every untrusted command through Asterhand's hard-redline gate.
		return "danger-full-access",
			map[string]any{"type": "dangerFullAccess"},
			"untrusted",
			"user"
	}
	wsPolicy := map[string]any{
		"type":          "workspaceWrite",
		"writableRoots": []string{workspaceRoot},
	}
	switch approvalMode {
	case ApprovalModeStrictDeny:
		return "workspace-write", wsPolicy, "never", "user"
	case ApprovalModeAskHuman:
		return "workspace-write", wsPolicy, "on-request", "user"
	default: // ApprovalModeAuto
		return "workspace-write", wsPolicy, "on-request", "auto_review"
	}
}

func parseCodexInteractionRequest(method string, params json.RawMessage) InteractionRequest {
	switch method {
	case "item/tool/requestUserInput":
		var p struct {
			Questions []struct {
				ID       string `json:"id"`
				Header   string `json:"header"`
				Question string `json:"question"`
				IsOther  bool   `json:"isOther"`
				IsSecret bool   `json:"isSecret"`
				Options  []struct {
					Label       string `json:"label"`
					Description string `json:"description"`
				} `json:"options"`
			} `json:"questions"`
		}
		_ = json.Unmarshal(params, &p)
		qs := make([]InteractionQuestion, 0, len(p.Questions))
		for _, q := range p.Questions {
			opts := make([]InteractionOption, 0, len(q.Options))
			for _, o := range q.Options {
				opts = append(opts, InteractionOption{Label: o.Label, Description: o.Description})
			}
			qs = append(qs, InteractionQuestion{
				ID:       q.ID,
				Header:   q.Header,
				Question: q.Question,
				IsOther:  q.IsOther,
				IsSecret: q.IsSecret,
				Options:  opts,
			})
		}
		summary := "Codex requested user input"
		if len(qs) > 0 && strings.TrimSpace(qs[0].Question) != "" {
			summary = truncateSummary(qs[0].Question, 160)
		}
		return InteractionRequest{
			Kind:      InteractionInput,
			ToolName:  "requestUserInput",
			Summary:   summary,
			Questions: qs,
		}

	case "item/commandExecution/requestApproval", "execCommandApproval":
		var p struct {
			Command string   `json:"command"`
			Cwd     string   `json:"cwd"`
			Reason  string   `json:"reason"`
			Paths   []string `json:"paths"`
		}
		_ = json.Unmarshal(params, &p)
		summary := firstNonEmptyStr(truncateSummary(p.Command, 160), p.Reason, "Run command")
		return InteractionRequest{
			Kind:     InteractionApproval,
			ToolName: "commandExecution",
			Summary:  summary,
			Command:  p.Command,
			Paths:    p.Paths,
			Reason:   p.Reason,
		}

	case "item/fileChange/requestApproval", "applyPatchApproval":
		var p struct {
			Reason    string `json:"reason"`
			GrantRoot string `json:"grantRoot"`
			Changes   []struct {
				Path string `json:"path"`
			} `json:"changes"`
		}
		_ = json.Unmarshal(params, &p)
		var paths []string
		if p.GrantRoot != "" {
			paths = append(paths, p.GrantRoot)
		}
		for _, c := range p.Changes {
			if c.Path != "" {
				paths = append(paths, c.Path)
			}
		}
		summary := firstNonEmptyStr(p.Reason, strings.Join(paths, ", "), "Modify files")
		return InteractionRequest{
			Kind:     InteractionApproval,
			ToolName: "fileChange",
			Summary:  truncateSummary(summary, 160),
			Paths:    paths,
			Reason:   p.Reason,
		}

	case "item/permissions/requestApproval":
		var p struct {
			Reason      string `json:"reason"`
			Permissions struct {
				FileSystem *struct {
					Write []string `json:"write"`
					Read  []string `json:"read"`
				} `json:"fileSystem"`
				Network *struct {
					Enabled *bool `json:"enabled"`
				} `json:"network"`
			} `json:"permissions"`
		}
		_ = json.Unmarshal(params, &p)
		var paths []string
		if p.Permissions.FileSystem != nil {
			paths = append(paths, p.Permissions.FileSystem.Write...)
			paths = append(paths, p.Permissions.FileSystem.Read...)
		}
		summary := firstNonEmptyStr(p.Reason, "Grant additional permissions")
		if p.Permissions.Network != nil && p.Permissions.Network.Enabled != nil && *p.Permissions.Network.Enabled {
			summary = firstNonEmptyStr(p.Reason, "Enable network access")
		}
		return InteractionRequest{
			Kind:     InteractionApproval,
			ToolName: "permissions",
			Summary:  truncateSummary(summary, 160),
			Paths:    paths,
			Reason:   p.Reason,
		}
	}

	isApproval := strings.Contains(method, "Approval") || strings.Contains(method, "requestApproval")
	kind := InteractionInput
	if isApproval {
		kind = InteractionApproval
	}
	return InteractionRequest{
		Kind:     kind,
		ToolName: method,
		Summary:  "Codex requested " + method,
	}
}

func buildCodexInteractionReply(method string, params json.RawMessage, req InteractionRequest, dec InteractionDecision) (result any, rpcErr any, shouldInterrupt bool) {
	if method == "item/tool/requestUserInput" {
		if dec.Decision == DecisionCancelTurn {
			return nil, map[string]any{"code": -32001, "message": "Cancelled by user"}, true
		}
		answers := map[string]map[string][]string{}
		for _, q := range req.Questions {
			vals := dec.Answers[q.ID]
			if len(vals) == 0 && dec.Text != "" {
				vals = []string{dec.Text}
			}
			answers[q.ID] = map[string][]string{"answers": vals}
		}
		if len(answers) == 0 && len(dec.Answers) > 0 {
			for k, vals := range dec.Answers {
				answers[k] = map[string][]string{"answers": vals}
			}
		}
		return map[string]any{"answers": answers}, nil, false
	}

	if method == "item/permissions/requestApproval" {
		if dec.Decision == DecisionCancelTurn {
			return map[string]any{"permissions": map[string]any{}}, nil, true
		}
		if dec.Decision == DecisionDeny {
			return map[string]any{"permissions": map[string]any{}}, nil, false
		}
		var p struct {
			Permissions any `json:"permissions"`
		}
		_ = json.Unmarshal(params, &p)
		// Always grant at most per-turn scope to Codex; Asterhand's Go Service
		// owns revocable session auto-approve and hard-redline gating.
		scope := "turn"
		if p.Permissions == nil {
			p.Permissions = map[string]any{}
		}
		return map[string]any{"permissions": p.Permissions, "scope": scope}, nil, false
	}

	if method == "item/commandExecution/requestApproval" || method == "item/fileChange/requestApproval" || method == "execCommandApproval" || method == "applyPatchApproval" {
		switch dec.Decision {
		case DecisionAllowOnce, DecisionAllowSession:
			// Always approve one request at a time on the native harness so
			// revoking session_auto_approve in Web UI takes effect immediately
			// and every subsequent request still passes Go's hard-redline gate.
			return map[string]any{"decision": "accept"}, nil, false
		case DecisionDeny:
			return map[string]any{"decision": "decline"}, nil, false
		default:
			return map[string]any{"decision": "cancel"}, nil, true
		}
	}

	return nil, map[string]any{"code": -32001, "message": firstNonEmptyStr(dec.Message, "Unsupported interactive request")}, true
}

func parseClaudeInteractionRequest(toolName string, input json.RawMessage) InteractionRequest {
	if strings.EqualFold(toolName, "AskUserQuestion") {
		var p struct {
			Questions []struct {
				ID          string `json:"id"`
				Header      string `json:"header"`
				Question    string `json:"question"`
				MultiSelect bool   `json:"multiSelect"`
				Options     []struct {
					Label       string `json:"label"`
					Description string `json:"description"`
				} `json:"options"`
			} `json:"questions"`
		}
		_ = json.Unmarshal(input, &p)
		qs := make([]InteractionQuestion, 0, len(p.Questions))
		for i, q := range p.Questions {
			id := firstNonEmptyStr(q.ID, q.Question, fmt.Sprintf("q%d", i+1))
			opts := make([]InteractionOption, 0, len(q.Options))
			for _, o := range q.Options {
				opts = append(opts, InteractionOption{Label: o.Label, Description: o.Description})
			}
			qs = append(qs, InteractionQuestion{
				ID:       id,
				Header:   q.Header,
				Question: q.Question,
				Multi:    q.MultiSelect,
				Options:  opts,
			})
		}
		summary := "Claude Code asked a question"
		if len(qs) > 0 && strings.TrimSpace(qs[0].Question) != "" {
			summary = truncateSummary(qs[0].Question, 160)
		}
		return InteractionRequest{
			Kind:      InteractionInput,
			ToolName:  toolName,
			Summary:   summary,
			Questions: qs,
		}
	}

	var fields struct {
		Command     string `json:"command"`
		Description string `json:"description"`
		FilePath    string `json:"file_path"`
		Path        string `json:"path"`
		URL         string `json:"url"`
	}
	_ = json.Unmarshal(input, &fields)
	var paths []string
	if fields.FilePath != "" {
		paths = append(paths, fields.FilePath)
	}
	if fields.Path != "" && fields.Path != fields.FilePath {
		paths = append(paths, fields.Path)
	}
	_, toolDesc := summarizeClaudeToolUse(toolName, input)
	summary := firstNonEmptyStr(toolDesc, fields.Description, truncateSummary(fields.Command, 160), fields.URL, toolName)
	return InteractionRequest{
		Kind:     InteractionApproval,
		ToolName: toolName,
		Summary:  truncateSummary(summary, 160),
		Command:  fields.Command,
		Paths:    paths,
		Reason:   fields.Description,
	}
}

func buildClaudeInteractionReply(toolName string, origInput json.RawMessage, req InteractionRequest, dec InteractionDecision) (map[string]any, bool) {
	if strings.EqualFold(toolName, "AskUserQuestion") {
		if dec.Decision == DecisionCancelTurn {
			return map[string]any{
				"behavior":  "deny",
				"message":   firstNonEmptyStr(dec.Message, "User cancelled the turn"),
				"interrupt": true,
			}, true
		}
		updated := map[string]any{}
		_ = json.Unmarshal(origInput, &updated)
		ansMap := map[string]string{}
		for _, q := range req.Questions {
			vals := dec.Answers[q.ID]
			if len(vals) == 0 {
				vals = dec.Answers[q.Question]
			}
			val := strings.Join(vals, ", ")
			if val == "" {
				val = strings.TrimSpace(dec.Text)
			}
			if q.Question != "" {
				ansMap[q.Question] = val
			}
			if q.ID != "" {
				ansMap[q.ID] = val
			}
		}
		if len(ansMap) == 0 && strings.TrimSpace(dec.Text) != "" {
			ansMap["answer"] = strings.TrimSpace(dec.Text)
		}
		updated["answers"] = ansMap
		return map[string]any{
			"behavior":     "allow",
			"updatedInput": updated,
		}, false
	}

	switch dec.Decision {
	case DecisionAllowOnce, DecisionAllowSession:
		var updated any
		if len(origInput) > 0 {
			_ = json.Unmarshal(origInput, &updated)
		}
		if updated == nil {
			updated = map[string]any{}
		}
		return map[string]any{
			"behavior":     "allow",
			"updatedInput": updated,
		}, false
	case DecisionDeny:
		return map[string]any{
			"behavior":  "deny",
			"message":   firstNonEmptyStr(dec.Message, "User denied this tool invocation; continue with a safer alternative if possible."),
			"interrupt": false,
		}, false
	default:
		return map[string]any{
			"behavior":  "deny",
			"message":   firstNonEmptyStr(dec.Message, "Turn cancelled by user"),
			"interrupt": true,
		}, true
	}
}

type deepSeekPermissionOption struct {
	OptionID string
	Kind     string
}

func normalizeACPOptionKind(raw string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(raw)), "-", "_")
}

func parseDeepSeekPermissionRequest(params, fallbackRaw json.RawMessage, fallbackTitle, fallbackKind string) (req InteractionRequest, opts []deepSeekPermissionOption) {
	var p struct {
		ToolCall struct {
			ToolCallID string          `json:"toolCallId"`
			Title      string          `json:"title"`
			Kind       string          `json:"kind"`
			RawInput   json.RawMessage `json:"rawInput"`
		} `json:"toolCall"`
		Options []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
			Name     string `json:"name"`
		} `json:"options"`
	}
	_ = json.Unmarshal(params, &p)

	var nativeOpts []string
	for _, o := range p.Options {
		if o.OptionID == "" {
			continue
		}
		nativeOpts = append(nativeOpts, o.OptionID)
		opts = append(opts, deepSeekPermissionOption{
			OptionID: o.OptionID,
			Kind:     normalizeACPOptionKind(o.Kind),
		})
	}

	rawInput := p.ToolCall.RawInput
	if len(strings.TrimSpace(string(rawInput))) == 0 || string(bytesTrim(rawInput)) == "null" || string(bytesTrim(rawInput)) == "{}" {
		rawInput = fallbackRaw
	}
	var inFields struct {
		Command       string `json:"command"`
		Cmd           string `json:"cmd"`
		Script        string `json:"script"`
		Path          string `json:"path"`
		FilePath      string `json:"file_path"`
		FilePathCamel string `json:"filePath"`
		TargetFile    string `json:"target_file"`
		Destination   string `json:"destination"`
		Reason        string `json:"reason"`
		Justification string `json:"justification"`
		Description   string `json:"description"`
		Mode          string `json:"mode"`
	}
	if len(rawInput) > 0 {
		_ = json.Unmarshal(rawInput, &inFields)
	}
	cmd := firstNonEmptyStr(inFields.Command, inFields.Cmd, inFields.Script)
	var paths []string
	for _, cand := range []string{inFields.Path, inFields.FilePath, inFields.FilePathCamel, inFields.TargetFile, inFields.Destination} {
		if cand != "" && !containsString(paths, cand) {
			paths = append(paths, cand)
		}
	}
	title := firstNonEmptyStr(p.ToolCall.Title, fallbackTitle, inFields.Mode, "permission request")
	titleHasCmdPrefix := false
	for _, pfx := range []string{"pwsh:", "powershell:", "bash:", "sh:", "cmd:"} {
		if len(title) > len(pfx) && strings.EqualFold(title[:len(pfx)], pfx) {
			titleHasCmdPrefix = true
			if cmd == "" {
				cmd = strings.TrimSpace(title[len(pfx):])
			}
			break
		}
	}
	summary := title
	if cmd != "" && !titleHasCmdPrefix && !strings.Contains(summary, cmd) {
		summary = title + ": " + truncateSummary(cmd, 120)
	}
	return InteractionRequest{
		Kind:          InteractionApproval,
		ToolName:      firstNonEmptyStr(p.ToolCall.Kind, fallbackKind, "permission"),
		Summary:       truncateSummary(summary, 160),
		Command:       cmd,
		Paths:         paths,
		Reason:        firstNonEmptyStr(inFields.Justification, inFields.Reason, inFields.Description, inFields.Mode),
		NativeOptions: nativeOpts,
	}, opts
}

func bytesTrim(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

func containsString(list []string, target string) bool {
	for _, s := range list {
		if s == target {
			return true
		}
	}
	return false
}

type codexAsyncQuestion struct {
	Title   string   `json:"title"`
	Options []string `json:"options"`
}

func formatInteractionAnswerText(req InteractionRequest, dec InteractionDecision) string {
	var parts []string
	for _, q := range req.Questions {
		if vals := dec.Answers[q.ID]; len(vals) > 0 {
			if q.Question != "" {
				parts = append(parts, fmt.Sprintf("%s: %s", q.Question, strings.Join(vals, ", ")))
			} else {
				parts = append(parts, strings.Join(vals, ", "))
			}
		}
	}
	if len(parts) == 0 {
		for _, vals := range dec.Answers {
			if len(vals) > 0 {
				parts = append(parts, strings.Join(vals, ", "))
			}
		}
	}
	if txt := strings.TrimSpace(dec.Text); txt != "" {
		parts = append(parts, txt)
	}
	if len(parts) == 0 {
		return "Proceed with your best judgment."
	}
	return strings.Join(parts, "\n")
}

func handleCodexAsyncQuestions(s *codexSession, at *codexActiveTurn, item codexThreadItem) {
	s.mu.Lock()
	if at == nil || at.onInteraction == nil || len(item.Questions) == 0 {
		s.mu.Unlock()
		return
	}
	if at.handledQuestions == nil {
		at.handledQuestions = map[string]bool{}
	}
	key := firstNonEmptyStr(item.ID, item.Text)
	if at.handledQuestions[key] {
		s.mu.Unlock()
		return
	}
	at.handledQuestions[key] = true
	handler := at.onInteraction
	turnCtx := at.turnCtx
	tid := at.turnID
	threadID := s.threadID
	s.mu.Unlock()

	if turnCtx == nil {
		turnCtx = context.Background()
	}
	qs := make([]InteractionQuestion, 0, len(item.Questions))
	for i, q := range item.Questions {
		opts := make([]InteractionOption, 0, len(q.Options))
		for _, o := range q.Options {
			if strings.TrimSpace(o) != "" {
				opts = append(opts, InteractionOption{Label: o})
			}
		}
		qs = append(qs, InteractionQuestion{
			ID:       fmt.Sprintf("q%d", i+1),
			Question: firstNonEmptyStr(q.Title, item.Text),
			Options:  opts,
		})
	}
	req := InteractionRequest{
		Kind:      InteractionInput,
		ToolName:  "request_user_input_async",
		Summary:   truncateSummary(firstNonEmptyStr(qs[0].Question, item.Text, "Codex asked a question"), 160),
		Questions: qs,
	}

	go func() {
		dec, err := handler(turnCtx, req)
		if err != nil || dec.Decision == DecisionCancelTurn {
			if tid != "" {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_, _ = s.request(ctx, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": tid})
			}
			return
		}
		ansText := formatInteractionAnswerText(req, dec)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = s.request(ctx, "turn/steer", map[string]any{
			"threadId":       threadID,
			"expectedTurnId": tid,
			"input": []map[string]any{
				{"type": "text", "text": ansText},
			},
		})
	}()
}

func buildDeepSeekPermissionReply(opts []deepSeekPermissionOption, dec InteractionDecision) (map[string]any, bool) {
	hasKind := false
	for _, o := range opts {
		if o.Kind != "" {
			hasKind = true
			break
		}
	}
	pickExact := func(wantKind string) string {
		for _, o := range opts {
			if o.Kind == wantKind {
				return o.OptionID
			}
		}
		if !hasKind {
			for _, o := range opts {
				if normalizeACPOptionKind(o.OptionID) == wantKind {
					return o.OptionID
				}
			}
		}
		return ""
	}
	switch dec.Decision {
	case DecisionAllowOnce, DecisionAllowSession:
		// Always select ACP allow_once even for allow_session so Asterhand's
		// Go hard-redline gate inspects every subsequent request and Web revoke
		// takes effect immediately. Never silently escalate to allow_always.
		if opt := pickExact("allow_once"); opt != "" {
			return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": opt}}, false
		}
		return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, false
	case DecisionDeny:
		if opt := pickExact("reject_once"); opt != "" {
			return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": opt}}, false
		}
		return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, false
	default:
		return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, true
	}
}

func resolveDeepSeekPermission(s *deepseekSession, idVal any, sid string, active *deepseekActiveTurn, handler InteractionHandler, turnCtx context.Context, ireq InteractionRequest, opts []deepSeekPermissionOption) {
	dec, err := handler(turnCtx, ireq)
	if err != nil {
		dec = InteractionDecision{Decision: DecisionCancelTurn, Message: err.Error()}
	}
	res, shouldCancel := buildDeepSeekPermissionReply(opts, dec)
	if shouldCancel {
		s.mu.Lock()
		if s.active == active {
			if dec.Actor == "policy" {
				s.active.blockedCode, s.active.blockedMsg = TurnBlockedApproval, firstNonEmptyStr(dec.Message, "Blocked interactive approval")
			} else {
				s.active.interrupted = true
			}
		}
		s.mu.Unlock()
	}
	_ = s.writeJSON(map[string]any{"jsonrpc": "2.0", "id": idVal, "result": res})
	if shouldCancel && sid != "" {
		_ = s.notify("session/cancel", map[string]any{"sessionId": sid})
	}
}

func rejectDeepSeekLegacyPermission(s *deepseekSession, idVal any, sid string, active *deepseekActiveTurn, detail string) {
	blockedMsg := "DeepSeek Harness requested interactive permission approval, which is not supported in this milestone"
	if detail != "" {
		blockedMsg = fmt.Sprintf("DeepSeek Harness requested interactive permission approval (%s), which is not supported in this milestone", truncateSummary(detail, 120))
	}
	s.mu.Lock()
	if active != nil && active.blockedCode == "" {
		active.blockedCode, active.blockedMsg = TurnBlockedApproval, blockedMsg
	}
	s.mu.Unlock()
	if active != nil && active.onEvent != nil {
		active.onEvent(AdapterEvent{Kind: "system", Summary: blockedMsg})
	}
	_ = s.writeJSON(map[string]any{"jsonrpc": "2.0", "id": idVal, "result": map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}})
	if sid != "" {
		go func() { _ = s.notify("session/cancel", map[string]any{"sessionId": sid}) }()
	}
}
