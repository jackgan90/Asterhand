// Package wire contains the closed local-command capability of the input host.
package wire

import (
	"encoding/json"
	"regexp"

	"github.com/jackgan90/Asterhand/internal/i18n"
)

var identifier = regexp.MustCompile(`^[a-zA-Z0-9_-]{16,64}$`)

func ValidID(id string) bool { return identifier.MatchString(id) }

type Request struct {
	Action   string `json:"action"`
	ID       string `json:"command_id,omitempty"`
	Revision string `json:"revision,omitempty"`
	Offset   int    `json:"offset,omitempty"`
	Catalog  string `json:"catalog,omitempty"`
	Sequence uint64 `json:"sequence,omitempty"`
	RunID    string `json:"run_id,omitempty"`
	Cursor   int64  `json:"cursor,omitempty"`
}

func (r Request) Validate() error {
	valid := false
	switch r.Action {
	case "list":
		valid = r.ID == "" && r.Revision == "" && r.Sequence == 0 && r.Offset >= 0 && r.Offset <= 10000 && (r.Catalog == "" || ValidID(r.Catalog))
	case "launch":
		valid = ValidID(r.ID) && ValidID(r.Revision) && r.Sequence > 0 && r.Offset == 0 && r.Catalog == ""
	case "settings":
		valid = (r.ID == "" || ValidID(r.ID)) && r.Revision == "" && r.Sequence == 0 && r.Offset == 0 && r.Catalog == ""
	case "status", "logs":
		valid = ValidID(r.ID) && r.Revision == "" && r.Sequence == 0 && r.Offset == 0 && r.Catalog == ""
	case "stop", "read_log", "tail_log":
		valid = ValidID(r.ID) && ValidID(r.RunID) && r.Cursor >= 0 && r.Revision == "" && r.Sequence == 0 && r.Offset == 0 && r.Catalog == ""
	}
	if r.Action != "read_log" && r.Cursor != 0 || r.Action != "read_log" && r.Action != "stop" && r.Action != "tail_log" && r.RunID != "" {
		valid = false
	}
	if !valid {
		return i18n.Err("err.localcmd.invalid_wire_req")
	}
	return nil
}

type Entry struct {
	ExecutionMode string            `json:"execution_mode"`
	RunID         string            `json:"run_id,omitempty"`
	Phase         string            `json:"phase,omitempty"`
	Result        string            `json:"result,omitempty"`
	Reason        string            `json:"reason,omitempty"`
	ID            string            `json:"id"`
	Revision      string            `json:"revision"`
	Name          string            `json:"name"`
	Aliases       []string          `json:"aliases"`
	Description   string            `json:"description"`
	State         string            `json:"state"`
	Detail        string            `json:"detail,omitempty"`
	DetailKey     string            `json:"detail_key,omitempty"`
	DetailArgs    map[string]string `json:"detail_args,omitempty"`
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

func FailKey(code, key string, args ...map[string]string) *Error {
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

type Response struct {
	Run              json.RawMessage `json:"run,omitempty"`
	Log              json.RawMessage `json:"log,omitempty"`
	ManagedAvailable bool            `json:"managed_available,omitempty"`
	Entries          []Entry         `json:"entries,omitempty"`
	Catalog          string          `json:"catalog,omitempty"`
	Next             int             `json:"next,omitempty"`
	Started          bool            `json:"started,omitempty"`
	Error            *Error          `json:"error,omitempty"`
}
