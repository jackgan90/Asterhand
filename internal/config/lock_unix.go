//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"syscall"
)

func Lock(home string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(home, "service.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
