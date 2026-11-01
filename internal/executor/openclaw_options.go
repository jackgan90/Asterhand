package executor

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/i18n"
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
			return options, i18n.Err("err.executor.options_not_object")
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&options); err != nil {
			return options, i18n.Wrap("err.executor.options_invalid", nil, err)
		}
		if decoder.Decode(new(any)) != io.EOF {
			return options, i18n.Err("err.executor.options_extra_json")
		}
	}
	paths := []string{options.NodePath, options.EntryPath, options.PluginPath}
	for _, path := range options.ProviderPaths {
		paths = append(paths, path)
	}
	for _, path := range paths {
		if path != "" && !filepath.IsAbs(path) {
			return options, i18n.Err("err.executor.options_abs_path")
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
