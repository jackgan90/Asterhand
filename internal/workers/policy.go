package workers

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/harnesses"
)

type RiskAssessment struct {
	RiskLevel        string
	HardRedline      bool
	HardDeny         bool
	OutsideWorkspace bool
	Reasons          []string
}

func NormalizeApprovalMode(accessMode, requested string, caps harnesses.Capabilities) (string, string, error) {
	req := strings.TrimSpace(strings.ToLower(requested))
	if accessMode == harnesses.AccessReadOnly {
		switch req {
		case "", ApprovalModeAuto, ApprovalModeDenyEscalation, ApprovalModeStrictDeny:
			return ApprovalModeDenyEscalation, ApprovalModeDenyEscalation, nil
		case ApprovalModeAskHuman:
			return "", "", automation.Fail("invalid_parameters", "read_only workers do not allow escalation approvals")
		default:
			return "", "", automation.Fail("invalid_parameters", "approval_mode must be auto, ask_human, or strict_deny")
		}
	}
	if req == "" {
		req = ApprovalModeAuto
	}
	switch req {
	case ApprovalModeAuto:
		if caps.SupportsAutoApproval {
			return ApprovalModeAuto, ApprovalModeAuto, nil
		}
		return ApprovalModeAuto, ApprovalModeAskHuman, nil
	case ApprovalModeAskHuman:
		return ApprovalModeAskHuman, ApprovalModeAskHuman, nil
	case ApprovalModeStrictDeny:
		return ApprovalModeStrictDeny, ApprovalModeStrictDeny, nil
	default:
		return "", "", automation.Fail("invalid_parameters", "approval_mode must be auto, ask_human, or strict_deny")
	}
}

var (
	reDestructiveOS = regexp.MustCompile(`(?i)\b(format(\.com|\.exe)?\s+[a-z]:|diskpart\b|bcdedit\b|shutdown(\.exe)?\b|restart-computer\b|stop-computer\b|reg(\.exe)?\s+(delete|add)\s+[^\n]*(hklm|hkey_local_machine)|sc(\.exe)?\s+(delete|config|stop)\b|net(\.exe)?\s+(user|localgroup)\b|takeown(\.exe)?\b|mkfs(\.[a-z0-9]+)?\b|dd\s+if=)`)
	reRootOrHomeDel = regexp.MustCompile(`(?i)(rm\s+-[a-z]*r[a-z]*f|rm\s+-[a-z]*f[a-z]*r|remove-item\b[^\n]*-recurse|rd\s+/s|rmdir\s+/s|del\s+/s)[^\n]*([a-z]:[\\/]\s*$|[a-z]:[\\/](windows|program files|users|programdata)|\s+/\s*$|\s+~([\\/]|\s|$)|\$env:userprofile|%userprofile%)`)
	reWinPathToken  = regexp.MustCompile(`(?i)[a-z]:[\\/][^\s"'` + "`" + `|;&><()]+`)
	reHomeToken     = regexp.MustCompile(`(?i)(~|\$env:userprofile|%userprofile%|\$home)[\\/][^\s"'` + "`" + `|;&><()]+`)
	reUnixSysToken  = regexp.MustCompile(`(?i)(^|\s)(/(etc|usr|bin|sbin|var|boot|dev|proc|sys)(/[^\s"'|;&><()]*)?)`)
)

func EvaluateInteractionRisk(nodeHome, workspaceRoot, accessMode string, req InteractionRequest) RiskAssessment {
	out := RiskAssessment{RiskLevel: "medium"}
	if req.Kind == InteractionInput {
		out.RiskLevel = "low"
		return out
	}
	if accessMode == harnesses.AccessReadOnly {
		out.RiskLevel = "critical"
		out.HardRedline = true
		out.HardDeny = true
		out.Reasons = append(out.Reasons, "read_only worker cannot escalate permissions")
		return out
	}

	canonWS, _ := harnesses.CanonicalPath(workspaceRoot)
	cmd := strings.TrimSpace(req.Command)

	if cmd != "" {
		if reDestructiveOS.MatchString(cmd) {
			out.RiskLevel = "critical"
			out.HardRedline = true
			out.Reasons = append(out.Reasons, "Destructive OS-level or system administration command")
		}
		if reRootOrHomeDel.MatchString(cmd) {
			out.RiskLevel = "critical"
			out.HardRedline = true
			out.Reasons = append(out.Reasons, "Recursive deletion targeting root, system, or user home path")
		}
		if hasSensitiveCredentialRef(cmd) {
			out.RiskLevel = "critical"
			out.HardRedline = true
			out.Reasons = append(out.Reasons, "Command references sensitive credential or harness configuration directory")
		}
	}

	candidates := append([]string{}, req.Paths...)
	candidates = append(candidates, extractCommandPaths(cmd)...)

	for _, raw := range candidates {
		raw = strings.TrimSpace(strings.Trim(raw, `"'`+"`"))
		if raw == "" {
			continue
		}
		resolved := expandCandidatePath(raw, canonWS)
		if resolved == "" {
			continue
		}
		if isHardRedlinePath(resolved, nodeHome) {
			out.RiskLevel = "critical"
			out.HardRedline = true
			out.OutsideWorkspace = true
			out.Reasons = appendUniqueReason(out.Reasons, "Targets protected system, node data, or credential path")
			continue
		}
		if canonWS != "" && !harnesses.WithinRoot(canonWS, resolved) {
			out.OutsideWorkspace = true
			if out.RiskLevel != "critical" {
				out.RiskLevel = "high"
			}
			out.Reasons = appendUniqueReason(out.Reasons, "Targets path outside worker workspace")
			if isRecursiveDeleteCommand(cmd) {
				out.RiskLevel = "critical"
				out.HardRedline = true
				out.Reasons = appendUniqueReason(out.Reasons, "Recursive deletion outside worker workspace")
			}
		}
	}

	// Escalation keywords (e.g. danger-full-access) elevate risk to high when not already critical.
	combined := strings.ToLower(req.ToolName + " " + req.Summary + " " + req.Reason)
	if strings.Contains(combined, "danger-full-access") || strings.Contains(combined, "full-access") || strings.Contains(combined, "escalat") {
		if out.RiskLevel == "low" || out.RiskLevel == "medium" {
			out.RiskLevel = "high"
		}
	}
	return out
}

func isRecursiveDeleteCommand(cmd string) bool {
	lower := strings.ToLower(cmd)
	return strings.Contains(lower, "rm -rf") ||
		strings.Contains(lower, "rm -fr") ||
		(strings.Contains(lower, "remove-item") && strings.Contains(lower, "-recurse")) ||
		strings.Contains(lower, "rd /s") ||
		strings.Contains(lower, "rmdir /s") ||
		strings.Contains(lower, "del /s")
}

func hasSensitiveCredentialRef(cmd string) bool {
	lower := strings.ToLower(strings.ReplaceAll(cmd, `\`, `/`))
	markers := []string{
		"/.ssh/", "/.ssh\"", "/.ssh'", "/.ssh ",
		"/.aws/", "/.gnupg/", "/.kube/", "/.azure/",
		"/.config/gcloud", "/.gemini/", "/.codex/", "/.claude/", "/.dsh/",
		"/.claude.json",
	}
	for _, m := range markers {
		if strings.Contains(lower, m) || strings.HasSuffix(lower, strings.TrimRight(m, "/ ")) {
			return true
		}
	}
	return false
}

func extractCommandPaths(cmd string) []string {
	if cmd == "" {
		return nil
	}
	var out []string
	out = append(out, reWinPathToken.FindAllString(cmd, -1)...)
	out = append(out, reHomeToken.FindAllString(cmd, -1)...)
	for _, m := range reUnixSysToken.FindAllStringSubmatch(cmd, -1) {
		if len(m) >= 3 && m[2] != "" {
			out = append(out, m[2])
		}
	}
	return out
}

func expandCandidatePath(raw, workspaceRoot string) string {
	lower := strings.ToLower(raw)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		switch {
		case strings.HasPrefix(lower, "~/") || strings.HasPrefix(lower, `~\`):
			raw = filepath.Join(home, raw[2:])
		case strings.HasPrefix(lower, "$env:userprofile\\") || strings.HasPrefix(lower, "$env:userprofile/"):
			raw = filepath.Join(home, raw[len("$env:userprofile/"):])
		case strings.HasPrefix(lower, "%userprofile%\\") || strings.HasPrefix(lower, "%userprofile%/"):
			raw = filepath.Join(home, raw[len("%userprofile%/"):])
		case strings.HasPrefix(lower, "$home\\") || strings.HasPrefix(lower, "$home/"):
			raw = filepath.Join(home, raw[len("$home/"):])
		}
	}
	if !filepath.IsAbs(raw) && workspaceRoot != "" {
		raw = filepath.Join(workspaceRoot, raw)
	}
	canon, err := harnesses.CanonicalPath(raw)
	if err != nil {
		return filepath.Clean(raw)
	}
	return canon
}

func isHardRedlinePath(canon, nodeHome string) bool {
	if canon == "" {
		return false
	}
	if filepath.Dir(canon) == canon {
		return true
	}
	if nodeHome != "" {
		if ch, err := harnesses.CanonicalPath(nodeHome); err == nil && harnesses.WithinRoot(ch, canon) {
			return true
		}
	}
	if homeDir, err := os.UserHomeDir(); err == nil && homeDir != "" {
		if cu, err := harnesses.CanonicalPath(homeDir); err == nil {
			if harnesses.SamePath(cu, canon) {
				return true
			}
			for _, rel := range []string{
				".ssh", ".aws", ".gnupg", ".kube", ".azure",
				filepath.Join(".config", "gcloud"),
				".gemini", ".codex", ".claude", ".claude.json", ".dsh",
			} {
				if harnesses.WithinRoot(filepath.Join(cu, rel), canon) {
					return true
				}
			}
		}
	}
	if runtime.GOOS == "windows" {
		for _, env := range []string{"SystemRoot", "WINDIR", "ProgramFiles", "ProgramFiles(x86)", "ProgramData"} {
			if v := os.Getenv(env); v != "" {
				if cs, err := harnesses.CanonicalPath(v); err == nil && harnesses.WithinRoot(cs, canon) {
					return true
				}
			}
		}
	} else {
		for _, sysDir := range []string{"/etc", "/usr", "/bin", "/sbin", "/boot", "/dev", "/proc", "/sys"} {
			if harnesses.WithinRoot(sysDir, canon) {
				return true
			}
		}
	}
	return false
}

func appendUniqueReason(list []string, item string) []string {
	for _, existing := range list {
		if existing == item {
			return list
		}
	}
	return append(list, item)
}
