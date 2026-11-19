//go:build !windows

package sandbox

import (
	"context"
	"os"
	"path/filepath"
)

type stubBackend struct{}

// NewBackend returns a non-Windows platform stub reporting unsupported until
// Seatbelt (macOS) and Bubblewrap (Linux) backends are implemented in Stage 2.
func NewBackend() Backend { return stubBackend{} }

func (stubBackend) Supported() bool           { return false }
func (stubBackend) Prepare(_, _ string) error { return fail("unsupported_platform") }
func (stubBackend) Start(context.Context, SpawnSpec) (SessionProcess, error) {
	return nil, fail("unsupported_platform")
}
func (stubBackend) CleanupTemp(_ string) error { return nil }

// ReinheritHandleDACL is a no-op outside Windows NTFS ACLs.
func ReinheritHandleDACL(_ *os.File) error { return nil }

// OpenSharedReadLocked opens a regular non-symlink file and returns its canonical path and metadata.
func OpenSharedReadLocked(path string) (*os.File, string, int64, int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", 0, 0, os.ErrNotExist
		}
		return nil, "", 0, 0, fail("invalid_path")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, "", 0, 0, fail("unsafe_links_detected")
	}
	canon, err := evalRealPath(path)
	if err != nil {
		return nil, "", 0, 0, fail("unsafe_links_detected")
	}
	f, err := os.Open(canon)
	if err != nil {
		return nil, "", 0, 0, fail("io_error")
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, "", 0, 0, fail("io_error")
	}
	return f, canon, st.Size(), st.ModTime().UnixNano(), nil
}

func evalRealPath(p string) (string, error) {
	return filepath.EvalSymlinks(p)
}
