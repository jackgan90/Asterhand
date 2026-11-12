package automation

import (
	"encoding/json"

	"github.com/jackgan90/Asterhand/internal/localcommands"
)

type Empty struct{}
type CommandID struct {
	ID string `json:"command_id"`
}
type CommandGet struct {
	ID     string `json:"command_id"`
	Format string `json:"format,omitempty" description:"object (default) or json_fragments for a large complete definition"`
	Cursor string `json:"cursor,omitempty"`
}
type CommandVersion struct {
	ID       string `json:"command_id"`
	Revision string `json:"expected_revision"`
}
type CommandList struct {
	Query  string `json:"query,omitempty"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
type CommandInput struct {
	Name        string                     `json:"name"`
	Aliases     []string                   `json:"aliases,omitempty"`
	Description string                     `json:"description,omitempty"`
	Concurrent  bool                       `json:"allow_concurrent,omitempty"`
	Timeout     *int64                     `json:"timeout_seconds,omitempty"`
	Graph       *localcommands.ScriptGraph `json:"graph"`
}

type CommandValidate struct {
	ID        string       `json:"command_id,omitempty"`
	Candidate CommandInput `json:"candidate"`
}

func (a CommandInput) Command() localcommands.Command {
	return localcommands.Command{Name: a.Name, Aliases: a.Aliases, Description: a.Description, AllowConcurrent: a.Concurrent, TimeoutSeconds: a.Timeout, Graph: a.Graph}
}

type CommandClone struct {
	ID       string   `json:"command_id"`
	Revision string   `json:"expected_revision"`
	Name     string   `json:"name"`
	Aliases  []string `json:"aliases,omitempty"`
}

type CommandPatch struct {
	ID          string                     `json:"command_id"`
	Revision    string                     `json:"expected_revision"`
	Name        *string                    `json:"name,omitempty"`
	Aliases     *[]string                  `json:"aliases,omitempty"`
	Description *string                    `json:"description,omitempty"`
	Concurrent  *bool                      `json:"allow_concurrent,omitempty"`
	Timeout     json.RawMessage            `json:"timeout_seconds,omitempty"`
	Graph       *localcommands.ScriptGraph `json:"graph,omitempty"`
	Changes     []GraphChange              `json:"changes,omitempty"`
	AutoLayout  bool                       `json:"auto_layout,omitempty"`
}

type GraphChange struct {
	Action string                   `json:"action" description:"add_step, replace_step, remove_step, connect, disconnect, or reorder"`
	StepID string                   `json:"step_id,omitempty"`
	Step   *localcommands.GraphNode `json:"step,omitempty"`
	Edge   *localcommands.GraphEdge `json:"edge,omitempty"`
	Order  []string                 `json:"order,omitempty"`
}

type RunID struct {
	CommandID string `json:"command_id"`
	ID        string `json:"run_id"`
}
type RunStop struct {
	CommandID string   `json:"command_id"`
	IDs       []string `json:"run_ids"`
}
type RunWait struct {
	CommandID string `json:"command_id"`
	ID        string `json:"run_id"`
	Seconds   int    `json:"timeout_seconds,omitempty"`
}
