package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func classifyDeepSeekStopReason(stopReason string) (state, code, msg string) {
	switch stopReason {
	case "max_tokens":
		return TurnInterrupted, "max_tokens", "DeepSeek stopped before completing because the output token limit (max_tokens) was reached; partial output was preserved."
	case "max_turn_requests":
		return TurnInterrupted, "max_turn_requests", "DeepSeek stopped before completing because the maximum number of model requests for this turn (max_turn_requests) was reached; partial output was preserved."
	case "refusal":
		return TurnFailed, "refusal", "DeepSeek refused to complete this turn (refusal); any partial output was preserved."
	default:
		return TurnInterrupted, stopReason, fmt.Sprintf("DeepSeek stopped before completing the turn (stop_reason=%s); any partial output was preserved.", stopReason)
	}
}

type acpConfigOption struct {
	ID           string               `json:"id"`
	Category     string               `json:"category"`
	CurrentValue string               `json:"currentValue"`
	Options      []acpSelectOptionAny `json:"options"`
}

type acpSelectOptionAny struct {
	Value   string               `json:"value"`
	Name    string               `json:"name"`
	Group   string               `json:"group"`
	Options []acpSelectOptionAny `json:"options"`
}

func (s *deepseekSession) indexConfigOptions(opts []acpConfigOption, captureNative bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.indexConfigOptionsLocked(opts, captureNative)
}

func (s *deepseekSession) indexConfigOptionsLocked(opts []acpConfigOption, captureNative bool) {
	capturedAny := false
	for _, opt := range opts {
		switch opt.ID {
		case "model":
			s.currentModel = opt.CurrentValue
			if captureNative && !s.nativeCaptured {
				s.nativeModel = opt.CurrentValue
				capturedAny = true
			}
			s.modelOptions = map[string]string{}
			flattenACPOptions(opt.Options, func(val, name string) {
				s.modelOptions[val] = val
				s.modelOptions[strings.ToLower(val)] = val
				if name != "" {
					s.modelOptions[strings.ToLower(name)] = val
				}
				var tuple []string
				if json.Unmarshal([]byte(val), &tuple) == nil && len(tuple) >= 2 {
					s.modelOptions[strings.ToLower(tuple[1])] = val
					s.modelOptions[strings.ToLower(tuple[0]+"/"+tuple[1])] = val
				}
			})
		case "reasoning_effort":
			s.currentEffort = opt.CurrentValue
			if captureNative && !s.nativeCaptured {
				s.nativeEffort = opt.CurrentValue
				capturedAny = true
			}
			s.effortOptions = map[string]string{}
			flattenACPOptions(opt.Options, func(val, name string) {
				s.effortOptions[val] = val
				s.effortOptions[strings.ToLower(val)] = val
				if name != "" {
					s.effortOptions[strings.ToLower(name)] = val
				}
			})
		}
	}
	if captureNative && !s.nativeCaptured && capturedAny {
		s.nativeCaptured = true
	}
}

func flattenACPOptions(items []acpSelectOptionAny, fn func(val, name string)) {
	for _, it := range items {
		if len(it.Options) > 0 {
			flattenACPOptions(it.Options, fn)
			continue
		}
		if strings.TrimSpace(it.Value) != "" {
			fn(strings.TrimSpace(it.Value), strings.TrimSpace(it.Name))
		}
	}
}

func (s *deepseekSession) resolveModelOption(want string) string {
	want = strings.TrimSpace(want)
	if want == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.modelOptions[want]; ok {
		return v
	}
	if v, ok := s.modelOptions[strings.ToLower(want)]; ok {
		return v
	}
	return want
}

func (s *deepseekSession) resolveEffortOption(want string) string {
	want = strings.TrimSpace(want)
	if want == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.effortOptions[want]; ok {
		return v
	}
	if v, ok := s.effortOptions[strings.ToLower(want)]; ok {
		return v
	}
	return strings.ToLower(want)
}

func (s *deepseekSession) resolveEffectiveModel(turnOverride string) string {
	if want := strings.TrimSpace(turnOverride); want != "" {
		return s.resolveModelOption(want)
	}
	if want := strings.TrimSpace(s.openSpec.Model); want != "" {
		return s.resolveModelOption(want)
	}
	s.mu.Lock()
	native := s.nativeModel
	s.mu.Unlock()
	return s.resolveModelOption(native)
}

func (s *deepseekSession) resolveEffectiveEffort(turnOverride string) string {
	if want := strings.TrimSpace(turnOverride); want != "" {
		return s.resolveEffortOption(want)
	}
	if want := strings.TrimSpace(s.openSpec.ReasoningEffort); want != "" {
		return s.resolveEffortOption(want)
	}
	s.mu.Lock()
	native := s.nativeEffort
	s.mu.Unlock()
	return s.resolveEffortOption(native)
}

func (s *deepseekSession) applyConfigOverrides(ctx context.Context, modelReq, effortReq string) error {
	if resolvedModel := s.resolveEffectiveModel(modelReq); resolvedModel != "" {
		s.mu.Lock()
		changed := resolvedModel != s.currentModel
		sid := s.sessionID
		s.mu.Unlock()
		if changed {
			raw, err := s.request(ctx, "session/set_config_option", map[string]any{
				"sessionId": sid,
				"configId":  "model",
				"value":     resolvedModel,
			})
			if err != nil {
				return fmt.Errorf("deepseek set model failed: %w", err)
			}
			s.syncConfigAfterSet("model", resolvedModel, raw)
		}
	}
	if resolvedEffort := s.resolveEffectiveEffort(effortReq); resolvedEffort != "" {
		s.mu.Lock()
		changed := resolvedEffort != s.currentEffort
		sid := s.sessionID
		s.mu.Unlock()
		if changed {
			raw, err := s.request(ctx, "session/set_config_option", map[string]any{
				"sessionId": sid,
				"configId":  "reasoning_effort",
				"value":     resolvedEffort,
			})
			if err != nil {
				return fmt.Errorf("deepseek set reasoning_effort failed: %w", err)
			}
			s.syncConfigAfterSet("reasoning_effort", resolvedEffort, raw)
		}
	}
	return nil
}

func (s *deepseekSession) syncConfigAfterSet(configID, appliedValue string, raw json.RawMessage) {
	var resp struct {
		ConfigOptions []acpConfigOption `json:"configOptions"`
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seenTarget := false
	if len(raw) > 0 && json.Unmarshal(raw, &resp) == nil && len(resp.ConfigOptions) > 0 {
		s.indexConfigOptionsLocked(resp.ConfigOptions, false)
		for _, opt := range resp.ConfigOptions {
			if opt.ID == configID && opt.CurrentValue != "" {
				seenTarget = true
				break
			}
		}
	}
	if !seenTarget {
		switch configID {
		case "model":
			s.currentModel = appliedValue
		case "reasoning_effort":
			s.currentEffort = appliedValue
		}
	}
}

func buildDeepSeekItems(commentary []NativeItem, turnID, text, phase string) []NativeItem {
	items := append([]NativeItem(nil), commentary...)
	if text == "" {
		return items
	}
	return append(items, NativeItem{
		ItemID: turnID + "_answer",
		Type:   "agentMessage",
		Phase:  phase,
		Text:   text,
	})
}

func extractACPChunkText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var obj struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.Text
	}
	return ""
}

func summarizeDeepSeekToolCall(title string, rawInput json.RawMessage) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "tool"
	}
	if len(rawInput) == 0 {
		return title
	}
	var in struct {
		FilePath  string `json:"file_path"`
		Path      string `json:"path"`
		Pattern   string `json:"pattern"`
		Glob      string `json:"glob"`
		Directory string `json:"directory"`
		Command   string `json:"command"`
		Script    string `json:"script"`
		Query     string `json:"query"`
		URL       string `json:"url"`
	}
	if json.Unmarshal(rawInput, &in) != nil {
		return title
	}
	cmd := firstNonEmpty(in.Command, in.Script)
	if cmd != "" {
		return title + ": " + firstLine(cmd, 120)
	}
	target := firstNonEmpty(in.FilePath, in.Path, in.Pattern, in.Glob, in.Directory, in.Query, in.URL)
	if target != "" {
		return title + " " + firstLine(target, 120)
	}
	return title
}

func extractACPToolFailure(contentRaw, rawOutput json.RawMessage) string {
	if txt := extractACPContentText(contentRaw); txt != "" {
		return firstLine(txt, 140)
	}
	if len(rawOutput) > 0 {
		var obj struct {
			Error   string `json:"error"`
			Message string `json:"message"`
			Stderr  string `json:"stderr"`
		}
		if json.Unmarshal(rawOutput, &obj) == nil {
			if msg := firstNonEmpty(obj.Error, obj.Message, obj.Stderr); msg != "" {
				return firstLine(msg, 140)
			}
		}
	}
	return ""
}

func extractACPContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var items []struct {
		Text    string `json:"text"`
		Content *struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &items) == nil {
		for _, it := range items {
			if it.Content != nil && strings.TrimSpace(it.Content.Text) != "" {
				return strings.TrimSpace(it.Content.Text)
			}
			if strings.TrimSpace(it.Text) != "" {
				return strings.TrimSpace(it.Text)
			}
		}
		return ""
	}
	return strings.TrimSpace(extractACPChunkText(raw))
}

func firstLine(s string, maxChars int) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexAny(s, "\r\n"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	return truncateSummary(s, maxChars)
}

func extractACPNonZeroExitCode(contentRaw json.RawMessage) string {
	txt := strings.TrimSpace(extractACPContentText(contentRaw))
	if txt == "" {
		return ""
	}
	last := txt
	if idx := strings.LastIndexAny(txt, "\r\n"); idx >= 0 {
		last = strings.TrimSpace(txt[idx+1:])
	}
	const prefix = "[exit code:"
	lower := strings.ToLower(last)
	if !strings.HasPrefix(lower, prefix) || !strings.HasSuffix(last, "]") {
		return ""
	}
	numStr := strings.TrimSpace(last[len(prefix) : len(last)-1])
	n, err := strconv.Atoi(numStr)
	if err != nil || n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func classifyDeepSeekToolKind(title string) string {
	lower := strings.ToLower(strings.TrimSpace(title))
	switch {
	case strings.Contains(lower, "write") || strings.Contains(lower, "edit") || strings.Contains(lower, "patch"):
		return "edit"
	case strings.Contains(lower, "pwsh") || strings.Contains(lower, "bash") || strings.Contains(lower, "shell") || strings.Contains(lower, "exec"):
		return "command"
	case strings.Contains(lower, "glob") || strings.Contains(lower, "grep") || strings.Contains(lower, "search") || strings.Contains(lower, "web"):
		return "search"
	default:
		return "tool"
	}
}
