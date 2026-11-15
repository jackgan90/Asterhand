// Package sandbox provides the cross-platform restricted script execution backend
// boundary, environment scrubbing, protected-path guards, and bounded multi-encoding
// output buffering.
package sandbox

import (
	"context"
	"errors"
	"strings"
	"time"
)

const (
	MaxOutputPollBytes    = 64 << 10
	MaxOutputSessionBytes = 256 << 10
	PipeDrainTimeout      = 1500 * time.Millisecond
)

// Error represents a machine-readable sandbox failure code safe for i18n mapping.
type Error struct {
	Code string `json:"code"`
}

func (e *Error) Error() string { return "sandbox: " + e.Code }

// I18nKey exposes the shared workspace operation error template key.
func (e *Error) I18nKey() (string, map[string]string) {
	return "err.workspace.operation", map[string]string{"code": e.Code}
}

func fail(code string) error { return &Error{Code: code} }

// Code extracts the structured machine error code from err, defaulting to io_error.
func Code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "io_error"
}

// ProtectedDir identifies a sensitive directory using an abstract non-host-leaking label.
type ProtectedDir struct {
	Label string
	Path  string
}

// SpawnSpec defines parameters to launch one sandboxed command session.
type SpawnSpec struct {
	WorkspaceRoot  string
	TempRoot       string
	HomeRoot       string
	Command        string
	Workdir        string
	Env            map[string]string
	BundledNodeDir string
	Timeout        time.Duration
	ProtectedDirs  []ProtectedDir
}

// OutputSlice holds a bounded UTF-8 window read from session output.
type OutputSlice struct {
	Output     string
	NextOffset int64
	TotalBytes int64
	Truncated  bool
}

// ExitInfo summarizes the terminal state of a sandboxed command session.
type ExitInfo struct {
	Exited     bool
	ExitCode   int
	Reason     string
	DurationMs int64
	TLSHint    string
}

// SessionProcess represents a running or finished OS-isolated command tree.
type SessionProcess interface {
	OutputSnapshot(offset int64, limit int) OutputSlice
	Done() <-chan struct{}
	ExitInfo() ExitInfo
	Terminate(reason string)
	ProcessesStopped() (bool, error)
	Close() error
}

// Backend abstracts OS-level sandbox preparation, execution, and cleanup.
type Backend interface {
	Supported() bool
	Prepare(workspaceRoot, tempRoot string) error
	Start(ctx context.Context, spec SpawnSpec) (SessionProcess, error)
	CleanupTemp(tempRoot string) error
}

// DetectWindowsTLSHint inspects non-zero script failures for restricted Windows
// Schannel/WinHTTP SSL credential failures and returns a structured hint key.
func DetectWindowsTLSHint(exitCode int, output string) string {
	if exitCode == 0 || output == "" {
		return ""
	}
	lower := strings.ToLower(output)
	for _, needle := range []string{
		"0x8009030e",
		"sec_e_no_credentials",
		"the underlying connection was closed",
		"authenticationexception",
		"winhttpexception",
	} {
		if strings.Contains(lower, needle) {
			return "windows_schannel_restricted"
		}
	}
	return ""
}
