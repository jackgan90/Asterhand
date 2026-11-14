// Package localcommands owns local user-registered script launch definitions.
// It is independent of the model, task engine and peer protocol.
package localcommands

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/operations"
)

type Command struct {
	NotifyOnCompletion NotificationSetting `json:"notify_on_completion" description:"Show a desktop notification after this run completes; defaults to true for new commands and older definitions without this field"`
	Graph              *ScriptGraph        `json:"graph,omitempty" description:"Static execution DAG owned by this command"`
	AllowConcurrent    bool                `json:"allow_concurrent" description:"False restricts the command to one active run; true allows up to 8 active runs for this command (32 active runs node-wide, sharing 8 script execution slots)"`
	TimeoutSeconds     *int64              `json:"timeout_seconds,omitempty" description:"Optional whole-graph timeout in seconds (1..9223372036, or null for no timeout; includes dependency and slot wait time)"`
	ID                 string              `json:"id" description:"Stable command ID matching ^[a-zA-Z0-9_-]{16,64}$ (empty string on unsaved commands.validate output)"`
	Revision           string              `json:"revision" description:"Current command revision matching ^[a-zA-Z0-9_-]{16,64}$ (empty string on unsaved commands.validate output)"`
	Name               string              `json:"name" description:"Primary ASCII command name matching ^[A-Za-z][A-Za-z0-9_-]{0,63}$; case-insensitively unique across all command names and aliases"`
	Aliases            []string            `json:"aliases" description:"0..16 aliases (<=128 UTF-8 bytes each, no control chars, no leading '/'); case-insensitively unique across all commands"`
	Description        string              `json:"description" description:"Command description (<=512 UTF-8 bytes, no control chars)"`
}

type Catalog struct {
	Publication    *operations.Publication `json:"automation_publication,omitempty"`
	PendingCleanup []string                `json:"pending_cleanup,omitempty"`
	Schema         int                     `json:"schema_version"`
	Revision       string                  `json:"revision"`
	Commands       []Command               `json:"commands"`
}

type Mutation struct {
	Revision string  `json:"revision"`
	Command  Command `json:"command"`
}

type SaveResult struct {
	Catalog Catalog `json:"catalog"`
	ID      string  `json:"id"`
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
	if !command.NotifyOnCompletion.Present() {
		command.NotifyOnCompletion = NotificationOn
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
	if err := validateTimeout(command.TimeoutSeconds); err != nil {
		return command, err
	}
	graph, err := normalizeGraph(command.Graph, policy)
	command.Graph = graph
	return command, err
}

func ScriptPath(value string) (string, error) { return commandPolicy.ScriptPath(value) }

func containsName(names []string, name string) bool {
	return slices.ContainsFunc(names, func(value string) bool { return strings.EqualFold(value, name) })
}

func commandNames(c Command) []string { return append([]string{c.Name}, c.Aliases...) }

func validateCatalog(c Catalog) error { return validateCatalogWithPolicy(c, commandPolicy) }

func validateCatalogWithPolicy(c Catalog, policy CommandPolicy) error {
	if c.Schema != 4 || !wire.ValidID(c.Revision) || len(c.Commands) > 10000 {
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
			for _, name := range commandNames(command) {
				if containsName(commandNames(other), name) {
					return i18n.Err("err.localcmd.name_conflict_generic")
				}
			}
		}
	}
	return nil
}
