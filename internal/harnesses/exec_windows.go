//go:build windows

package harnesses

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

func defaultCommandRunner(ctx context.Context, exe string, args ...string) (string, string, error) {
	ext := strings.ToLower(filepath.Ext(exe))
	var cmd *exec.Cmd
	if ext == ".cmd" || ext == ".bat" {
		comspec := os.Getenv("ComSpec")
		if comspec == "" {
			comspec = `C:\Windows\System32\cmd.exe`
		}
		var b strings.Builder
		b.WriteString(windows.EscapeArg(comspec))
		b.WriteString(` /d /s /c "`)
		b.WriteString(windows.EscapeArg(exe))
		for _, a := range args {
			b.WriteByte(' ')
			b.WriteString(windows.EscapeArg(a))
		}
		b.WriteByte('"')
		cmd = exec.CommandContext(ctx, comspec)
		cmd.SysProcAttr = &syscall.SysProcAttr{
			HideWindow: true,
			CmdLine:    b.String(),
		}
	} else {
		cmd = exec.CommandContext(ctx, exe, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return outBuf.String(), errBuf.String(), err
}
