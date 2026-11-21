package harnesses

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/jackgan90/Asterhand/internal/config"
)

const (
	MCPObservedNotConfigured = "not_configured"
	MCPObservedRegistered    = "registered"
	MCPObservedConflict      = "conflict"
	MCPObservedError         = "error"
)

// DefaultAntigravityMCPConfigPath returns the global Antigravity MCP config path.
func DefaultAntigravityMCPConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("cannot resolve user home directory")
	}
	return filepath.Join(home, ".gemini", "config", "mcp_config.json"), nil
}

// ObserveAntigravityMCP inspects only the "asterhand" entry in Antigravity's
// mcp_config.json without copying or exposing any other MCP server credentials.
func ObserveAntigravityMCP(configPath, expectedHome string) (status string, err error) {
	root, exists, err := readJSONObject(configPath)
	if err != nil {
		return MCPObservedError, err
	}
	if !exists {
		return MCPObservedNotConfigured, nil
	}
	servers, _ := root["mcpServers"].(map[string]any)
	entry, ok := servers["asterhand"]
	if !ok {
		return MCPObservedNotConfigured, nil
	}
	if isManagedAntigravityEntry(entry, expectedHome) {
		return MCPObservedRegistered, nil
	}
	return MCPObservedConflict, nil
}

// ReconcileAntigravityMCP performs an atomic single-key insert/update/remove on
// mcpServers.asterhand. It never backs up the full external config file so
// third-party MCP credentials in other entries are never duplicated.
func ReconcileAntigravityMCP(configPath, executable, nodeHome string, desired, takeOver bool) (string, *ManagedMCPRecord, error) {
	root, exists, err := readJSONObject(configPath)
	if err != nil {
		return MCPObservedError, nil, err
	}
	if !exists {
		if !desired {
			return MCPObservedNotConfigured, nil, nil
		}
		root = map[string]any{}
	}
	var servers map[string]any
	if raw, ok := root["mcpServers"]; ok && raw != nil {
		m, ok := raw.(map[string]any)
		if !ok {
			return MCPObservedError, nil, errors.New("mcpServers field is not a JSON object")
		}
		servers = m
	} else {
		servers = map[string]any{}
	}

	existing, hasExisting := servers["asterhand"]
	if hasExisting && !isManagedAntigravityEntry(existing, nodeHome) && !takeOver {
		return MCPObservedConflict, nil, errors.New("unmanaged asterhand MCP entry already exists")
	}

	if !desired {
		if !hasExisting {
			return MCPObservedNotConfigured, nil, nil
		}
		delete(servers, "asterhand")
		root["mcpServers"] = servers
		if err := writeJSONObjectAtomic(configPath, root); err != nil {
			return MCPObservedError, nil, err
		}
		return MCPObservedNotConfigured, nil, nil
	}

	args := []string{"mcp", "--home", nodeHome, "--caller", HarnessAntigravity}
	servers["asterhand"] = map[string]any{
		"command": executable,
		"args":    args,
	}
	root["mcpServers"] = servers
	if err := writeJSONObjectAtomic(configPath, root); err != nil {
		return MCPObservedError, nil, err
	}
	return MCPObservedRegistered, &ManagedMCPRecord{
		ConfigPath: configPath,
		Command:    executable,
		Args:       args,
	}, nil
}

func isManagedAntigravityEntry(entry any, expectedHome string) bool {
	m, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	rawArgs, ok := m["args"].([]any)
	if !ok {
		return false
	}
	args := make([]string, 0, len(rawArgs))
	for _, a := range rawArgs {
		s, ok := a.(string)
		if !ok {
			return false
		}
		args = append(args, s)
	}
	hasCaller := false
	hasHome := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--caller" && args[i+1] == HarnessAntigravity {
			hasCaller = true
		}
		if args[i] == "--home" && SamePath(args[i+1], expectedHome) {
			hasHome = true
		}
	}
	return slices.Contains(args, "mcp") && hasCaller && hasHome
}

func readJSONObject(path string) (map[string]any, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF}))
	if len(trimmed) == 0 {
		return map[string]any{}, true, nil
	}
	var root map[string]any
	if err := json.Unmarshal(trimmed, &root); err != nil {
		return nil, true, err
	}
	if root == nil {
		root = map[string]any{}
	}
	return root, true, nil
}

func writeJSONObjectAtomic(path string, root map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return config.AtomicFile(path, b, 0600)
}
