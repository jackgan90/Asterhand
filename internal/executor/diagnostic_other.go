//go:build !windows

package executor

import "os/exec"

func hideDiagnostic(cmd *exec.Cmd) {}
