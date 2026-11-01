// Package wire defines the closed conversation client protocol. It has no node,
// database, runtime, credential, filesystem, or browser dependencies.
package wire

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/prompt"
)

const (
	DefaultRole   = "default"
	MaxInputBytes = 16000
	PageBytes     = 64 * 1024
	FragmentBytes = 8 * 1024
	TurnTimeout   = 10 * time.Minute
)

type Target struct {
	Node string `json:"node_id"`
	Role string `json:"role_id"`
}

type Capabilities struct {
	History         bool `json:"history"`
	Streaming       bool `json:"streaming"`
	Cancellation    bool `json:"cancellation"`
	DurableReceipts bool `json:"durable_receipts"`
}

// Descriptor describes the currently configured logical runtime, not a process.
// Binding changes only when the runtime/storage identity changes.
type Descriptor struct {
	ID            string
	Binding       string
	Provider      string
	Model         string
	CanSend       bool
	SendError     string
	SendErrorKey  string
	SendErrorArgs map[string]string
	Capabilities  Capabilities
	Prompt        prompt.Application
}

type Conversation struct {
	ID           string    `json:"id"`
	Target       Target    `json:"target"`
	RoleRevision string    `json:"role_revision"`
	Runtime      string    `json:"runtime"`
	State        string    `json:"state"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Turn separates custody, execution, and assistant persistence. A disconnected
// client must not infer any one of these facts from the other two.
type Turn struct {
	Prompt           prompt.Application `json:"prompt"`
	ID               string             `json:"id"`
	Operation        string             `json:"operation_id"`
	Conversation     string             `json:"conversation_id"`
	Delivery         string             `json:"delivery"`
	Run              string             `json:"run"`
	Persistence      string             `json:"persistence"`
	UserMessage      string             `json:"user_message_id,omitempty"`
	AssistantMessage string             `json:"assistant_message_id,omitempty"`
	Provider         string             `json:"provider"`
	Model            string             `json:"model"`
	StartedAt        time.Time          `json:"started_at"`
	UpdatedAt        time.Time          `json:"updated_at"`
}

type Block struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type Message struct {
	ID        string  `json:"id"`
	Role      string  `json:"role"`
	Turn      string  `json:"turn_id,omitempty"`
	Timestamp string  `json:"timestamp,omitempty"`
	Blocks    []Block `json:"blocks"`
	Truncated bool    `json:"truncated,omitempty"`
	Transient bool    `json:"transient,omitempty"`
}

type Page struct {
	Messages []Message `json:"messages"`
	Next     int       `json:"next_offset,omitempty"`
	HasMore  bool      `json:"has_more"`
	Cursor   string    `json:"cursor"`
	Reset    bool      `json:"reset,omitempty"`
}

type Fragment struct {
	Message string `json:"message_id"`
	Text    string `json:"text"`
	Offset  int    `json:"offset"`
	Next    int    `json:"next_offset"`
	HasMore bool   `json:"has_more"`
}

// Request is a closed semantic protocol shared by local and future transports.
// It contains no native RPC name, URL, credential, or filesystem path.
type Request struct {
	Action        string `json:"action"`
	Target        Target `json:"target"`
	Conversation  string `json:"conversation_id,omitempty"`
	Operation     string `json:"operation_id,omitempty"`
	Text          string `json:"text,omitempty"`
	Message       string `json:"message_id,omitempty"`
	Offset        int    `json:"offset,omitempty"`
	Cursor        string `json:"cursor,omitempty"`
	DraftRevision uint64 `json:"draft_revision,omitempty"`
}

type Response struct {
	Connection         string             `json:"connection,omitempty"`
	Queue              *QueueState        `json:"queue,omitempty"`
	QueueItem          *QueueItem         `json:"queue_item,omitempty"`
	Prompt             prompt.Application `json:"prompt"`
	Target             Target             `json:"target"`
	Capabilities       Capabilities       `json:"capabilities"`
	CanSend            bool               `json:"can_send"`
	Busy               bool               `json:"busy"`
	ActiveConversation string             `json:"active_conversation,omitempty"`
	Conversations      []Conversation     `json:"conversations,omitempty"`
	Conversation       *Conversation      `json:"conversation,omitempty"`
	Turn               *Turn              `json:"turn,omitempty"`
	Live               *Message           `json:"live,omitempty"`
	Page               *Page              `json:"page,omitempty"`
	Fragment           *Fragment          `json:"fragment,omitempty"`
	Error              *Error             `json:"error,omitempty"`
}

type Error struct {
	Code        string            `json:"code"`
	Message     string            `json:"message"`
	MessageKey  string            `json:"message_key,omitempty"`
	MessageArgs map[string]string `json:"message_args,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.MessageKey != "" {
		return i18n.LocalizeMessage(i18n.Effective(), e.MessageKey, e.MessageArgs, e.Message)
	}
	return e.Message
}

func (e *Error) I18nKey() (string, map[string]string) {
	if e == nil {
		return "", nil
	}
	return e.MessageKey, i18n.CloneArgs(e.MessageArgs)
}

func Fail(code, message string) error {
	if i18n.HasKey(message) {
		return FailKey(code, message)
	}
	return &Error{Code: code, Message: message}
}

func FailKey(code, key string, args ...map[string]string) error {
	var params map[string]string
	if len(args) > 0 {
		params = i18n.CloneArgs(args[0])
	}
	return &Error{
		Code:        code,
		Message:     i18n.T(i18n.Effective(), key, params),
		MessageKey:  key,
		MessageArgs: params,
	}
}

func PublicError(err error) *Error {
	var known *Error
	if errors.As(err, &known) {
		out := *known
		out.Message = out.Error()
		out.MessageArgs = i18n.CloneArgs(known.MessageArgs)
		return &out
	}
	if key, args, ok := i18n.ErrorDetails(err); ok {
		return &Error{
			Code:        "unavailable",
			Message:     i18n.LocalizeMessage(i18n.Effective(), key, args, err.Error()),
			MessageKey:  key,
			MessageArgs: args,
		}
	}
	return &Error{
		Code:       "unavailable",
		Message:    i18n.T(i18n.Effective(), "err.conv.wire.unavailable"),
		MessageKey: "err.conv.wire.unavailable",
	}
}

func ValidID(id string) bool {
	if len(id) < 16 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (r Request) Validate() error {
	if len(r.Cursor) > 2048 || r.Offset < 0 || r.Offset > 32*1024*1024 {
		return FailKey("invalid_request", "err.conv.wire.invalid_page")
	}
	if r.Action == "hello" {
		if r.Target != (Target{}) {
			return FailKey("invalid_request", "err.conv.wire.hello_has_target")
		}
	} else if !ValidID(r.Target.Node) || r.Target.Role != DefaultRole {
		return FailKey("invalid_target", "err.conv.wire.invalid_target")
	}
	needsConversation := r.Action == "select" || r.Action == "history" || r.Action == "message" || r.Action == "submit" || r.Action == "enqueue" || r.Action == "queue_item" || r.Action == "reconcile" || r.Action == "cancel"
	needsOperation := r.Action == "create" || r.Action == "prepare" || r.Action == "submit" || r.Action == "enqueue" || r.Action == "queue_item"
	if (needsConversation && !ValidID(r.Conversation)) || (!needsConversation && r.Conversation != "") ||
		(needsOperation && !ValidID(r.Operation)) || (r.Operation != "" && !ValidID(r.Operation)) {
		return FailKey("invalid_request", "err.conv.wire.invalid_ids")
	}
	if r.Action != "submit" && r.Action != "enqueue" && (r.Text != "" || r.DraftRevision != 0) || r.Action != "message" && r.Message != "" ||
		r.Action != "history" && r.Action != "message" && r.Action != "list" && r.Offset != 0 || r.Action != "history" && r.Cursor != "" {
		return FailKey("invalid_request", "err.conv.wire.extra_fields")
	}
	if r.Operation != "" && !needsOperation && r.Action != "reconcile" && r.Action != "cancel" {
		return FailKey("invalid_request", "err.conv.wire.extra_op_id")
	}
	switch r.Action {
	case "hello", "list", "connect", "connection", "select", "create", "prepare", "history", "reconcile", "cancel", "queue", "queue_item":
	case "message":
		if !ValidID(r.Message) {
			return FailKey("invalid_request", "err.conv.wire.invalid_message_id")
		}
	case "submit", "enqueue":
		if strings.TrimSpace(r.Text) == "" || len(r.Text) > MaxInputBytes || !utf8.ValidString(r.Text) || strings.ContainsRune(r.Text, '\r') {
			return FailKey("invalid_text", "err.conv.wire.invalid_text")
		}
		if strings.HasPrefix(strings.TrimSpace(r.Text), "/") {
			return FailKey("command_not_allowed", "err.conv.wire.slash_not_allowed")
		}
	default:
		return FailKey("unsupported", "err.conv.unsupported_action")
	}
	return nil
}

type Transport interface {
	Call(context.Context, Request) (Response, error)
}

// Client fixes a target independently of transport selection. Retrying a call
// never chooses another node or role.
type Client struct {
	Target    Target
	Transport Transport
}

func (c Client) Call(ctx context.Context, r Request) (Response, error) {
	if r.Action != "hello" && r.Target != c.Target {
		return Response{}, FailKey("wrong_target", "err.conv.wire.wrong_target")
	}
	return c.Transport.Call(ctx, r)
}
