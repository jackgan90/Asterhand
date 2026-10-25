package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacySelectionMigrationPreservesIdentityAndSecret(t *testing.T) {
	m, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = m.SetKey("synthetic-legacy-secret"); err != nil {
		t.Fatal(err)
	}
	id := m.Get().NodeID
	legacy, _ := json.Marshal(map[string]any{"node_id": id, "model": "deepseek/deepseek-v4-pro", "model_verified": true})
	if err = AtomicFile(filepath.Join(m.Home, "config.json"), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(m.Home, "deepseek.secret"))
	migrated, err := Open(m.Home)
	if err != nil {
		t.Fatal(err)
	}
	c := migrated.Get()
	if c.NodeID != id || c.AgentRuntime != "openclaw" || c.LLM.Provider != "deepseek" || c.LLM.Model != "deepseek/deepseek-v4-pro" || c.LLM.CredentialRef != "deepseek" {
		t.Fatalf("migration mismatch: %+v", c)
	}
	if key, err := migrated.Key(); err != nil || key != "synthetic-legacy-secret" {
		t.Fatal("legacy credential unavailable")
	}
	if err = migrated.Save(c); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(m.Home, "deepseek.secret"))
	if string(before) != string(after) {
		t.Fatal("migration rewrote credential")
	}
	persisted, _ := os.ReadFile(filepath.Join(m.Home, "config.json"))
	var fields map[string]json.RawMessage
	json.Unmarshal(persisted, &fields)
	if _, exists := fields["model"]; exists {
		t.Fatal("legacy field was persisted")
	}
}

func TestCredentialReferencesAreIsolatedAndBounded(t *testing.T) {
	m, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for ref, value := range map[string]string{"deepseek": "first", "other": "second"} {
		if err = m.SetCredential(ref, value); err != nil {
			t.Fatal(err)
		}
	}
	if err = m.SetCredential("other", ""); err != nil {
		t.Fatal(err)
	}
	if key, err := m.Credential("deepseek"); err != nil || key != "first" {
		t.Fatal("deletion crossed credential boundary")
	}
	if _, err := m.Credential("other"); err == nil {
		t.Fatal("deleted credential remains")
	}
	for _, ref := range []string{"", "../deepseek", "a/b", "CON:", "Upper", "a.secret"} {
		if m.SetCredential(ref, "value") == nil {
			t.Fatalf("accepted unsafe reference %q", ref)
		}
		if _, err := m.Credential(ref); err == nil {
			t.Fatalf("read unsafe reference %q", ref)
		}
	}
}
