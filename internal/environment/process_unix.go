//go:build !windows

package environment

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
