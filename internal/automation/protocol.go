// Package automation owns the public operation catalog and transport-independent
// dispatch. Domain services remain responsible for their business rules.
package automation

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/operations"
)

const APIVersion = "1"
const MaxRequest = 1 << 20
const MaxResponse = 64 << 10
const MaxWait = 30 * time.Second

const (
	ToolsetCore    = "core"
	ToolsetGeneral = "general"
	ToolsetCollab  = "collab"
)

type Request struct {
	Version   string          `json:"api_version"`
	Node      string          `json:"node_id"`
	Operation string          `json:"operation"`
	ID        string          `json:"operation_id,omitempty"`
	Arguments json.RawMessage `json:"arguments"`
}

type Response struct {
	Version  string              `json:"api_version"`
	Node     string              `json:"node_id"`
	Instance string              `json:"instance_id"`
	ID       string              `json:"operation_id,omitempty"`
	Data     any                 `json:"data,omitempty"`
	Receipt  *operations.Receipt `json:"receipt,omitempty"`
	Error    *Error              `json:"error,omitempty"`
}

type Error struct {
	Code       string            `json:"code"`
	MessageKey string            `json:"message_key,omitempty"`
	Message    string            `json:"message"`
	Args       map[string]string `json:"args,omitempty"`
	Field      string            `json:"field,omitempty"`
}

func (e *Error) Error() string         { return e.Code + ": " + e.Message }
func Fail(code, message string) *Error { return &Error{Code: code, Message: message} }

func PublicError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	code := "operation_failed"
	for _, candidate := range []error{operations.ErrMissing, operations.ErrConflict, operations.ErrExpired, operations.ErrInvalid, operations.ErrCapacity} {
		if errors.Is(err, candidate) {
			code = candidate.Error()
		}
	}
	if errors.Is(err, context.Canceled) {
		code = "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		code = "wait_timeout"
	}
	if key, args, ok := i18n.ErrorDetails(err); ok {
		return &Error{Code: code, MessageKey: key, Args: args, Message: err.Error()}
	}
	// Raw OS/runtime errors may contain command text, credentials or local paths.
	return Fail(code, code)
}

type Principal struct {
	ID             string              `json:"id"`
	Name           string              `json:"name"`
	CallerID       string              `json:"caller_id,omitempty"`
	PolicyRevision string              `json:"policy_revision,omitempty"`
	Roles          []string            `json:"roles"`
	// Missing scopes mean no access; ["*"] explicitly grants all objects of a
	// family, including creation. IDs do not grant access to another family.
	Scopes map[string][]string `json:"scopes"`
	// Nil Toolsets preserves legacy unrestricted local/human access. A non-nil
	// map explicitly constrains allowed toolsets (core, general, collab).
	Toolsets map[string]bool `json:"toolsets,omitempty"`
}

func (p Principal) AllowsToolset(toolset string) bool {
	if p.Toolsets == nil {
		return true
	}
	if toolset == "" {
		toolset = ToolsetGeneral
	}
	if toolset == ToolsetCore {
		return p.Toolsets[ToolsetCore] || p.Toolsets[ToolsetGeneral] || p.Toolsets[ToolsetCollab]
	}
	return p.Toolsets[toolset]
}

func (p Principal) Role(role string) bool {
	if role == "" {
		return len(p.Roles) > 0
	}
	for _, have := range p.Roles {
		if have == role {
			return true
		}
	}
	return false
}

func (p Principal) Allows(family, id string) bool {
	for _, scope := range p.Scopes[family] {
		if scope == "*" || id != "" && scope == id {
			return true
		}
	}
	return false
}

func (p Principal) Require(family, id string) error {
	if !p.Allows(family, id) {
		return Fail("forbidden", "Object is outside the granted scope")
	}
	return nil
}

type Call struct {
	Principal Principal
	Identity  operations.Identity
	// Authorize fetches the latest Principal from the server for mid-flight or
	// pre-return re-checks (such as receipt filtering when a toolset is revoked).
	Authorize func() (Principal, error)
	// Check must also be used immediately before an irreversible acceptance
	// point and before returning data from a long observation.
	Check func() error
	// Run invokes a composed business operation with the same authenticated
	// subject and an extra, server-owned acceptance guard (for draft leases).
	Run func(context.Context, Request, func() error) Response
}

type Outcome struct {
	Data any
	// Summary is a compact reference, never a graph, prompt or message body.
	Summary  any
	Accepted bool
	Unknown  bool
}

type Definition struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Role         string         `json:"role"`
	Toolset      string         `json:"toolset,omitempty"`
	Write        bool           `json:"write"`
	Batch        bool           `json:"batch"`
	Control      bool           `json:"control,omitempty"`
	Wait         bool           `json:"wait,omitempty"`
	Large        bool           `json:"large,omitempty"`
	Schema       map[string]any `json:"input_schema"`
	OutputSchema map[string]any `json:"output_schema"`
	normalize    func(json.RawMessage) (any, error)
	target       func(any) string
	access       func(Principal, any) error
	invoke       func(context.Context, Call, any) (Outcome, error)
}

func (d Definition) EffectiveToolset() string {
	if d.Toolset == "" {
		return ToolsetGeneral
	}
	return d.Toolset
}
