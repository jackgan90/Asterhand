//go:build windows

package localcommands

import (
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

func windowsInvocation(system string, c ScriptSpec, environment []string) (string, string, []string, error) {
	if strings.EqualFold(filepath.Ext(c.Script), ".ps1") {
		executable := filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe")
		args := []string{executable, "-NoLogo", "-NoProfile", "-ExecutionPolicy", "Bypass"}
		args = append(args, "-NonInteractive")
		args = append(args, "-File", c.Script)
		return executable, windows.ComposeCommandLine(append(args, c.Arguments...)), environment, nil
	}
	if ext := strings.ToLower(filepath.Ext(c.Script)); ext != ".bat" && ext != ".cmd" {
		return "", "", nil, problem("unsupported", "err.localcmd.unsupported_script")
	}
	executable := filepath.Join(system, "cmd.exe")
	parts := []string{cmdEscape(c.Script)}
	for _, value := range c.Arguments {
		if strings.Contains(value, `"`) {
			return "", "", nil, problem("invalid", "err.localcmd.batch_quote_run")
		}
		// Batch scripts read parameters with %~n. Backslashes are literal here,
		// including immediately before a closing quote; CRT quoting would alter them.
		parts = append(parts, cmdEscape(`"`+value+`"`))
	}
	line := windows.EscapeArg(executable) + ` /d /s /v:off` + " /c " + `"` + strings.Join(parts, " ") + `"`
	if len(utf16.Encode([]rune(line))) > 8190 {
		return "", "", nil, problem("invalid", "err.localcmd.cmd_too_long")
	}
	return executable, line, environment, nil
}

// Command processor metacharacters are escaped independently from Windows
// argument quoting. CALL would add another expansion pass and is not used.
func cmdEscape(value string) string {
	var result strings.Builder
	for _, c := range value {
		if strings.ContainsRune("()[]%!^\"`<>&|;, *?", c) {
			result.WriteByte('^')
		}
		result.WriteRune(c)
	}
	return result.String()
}

func environmentBlock(environment []string) []uint16 {
	slices.SortFunc(environment, func(a, b string) int { return strings.Compare(strings.ToUpper(a), strings.ToUpper(b)) })
	var block []uint16
	for _, entry := range environment {
		block = append(block, utf16.Encode([]rune(entry))...)
		block = append(block, 0)
	}
	return append(block, 0)
}
