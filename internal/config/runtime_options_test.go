package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeOptionsMigrationAndSnapshotIsolation(t *testing.T) {
	m, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = m.SetKey("synthetic-migration-key"); err != nil {
		t.Fatal(err)
	}
	id := m.Get().NodeID
	path := filepath.Join(m.Home, "config.json")
	legacy, _ := json.Marshal(map[string]any{"node_id": id, "model": "deepseek/deepseek-flash", "node_path": filepath.Join(m.Home, "node.exe"), "openclaw_path": filepath.Join(m.Home, "entry.mjs"), "plugin_path": filepath.Join(m.Home, "tools"), "provider_path": filepath.Join(m.Home, "provider")})
	if err = AtomicFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(m.Home, "deepseek.secret"))
	m, err = Open(m.Home)
	if err != nil {
		t.Fatal(err)
	}
	c := m.Get()
	var options map[string]json.RawMessage
	if err = json.Unmarshal(c.RuntimeOptions["openclaw"], &options); err != nil {
		t.Fatal(err)
	}
	if c.NodeID != id || len(options) != 4 {
		t.Fatal("legacy options lost")
	}
	c.SetRuntimeOptions("arbitrary-runtime", json.RawMessage(`{"nested":{"path":"fixture"}}`))
	if err = m.Save(c); err != nil {
		t.Fatal(err)
	}
	c.RuntimeOptions["arbitrary-runtime"][0] = '!'
	snapshot := m.Get()
	if snapshot.RuntimeOptions["arbitrary-runtime"][0] != '{' {
		t.Fatal("save retained caller-owned options")
	}
	snapshot.RuntimeOptions["arbitrary-runtime"][0] = '!'
	delete(snapshot.RuntimeOptions, "openclaw")
	if m.Get().RuntimeOptions["arbitrary-runtime"][0] != '{' || m.Get().RuntimeOptions["openclaw"] == nil {
		t.Fatal("Get leaked mutable options")
	}
	persisted, _ := os.ReadFile(path)
	var fields map[string]json.RawMessage
	json.Unmarshal(persisted, &fields)
	for _, key := range []string{"model", "node_path", "openclaw_path", "plugin_path", "provider_path"} {
		if fields[key] != nil {
			t.Fatalf("legacy field persisted: %s", key)
		}
	}
	after, _ := os.ReadFile(filepath.Join(m.Home, "deepseek.secret"))
	if string(before) != string(after) {
		t.Fatal("migration changed encrypted credential bytes")
	}
}

func TestExplicitOptionsTakePrecedenceOverLegacy(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(`{"openclaw_path":"old","runtime_options":{"openclaw":{"entry_path":"new"},"other":{"value":1}}}`), &c); err != nil {
		t.Fatal(err)
	}
	if string(c.RuntimeOptions["openclaw"]) != `{"entry_path":"new"}` || string(c.RuntimeOptions["other"]) != `{"value":1}` {
		t.Fatal("explicit options overwritten")
	}
}
