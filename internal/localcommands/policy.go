package localcommands

import (
	"path/filepath"
	"strings"
)

// CommandPolicy owns interpreter and path rules, separately from execution support.
type CommandPolicy interface {
	ScriptPath(string) (string, error)
	LocalPath(string) (string, error)
	EqualPath(string, string) bool
	ValidArgument(string, string) bool
}

var commandPolicy CommandPolicy = windowsCommandPolicy{}

type windowsCommandPolicy struct{}

func (windowsCommandPolicy) EqualPath(a, b string) bool { return strings.EqualFold(a, b) }
func (windowsCommandPolicy) ValidArgument(script, arg string) bool {
	return strings.EqualFold(filepath.Ext(script), ".ps1") || !strings.ContainsRune(arg, 34)
}

// ScriptPath applies the same path/type boundary to manual and picked paths.
func (windowsCommandPolicy) ScriptPath(value string) (string, error) {
	value, err := commandPolicy.LocalPath(value)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(filepath.Ext(value)) {
	case ".bat", ".cmd", ".ps1":
		return value, nil
	default:
		return "", problem("invalid", "err.localcmd.unsupported_ext")
	}
}

func (windowsCommandPolicy) LocalPath(value string) (string, error) {
	if !cleanText(value, 2048) || strings.TrimSpace(value) != value || !filepath.IsAbs(value) || strings.HasPrefix(value, `\\`) || strings.HasPrefix(value, "//") {
		return "", problem("invalid", "err.localcmd.local_abs_path_only")
	}
	value = filepath.Clean(value)
	// A colon outside the volume could select an NTFS stream, not a script file.
	if strings.Contains(strings.TrimPrefix(value, filepath.VolumeName(value)), ":") {
		return "", problem("invalid", "err.localcmd.no_ads")
	}
	return value, nil
}

func executionMode(c Command) string {
	if c.ExecutionMode == "" {
		return "unmanaged"
	}
	return c.ExecutionMode
}
func timeoutEqual(a, b *int64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
