//go:build !windows

package hostbundle

import "os"

func openGuard(path string) (*os.File, error) { return os.Open(path) }
