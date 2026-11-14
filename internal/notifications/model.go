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

type Snapshot struct {
	Revision uint64 `json:"revision"`
	Cards    []Card `json:"cards"`
	Theme    string `json:"theme"`
	Locale   string `json:"locale"`
}

type Event struct {
	Kind       string `json:"kind"`
	ID         uint64 `json:"id,omitempty"`
	Paused     bool   `json:"paused,omitempty"`
	Capacity   int    `json:"capacity,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
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
