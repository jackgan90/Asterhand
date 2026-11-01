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
	ID          string   `json:"id"`
	Revision    string   `json:"revision"`
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases"`
	Description string   `json:"description"`
	Script      string   `json:"script"`
	Arguments   []string `json:"arguments"`
	Directory   string   `json:"directory"`
	Window      string   `json:"window"`
}

type Catalog struct {
	Schema   int       `json:"schema_version"`
	Revision string    `json:"revision"`
	Commands []Command `json:"commands"`
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

func normalize(command Command) (Command, error) {
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
	var err error
	if command.Script, err = ScriptPath(command.Script); err != nil {
		return command, err
	}
	if command.Directory != "" {
		if command.Directory, err = localPath(command.Directory); err != nil {
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
		if !strings.EqualFold(filepath.Ext(command.Script), ".ps1") && strings.Contains(arg, `"`) {
			return command, problem("invalid", "err.localcmd.batch_quote_save")
		}
	}
	return command, nil
}

// ScriptPath applies the same path/type boundary to manual and picked paths.
func ScriptPath(value string) (string, error) {
	value, err := localPath(value)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(filepath.Ext(value)) {
	case ".bat", ".cmd", ".ps1":
		return value, nil
	default:
		return "", problem("invalid", "err.localcmd.unsupported_ext")
	}
}

func localPath(value string) (string, error) {
	if !cleanText(value, 2048) || strings.TrimSpace(value) != value || !filepath.IsAbs(value) || strings.HasPrefix(value, `\\`) || strings.HasPrefix(value, "//") {
		return "", problem("invalid", "err.localcmd.local_abs_path_only")
	}
	value = filepath.Clean(value)
	// A colon outside the volume could select an NTFS stream, not a script file.
	if strings.Contains(strings.TrimPrefix(value, filepath.VolumeName(value)), ":") {
		return "", problem("invalid", "err.localcmd.no_ads")
	}
	return value, nil
}

func (c Command) WorkingDirectory() string {
	if c.Directory == "" {
		return filepath.Dir(c.Script)
	}
	return c.Directory
}

func sameLaunch(a, b Command) bool {
	return strings.EqualFold(a.Script, b.Script) && strings.EqualFold(a.WorkingDirectory(), b.WorkingDirectory()) && a.Window == b.Window && slices.Equal(a.Arguments, b.Arguments)
}

func containsName(names []string, name string) bool {
	return slices.ContainsFunc(names, func(value string) bool { return strings.EqualFold(value, name) })
}

func commandNames(c Command) []string { return append([]string{c.Name}, c.Aliases...) }

func validateCatalog(c Catalog) error {
	if c.Schema != 1 || !wire.ValidID(c.Revision) || len(c.Commands) > 10000 {
		return i18n.Err("err.localcmd.catalog_version_size")
	}
	ids := map[string]bool{}
	for i, command := range c.Commands {
		if !wire.ValidID(command.ID) || !wire.ValidID(command.Revision) || ids[command.ID] {
			return i18n.Err("err.localcmd.invalid_id")
		}
		ids[command.ID] = true
		if _, err := normalize(command); err != nil {
			return err
		}
		for _, other := range c.Commands[:i] {
			if sameLaunch(command, other) {
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
