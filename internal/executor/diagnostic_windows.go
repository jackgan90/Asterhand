//go:build windows

package executor

import (
	"os/exec"
	"syscall"
)

func hideDiagnostic(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }
