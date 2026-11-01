package executor

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/packaging"
)

func (o *OpenClaw) Configuration(c config.Config) ConfigurationCapabilities {
	options, err := clawOptions(c)
	provider, providerErr := openClawProvider(c)
	if err != nil || providerErr != nil {
		status := unavailable(InvalidConfiguration, "status.availability.openclaw_invalid_config")
		return ConfigurationCapabilities{Chat: status, ModelTasks: status}
	}
	status := openClawInstallation(options, provider, c.LLM.Provider)
	result := ConfigurationCapabilities{Chat: status, ModelTasks: status}
	if !status.Available {
		return result
	}
	if options.PluginPath == "" || !regularFile(filepath.Join(options.PluginPath, "index.js")) || !regularFile(filepath.Join(options.PluginPath, "openclaw.plugin.json")) {
		result.ModelTasks = unavailable(TaskToolsMissing, "status.availability.task_tools_missing")
	}
	return result
}

func openClawInstallation(options openClawOptions, provider providerBridge, name string) Availability {
	if !regularFile(options.NodePath) || !regularFile(options.EntryPath) || options.ProviderPaths[name] == "" {
		return unavailable(RuntimeMissing, "status.availability.runtime_missing")
	}
	component, err := packaging.Dependencies().Component("openclaw")
	if err != nil || !packageVersion(filepath.Join(filepath.Dir(options.EntryPath), "package.json"), component.Version) ||
		!packageVersion(filepath.Join(options.ProviderPaths[name], "package.json"), provider.Version) {
		return unavailable(RuntimeIncompatible, "status.availability.runtime_incompatible")
	}
	return Availability{
		Available:  true,
		Code:       ConfigurationAvailable,
		Detail:     i18n.T(i18n.Effective(), "status.availability.ready"),
		MessageKey: "status.availability.ready",
	}
}

func regularFile(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func packageVersion(path, expected string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	var value struct {
		Version string `json:"version"`
	}
	// The locked OpenClaw manifest includes its exports and is about 146 KiB.
	// Keep local checks bounded without truncating a valid bundled manifest.
	const manifestLimit = 512 * 1024
	data, err := io.ReadAll(io.LimitReader(file, manifestLimit+1))
	return err == nil && len(data) <= manifestLimit && json.Unmarshal(data, &value) == nil && value.Version == expected
}
