package localcommands

import (
	"context"
	"io"
)

// Optional capabilities are scoped to a single node execution. No input route
// is exposed in the current product. Unsupported backends do not implement the
// input interfaces, rather than silently accepting input they cannot deliver.
type InputMode string
type OutputMode string

const (
	InputNone      InputMode  = "none"
	InputText      InputMode  = "text"
	InputTerminal  InputMode  = "terminal"
	OutputStreams  OutputMode = "streams"
	OutputTerminal OutputMode = "terminal"
)

type InteractionCapabilities struct {
	Input  InputMode
	Output OutputMode
}

type InteractionDescriptor interface {
	Interaction() InteractionCapabilities
}

// InputExecution must bound queued bytes, honor cancellation, and keep input
// alive independently of viewers. Callers bind and deduplicate input against
// the exact node execution identity; reconnecting never replays user input.
type InputExecution interface {
	ExecutionLifecycle
	WriteInput(context.Context, []byte) (int, error)
	CloseInput() error
}

// TerminalExecution adds terminal-specific controls without adding no-op
// methods to ordinary script executions. It requires a terminal output reader.
type TerminalExecution interface {
	InputExecution
	TerminalOutput() io.ReadCloser
	ResizeTerminal(context.Context, int, int) error
}
