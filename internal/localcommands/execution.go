package localcommands

import (
	"context"
	"io"
	"time"
)

// ExecutionLifecycle owns the complete scope, including ordinary descendants.
// Wait completes only after that scope is empty. Close follows Wait and drain.
type ExecutionLifecycle interface {
	ForceStop() error
	Wait(context.Context) (ExitEvidence, error)
	Close() error
}

// Execution is the dual-stream output capability used by current scripts.
// Terminal capabilities can reuse lifecycle control without fake stderr APIs.
type Execution interface {
	ExecutionLifecycle
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
}

type ExitEvidence struct {
	Kind   string  `json:"kind"`
	Code   *uint32 `json:"code,omitempty"`
	Signal string  `json:"signal,omitempty"`
}

type ExecutionBackend interface {
	Start(ScriptSpec, string) (Execution, error)
	// Reconcile returns true only when the old scope is confirmed empty.
	// Evidence is opaque and must never be interpreted as a bare process ID.
	Reconcile(string) (bool, error)
	Available() bool
}

// ExecutionClock measures elapsed time including system sleep. Wake only asks
// the controller to recheck; it is never evidence that a deadline has elapsed.
type ExecutionClock interface {
	Now() time.Duration
	Wake(context.Context) <-chan time.Time
}

type platformClock struct{}

func (platformClock) Wake(ctx context.Context) <-chan time.Time {
	ch := make(chan time.Time, 1)
	go func() {
		defer close(ch)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-ticker.C:
				select {
				case ch <- t:
				default:
				}
			}
		}
	}()
	return ch
}
