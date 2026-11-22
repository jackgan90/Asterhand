// Package notifications owns transient completion reminders, independently of
// operating systems, window lifetimes and command execution.
package notifications

import (
	"context"
	"time"
)

const (
	MaxVisible      = 3
	MaxPending      = 100
	DefaultDuration = 3 * time.Second
	MaxAge          = 5 * time.Minute
	AdmissionGap    = 300 * time.Millisecond
)

type Completion struct {
	CommandID string    `json:"command_id,omitempty"`
	RunID     string    `json:"run_id,omitempty"`
	Name      string    `json:"name,omitempty"`
	Result    string    `json:"result,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	Started   time.Time `json:"started_at"`
	Ended     time.Time `json:"ended_at"`
	Elapsed   float64   `json:"elapsed_seconds"`
}

type Card struct {
	Completion
	ID        uint64 `json:"id"`
	Phase     string `json:"phase"`
	Count     uint64 `json:"count,omitempty"`
	Failed    uint64 `json:"failed,omitempty"`
	ErrorKey  string `json:"error_key,omitempty"`
	Opening   bool   `json:"opening,omitempty"`
	remaining time.Duration
	exited    bool
}

type WorkerOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type WorkerQuestion struct {
	ID       string         `json:"id"`
	Header   string         `json:"header,omitempty"`
	Question string         `json:"question"`
	Multi    bool           `json:"multi_select,omitempty"`
	Options  []WorkerOption `json:"options,omitempty"`
}

type WorkerInteraction struct {
	RequestID        string           `json:"request_id"`
	Kind             string           `json:"kind"` // approval | input
	WorkerID         string           `json:"worker_id"`
	WorkerAlias      string           `json:"worker_alias,omitempty"`
	WorkspaceRoot    string           `json:"workspace_root,omitempty"`
	HarnessID        string           `json:"harness_id"`
	TurnID           string           `json:"turn_id"`
	ToolName         string           `json:"tool_name,omitempty"`
	Summary          string           `json:"summary"`
	Command          string           `json:"command,omitempty"`
	Paths            []string         `json:"paths,omitempty"`
	Reason           string           `json:"reason,omitempty"`
	RiskLevel        string           `json:"risk_level,omitempty"`
	HardRedline      bool             `json:"hard_redline,omitempty"`
	OutsideWorkspace bool             `json:"outside_workspace,omitempty"`
	AllowSessionOK   bool             `json:"allow_session_ok,omitempty"`
	Questions        []WorkerQuestion `json:"questions,omitempty"`
}

type WorkerActionEvent struct {
	RequestID string              `json:"request_id"`
	Decision  string              `json:"decision"`
	Text      string              `json:"text,omitempty"`
	Answers   map[string][]string `json:"answers,omitempty"`
}

type Snapshot struct {
	Revision     uint64              `json:"revision"`
	Cards        []Card              `json:"cards"`
	Interactions []WorkerInteraction `json:"interactions,omitempty"`
	Theme        string              `json:"theme"`
	Locale       string              `json:"locale"`
}

type Event struct {
	Kind         string             `json:"kind"`
	ID           uint64             `json:"id,omitempty"`
	Paused       bool               `json:"paused,omitempty"`
	Capacity     int                `json:"capacity,omitempty"`
	DurationMS   int64              `json:"duration_ms,omitempty"`
	WorkerAction *WorkerActionEvent `json:"worker_action,omitempty"`
}

// Presenter applies idempotent snapshots and emits acknowledgements/intent.
// It must bound its own IPC and native resources. It never launches commands.
type Presenter interface {
	Apply(context.Context, Snapshot) error
	Events() <-chan Event
	Close() error
}

type Status struct {
	State   string `json:"state"`
	Pending int    `json:"pending"`
	Visible int    `json:"visible"`
}
