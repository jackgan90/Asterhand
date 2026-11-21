package harnesses

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackgan90/Asterhand/internal/automation"
)

func TestCallerCredentialRevocationAndSwitchRecalculation(t *testing.T) {
	home := t.TempDir()
	agCfg := filepath.Join(t.TempDir(), "mcp_config.json")
	svc, err := Open(home, `C:\Asterhand\asterhand.exe`)
	if err != nil {
		t.Fatal(err)
	}
	svc.AntigravityConfigPath = agCfg

	tr, fl := true, false
	if _, err := svc.UpdatePolicy(UpdatePolicyInput{
		HarnessID:           HarnessAntigravity,
		GeneralToolsEnabled: &tr,
		CollabCallerEnabled: &tr,
		ReconcileMCP:        true,
	}); err != nil {
		t.Fatal(err)
	}

	tok, err := svc.CallerTokenForTest(HarnessAntigravity)
	if err != nil || len(tok) != 64 {
		t.Fatalf("expected 64-hex caller token, got len=%d err=%v", len(tok), err)
	}
	p, ok, err := svc.AuthenticateCaller(tok)
	if err != nil || !ok || !p.AllowsToolset(automation.ToolsetGeneral) || !p.AllowsToolset(automation.ToolsetCollab) {
		t.Fatalf("expected both toolsets allowed: p=%+v ok=%v err=%v", p, ok, err)
	}

	// Turn off collab only -> same token still valid for general, denied for collab!
	if _, err := svc.UpdatePolicy(UpdatePolicyInput{
		HarnessID:           HarnessAntigravity,
		CollabCallerEnabled: &fl,
	}); err != nil {
		t.Fatal(err)
	}
	p2, ok, err := svc.AuthenticateCaller(tok)
	if err != nil || !ok || !p2.AllowsToolset(automation.ToolsetGeneral) || p2.AllowsToolset(automation.ToolsetCollab) {
		t.Fatalf("expected general-only after turning off collab: p=%+v ok=%v err=%v", p2, ok, err)
	}

	// Revoke entire credential -> old token immediately rejected.
	if _, err := svc.RevokeCallerCredential(HarnessAntigravity); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := svc.AuthenticateCaller(tok); ok {
		t.Fatal("expected revoked token to fail authentication")
	}
}

func TestAntigravityMCPConflictAndNoFullBackup(t *testing.T) {
	home := t.TempDir()
	agCfg := filepath.Join(t.TempDir(), "mcp_config.json")
	// Pre-populate with another server carrying a secret header AND an unmanaged asterhand entry.
	initial := map[string]any{
		"mcpServers": map[string]any{
			"other": map[string]any{
				"url":     "https://example.invalid/mcp",
				"headers": map[string]any{"Authorization": "Bearer super-secret-token"},
			},
			"asterhand": map[string]any{
				"command": "C:\\manual\\asterhand.exe",
				"args":    []string{"mcp"},
			},
		},
	}
	b, _ := json.MarshalIndent(initial, "", "  ")
	if err := os.WriteFile(agCfg, b, 0600); err != nil {
		t.Fatal(err)
	}

	obs, rec, err := ReconcileAntigravityMCP(agCfg, `C:\Asterhand\asterhand.exe`, home, true, false)
	if err == nil || obs != MCPObservedConflict || rec != nil {
		t.Fatalf("expected conflict without takeover, got obs=%s rec=%v err=%v", obs, rec, err)
	}

	// With explicit takeover, replaces ONLY asterhand key and preserves other server intact.
	obs, rec, err = ReconcileAntigravityMCP(agCfg, `C:\Asterhand\asterhand.exe`, home, true, true)
	if err != nil || obs != MCPObservedRegistered || rec == nil {
		t.Fatalf("expected registered after takeover, got obs=%s err=%v", obs, err)
	}
	after, _, err := readJSONObject(agCfg)
	if err != nil {
		t.Fatal(err)
	}
	servers := after["mcpServers"].(map[string]any)
	other := servers["other"].(map[string]any)
	if other["headers"].(map[string]any)["Authorization"] != "Bearer super-secret-token" {
		t.Fatal("other server entry was modified")
	}

	// Unregister removes only asterhand entry.
	obs, _, err = ReconcileAntigravityMCP(agCfg, `C:\Asterhand\asterhand.exe`, home, false, false)
	if err != nil || obs != MCPObservedNotConfigured {
		t.Fatalf("expected not_configured after unregister, got obs=%s err=%v", obs, err)
	}
	after2, _, _ := readJSONObject(agCfg)
	if _, exists := after2["mcpServers"].(map[string]any)["other"]; !exists {
		t.Fatal("other server should remain after unregister")
	}
}

func TestWorkspaceResolutionAndSensitivePathDenylist(t *testing.T) {
	home := t.TempDir()
	wsDir := t.TempDir()
	outside := t.TempDir()
	svc, err := Open(home, `C:\Asterhand\asterhand.exe`)
	if err != nil {
		t.Fatal(err)
	}
	// Normal project directory resolves without manual registration and derives stable ID.
	ws1, _, err := svc.ResolveWorkspace(wsDir, AccessReadOnly)
	if err != nil {
		t.Fatalf("expected normal project dir allowed: %v", err)
	}
	ws2, _, err := svc.ResolveWorkspace(wsDir, AccessReadOnly)
	if err != nil || ws1.ID != ws2.ID {
		t.Fatalf("expected deterministic workspace ID, got %s vs %s (%v)", ws1.ID, ws2.ID, err)
	}
	// M1 caps access mode to read_only.
	if _, _, err := svc.ResolveWorkspace(wsDir, AccessWorkspaceWrite); err == nil {
		t.Fatal("expected workspace_write rejected in M1 read-only mode")
	}
	// Artifact path escaping workspace root is rejected.
	if _, err := CheckArtifactPath(ws1, filepath.Join(outside, "secret.txt")); err == nil {
		t.Fatal("expected outside artifact path rejected")
	}
	// Sensitive paths: Asterhand node home, user home itself, filesystem root, and OS temp root must be blocked.
	if _, _, err := svc.ResolveWorkspace(home, AccessReadOnly); err == nil {
		t.Fatal("expected node home directory rejected")
	}
	if userHome, err := os.UserHomeDir(); err == nil && userHome != "" {
		if _, _, err := svc.ResolveWorkspace(userHome, AccessReadOnly); err == nil {
			t.Fatal("expected user home root rejected")
		}
		if _, _, err := svc.ResolveWorkspace(filepath.VolumeName(userHome)+string(filepath.Separator), AccessReadOnly); err == nil {
			t.Fatal("expected filesystem root rejected")
		}
	}
	if _, _, err := svc.ResolveWorkspace(os.TempDir(), AccessReadOnly); err == nil {
		t.Fatal("expected OS temp root itself rejected")
	}
	// Worker revocation callback fires when disabling worker switch.
	revokedWorker := ""
	svc.OnRevokeWorker = func(id string) { revokedWorker = id }
	tr := true
	fl := false
	if _, err := svc.UpdatePolicy(UpdatePolicyInput{HarnessID: HarnessCodex, CollabWorkerEnabled: &tr}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdatePolicy(UpdatePolicyInput{HarnessID: HarnessCodex, CollabWorkerEnabled: &fl}); err != nil {
		t.Fatal(err)
	}
	if revokedWorker != HarnessCodex {
		t.Fatalf("expected OnRevokeWorker callback for codex, got %q", revokedWorker)
	}
}

func TestAntigravityEmptyFileObservedAsNotConfigured(t *testing.T) {
	home := t.TempDir()
	agCfg := filepath.Join(t.TempDir(), "mcp_config.json")
	if err := os.WriteFile(agCfg, nil, 0600); err != nil {
		t.Fatal(err)
	}
	obs, err := ObserveAntigravityMCP(agCfg, home)
	if err != nil || obs != MCPObservedNotConfigured {
		t.Fatalf("expected not_configured for 0-byte mcp_config.json, got obs=%s err=%v", obs, err)
	}
	obs, _, err = ReconcileAntigravityMCP(agCfg, `C:\Asterhand\asterhand.exe`, home, true, false)
	if err != nil || obs != MCPObservedRegistered {
		t.Fatalf("expected registered from 0-byte mcp_config.json, got obs=%s err=%v", obs, err)
	}
}

func TestAntigravityStartupReconcileUpdatesExecutablePath(t *testing.T) {
	home := t.TempDir()
	agCfg := filepath.Join(t.TempDir(), "mcp_config.json")

	svc1, err := Open(home, `C:\Asterhand\versions\v1\asterhand.exe`)
	if err != nil {
		t.Fatal(err)
	}
	svc1.AntigravityConfigPath = agCfg
	tr := true
	if _, err := svc1.UpdatePolicy(UpdatePolicyInput{
		HarnessID:           HarnessAntigravity,
		CollabCallerEnabled: &tr,
		ReconcileMCP:        true,
	}); err != nil {
		t.Fatal(err)
	}

	// Simulate install-local upgrade: node restarts from v2 executable with same home.
	svc2, err := Open(home, `C:\Asterhand\versions\v2\asterhand.exe`)
	if err != nil {
		t.Fatal(err)
	}
	svc2.AntigravityConfigPath = agCfg
	svc2.ReconcileManagedOnStartup()

	root, exists, err := readJSONObject(agCfg)
	if err != nil || !exists {
		t.Fatalf("expected mcp_config.json readable: exists=%v err=%v", exists, err)
	}
	entry := root["mcpServers"].(map[string]any)["asterhand"].(map[string]any)
	if entry["command"] != `C:\Asterhand\versions\v2\asterhand.exe` {
		t.Fatalf("expected startup reconcile to refresh command to v2, got %+v", entry["command"])
	}
}
