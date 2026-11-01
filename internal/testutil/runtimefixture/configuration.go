// Package runtimefixture prepares local prerequisite files for fake executors.
// These files never run Node or a model and are not compatibility evidence.
package runtimefixture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/packaging"
)

func Configure(t testing.TB, manager *config.Manager) string {
	t.Helper()
	root := t.TempDir()
	files := map[string][]byte{
		"node.exe": {}, "openclaw/openclaw.mjs": {},
		"tools/index.js": {}, "tools/openclaw.plugin.json": []byte(`{"id":"asterhand-tools"}`),
	}
	for directory, component := range map[string]string{"openclaw": "openclaw", "provider": "deepseek-provider"} {
		dependency, err := packaging.Dependencies().Component(component)
		if err != nil {
			t.Fatal(err)
		}
		files[directory+"/package.json"], _ = json.Marshal(map[string]string{"version": dependency.Version})
	}
	for relative, content := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	c := manager.Get()
	raw, _ := json.Marshal(map[string]any{"node_path": filepath.Join(root, "node.exe"),
		"entry_path": filepath.Join(root, "openclaw", "openclaw.mjs"), "plugin_path": filepath.Join(root, "tools"),
		"provider_paths": map[string]string{c.LLM.Provider: filepath.Join(root, "provider")}})
	c.SetRuntimeOptions("openclaw", raw)
	if err := manager.Save(c); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetCredential(c.LLM.CredentialRef, "synthetic-prerequisite-key"); err != nil {
		t.Fatal(err)
	}
	return root
}
