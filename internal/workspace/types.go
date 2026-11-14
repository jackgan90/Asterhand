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
	PageBytes         = 32 << 10
	PageItems         = 100
	MissingRevision   = "missing"
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
	Targets     []Target `json:"targets"`
	NewBytes    int64    `json:"new_bytes"`
	Fingerprint string   `json:"fingerprint"`
	Progress    []Step   `json:"progress,omitempty"`
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
	Fingerprint string    `json:"fingerprint"`
	Sequence    int64     `json:"sequence"`
	Started     time.Time `json:"started"`
	Finished    time.Time `json:"finished,omitempty"`
	State       string    `json:"state"`
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

func publicOperation(op Operation) Operation {
	op.Changes = append([]Change(nil), op.Changes...)
	for i := range op.Changes {
		op.Changes[i].Content = nil
	}
	return op
}
func artifactID(op string, index int) string { return fmt.Sprintf("%s.%d", op, index) }
