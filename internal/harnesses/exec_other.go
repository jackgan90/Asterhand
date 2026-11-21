//go:build !windows

package harnesses

import (
	"bytes"
	"context"
	"os/exec"
)

func defaultCommandRunner(ctx context.Context, exe string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, exe, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return outBuf.String(), errBuf.String(), err
}
