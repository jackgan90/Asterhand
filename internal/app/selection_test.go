package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/jackgan90/Asterhand/internal/llm"
)

func TestSettingsSelectionRejectsBeforeWritingAndInvalidatesProbe(t *testing.T) {
	n := newNode(t, &fakeExecutor{})
	c := n.cfg.Get()
	before := c.LLM
	save := func(selection llm.Selection, runtime, key string) int {
		body, _ := json.Marshal(map[string]any{"name": "contract", "root": c.Projects[0].Root, "llm": selection, "agent_runtime": runtime, "key": key})
		response := httptest.NewRecorder()
		n.app.settings(response, httptest.NewRequest(http.MethodPost, "/api/settings", bytes.NewReader(body)))
		return response.Code
	}
	for _, change := range []func(*llm.Selection){func(s *llm.Selection) { s.Provider = "unknown" }, func(s *llm.Selection) { s.Model = "foreign/model" }, func(s *llm.Selection) { s.CredentialRef = "../escape" }, func(s *llm.Selection) { s.Endpoint = "https://example.invalid" }} {
		s := before
		change(&s)
		if save(s, "openclaw", "synthetic-invalid") != 400 {
			t.Fatal("invalid selection accepted")
		}
		if n.cfg.Get().LLM != before {
			t.Fatal("rejected selection changed settings")
		}
		if _, err := n.cfg.Key(); err == nil {
			t.Fatal("rejected settings wrote credential")
		}
	}
	if save(before, "hermes-not-installed", "") != 400 {
		t.Fatal("unknown runtime accepted")
	}
	n.app.mu.Lock()
	n.app.probeRunning = true
	n.app.mu.Unlock()
	if save(before, "openclaw", "synthetic-during-probe") != 409 {
		t.Fatal("changed credentials during probe")
	}
	if _, err := n.cfg.Key(); err == nil {
		t.Fatal("probe conflict persisted credential")
	}
	n.app.mu.Lock()
	n.app.probeRunning = false
	n.app.mu.Unlock()
	if save(before, "openclaw", "synthetic-valid") != 200 {
		t.Fatal("valid selection rejected")
	}
	if n.cfg.Get().ModelVerified {
		t.Fatal("changed settings retained readiness")
	}
	response := httptest.NewRecorder()
	n.app.settings(response, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if bytes.Contains(response.Body.Bytes(), []byte("synthetic-valid")) {
		t.Fatal("settings exposed secret")
	}
	var result struct{ Providers, Runtimes []json.RawMessage }
	if json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Providers) != 1 || len(result.Runtimes) != 1 {
		t.Fatal("available selection catalog missing")
	}
}

func TestSettingsProtectsAdapterDirectoriesBeforeWritingKey(t *testing.T) {
	n := newNode(t, &fakeExecutor{})
	c := n.cfg.Get()
	base := t.TempDir()
	raw, _ := json.Marshal(map[string]any{"node_path": filepath.Join(base, "host", "node.exe"), "entry_path": filepath.Join(base, "agent", "entry.mjs"), "plugin_path": filepath.Join(base, "tools"), "provider_paths": map[string]string{c.LLM.Provider: filepath.Join(base, "model")}})
	c.SetRuntimeOptions(c.AgentRuntime, raw)
	if err := n.cfg.Save(c); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{"host", "agent", "tools", "model"} {
		body, _ := json.Marshal(map[string]any{"name": "contract", "root": filepath.Join(base, directory, "work"), "llm": c.LLM, "key": "synthetic-rejected"})
		response := httptest.NewRecorder()
		n.app.settings(response, httptest.NewRequest(http.MethodPost, "/api/settings", bytes.NewReader(body)))
		if response.Code != 400 {
			t.Fatalf("protected %s accepted: %d", directory, response.Code)
		}
		if _, err := n.cfg.Key(); err == nil {
			t.Fatal("rejected directory still wrote key")
		}
	}
}
