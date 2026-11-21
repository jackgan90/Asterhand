package harnesses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultCodexConfigPath returns Codex's global user config path for display.
// Asterhand never reads or backs up config.toml directly; all reads/writes go
// through the official `codex mcp` CLI subcommands.
func DefaultCodexConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("cannot resolve user home directory")
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
}

type codexMCPGetResponse struct {
	Name      string `json:"name"`
	Enabled   *bool  `json:"enabled"`
	Command   string `json:"command"`
	Args      []string `json:"args"`
	Transport *struct {
		Type    string   `json:"type"`
		Command string   `json:"command"`
		Args    []string `json:"args"`
	} `json:"transport"`
}

// ObserveCodexMCP inspects only the "asterhand" server via `codex mcp get asterhand --json`.
func ObserveCodexMCP(ctx context.Context, runner CommandRunner, codexExe string, spec MCPEntrySpec) (string, error) {
	if codexExe == "" {
		return MCPObservedNotConfigured, nil
	}
	qCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	stdout, stderr, err := runner(qCtx, codexExe, "mcp", "get", "asterhand", "--json")
	if err != nil {
		combined := strings.ToLower(stdout + "\n" + stderr)
		if strings.Contains(combined, "no mcp server named") || strings.Contains(combined, "not found") {
			return MCPObservedNotConfigured, nil
		}
		return MCPObservedError, fmt.Errorf("codex mcp get failed: %w", err)
	}
	var parsed codexMCPGetResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &parsed); err != nil {
		return MCPObservedError, fmt.Errorf("parse codex mcp get output: %w", err)
	}
	cmd := parsed.Command
	args := parsed.Args
	if parsed.Transport != nil {
		if parsed.Transport.Type != "" && parsed.Transport.Type != "stdio" {
			return MCPObservedConflict, nil
		}
		cmd = parsed.Transport.Command
		args = parsed.Transport.Args
	}
	if IsManagedMCPEntry(cmd, args, spec) {
		return MCPObservedRegistered, nil
	}
	return MCPObservedConflict, nil
}

// ReconcileCodexMCP registers or removes the "asterhand" server using the official
// `codex mcp add` / `codex mcp remove` commands with unmanaged conflict protection.
func ReconcileCodexMCP(ctx context.Context, runner CommandRunner, codexExe string, spec MCPEntrySpec, desired, takeOver bool) (string, *ManagedMCPRecord, error) {
	if codexExe == "" {
		return MCPObservedError, nil, errors.New("codex executable not found")
	}
	obs, obsErr := ObserveCodexMCP(ctx, runner, codexExe, spec)
	if obsErr != nil {
		return MCPObservedError, nil, obsErr
	}
	if obs == MCPObservedConflict && !takeOver {
		return MCPObservedConflict, nil, errors.New("unmanaged asterhand MCP entry already exists in Codex")
	}
	if !desired {
		if obs == MCPObservedNotConfigured {
			return MCPObservedNotConfigured, nil, nil
		}
		rCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if stdout, stderr, err := runner(rCtx, codexExe, "mcp", "remove", "asterhand"); err != nil {
			return MCPObservedError, nil, fmt.Errorf("codex mcp remove failed: %w (%s)", err, strings.TrimSpace(stderr+" "+stdout))
		}
		return MCPObservedNotConfigured, nil, nil
	}

	if obs == MCPObservedRegistered || obs == MCPObservedConflict {
		rCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, _, _ = runner(rCtx, codexExe, "mcp", "remove", "asterhand")
		cancel()
	}

	addArgs := make([]string, 0, len(spec.Args)+5)
	addArgs = append(addArgs, "mcp", "add", "asterhand", "--", spec.Command)
	addArgs = append(addArgs, spec.Args...)
	aCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if stdout, stderr, err := runner(aCtx, codexExe, addArgs...); err != nil {
		return MCPObservedError, nil, fmt.Errorf("codex mcp add failed: %w (%s)", err, strings.TrimSpace(stderr+" "+stdout))
	}
	cfgPath, _ := DefaultCodexConfigPath()
	return MCPObservedRegistered, &ManagedMCPRecord{
		ConfigPath: cfgPath,
		Command:    spec.Command,
		Args:       append([]string(nil), spec.Args...),
	}, nil
}
