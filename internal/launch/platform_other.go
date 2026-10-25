//go:build !windows

package launch

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if errors.Is(err, os.ErrNotExist) && filepath.Dir(abs) != abs {
		parent, parentErr := canonicalPath(filepath.Dir(abs))
		return filepath.Join(parent, filepath.Base(abs)), parentErr
	}
	return resolved, err
}

func acquireLaunchLock(ctx context.Context, home string) (func(), error) {
	path := filepath.Join(os.TempDir(), fmt.Sprintf("asterhand-launch-%d-%x.lock", os.Getuid(), sha256.Sum256([]byte(home))))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func serviceBusy(home string) (bool, error) {
	f, err := os.OpenFile(filepath.Join(home, "service.lock"), os.O_RDWR, 0600)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return true, nil
		}
		return false, err
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false, nil
}

func inspectProcess(pid int) (processRecord, error) {
	p, err := os.FindProcess(pid)
	if err != nil {
		return processRecord{}, err
	}
	defer p.Release()
	if err = p.Signal(syscall.Signal(0)); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return processRecord{}, os.ErrProcessDone
		}
		return processRecord{}, err
	}
	return processRecord{PID: pid}, nil
}

func configureChild(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
