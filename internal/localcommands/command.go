// Package localcommands owns local user-registered script launch definitions.
// It is independent of the model, task engine and peer protocol.
package localcommands

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
)

type Command struct {
	Kind           string       `json:"kind,omitempty"`
	Graph          *ScriptGraph `json:"graph,omitempty"`
	ExecutionMode  string       `json:"execution_mode"`
	TimeoutSeconds *int64       `json:"timeout_seconds,omitempty"`
	ID             string       `json:"id"`
	Revision       string       `json:"revision"`
	Name           string       `json:"name"`
	Aliases        []string     `json:"aliases"`
	Description    string       `json:"description"`
	Script         string       `json:"script"`
	Arguments      []string     `json:"arguments"`
	Directory      string       `json:"directory"`
	Window         string       `json:"window"`
}

type Catalog struct {
	PendingCleanup []string  `json:"pending_cleanup,omitempty"`
	Schema         int       `json:"schema_version"`
	Revision       string    `json:"revision"`
	Commands       []Command `json:"commands"`
}

type Mutation struct {
	Revision string  `json:"revision"`
	Command  Command `json:"command"`
}

type SaveResult struct {
	Catalog Catalog `json:"catalog"`
	ID      string  `json:"id"`
	Merged  bool    `json:"merged"`
}

var englishName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

func problem(code, key string, args ...map[string]string) error {
	return wire.FailKey(code, key, args...)
}

func cleanText(text string, limit int) bool {
	return len(text) <= limit && utf8.ValidString(text) && !strings.ContainsFunc(text, unicode.IsControl)
}

func normalize(command Command) (Command, error) { return normalizeWithPolicy(command, commandPolicy) }

func normalizeWithPolicy(command Command, policy CommandPolicy) (Command, error) {
	if command.Kind == "" {
		command.Kind = "script"
	}
	if command.Kind != "script" && command.Kind != "graph" {
		return command, graphProblem("err.graph.kind", "")
	}
	command.Name = strings.TrimSpace(command.Name)
	if !englishName.MatchString(command.Name) {
		return command, problem("invalid", "err.localcmd.invalid_name")
	}
	if len(command.Aliases) > 16 || !cleanText(command.Description, 512) {
		return command, problem("invalid", "err.localcmd.invalid_aliases_desc")
	}
	names := []string{command.Name}
	aliases := []string{}
	for _, alias := range command.Aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" || strings.HasPrefix(alias, "/") || !cleanText(alias, 128) {
			return command, problem("invalid", "err.localcmd.invalid_alias")
		}
		if !containsName(names, alias) {
			aliases, names = append(aliases, alias), append(names, alias)
		}
	}
	command.Aliases = aliases
	if command.ExecutionMode == "" {
		command.ExecutionMode = "unmanaged"
	}
	if command.ExecutionMode != "managed" && command.ExecutionMode != "unmanaged" {
		return command, problem("invalid", "err.localcmd.invalid_execution_mode")
	}
	if command.ExecutionMode == "managed" {
		command.Window = "background"
		if command.TimeoutSeconds != nil && (*command.TimeoutSeconds <= 0 || *command.TimeoutSeconds > 9223372036) {
			return command, problem("invalid", "err.localcmd.invalid_timeout")
		}
	} else {
		command.TimeoutSeconds = nil
	}
	if command.Kind == "graph" {
		if command.ExecutionMode != "managed" {
			return command, graphProblem("err.graph.managed", "")
		}
		graph, err := normalizeGraph(command.Graph, policy)
		command.Graph = graph
		command.Script, command.Directory, command.Arguments = "", "", []string{}
		return command, err
	}
	if command.Graph != nil {
		return command, graphProblem("err.graph.kind", "")
	}
	var err error
	if command.Script, err = policy.ScriptPath(command.Script); err != nil {
		return command, err
	}
	if command.Directory != "" {
		if command.Directory, err = policy.LocalPath(command.Directory); err != nil {
			return command, err
		}
	}
	if command.Window == "" {
		command.Window = "background"
	}
	if command.Window != "background" && command.Window != "terminal" {
		return command, problem("invalid", "err.localcmd.invalid_window")
	}
	if command.Arguments == nil {
		command.Arguments = []string{}
	}
	encoded, _ := json.Marshal(command.Arguments)
	if len(command.Arguments) > 128 || len(encoded) > 8192 {
		return command, problem("invalid", "err.localcmd.too_many_args")
	}
	for _, arg := range command.Arguments {
		if !cleanText(arg, 4096) {
			return command, problem("invalid", "err.localcmd.invalid_arg")
		}
		if !policy.ValidArgument(command.Script, arg) {
			return command, problem("invalid", "err.localcmd.batch_quote_save")
		}
	}
	return command, nil
}

func ScriptPath(value string) (string, error) { return commandPolicy.ScriptPath(value) }

func (c Command) WorkingDirectory() string {
	if c.Directory == "" {
		return filepath.Dir(c.Script)
	}
	return c.Directory
}

func sameLaunch(a, b Command) bool { return sameLaunchWithPolicy(a, b, commandPolicy) }

func sameLaunchWithPolicy(a, b Command, policy CommandPolicy) bool {
	if a.Kind == "graph" || b.Kind == "graph" {
		return false
	}
	return policy.EqualPath(a.Script, b.Script) && policy.EqualPath(a.WorkingDirectory(), b.WorkingDirectory()) && executionMode(a) == executionMode(b) && timeoutEqual(a.TimeoutSeconds, b.TimeoutSeconds) && a.Window == b.Window && slices.Equal(a.Arguments, b.Arguments)
}

func containsName(names []string, name string) bool {
	return slices.ContainsFunc(names, func(value string) bool { return strings.EqualFold(value, name) })
}

func commandNames(c Command) []string { return append([]string{c.Name}, c.Aliases...) }

func validateCatalog(c Catalog) error { return validateCatalogWithPolicy(c, commandPolicy) }

func validateCatalogWithPolicy(c Catalog, policy CommandPolicy) error {
	if c.Schema != 3 || !wire.ValidID(c.Revision) || len(c.Commands) > 10000 {
		return i18n.Err("err.localcmd.catalog_version_size")
	}
	ids := map[string]bool{}
	for i, command := range c.Commands {
		if !wire.ValidID(command.ID) || !wire.ValidID(command.Revision) || ids[command.ID] {
			return i18n.Err("err.localcmd.invalid_id")
		}
		ids[command.ID] = true
		if _, err := normalizeWithPolicy(command, policy); err != nil {
			return err
		}
		for _, other := range c.Commands[:i] {
			if sameLaunchWithPolicy(command, other, policy) {
				return i18n.Err("err.localcmd.duplicate_entry")
			}
			for _, name := range commandNames(command) {
				if containsName(commandNames(other), name) {
					return i18n.Err("err.localcmd.name_conflict_generic")
				}
			}
		}
	}
	return nil
}
