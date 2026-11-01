//go:build windows

package localcommands

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode/utf16"
	"unsafe"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"golang.org/x/sys/windows"
)

// StartWindows creates an independent process and releases both handles without
// waiting. token is an already-verified non-elevated user token, or zero for the
// current non-elevated user. It never inherits the node's handles or environment.
func StartWindows(command Command, token windows.Token) error {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return i18n.Wrap("err.localcmd.win_dir_failed", nil, err)
	}
	envToken := token
	if envToken == 0 {
		envToken = windows.GetCurrentProcessToken()
	}
	environment, err := envToken.Environ(false)
	if err != nil {
		return i18n.Wrap("err.localcmd.env_failed", nil, err)
	}
	executable, line, environment, err := windowsInvocation(system, command, environment)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return err
	}
	args, err := windows.UTF16FromString(line)
	if err != nil || len(args) > 32767 {
		return problem("invalid", "err.localcmd.cmdline_too_long")
	}
	directory, err := windows.UTF16PtrFromString(command.WorkingDirectory())
	if err != nil {
		return err
	}
	block := environmentBlock(environment)
	flags := uint32(windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW)
	if command.Window == "terminal" {
		flags = windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NEW_CONSOLE
	}
	startup := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{}))}
	var info windows.ProcessInformation
	if token == 0 {
		err = windows.CreateProcess(name, &args[0], nil, nil, false, flags, &block[0], directory, &startup, &info)
	} else {
		err = windows.CreateProcessAsUser(token, name, &args[0], nil, nil, false, flags, &block[0], directory, &startup, &info)
	}
	runtime.KeepAlive(block)
	if err != nil {
		return i18n.Wrap("err.localcmd.launch_failed", nil, err)
	}
	windows.CloseHandle(info.Thread)
	windows.CloseHandle(info.Process)
	return nil
}

func windowsInvocation(system string, c Command, environment []string) (string, string, []string, error) {
	if strings.EqualFold(filepath.Ext(c.Script), ".ps1") {
		executable := filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe")
		args := []string{executable, "-NoLogo", "-NoProfile", "-ExecutionPolicy", "Bypass"}
		if c.Window == "background" {
			args = append(args, "-NonInteractive")
		} else {
			args = append(args, "-NoExit")
		}
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
	mode := " /c "
	if c.Window == "terminal" {
		mode = " /k "
	}
	line := windows.EscapeArg(executable) + ` /d /s /v:off` + mode + `"` + strings.Join(parts, " ") + `"`
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
