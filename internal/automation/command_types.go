package automation

import (
	"encoding/json"

	"github.com/jackgan90/Asterhand/internal/localcommands"
)

type Empty struct{}
type CommandID struct {
	ID string `json:"command_id" description:"Command ID matching ^[a-zA-Z0-9_-]{16,64}$"`
}
type CommandGet struct {
	ID     string `json:"command_id" description:"Command ID matching ^[a-zA-Z0-9_-]{16,64}$"`
	Format string `json:"format,omitempty" description:"object (default) or json_fragments for a large complete definition"`
	Cursor string `json:"cursor,omitempty" description:"Opaque fragment cursor returned by a previous json_fragments response"`
}
type CommandVersion struct {
	ID       string `json:"command_id" description:"Command ID matching ^[a-zA-Z0-9_-]{16,64}$"`
	Revision string `json:"expected_revision" description:"Latest command revision (^[a-zA-Z0-9_-]{16,64}$) from commands_get or commands_list; stale revision fails with conflict"`
}
type CommandList struct {
	Query  string `json:"query,omitempty" description:"Optional name/alias search query (leading '/' is stripped)"`
	Cursor string `json:"cursor,omitempty" description:"Opaque pagination cursor from a previous page response"`
	Limit  int    `json:"limit,omitempty" description:"Page size (1..200; 0 uses default)"`
}
type CommandInput struct {
	NotifyOnCompletion localcommands.NotificationSetting `json:"notify_on_completion,omitempty" description:"Desktop completion notification (default true); false disables it; null is invalid"`
	Name               string                            `json:"name" description:"Primary ASCII command name matching ^[A-Za-z][A-Za-z0-9_-]{0,63}$; trimmed and case-insensitively unique across all command names and aliases (catalog max 10000 commands, 16 MiB)"`
	Aliases            []string                          `json:"aliases,omitempty" description:"Optional 0..16 aliases (<=128 UTF-8 bytes each, no control chars, cannot start with '/'); trimmed, deduplicated, and case-insensitively unique across all commands"`
	Description        string                            `json:"description,omitempty" description:"Optional description (<=512 UTF-8 bytes, no control chars)"`
	Concurrent         bool                              `json:"allow_concurrent,omitempty" description:"Default false (mutex per command). Set true to allow overlapping instances (max 8 active instances per command, 32 node-wide; all active script steps share 8 execution slots)"`
	Timeout            *int64                            `json:"timeout_seconds,omitempty" description:"Optional whole-graph timeout in seconds (1..9223372036, or null/omitted for no timeout; use null when steps spawn long-lived child processes)"`
	Graph              *localcommands.ScriptGraph        `json:"graph" description:"Required static DAG (3..64 nodes, 0..512 edges, parallelism 1..16). Non-existent script files are allowed at save time and checked asynchronously / before launch"`
}

type CommandValidate struct {
	ID        string       `json:"command_id,omitempty" description:"Optional existing command ID (^[a-zA-Z0-9_-]{16,64}$) when validating edits to an existing command"`
	Candidate CommandInput `json:"candidate" description:"Candidate command and graph to normalize and validate without saving"`
}

func (a CommandInput) Command() localcommands.Command {
	return localcommands.Command{Name: a.Name, Aliases: a.Aliases, Description: a.Description, AllowConcurrent: a.Concurrent, NotifyOnCompletion: a.NotifyOnCompletion, TimeoutSeconds: a.Timeout, Graph: a.Graph}
}

type CommandClone struct {
	ID       string   `json:"command_id" description:"Source command ID matching ^[a-zA-Z0-9_-]{16,64}$"`
	Revision string   `json:"expected_revision" description:"Expected source command revision matching ^[a-zA-Z0-9_-]{16,64}$"`
	Name     string   `json:"name" description:"Unique primary ASCII name for the cloned command (^[A-Za-z][A-Za-z0-9_-]{0,63}$)"`
	Aliases  []string `json:"aliases,omitempty" description:"Optional 0..16 unique aliases for the cloned command"`
}

type CommandPatch struct {
	NotifyOnCompletion localcommands.NotificationSetting `json:"notify_on_completion,omitempty" description:"Replace completion notification setting; omit to keep the current value; null is invalid"`
	ID                 string                            `json:"command_id" description:"Target command ID matching ^[a-zA-Z0-9_-]{16,64}$"`
	Revision           string                            `json:"expected_revision" description:"Latest command revision (^[a-zA-Z0-9_-]{16,64}$) from commands_get or commands_list; edits are rejected while any run of this command is active"`
	Name               *string                           `json:"name,omitempty" description:"Replacement primary ASCII name (^[A-Za-z][A-Za-z0-9_-]{0,63}$), or omit to keep"`
	Aliases            *[]string                         `json:"aliases,omitempty" description:"Replacement alias list (0..16 items, <=128 UTF-8 bytes each, no leading '/'), or omit to keep"`
	Description        *string                           `json:"description,omitempty" description:"Replacement description (<=512 UTF-8 bytes, no control chars), or omit to keep"`
	Concurrent         *bool                             `json:"allow_concurrent,omitempty" description:"Replacement allow_concurrent flag, or omit to keep"`
	Timeout            json.RawMessage                   `json:"timeout_seconds,omitempty" description:"Replacement whole-graph timeout in seconds (integer 1..9223372036, explicit null to clear, or omit to keep)"`
	Graph              *localcommands.ScriptGraph        `json:"graph,omitempty" description:"Full replacement DAG (mutually exclusive with changes)"`
	Changes            []GraphChange                     `json:"changes,omitempty" description:"Ordered incremental DAG edits (max 1024; mutually exclusive with graph)"`
	AutoLayout         bool                              `json:"auto_layout,omitempty" description:"When true, recompute node x/y coordinates (x = 80 + col*280, y = 80 + row*110) after applying graph edits"`
}

type GraphChange struct {
	Action string                   `json:"action" description:"add_step (requires step), replace_step (requires step_id and matching step), remove_step (requires step_id), connect (requires edge), disconnect (requires edge), or reorder (requires order naming every step once)"`
	StepID string                   `json:"step_id,omitempty" description:"Target step ID for replace_step or remove_step (omit for other actions)"`
	Step   *localcommands.GraphNode `json:"step,omitempty" description:"Complete step definition for add_step or replace_step (omit for other actions)"`
	Edge   *localcommands.GraphEdge `json:"edge,omitempty" description:"Directed edge for connect or disconnect (omit for other actions)"`
	Order  []string                 `json:"order,omitempty" description:"Complete permutation of all node IDs for reorder (omit for other actions)"`
}

type RunID struct {
	CommandID string `json:"command_id" description:"Command ID matching ^[a-zA-Z0-9_-]{16,64}$"`
	ID        string `json:"run_id" description:"Explicit run ID matching ^[a-zA-Z0-9_-]{16,64}$"`
}
type RunStop struct {
	CommandID string   `json:"command_id" description:"Command ID matching ^[a-zA-Z0-9_-]{16,64}$"`
	IDs       []string `json:"run_ids" description:"1..8 explicit run IDs to stop (terminates each run's Windows Job process tree; other runs are unaffected)"`
}
type RunWait struct {
	CommandID string `json:"command_id" description:"Command ID matching ^[a-zA-Z0-9_-]{16,64}$"`
	ID        string `json:"run_id" description:"Explicit run ID matching ^[a-zA-Z0-9_-]{16,64}$"`
	Seconds   int    `json:"timeout_seconds,omitempty" description:"Observation timeout in seconds (1..30; 0 defaults to 30; timeout/cancel never stops the run)"`
}
