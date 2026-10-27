package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jackgan90/Asterhand/internal/config"
)

func conversationConfig(c config.Config, options openClawOptions, provider providerBridge, root string, port int, promptEnabled bool) ([]byte, error) {
	value := map[string]any{
		"gateway": map[string]any{"mode": "local", "bind": "loopback", "port": port,
			"auth":      map[string]any{"mode": "token", "token": "${OPENCLAW_GATEWAY_TOKEN}"},
			"controlUi": map[string]any{"enabled": false}},
		"agents": map[string]any{"defaults": map[string]any{"model": c.LLM.Model,
			"workspace": filepath.Join(root, "workspace"), "skipBootstrap": false,
			"heartbeat":  map[string]any{"every": "0m"},
			"compaction": map[string]any{"memoryFlush": map[string]any{"enabled": false}},
			"models":     map[string]any{c.LLM.Model: map[string]any{"agentRuntime": map[string]any{"id": "openclaw"}}}}},
		"tools": map[string]any{"deny": []string{"*"}, "toolSearch": false, "codeMode": map[string]any{"enabled": false}},
		"plugins": map[string]any{"slots": map[string]any{"memory": "none"}, "allow": []string{provider.PluginID},
			"load":    map[string]any{"paths": []string{options.ProviderPaths[c.LLM.Provider]}},
			"entries": map[string]any{provider.PluginID: map[string]any{"enabled": true}}},
		"commands": map[string]any{"native": false, "nativeSkills": false, "text": false, "bash": false,
			"config": false, "debug": false, "restart": false, "allowFrom": map[string]any{"*": []string{}}},
		"session": map[string]any{"maintenance": map[string]any{"mode": "warn"}, "reset": map[string]any{"mode": "none"}},
		"cron":    map[string]any{"enabled": false}, "browser": map[string]any{"enabled": false},
		"discovery": map[string]any{"mdns": map[string]any{"mode": "off"}},
		"update":    map[string]any{"checkOnStart": false},
	}
	configureConversationPrompt(value, root, promptEnabled)
	return json.MarshalIndent(value, "", "  ")
}

func conversationEnvironment(root, node, keyName, key, token string) []string {
	var environment []string
	for _, name := range []string{"SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT"} {
		if value := os.Getenv(name); value != "" {
			environment = append(environment, name+"="+value)
		}
	}
	for name, directory := range map[string]string{"USERPROFILE": "userprofile", "HOME": "userprofile", "APPDATA": "appdata", "LOCALAPPDATA": "localappdata", "TEMP": "temp", "TMP": "temp"} {
		path := filepath.Join(root, directory)
		if name == "TEMP" || name == "TMP" {
			path = conversationTempPath(path)
		}
		environment = append(environment, name+"="+path)
	}
	environment = append(environment,
		"PATH="+filepath.Dir(node)+string(os.PathListSeparator)+filepath.Join(os.Getenv("SYSTEMROOT"), "System32"),
		"OPENCLAW_STATE_DIR="+filepath.Join(root, "state"), "OPENCLAW_CONFIG_PATH="+filepath.Join(root, "openclaw.json"),
		"OPENCLAW_GATEWAY_TOKEN="+token, "OPENCLAW_NO_RESPAWN=1", "NODE_DISABLE_COMPILE_CACHE=1", "NO_COLOR=1")
	if key != "" {
		environment = append(environment, keyName+"="+key)
	}
	return environment
}

// Node mkdtemp does not automatically expand long Windows paths. OpenClaw adds
// nested plugin-capture/build directories below this isolated temporary root.
func conversationTempPath(path string) string {
	if runtime.GOOS != "windows" || !filepath.IsAbs(path) || strings.HasPrefix(path, `\\?\`) {
		return path
	}
	if strings.HasPrefix(path, `\\`) {
		return `\\?\UNC\` + strings.TrimPrefix(path, `\\`)
	}
	return `\\?\` + path
}
