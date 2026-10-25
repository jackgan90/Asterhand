package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/jackgan90/Asterhand/internal/config"
)

// These fields belong exclusively to this adapter, not the public Config schema.
type openClawOptions struct {
	NodePath      string            `json:"node_path"`
	EntryPath     string            `json:"entry_path"`
	PluginPath    string            `json:"plugin_path"`
	ProviderPaths map[string]string `json:"provider_paths"`
}

func clawOptions(c config.Config) (openClawOptions, error) {
	var options openClawOptions
	if raw, exists := c.RuntimeOptions["openclaw"]; exists {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return options, fmt.Errorf("OpenClaw 选项必须为 JSON 对象")
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&options); err != nil {
			return options, fmt.Errorf("OpenClaw 选项无效: %w", err)
		}
		if decoder.Decode(new(any)) != io.EOF {
			return options, fmt.Errorf("OpenClaw 选项包含多余 JSON")
		}
	}
	paths := []string{options.NodePath, options.EntryPath, options.PluginPath}
	for _, path := range options.ProviderPaths {
		paths = append(paths, path)
	}
	for _, path := range paths {
		if path != "" && !filepath.IsAbs(path) {
			return options, fmt.Errorf("OpenClaw 运行时路径必须为绝对路径")
		}
	}
	return options, nil
}

func setClawOptions(c *config.Config, options openClawOptions) {
	raw, _ := json.Marshal(options)
	c.SetRuntimeOptions("openclaw", raw)
}

func (o *OpenClaw) ProtectedPaths(c config.Config) ([]string, error) {
	options, err := clawOptions(c)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, path := range []string{options.NodePath, options.EntryPath} {
		if path != "" {
			paths = append(paths, filepath.Dir(path))
		}
	}
	if options.PluginPath != "" {
		paths = append(paths, options.PluginPath)
	}
	for _, path := range options.ProviderPaths {
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths, nil
}
