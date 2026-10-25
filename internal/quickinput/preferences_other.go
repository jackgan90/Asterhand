//go:build !windows

package quickinput

import "os"

func replacePreferences(from, to string) error { return os.Rename(from, to) }
