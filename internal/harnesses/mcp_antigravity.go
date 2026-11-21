package harnesses

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jackgan90/Asterhand/internal/config"
)

const (
	MCPObservedNotConfigured = "not_configured"
	MCPObservedRegistered    = "registered"
	MCPObservedConflict      = "conflict"
	MCPObservedError         = "error"
)

// MCPEntrySpec describes the expected Asterhand-managed MCP registration for a caller.
type MCPEntrySpec struct {
	CallerID      string
	NodeHome      string
	Command       string
	Args          []string
	StableCommand string // optional <InstallRoot>\asterhand-mcp.exe
}

// IsManagedMCPEntry reports whether a stdio MCP server entry belongs to this
// Asterhand node and caller (either the versionless asterhand-mcp.exe entry or
// the legacy/dev "asterhand.exe mcp --home <home> --caller <id>" entry).
func IsManagedMCPEntry(cmd string, args []string, spec MCPEntrySpec) bool {
	if cmd == "" || len(args) == 0 {
		return false
	}
	hasCaller := false
	hasHome := false
	homeArg := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--caller" && i+1 < len(args) && args[i+1] == spec.CallerID {
			hasCaller = true
		} else if a == "--caller="+spec.CallerID {
			hasCaller = true
		}
		if (a == "--home" || a == "-home") && i+1 < len(args) {
			hasHome = true
			homeArg = args[i+1]
		} else if strings.HasPrefix(a, "--home=") {
			hasHome = true
			homeArg = strings.TrimPrefix(a, "--home=")
		} else if strings.HasPrefix(a, "-home=") {
			hasHome = true
			homeArg = strings.TrimPrefix(a, "-home=")
		}
	}
	if !hasCaller {
		return false
	}
	// Stable versionless asterhand-mcp.exe entry.
	if strings.EqualFold(filepath.Base(cmd), "asterhand-mcp.exe") || strings.EqualFold(filepath.Base(cmd), "asterhand-mcp") {
		if hasHome {
			return SamePath(homeArg, spec.NodeHome)
		}
		return spec.StableCommand != "" && SamePath(cmd, spec.StableCommand)
	}
	// Direct / legacy asterhand.exe mcp entry.
	return slices.Contains(args, "mcp") && hasHome && SamePath(homeArg, spec.NodeHome)
}

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
func ObserveAntigravityMCP(configPath string, spec MCPEntrySpec) (status string, err error) {
	return observeJSONMCPEntry(configPath, spec)
}

// ReconcileAntigravityMCP performs an atomic single-key insert/update/remove on
// mcpServers.asterhand. It never backs up the full external config file so
// third-party MCP credentials in other entries are never duplicated.
func ReconcileAntigravityMCP(configPath string, spec MCPEntrySpec, desired, takeOver bool) (string, *ManagedMCPRecord, error) {
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
	if hasExisting && !isManagedJSONEntry(existing, spec) && !takeOver {
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

	args := append([]string(nil), spec.Args...)
	servers["asterhand"] = map[string]any{
		"command": spec.Command,
		"args":    args,
	}
	root["mcpServers"] = servers
	if err := writeJSONObjectAtomic(configPath, root); err != nil {
		return MCPObservedError, nil, err
	}
	return MCPObservedRegistered, &ManagedMCPRecord{
		ConfigPath: configPath,
		Command:    spec.Command,
		Args:       args,
	}, nil
}

func observeJSONMCPEntry(configPath string, spec MCPEntrySpec) (string, error) {
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
	if isManagedJSONEntry(entry, spec) {
		return MCPObservedRegistered, nil
	}
	return MCPObservedConflict, nil
}

func isManagedJSONEntry(entry any, spec MCPEntrySpec) bool {
	m, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	cmd, _ := m["command"].(string)
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
	return IsManagedMCPEntry(cmd, args, spec)
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
