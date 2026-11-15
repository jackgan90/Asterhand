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

func (stubBackend) Supported() bool                              { return false }
func (stubBackend) Prepare(_, _ string) error                    { return fail("unsupported_platform") }
func (stubBackend) Start(context.Context, SpawnSpec) (SessionProcess, error) {
	return nil, fail("unsupported_platform")
}
func (stubBackend) CleanupTemp(_ string) error { return nil }

// ReinheritHandleDACL is a no-op outside Windows NTFS ACLs.
func ReinheritHandleDACL(_ *os.File) error { return nil }

func evalRealPath(p string) (string, error) {
	return filepath.EvalSymlinks(p)
}
