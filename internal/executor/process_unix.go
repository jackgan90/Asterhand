//go:build !windows

package executor

import (
	"os/exec"
	"sync"
	"syscall"
)

func startManaged(cmd *exec.Cmd) (func(), error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }) }, nil
}
