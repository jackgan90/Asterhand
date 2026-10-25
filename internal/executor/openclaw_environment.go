package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/packaging"
)

func (o *OpenClaw) Describe() Descriptor {
	return Descriptor{ID: "openclaw", Name: "OpenClaw", Providers: []string{"deepseek"}, Capabilities: Capabilities{Modes: []string{"route", "execute", "probe"}, ToolCallbacks: true, UserQuestions: true, ProcessCancellation: true}}
}
func (o *OpenClaw) Validate(c config.Config) error {
	if _, err := clawOptions(c); err != nil {
		return err
	}
	_, err := openClawProvider(c)
	return err
}
func (o *OpenClaw) Discover(c config.Config, base string) config.Config {
	options, err := clawOptions(c)
	if err != nil {
		return c
	}
	if c.RuntimeOptionsExplicit["openclaw"] {
		return c
	}
	bridge, err := openClawProvider(c)
	if err != nil {
		return c
	}
	if _, marked := c.RuntimeOptionsExplicit["openclaw"]; !marked && hasClawPaths(options) && !legacyBundlePaths(options, c.LLM.Provider, bridge) {
		c.MarkRuntimeOptionsExplicit("openclaw", true)
		return c
	}
	node := "node"
	if runtime.GOOS == "windows" {
		node = "node.exe"
	}
	path := filepath.Join(base, "runtime", "node", node)
	if _, err := os.Stat(path); err != nil {
		return c
	}
	options.NodePath = path
	options.EntryPath = filepath.Join(base, "runtime", "node_modules", "openclaw", "openclaw.mjs")
	options.PluginPath = filepath.Join(base, "plugins", "asterhand-tools")
	if options.ProviderPaths == nil {
		options.ProviderPaths = map[string]string{}
	}
	options.ProviderPaths[c.LLM.Provider] = bridge.Path(base)
	setClawOptions(&c, options)
	c.MarkRuntimeOptionsExplicit("openclaw", false)
	return c
}

func hasClawPaths(options openClawOptions) bool {
	return options.NodePath != "" || options.EntryPath != "" || options.PluginPath != "" || len(options.ProviderPaths) > 0
}

// Pre-F005 configurations had no provenance. Recognize only a complete known
// bundle layout; preserve every other legacy configuration as explicit.
func legacyBundlePaths(options openClawOptions, provider string, bridge providerBridge) bool {
	if options.NodePath == "" {
		return false
	}
	base := filepath.Dir(filepath.Dir(filepath.Dir(options.NodePath)))
	node := "node"
	if runtime.GOOS == "windows" {
		node = "node.exe"
	}
	pairs := [][2]string{
		{options.NodePath, filepath.Join(base, "runtime", "node", node)},
		{options.EntryPath, filepath.Join(base, "runtime", "node_modules", "openclaw", "openclaw.mjs")},
		{options.PluginPath, filepath.Join(base, "plugins", "asterhand-tools")},
		{options.ProviderPaths[provider], bridge.Path(base)},
	}
	for _, pair := range pairs {
		actual, expected := filepath.Clean(pair[0]), filepath.Clean(pair[1])
		if runtime.GOOS == "windows" {
			actual, expected = strings.ToLower(actual), strings.ToLower(expected)
		}
		if actual != expected {
			return false
		}
	}
	return true
}
func (o *OpenClaw) Check(ctx context.Context, c config.Config) (map[string]any, error) {
	checks := map[string]any{"runtime": o.Describe()}
	options, err := clawOptions(c)
	if err != nil {
		return checks, err
	}
	bridge, err := openClawProvider(c)
	if err != nil {
		return checks, err
	}
	var issues []string
	providerPath := options.ProviderPaths[c.LLM.Provider]
	for name, path := range map[string]string{"node": options.NodePath, "openclaw": options.EntryPath, "plugin": options.PluginPath, "provider": providerPath} {
		_, err := os.Stat(path)
		checks[name] = map[string]any{"path": filepath.Clean(path), "exists": err == nil}
		if err != nil {
			issues = append(issues, name+" 未安装")
		}
	}
	if len(issues) > 0 {
		return checks, errors.New(strings.Join(issues, "; "))
	}
	catalog := packaging.Dependencies()
	component, err := catalog.Component("openclaw")
	if err != nil {
		return checks, err
	}
	cmd := exec.CommandContext(ctx, options.NodePath, "--version")
	hideDiagnostic(cmd)
	version, err := cmd.Output()
	checks["node_version"] = strings.TrimSpace(string(version))
	if err != nil || strings.TrimSpace(string(version)) != "v"+catalog.Node.Version {
		issues = append(issues, "Node.js 版本与发布清单不符")
	}
	packages := map[string]struct{ Path, Version string }{
		"openclaw": {filepath.Join(filepath.Dir(options.EntryPath), "package.json"), component.Version},
		"provider": {filepath.Join(providerPath, "package.json"), bridge.Version},
	}
	for name, p := range packages {
		b, err := os.ReadFile(p.Path)
		var pkg struct {
			Version string `json:"version"`
		}
		if err != nil || json.Unmarshal(b, &pkg) != nil || pkg.Version != p.Version {
			issues = append(issues, fmt.Sprintf("%s 版本与发布清单不符", name))
		}
		checks[name+"_version"] = pkg.Version
	}
	if len(issues) > 0 {
		return checks, errors.New(strings.Join(issues, "; "))
	}
	return checks, nil
}
