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

func saveNodeSettings(t *testing.T, n *testNode, change func(map[string]any)) *httptest.ResponseRecorder {
	t.Helper()
	c := n.cfg.Get()
	input := map[string]any{"name": c.Name, "endpoint": c.Endpoint, "root": c.Projects[0].Root, "llm": c.LLM, "agent_runtime": c.AgentRuntime}
	change(input)
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	n.app.settings(response, httptest.NewRequest(http.MethodPost, "/api/settings", bytes.NewReader(body)))
	return response
}

func TestMetadataSettingsPreserveReadinessAndExplainRestart(t *testing.T) {
	n := newNode(t, &fakeExecutor{})
	response := saveNodeSettings(t, n, func(v map[string]any) {
		v["name"] = "renamed"
		v["endpoint"] = "https://100.121.127.89:17433"
		v["peer_listen"] = "100.121.127.89:17433"
	})
	var result map[string]bool
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || !result["ready"] || !result["restart_required"] {
		t.Fatal(response.Code, response.Body.String())
	}
	if !n.cfg.Get().ModelVerified {
		t.Fatal("metadata invalidated model verification")
	}
	probe := httptest.NewRecorder()
	n.app.probe(probe, httptest.NewRequest(http.MethodGet, "/api/probe", nil))
	if !bytes.Contains(probe.Body.Bytes(), []byte(`"passed"`)) {
		t.Fatal(probe.Body.String())
	}
	if n.app.info(nil)["network_warning"] == "" {
		t.Fatal("pending listener not visible")
	}
	n.close()
	// The next start actually applies the saved listener; use a free local socket
	// in this isolated fixture instead of binding the developer's Tailscale IP.
	c := n.cfg.Get()
	c.PeerListen = "127.0.0.1:0"
	if err := n.cfg.Save(c); err != nil {
		t.Fatal(err)
	}
	n.start(t, &fakeExecutor{})
	if n.app.info(nil)["restart_required"] != false || !n.cfg.Get().ModelVerified {
		t.Fatal("restart lost preserved readiness")
	}
}

func TestExecutionSettingsInvalidateAndClearStaleProbe(t *testing.T) {
	for _, kind := range []string{"root", "model", "credential-ref", "key", "delete-key"} {
		t.Run(kind, func(t *testing.T) {
			n := newNode(t, &fakeExecutor{})
			n.app.mu.Lock()
			n.app.probeStatus = "passed"
			n.app.mu.Unlock()
			response := saveNodeSettings(t, n, func(v map[string]any) {
				switch kind {
				case "model":
					selection := n.cfg.Get().LLM
					for _, descriptor := range llm.Available() {
						if descriptor.ID == selection.Provider {
							for _, model := range descriptor.Models {
								if model != selection.Model {
									selection.Model = model
									break
								}
							}
						}
					}
					v["llm"] = selection
				case "root":
					v["root"] = filepath.Join(t.TempDir(), "another")
				case "credential-ref":
					selection := n.cfg.Get().LLM
					selection.CredentialRef = "alternate"
					v["llm"] = selection
				case "key":
					v["key"] = "synthetic-new-key"
				case "delete-key":
					v["delete_key"] = true
				}
			})
			if response.Code != 200 || n.cfg.Get().ModelVerified {
				t.Fatal(response.Code, response.Body.String())
			}
			probe := httptest.NewRecorder()
			n.app.probe(probe, httptest.NewRequest(http.MethodGet, "/api/probe", nil))
			if !bytes.Contains(probe.Body.Bytes(), []byte(`"not_run"`)) {
				t.Fatal("stale passed", probe.Body.String())
			}
		})
	}
}

func TestInvalidListenerDoesNotPersistSettings(t *testing.T) {
	n := newNode(t, &fakeExecutor{})
	before := n.cfg.Get()
	response := saveNodeSettings(t, n, func(v map[string]any) { v["name"] = "should-not-save"; v["peer_listen"] = "invalid" })
	if response.Code != 400 || n.cfg.Get().Name != before.Name || !n.cfg.Get().ModelVerified {
		t.Fatal(response.Body.String())
	}
}

func TestMetadataSaveKeepsFailedProbeDiagnostic(t *testing.T) {
	n := newNode(t, &fakeExecutor{})
	c := n.cfg.Get()
	c.ModelVerified = false
	if err := n.cfg.Save(c); err != nil {
		t.Fatal(err)
	}
	n.app.mu.Lock()
	n.app.probeStatus = "failed"
	n.app.probeError = "synthetic failure"
	n.app.mu.Unlock()
	response := saveNodeSettings(t, n, func(v map[string]any) { v["name"] = "rename while unready" })
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	probe := httptest.NewRecorder()
	n.app.probe(probe, httptest.NewRequest(http.MethodGet, "/api/probe", nil))
	if !bytes.Contains(probe.Body.Bytes(), []byte("synthetic failure")) {
		t.Fatal("lost failed probe diagnostic")
	}
}
