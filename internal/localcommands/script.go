package localcommands

import (
	"encoding/json"
	"path/filepath"
)

// ScriptSpec describes one graph node's platform invocation, not a command.
type ScriptSpec struct {
	Script    string
	Arguments []string
	Directory string
}

func (s ScriptSpec) WorkingDirectory() string {
	if s.Directory == "" {
		return filepath.Dir(s.Script)
	}
	return s.Directory
}

func validateTimeout(seconds *int64) error {
	if seconds != nil && (*seconds <= 0 || *seconds > 9223372036) {
		return problem("invalid", "err.localcmd.invalid_timeout")
	}
	return nil
}

func normalizeScript(s ScriptSpec, policy CommandPolicy) (ScriptSpec, error) {
	var err error
	if s.Script, err = policy.ScriptPath(s.Script); err != nil {
		return s, err
	}
	if s.Directory != "" {
		if s.Directory, err = policy.LocalPath(s.Directory); err != nil {
			return s, err
		}
	}
	if s.Arguments == nil {
		s.Arguments = []string{}
	}
	encoded, _ := json.Marshal(s.Arguments)
	if len(s.Arguments) > 128 || len(encoded) > 8192 {
		return s, problem("invalid", "err.localcmd.too_many_args")
	}
	for _, arg := range s.Arguments {
		if !cleanText(arg, 4096) {
			return s, problem("invalid", "err.localcmd.invalid_arg")
		}
		if !policy.ValidArgument(s.Script, arg) {
			return s, problem("invalid", "err.localcmd.batch_quote_save")
		}
	}
	return s, nil
}
