package workers

import (
	"fmt"
	"strings"
)

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
