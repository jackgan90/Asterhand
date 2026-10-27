package conversation

import (
	"context"
	"github.com/jackgan90/Asterhand/internal/prompt"
	"time"
)

// Binding is private to the owner node. Native session/lifecycle identities are
// immutable after creation, even if defaults or process incarnations change.
type Binding struct {
	Runtime    string `json:"runtime"`
	Generation string `json:"generation"`
	Key        string `json:"key"`
	Session    string `json:"session"`
	Lifecycle  string `json:"lifecycle"`
}

type Submission struct {
	ID        string
	Text      string
	StartedAt time.Time
}

type Receipt struct {
	Prompt      *prompt.Application `json:"prompt,omitempty"`
	Accepted    bool                `json:"accepted"`
	UserMessage string              `json:"user_message"`
}

// Stopped means the adapter proved that the original run cannot still execute
// or resume. A query timeout alone must never set it.
type Observation struct {
	Prompt           *prompt.Application `json:"prompt,omitempty"`
	Accepted         bool                `json:"accepted"`
	UserMessage      string              `json:"user_message"`
	AssistantMessage string              `json:"assistant_message"`
	Stopped          bool                `json:"stopped"`
	Run              string              `json:"run"`
	Saved            bool                `json:"saved"`
	Live             *Message            `json:"live"`
}

type Runtime interface {
	Describe() (Descriptor, error)
	Create(context.Context, Binding) (Binding, error)
	Submit(context.Context, Binding, Submission) (Receipt, error)
	Observe(context.Context, Binding, string, time.Time) (Observation, error)
	History(context.Context, Binding, int, string) (Page, error)
	Message(context.Context, Binding, string, int) (Fragment, error)
	Cancel(context.Context, Binding, string) error
	Close() error
}
