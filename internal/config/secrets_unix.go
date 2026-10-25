//go:build !windows

package config

import "os"

// Linux uses a mode-0600 file in a mode-0700 application directory; no encryption claim.
func protect(b []byte) ([]byte, error)   { return b, nil }
func unprotect(b []byte) ([]byte, error) { return b, nil }
func privateDirectory(path string) error { return os.Chmod(path, 0700) }
