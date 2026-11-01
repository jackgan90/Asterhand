// Package localcommands owns local user-registered script launch definitions.
// It is independent of the model, task engine and peer protocol.
package localcommands

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

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

func problem(code, message string) error { return &wire.Error{Code: code, Message: message} }

func cleanText(text string, limit int) bool {
	return len(text) <= limit && utf8.ValidString(text) && !strings.ContainsFunc(text, unicode.IsControl)
}

func normalize(command Command) (Command, error) {
	command.Name = strings.TrimSpace(command.Name)
	if !englishName.MatchString(command.Name) {
		return command, problem("invalid", "英文名须以字母开头，只包含字母、数字、连字符或下划线，最多 64 字符")
	}
	if len(command.Aliases) > 16 || !cleanText(command.Description, 512) {
		return command, problem("invalid", "最多 16 个别名，描述最多 512 UTF-8 字节且不包含控制字符")
	}
	names := []string{command.Name}
	aliases := []string{}
	for _, alias := range command.Aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" || strings.HasPrefix(alias, "/") || !cleanText(alias, 128) {
			return command, problem("invalid", "别名不能为空、以 / 开头或包含控制字符，最多 128 UTF-8 字节")
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
		return command, problem("invalid", "请选择后台运行或打开终端")
	}
	if command.Arguments == nil {
		command.Arguments = []string{}
	}
	encoded, _ := json.Marshal(command.Arguments)
	if len(command.Arguments) > 128 || len(encoded) > 8192 {
		return command, problem("invalid", "固定参数最多 128 项、合计 8 KiB")
	}
	for _, arg := range command.Arguments {
		if !cleanText(arg, 4096) {
			return command, problem("invalid", "参数不支持控制字符，每项最多 4096 UTF-8 字节")
		}
		if !strings.EqualFold(filepath.Ext(command.Script), ".ps1") && strings.Contains(arg, `"`) {
			return command, problem("invalid", "批处理参数不支持字面双引号，请改用 .ps1；含空格参数无需自行加引号")
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
		return "", problem("invalid", "仅支持 .bat、.cmd 和 Windows PowerShell 5.1 .ps1 脚本")
	}
}

func localPath(value string) (string, error) {
	if !cleanText(value, 2048) || strings.TrimSpace(value) != value || !filepath.IsAbs(value) || strings.HasPrefix(value, `\\`) || strings.HasPrefix(value, "//") {
		return "", problem("invalid", "请填写本机绝对路径，不支持网络共享或设备路径")
	}
	value = filepath.Clean(value)
	// A colon outside the volume could select an NTFS stream, not a script file.
	if strings.Contains(strings.TrimPrefix(value, filepath.VolumeName(value)), ":") {
		return "", problem("invalid", "路径不能包含文件数据流")
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
		return fmt.Errorf("命令配置版本或大小无效")
	}
	ids := map[string]bool{}
	for i, command := range c.Commands {
		if !wire.ValidID(command.ID) || !wire.ValidID(command.Revision) || ids[command.ID] {
			return fmt.Errorf("命令身份无效")
		}
		ids[command.ID] = true
		if _, err := normalize(command); err != nil {
			return err
		}
		for _, other := range c.Commands[:i] {
			if sameLaunch(command, other) {
				return fmt.Errorf("命令配置存在重复启动项")
			}
			for _, name := range commandNames(command) {
				if containsName(commandNames(other), name) {
					return fmt.Errorf("命令名称冲突")
				}
			}
		}
	}
	return nil
}
