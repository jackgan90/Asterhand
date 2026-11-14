package automation

import (
	"encoding/json"
	"strings"

	"github.com/jackgan90/Asterhand/internal/drafts"
	"github.com/jackgan90/Asterhand/internal/localcommands"
	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/quickinput"
)

type DraftGet struct {
	ID     string `json:"draft_id"`
	Format string `json:"format,omitempty" description:"object (default) or json_fragments for a large complete snapshot"`
	Cursor string `json:"cursor,omitempty"`
}

// Text fields preserve unfinished input. Conversion belongs to explicit commit.
type CommandDraft struct {
	NotifyOnCompletion localcommands.NotificationSetting `json:"notify_on_completion,omitempty" description:"Completion notification switch; omit to preserve the existing editor value"`
	Name               string                            `json:"name"`
	Aliases            string                            `json:"aliases"`
	Description        string                            `json:"description"`
	Concurrent         bool                              `json:"allow_concurrent"`
	Timeout            string                            `json:"timeout"`
	TimeoutUnit        string                            `json:"timeout_unit"`
	Graph              DraftGraph                        `json:"graph"`
}
type DraftGraph struct {
	Nodes       []DraftStep               `json:"nodes"`
	Edges       []localcommands.GraphEdge `json:"edges"`
	Parallelism json.RawMessage           `json:"parallelism"`
}
type DraftStep struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	Script    string          `json:"script,omitempty"`
	Arguments []string        `json:"arguments,omitempty"`
	Directory string          `json:"directory,omitempty"`
	Timeout   json.RawMessage `json:"timeout_seconds,omitempty"`
	Policy    string          `json:"failure_policy,omitempty"`
	X         int             `json:"x"`
	Y         int             `json:"y"`
}
type TextDraft struct {
	Text string `json:"text"`
	Mode string `json:"mode,omitempty"`
}
type SettingsDraft struct {
	Name    *string             `json:"name,omitempty"`
	Model   *string             `json:"model,omitempty"`
	Enabled *bool               `json:"enabled,omitempty"`
	Binding *quickinput.Binding `json:"binding,omitempty"`
	Theme   *string             `json:"theme_id,omitempty"`
	Locale  *string             `json:"locale,omitempty"`
}

func ValidateDraftContent(kind string, target drafts.Target, b json.RawMessage) error {
	switch kind {
	case "command":
		v, err := Decode[CommandDraft](b)
		if err != nil {
			return err
		}
		return v.Validate()
	case "chat", "task_goal", "task_answer":
		v, err := Decode[TextDraft](b)
		if err != nil {
			return err
		}
		if len(v.Text) > 16000 || strings.ContainsRune(v.Text, 0) {
			return Fail("invalid_parameters", "Draft text exceeds its limit")
		}
		if kind == "chat" {
			if v.Mode != "" && v.Mode != "message" && v.Mode != "quoted_command" {
				return Fail("invalid_parameters", "Unknown chat mode")
			}
		} else if v.Mode != "" {
			return Fail("invalid_parameters", "Task input has no chat mode")
		}
		return nil
	case "settings":
		v, err := Decode[SettingsDraft](b)
		if err != nil {
			return err
		}
		// Update replaces a complete editor snapshot. Optional fields describe
		// the two disjoint settings objects, not a patch within either object.
		switch target.Settings {
		case "node":
			if v.Name == nil || v.Model == nil || v.Enabled != nil || v.Binding != nil || v.Theme != nil || v.Locale != nil {
				return Fail("invalid_parameters", "Node drafts require name and model, without desktop fields")
			}
		case "desktop":
			if v.Name != nil || v.Model != nil || v.Enabled == nil || v.Binding == nil || v.Theme == nil || v.Locale == nil {
				return Fail("invalid_parameters", "Desktop drafts require enabled, binding, theme_id and locale, without node fields")
			}
		default:
			return Fail("invalid_parameters", "Settings draft target is missing")
		}
		for _, text := range []*string{v.Name, v.Model, v.Theme, v.Locale} {
			if text != nil && len(*text) > 4096 {
				return Fail("invalid_parameters", "Setting draft field is too large")
			}
		}
		return nil
	default:
		return Fail("invalid_parameters", "Unsupported draft kind")
	}
}

func rawNumberText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if len(s) > 64 {
			return "", Fail("invalid_parameters", "Numeric draft is too long")
		}
		return s, nil
	}
	var n json.Number
	if json.Unmarshal(raw, &n) != nil {
		return "", Fail("invalid_parameters", "Expected numeric text, a number or null")
	}
	return string(n), nil
}

func (v CommandDraft) Validate() error {
	if len(v.Name) > 1024 || len(v.Aliases) > 8192 || len(v.Description) > 16384 || len(v.Timeout) > 64 || len(v.TimeoutUnit) > 16 || len(v.Graph.Nodes) > 64 || len(v.Graph.Edges) > 512 {
		return Fail("invalid_parameters", "Command draft exceeds its limits")
	}
	if _, err := rawNumberText(v.Graph.Parallelism); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, n := range v.Graph.Nodes {
		if !wire.ValidID(n.ID) || seen[n.ID] || n.Kind != "start" && n.Kind != "end" && n.Kind != "script" || len(n.Name) > 1024 || len(n.Script) > 32768 || len(n.Directory) > 32768 || len(n.Arguments) > 128 || n.X < -8000 || n.X > 8000 || n.Y < -8000 || n.Y > 8000 || len(n.Policy) > 64 {
			return Fail("invalid_parameters", "Invalid draft step shape")
		}
		seen[n.ID] = true
		for _, s := range n.Arguments {
			if len(s) > 32768 {
				return Fail("invalid_parameters", "Draft argument is too large")
			}
		}
		if _, err := rawNumberText(n.Timeout); err != nil {
			return err
		}
	}
	for _, e := range v.Graph.Edges {
		if !wire.ValidID(e.From) || !wire.ValidID(e.To) {
			return Fail("invalid_parameters", "Invalid draft edge identity")
		}
	}
	return nil
}
