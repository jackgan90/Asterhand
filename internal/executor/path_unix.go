//go:build !windows

package executor

import "path/filepath"

func resolveExistingPath(path string) (string, error) { return filepath.EvalSymlinks(path) }
