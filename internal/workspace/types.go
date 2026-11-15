// Package workspace owns the bounded local assistant filesystem, versions and
// operation receipts. Native adapters and public business APIs share this owner.
package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jackgan90/Asterhand/internal/platform/sandbox"
)

const (
	MaxFileBytes      = 2 << 20
	MaxBatchBytes     = 16 << 20
	MaxTargets        = 32
	MaxWorkspaceBytes = 256 << 20
	MaxWorkspaceFiles = 10000
	MaxVersionBytes   = 1 << 30
	MaxActivityBytes  = 64 << 20
	MaxCalls          = 100
	MaxOperationBytes = 8 << 10
	MaxSummaryRunes   = 160
	MaxSummaryBytes   = 240
	PageBytes         = 32 << 10
	PageItems         = 100
	MissingRevision   = "missing"

	MaxActiveCalls    = 8
	MaxModifyingCalls = 4
	MaxIndexCalls     = 1
	MaxQueuedCalls    = 64
	DefaultRoot       = "workspace"

	MaxExecSessionsPerTurn = 8
	DefaultExecYieldMs     = 10000
	MaxExecYieldMs         = 30000
	MaxExecHttpPollMs      = 2500
	DefaultExecTimeoutSec  = 120
	MaxExecTimeoutSec      = 600
	MaxExecPollBytes       = 64 << 10
	MaxExecSessionBytes    = 256 << 10
	MaxSandboxTempBytes    = 256 << 20
	MaxSandboxTempFiles    = 10000
)

type Error struct {
	Code string `json:"code"`
}

func (e *Error) Error() string { return "workspace: " + e.Code }
func (e *Error) I18nKey() (string, map[string]string) {
	return "err.workspace.operation", map[string]string{"code": e.Code}
}
func fail(code string) error { return &Error{Code: code} }
func Code(err error) string {
	if os.IsNotExist(err) {
		return "not_found"
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	var se *sandbox.Error
	if errors.As(err, &se) && se.Code != "" {
		return se.Code
	}
	return "io_error"
}
func revision(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// Scope is supplied by a trusted caller, never inferred from model text.
type Scope struct {
	Conversation string `json:"conversation_id,omitempty"`
	Turn         string `json:"turn_id,omitempty"`
	Policy       string `json:"policy_revision"`
}
type Target struct {
	Path     string `json:"path"`
	Expected string `json:"expected_revision,omitempty"`
	Remove   bool   `json:"remove,omitempty"`
}
type Begin struct {
	ID          string   `json:"id"`
	Scope       Scope    `json:"scope"`
	Tool        string   `json:"tool"`
	Summary     string   `json:"summary,omitempty"`
	Targets     []Target `json:"targets"`
	Path        string   `json:"path,omitempty"`
	NewBytes    int64    `json:"new_bytes"`
	Fingerprint string   `json:"fingerprint"`
	Progress    []Step   `json:"progress,omitempty"`
}

// Register records a call during native batch preparation without waiting on locks.
type Register struct {
	ID               string   `json:"id"`
	Batch            string   `json:"batch"`
	Order            int      `json:"order"`
	Scope            Scope    `json:"scope"`
	Tool             string   `json:"tool"`
	Summary          string   `json:"summary,omitempty"`
	InputFingerprint string   `json:"input_fingerprint"`
	Targets          []Target `json:"targets,omitempty"`
	Path             string   `json:"path,omitempty"`
	NewBytes         int64    `json:"new_bytes,omitempty"`
	Progress         []Step   `json:"progress,omitempty"`
}

type SealItem struct {
	ID          string   `json:"id"`
	Tool        string   `json:"tool"`
	Summary     string   `json:"summary,omitempty"`
	Targets     []Target `json:"targets,omitempty"`
	Path        string   `json:"path,omitempty"`
	NewBytes    int64    `json:"new_bytes,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	Progress    []Step   `json:"progress,omitempty"`
	Aborted     bool     `json:"aborted,omitempty"`
	Reason      string   `json:"reason,omitempty"`
}

type SealBatch struct {
	Batch string     `json:"batch"`
	Scope Scope      `json:"scope"`
	Items []SealItem `json:"items"`
}

// Step is model-declared progress, separate from observed file changes.
type Step struct {
	Text   string `json:"step"`
	Status string `json:"status"`
}
type Change struct {
	Path       string `json:"path"`
	Before     string `json:"before_revision"`
	After      string `json:"after_revision"`
	State      string `json:"state"`
	Restorable bool   `json:"restorable"`
	// Content belongs only in the protected journal, never in activity output.
	Content []byte `json:"content,omitempty"`
}
type Operation struct {
	ID          string    `json:"id"`
	Scope       Scope     `json:"scope"`
	Tool        string    `json:"tool"`
	Summary     string    `json:"summary,omitempty"`
	Fingerprint string    `json:"fingerprint"`
	Sequence    int64     `json:"sequence"`
	Started     time.Time `json:"started"`
	Finished    time.Time `json:"finished,omitempty"`
	State       string    `json:"state"`
	Phase       string    `json:"phase,omitempty"`
	WaitReason  string    `json:"wait_reason,omitempty"`
	Changes     []Change  `json:"changes"`
	Detail      string    `json:"detail,omitempty"`
	Progress    []Step    `json:"progress,omitempty"`
	Resolution  string    `json:"resolution_id,omitempty"`
	// These parents were absent at admission and may remain after partial
	// completion. File restore never recursively removes directories.
	ParentDirectories []string `json:"new_parent_directories,omitempty"`
}
type Entry struct {
	Path     string `json:"path"`
	Type     string `json:"type"`
	Size     int64  `json:"size"`
	Modified int64  `json:"mtimeMs"`
	Revision string `json:"revision,omitempty"`
}
type Fragment struct {
	Path     string `json:"path"`
	Revision string `json:"revision"`
	Data     []byte `json:"data"`
	Next     int64  `json:"next"`
	More     bool   `json:"has_more"`
}
type Page struct {
	Items     []Entry `json:"items"`
	Next      string  `json:"next"`
	Truncated bool    `json:"truncated"`
}
type Mutation struct {
	Action              string `json:"action"`
	Path                string `json:"path"`
	Destination         string `json:"destination,omitempty"`
	Expected            string `json:"expected_revision"`
	DestinationExpected string `json:"destination_revision,omitempty"`
	Content             string `json:"content,omitempty"`
}
type Artifact struct {
	ID        string `json:"id"`
	Operation string `json:"operation_id"`
	Path      string `json:"path"`
	Revision  string `json:"revision"`
	Scope     Scope  `json:"scope"`
}

type Status struct {
	ID           string           `json:"workspace_id"`
	Supported    bool             `json:"supported"`
	Authorized   bool             `json:"authorized"`
	Ready        bool             `json:"ready"`
	Policy       string           `json:"policy_revision"`
	Reason       string           `json:"unavailable_reason,omitempty"`
	Busy         bool             `json:"busy"`
	Enforcement  string           `json:"enforcement"`
	Capabilities []string         `json:"capabilities"`
	Limits       map[string]int64 `json:"limits"`
}

// TraceEvent records bounded, non-secret low-level lifecycle and I/O checkpoints.
type TraceEvent struct {
	Seq    int64  `json:"seq"`
	CallID string `json:"call_id"`
	Tool   string `json:"tool"`
	Stage  string `json:"stage"`
	Effect string `json:"effect,omitempty"`
	Root   string `json:"root,omitempty"`
	Key    string `json:"key,omitempty"`
	Access string `json:"access,omitempty"`
}

func publicOperation(op Operation) Operation {
	op.Changes = append([]Change(nil), op.Changes...)
	for i := range op.Changes {
		op.Changes[i].Content = nil
	}
	return op
}
func artifactID(op string, index int) string { return fmt.Sprintf("%s.%d", op, index) }

// ExecStartRequest starts a sandboxed script session owned by an active exec_command call.
type ExecStartRequest struct {
	CallID     string            `json:"id"`
	Command    string            `json:"command"`
	Workdir    string            `json:"workdir,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	YieldMs    *int              `json:"yield_ms,omitempty"`
	TimeoutSec int               `json:"timeout_sec,omitempty"`
}

// ExecPollRequest polls output/status for a background or finished script session.
type ExecPollRequest struct {
	CallID    string `json:"id"`
	SessionID string `json:"session_id"`
	Offset    *int64 `json:"offset,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	WaitMs    *int   `json:"wait_ms,omitempty"`
}

// ExecControlRequest queries status, logs, or lists sessions for process_control.
type ExecControlRequest struct {
	CallID    string `json:"id"`
	Action    string `json:"subaction"`
	SessionID string `json:"session_id,omitempty"`
	Offset    int64  `json:"offset"`
	Limit     int    `json:"limit,omitempty"`
}

// ExecKillRequest terminates either a session by session_id or the foreground session for call id.
type ExecKillRequest struct {
	CallID    string `json:"id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Reason    string `json:"detail,omitempty"`
}

// ExecSessionSummary describes public status of one script execution session.
type ExecSessionSummary struct {
	SessionID  string `json:"session_id"`
	Status     string `json:"status"`
	ExitCode   int    `json:"exit_code,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	TotalBytes int64  `json:"total_bytes"`
	Truncated  bool   `json:"truncated,omitempty"`
	TLSHint    string `json:"tls_hint,omitempty"`
	Warning    string `json:"warning,omitempty"`
}

// ExecResult is returned to the OpenClaw workspace plugin for exec_command and process_control.
type ExecResult struct {
	Status     string               `json:"status"`
	SessionID  string               `json:"session_id,omitempty"`
	Output     string               `json:"output,omitempty"`
	NextOffset int64                `json:"next_offset,omitempty"`
	TotalBytes int64                `json:"total_bytes,omitempty"`
	Truncated  bool                 `json:"truncated,omitempty"`
	ExitCode   int                  `json:"exit_code,omitempty"`
	DurationMs int64                `json:"duration_ms,omitempty"`
	Reason     string               `json:"reason,omitempty"`
	TLSHint    string               `json:"tls_hint,omitempty"`
	Warning    string               `json:"warning,omitempty"`
	Sessions   []ExecSessionSummary `json:"sessions,omitempty"`
}
