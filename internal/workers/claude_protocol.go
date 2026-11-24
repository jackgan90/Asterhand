package workers

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackgan90/Asterhand/internal/harnesses"
)

var claudeAskHumanAllowedTools = strings.Join([]string{
	"Read", "Glob", "Grep",
	"Bash(git status*)", "Bash(git diff*)", "Bash(git log*)",
}, ",")

func buildClaudeArgs(spec ThreadOpenSpec, sessionID string, resume bool, modelName, effort string) []string {
	args := []string{
		"-p",
		"--verbose",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--no-chrome",
		"--strict-mcp-config",
		"--mcp-config", `{"mcpServers":{}}`,
	}
	if spec.AccessMode == harnesses.AccessWorkspaceWrite {
		if spec.ApprovalMode == "" || spec.ApprovalMode == ApprovalModeAuto {
			args = append(args,
				"--tools", "Read,Glob,Grep,Edit,Write,Bash,AskUserQuestion",
				"--permission-mode", "auto",
				"--permission-prompt-tool", "stdio",
			)
		} else {
			args = append(args,
				"--tools", "Read,Glob,Grep,Edit,Write,Bash,AskUserQuestion",
				"--allowed-tools", claudeAskHumanAllowedTools,
				"--permission-mode", "acceptEdits",
				"--permission-prompt-tool", "stdio",
			)
		}
	} else {
		args = append(args,
			"--tools", "Read,Glob,Grep,AskUserQuestion",
			"--allowed-tools", "Read,Glob,Grep",
			"--permission-mode", "default",
			"--permission-prompt-tool", "stdio",
		)
	}
	if strings.TrimSpace(modelName) != "" {
		args = append(args, "--model", strings.TrimSpace(modelName))
	}
	if strings.TrimSpace(effort) != "" {
		args = append(args, "--effort", strings.TrimSpace(effort))
	}
	if strings.TrimSpace(spec.RoleHint) != "" {
		args = append(args, "--append-system-prompt", strings.TrimSpace(spec.RoleHint))
	}
	if resume {
		args = append(args, "--resume", sessionID)
	} else {
		args = append(args, "--session-id", sessionID)
	}
	return args
}

func claudeMessageHasToolResult(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var msg struct {
		Content []struct {
			Type string `json:"type"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return false
	}
	for _, b := range msg.Content {
		if b.Type == "tool_result" {
			return true
		}
	}
	return false
}

func summarizeClaudeStreamStart(line string) string {
	var env struct {
		Event struct {
			Type         string `json:"type"`
			ContentBlock struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"content_block"`
		} `json:"event"`
	}
	if json.Unmarshal([]byte(line), &env) != nil || env.Event.Type != "content_block_start" {
		return ""
	}
	switch env.Event.ContentBlock.Type {
	case "tool_use":
		if name := strings.TrimSpace(env.Event.ContentBlock.Name); name != "" {
			return "Generating " + name + " tool input..."
		}
		return "Generating tool input..."
	case "thinking":
		return "Thinking..."
	case "text":
		return "Writing response..."
	default:
		return ""
	}
}

func summarizeClaudeToolUse(name string, raw json.RawMessage) (string, string) {
	var in struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
		Pattern  string `json:"pattern"`
		Command  string `json:"command"`
	}
	_ = json.Unmarshal(raw, &in)
	switch name {
	case "Edit", "Write", "NotebookEdit":
		target := firstNonEmpty(in.FilePath, in.Path)
		if target != "" {
			return "edit", truncateSummary(name+" "+target, 160)
		}
		return "edit", name
	case "Bash":
		if in.Command != "" {
			return "command", truncateSummary("Bash: "+in.Command, 160)
		}
		return "command", "Bash"
	case "Glob", "Grep":
		target := firstNonEmpty(in.Pattern, in.Path)
		if target != "" {
			return "search", truncateSummary(name+" "+target, 160)
		}
		return "search", name
	case "Read":
		if in.FilePath != "" {
			return "tool", truncateSummary("Read "+in.FilePath, 160)
		}
		return "tool", "Read"
	default:
		return "tool", truncateSummary("Tool: "+firstNonEmpty(name, "unknown"), 160)
	}
}

func isClaudeSessionMissing(msg, stderr string) bool {
	combined := strings.ToLower(msg + "\n" + stderr)
	return strings.Contains(combined, "no conversation found with session id") ||
		strings.Contains(combined, "session not found")
}

func newUUIDv4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
