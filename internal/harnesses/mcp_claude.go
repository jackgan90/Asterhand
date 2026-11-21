package harnesses

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultClaudeConfigPath returns Claude Code's user-scope config file (~/.claude.json).
func DefaultClaudeConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("cannot resolve user home directory")
	}
	return filepath.Join(home, ".claude.json"), nil
}

// ObserveClaudeMCP inspects only mcpServers.asterhand inside ~/.claude.json
// without copying or exposing any other MCP server credentials or user settings.
func ObserveClaudeMCP(configPath string, spec MCPEntrySpec) (string, error) {
	return observeJSONMCPEntry(configPath, spec)
}

// ReconcileClaudeMCP registers or removes the user-scope "asterhand" MCP server
// via the official `claude mcp add --scope user` / `claude mcp remove --scope user`
// CLI commands, guarded by unmanaged conflict protection.
func ReconcileClaudeMCP(ctx context.Context, runner CommandRunner, claudeExe, configPath string, spec MCPEntrySpec, desired, takeOver bool) (string, *ManagedMCPRecord, error) {
	if claudeExe == "" {
		return MCPObservedError, nil, errors.New("claude executable not found")
	}
	obs, obsErr := ObserveClaudeMCP(configPath, spec)
	if obsErr != nil {
		return MCPObservedError, nil, obsErr
	}
	if obs == MCPObservedConflict && !takeOver {
		return MCPObservedConflict, nil, errors.New("unmanaged asterhand MCP entry already exists in Claude Code")
	}
	if !desired {
		if obs == MCPObservedNotConfigured {
			return MCPObservedNotConfigured, nil, nil
		}
		rCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if stdout, stderr, err := runner(rCtx, claudeExe, "mcp", "remove", "--scope", "user", "asterhand"); err != nil {
			return MCPObservedError, nil, fmt.Errorf("claude mcp remove failed: %w (%s)", err, strings.TrimSpace(stderr+" "+stdout))
		}
		return MCPObservedNotConfigured, nil, nil
	}

	if obs == MCPObservedRegistered || obs == MCPObservedConflict {
		rCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, _, _ = runner(rCtx, claudeExe, "mcp", "remove", "--scope", "user", "asterhand")
		cancel()
	}

	addArgs := make([]string, 0, len(spec.Args)+7)
	addArgs = append(addArgs, "mcp", "add", "--scope", "user", "asterhand", "--", spec.Command)
	addArgs = append(addArgs, spec.Args...)
	aCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if stdout, stderr, err := runner(aCtx, claudeExe, addArgs...); err != nil {
		return MCPObservedError, nil, fmt.Errorf("claude mcp add failed: %w (%s)", err, strings.TrimSpace(stderr+" "+stdout))
	}
	return MCPObservedRegistered, &ManagedMCPRecord{
		ConfigPath: configPath,
		Command:    spec.Command,
		Args:       append([]string(nil), spec.Args...),
	}, nil
}
