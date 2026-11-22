package harnesses

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jackgan90/Asterhand/internal/automation"
)

func isolatedService(t *testing.T, home, exe string) *Service {
	t.Helper()
	svc, err := Open(home, exe)
	if err != nil {
		t.Fatal(err)
	}
	svc.AntigravityConfigPath = filepath.Join(t.TempDir(), "mcp_config.json")
	svc.ClaudeConfigPath = filepath.Join(t.TempDir(), "claude.json")
	svc.DisableVersionProbe = true
	svc.CmdRunner = func(ctx context.Context, exe string, args ...string) (string, string, error) {
		return "", "No MCP server named 'asterhand' found.", errors.New("exit status 1")
	}
	return svc
}

func TestCallerCredentialRevocationAndSwitchRecalculation(t *testing.T) {
	home := t.TempDir()
	svc := isolatedService(t, home, `C:\Asterhand\asterhand.exe`)

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

func TestAllEightSwitchCombinationsMatrix(t *testing.T) {
	home := t.TempDir()
	svc := isolatedService(t, home, `C:\Asterhand\asterhand.exe`)

	fakeCodex := filepath.Join(t.TempDir(), "codex.exe")
	if err := os.WriteFile(fakeCodex, []byte("stub"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdatePolicy(UpdatePolicyInput{HarnessID: HarnessCodex, ManualPath: &fakeCodex}); err != nil {
		t.Fatal(err)
	}

	bools := []bool{false, true}
	for _, gen := range bools {
		for _, caller := range bools {
			for _, worker := range bools {
				g, c, w := gen, caller, worker
				st, err := svc.UpdatePolicy(UpdatePolicyInput{
					HarnessID:           HarnessCodex,
					GeneralToolsEnabled: &g,
					CollabCallerEnabled: &c,
					CollabWorkerEnabled: &w,
				})
				if err != nil {
					t.Fatalf("combo (%v,%v,%v): %v", gen, caller, worker, err)
				}
				var view HarnessView
				for _, h := range st.Harnesses {
					if h.ID == HarnessCodex {
						view = h
					}
				}
				wantDesired := gen || caller
				if view.MCPDesired != wantDesired {
					t.Fatalf("combo (%v,%v,%v): MCPDesired=%v want %v", gen, caller, worker, view.MCPDesired, wantDesired)
				}
				if view.CallerCredentialActive != wantDesired {
					t.Fatalf("combo (%v,%v,%v): CallerCredentialActive=%v want %v", gen, caller, worker, view.CallerCredentialActive, wantDesired)
				}
				if wantDesired {
					tok, err := svc.CallerTokenForTest(HarnessCodex)
					if err != nil {
						t.Fatalf("combo (%v,%v,%v): missing caller token: %v", gen, caller, worker, err)
					}
					p, ok, err := svc.AuthenticateCaller(tok)
					if err != nil || !ok {
						t.Fatalf("combo (%v,%v,%v): auth failed ok=%v err=%v", gen, caller, worker, ok, err)
					}
					if p.AllowsToolset(automation.ToolsetGeneral) != gen || p.AllowsToolset(automation.ToolsetCollab) != caller {
						t.Fatalf("combo (%v,%v,%v): unexpected toolsets %+v", gen, caller, worker, p.Toolsets)
					}
				}
				gotWorker, _ := svc.IsWorkerEnabled(HarnessCodex)
				if gotWorker != worker {
					t.Fatalf("combo (%v,%v,%v): IsWorkerEnabled=%v want %v", gen, caller, worker, gotWorker, worker)
				}
			}
		}
	}
}

func TestAntigravityMCPConflictAndNoFullBackup(t *testing.T) {
	home := t.TempDir()
	agCfg := filepath.Join(t.TempDir(), "mcp_config.json")
	spec := MCPEntrySpec{
		CallerID: HarnessAntigravity,
		NodeHome: home,
		Command:  `C:\Asterhand\asterhand.exe`,
		Args:     []string{"mcp", "--home", home, "--caller", HarnessAntigravity},
	}
	initial := map[string]any{
		"mcpServers": map[string]any{
			"other": map[string]any{
				"url":     "https://example.invalid/mcp",
				"headers": map[string]any{"Authorization": "Bearer super-secret-token"},
			},
			"asterhand": map[string]any{
				"command": `C:\manual\asterhand.exe`,
				"args":    []string{"mcp"},
			},
		},
	}
	b, _ := json.MarshalIndent(initial, "", "  ")
	if err := os.WriteFile(agCfg, b, 0600); err != nil {
		t.Fatal(err)
	}

	obs, rec, err := ReconcileAntigravityMCP(agCfg, spec, true, false)
	if err == nil || obs != MCPObservedConflict || rec != nil {
		t.Fatalf("expected conflict without takeover, got obs=%s rec=%v err=%v", obs, rec, err)
	}

	obs, rec, err = ReconcileAntigravityMCP(agCfg, spec, true, true)
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

	obs, _, err = ReconcileAntigravityMCP(agCfg, spec, false, false)
	if err != nil || obs != MCPObservedNotConfigured {
		t.Fatalf("expected not_configured after unregister, got obs=%s err=%v", obs, err)
	}
	after2, _, _ := readJSONObject(agCfg)
	if _, exists := after2["mcpServers"].(map[string]any)["other"]; !exists {
		t.Fatal("other server should remain after unregister")
	}
}

func TestCodexAndClaudeMCPReconcileAndConflictGuard(t *testing.T) {
	home := t.TempDir()
	codexSpec := MCPEntrySpec{
		CallerID: HarnessCodex,
		NodeHome: home,
		Command:  `C:\Asterhand\asterhand-mcp.exe`,
		Args:     []string{"--caller", HarnessCodex},
		StableCommand: `C:\Asterhand\asterhand-mcp.exe`,
	}

	// Simulate Codex CLI store with an unmanaged asterhand entry first.
	var codexEntry *codexMCPGetResponse = &codexMCPGetResponse{
		Name: "asterhand",
		Transport: &struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}{Type: "stdio", Command: `C:\custom\my-mcp.exe`, Args: []string{"serve"}},
	}
	var codexCalls [][]string
	codexRunner := func(ctx context.Context, exe string, args ...string) (string, string, error) {
		codexCalls = append(codexCalls, append([]string(nil), args...))
		if len(args) >= 2 && args[0] == "mcp" && args[1] == "get" {
			if codexEntry == nil {
				return "", "Error: No MCP server named 'asterhand' found.", errors.New("exit 1")
			}
			b, _ := json.Marshal(codexEntry)
			return string(b), "", nil
		}
		if len(args) >= 2 && args[0] == "mcp" && args[1] == "remove" {
			codexEntry = nil
			return "Removed", "", nil
		}
		if len(args) >= 5 && args[0] == "mcp" && args[1] == "add" && args[3] == "--" {
			codexEntry = &codexMCPGetResponse{
				Name: "asterhand",
				Transport: &struct {
					Type    string   `json:"type"`
					Command string   `json:"command"`
					Args    []string `json:"args"`
				}{Type: "stdio", Command: args[4], Args: args[5:]},
			}
			return "Added", "", nil
		}
		return "", "unexpected args", errors.New("unexpected")
	}

	obs, _, err := ReconcileCodexMCP(context.Background(), codexRunner, "codex.exe", codexSpec, true, false)
	if err == nil || obs != MCPObservedConflict {
		t.Fatalf("expected Codex conflict without takeover, got obs=%s err=%v", obs, err)
	}
	obs, rec, err := ReconcileCodexMCP(context.Background(), codexRunner, "codex.exe", codexSpec, true, true)
	if err != nil || obs != MCPObservedRegistered || rec == nil {
		t.Fatalf("expected Codex registered on takeover, got obs=%s err=%v", obs, err)
	}
	obs, err = ObserveCodexMCP(context.Background(), codexRunner, "codex.exe", codexSpec)
	if err != nil || obs != MCPObservedRegistered {
		t.Fatalf("expected Codex observe registered, got obs=%s err=%v", obs, err)
	}
	obs, _, err = ReconcileCodexMCP(context.Background(), codexRunner, "codex.exe", codexSpec, false, false)
	if err != nil || obs != MCPObservedNotConfigured {
		t.Fatalf("expected Codex unregistered, got obs=%s err=%v", obs, err)
	}

	// Claude Code: ~/.claude.json inspection + `claude mcp add/remove --scope user`.
	claudeCfg := filepath.Join(t.TempDir(), ".claude.json")
	if err := writeJSONObjectAtomic(claudeCfg, map[string]any{
		"mcpServers": map[string]any{
			"other": map[string]any{"command": "npx", "args": []string{"other-mcp"}},
			"asterhand": map[string]any{"command": "python", "args": []string{"manual.py"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	claudeSpec := MCPEntrySpec{
		CallerID: HarnessClaudeCode,
		NodeHome: home,
		Command:  `C:\Asterhand\asterhand-mcp.exe`,
		Args:     []string{"--caller", HarnessClaudeCode},
		StableCommand: `C:\Asterhand\asterhand-mcp.exe`,
	}
	claudeRunner := func(ctx context.Context, exe string, args ...string) (string, string, error) {
		root, _, _ := readJSONObject(claudeCfg)
		servers, _ := root["mcpServers"].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
		}
		if slices.Contains(args, "remove") {
			delete(servers, "asterhand")
		} else if slices.Contains(args, "add") {
			idx := slices.Index(args, "--")
			servers["asterhand"] = map[string]any{"command": args[idx+1], "args": args[idx+2:]}
		}
		root["mcpServers"] = servers
		_ = writeJSONObjectAtomic(claudeCfg, root)
		return "ok", "", nil
	}
	obs, _, err = ReconcileClaudeMCP(context.Background(), claudeRunner, "claude.exe", claudeCfg, claudeSpec, true, false)
	if err == nil || obs != MCPObservedConflict {
		t.Fatalf("expected Claude conflict without takeover, got obs=%s err=%v", obs, err)
	}
	obs, _, err = ReconcileClaudeMCP(context.Background(), claudeRunner, "claude.exe", claudeCfg, claudeSpec, true, true)
	if err != nil || obs != MCPObservedRegistered {
		t.Fatalf("expected Claude registered on takeover, got obs=%s err=%v", obs, err)
	}
	obs, err = ObserveClaudeMCP(claudeCfg, claudeSpec)
	if err != nil || obs != MCPObservedRegistered {
		t.Fatalf("expected Claude observe registered, got obs=%s err=%v", obs, err)
	}
	obs, _, err = ReconcileClaudeMCP(context.Background(), claudeRunner, "claude.exe", claudeCfg, claudeSpec, false, false)
	if err != nil || obs != MCPObservedNotConfigured {
		t.Fatalf("expected Claude not_configured after remove, got obs=%s err=%v", obs, err)
	}
	// Ensure other entry in ~/.claude.json was untouched.
	after, _, _ := readJSONObject(claudeCfg)
	if _, ok := after["mcpServers"].(map[string]any)["other"]; !ok {
		t.Fatal("other server in .claude.json should remain intact")
	}
}

func TestWorkspaceResolutionDenylistAndWriteOptIn(t *testing.T) {
	home := t.TempDir()
	wsDir := t.TempDir()
	outside := t.TempDir()
	svc := isolatedService(t, home, `C:\Asterhand\asterhand.exe`)

	ws1, _, err := svc.ResolveWorkspace(wsDir, AccessReadOnly)
	if err != nil {
		t.Fatalf("expected normal project dir allowed: %v", err)
	}
	ws2, _, err := svc.ResolveWorkspace(wsDir, AccessReadOnly)
	if err != nil || ws1.ID != ws2.ID {
		t.Fatalf("expected deterministic workspace ID, got %s vs %s (%v)", ws1.ID, ws2.ID, err)
	}
	// M2 allows workspace_write at workspace resolution level unless MaxAccessMode caps it.
	if _, _, err := svc.ResolveWorkspace(wsDir, AccessWorkspaceWrite); err != nil {
		t.Fatalf("expected workspace_write allowed by default in M2: %v", err)
	}
	svc.MaxAccessMode = AccessReadOnly
	if _, _, err := svc.ResolveWorkspace(wsDir, AccessWorkspaceWrite); err == nil {
		t.Fatal("expected explicit MaxAccessMode=read_only to cap workspace_write")
	}
	svc.MaxAccessMode = ""

	if _, err := CheckArtifactPath(ws1, filepath.Join(outside, "secret.txt")); err == nil {
		t.Fatal("expected outside artifact path rejected")
	}
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

	// Per-harness AllowWorkspaceWrite opt-in & revocation callbacks.
	revokedWorker := ""
	revokedWrite := ""
	svc.OnRevokeWorker = func(id string) { revokedWorker = id }
	svc.OnRevokeWorkerWrite = func(id string) { revokedWrite = id }
	tr, fl := true, false
	fakeCodex := filepath.Join(t.TempDir(), "codex.exe")
	_ = os.WriteFile(fakeCodex, []byte("stub"), 0600)
	if _, err := svc.UpdatePolicy(UpdatePolicyInput{HarnessID: HarnessCodex, CollabWorkerEnabled: &tr, ManualPath: &fakeCodex}); err != nil {
		t.Fatal(err)
	}
	// Write disabled by default even when worker is enabled!
	if _, err := svc.IsWorkerAccessEnabled(HarnessCodex, AccessWorkspaceWrite); err == nil {
		t.Fatal("expected workspace_write rejected until allow_workspace_write is enabled")
	}
	if _, err := svc.UpdatePolicy(UpdatePolicyInput{HarnessID: HarnessCodex, AllowWorkspaceWrite: &tr}); err != nil {
		t.Fatal(err)
	}
	if exe, err := svc.IsWorkerAccessEnabled(HarnessCodex, AccessWorkspaceWrite); err != nil || exe != fakeCodex {
		t.Fatalf("expected workspace_write allowed after opt-in: exe=%q err=%v", exe, err)
	}
	if _, err := svc.UpdatePolicy(UpdatePolicyInput{HarnessID: HarnessCodex, AllowWorkspaceWrite: &fl}); err != nil {
		t.Fatal(err)
	}
	if revokedWrite != HarnessCodex || revokedWorker != "" {
		t.Fatalf("expected only OnRevokeWorkerWrite to fire, got write=%q worker=%q", revokedWrite, revokedWorker)
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
	spec := MCPEntrySpec{
		CallerID: HarnessAntigravity,
		NodeHome: home,
		Command:  `C:\Asterhand\asterhand.exe`,
		Args:     []string{"mcp", "--home", home, "--caller", HarnessAntigravity},
	}
	obs, err := ObserveAntigravityMCP(agCfg, spec)
	if err != nil || obs != MCPObservedNotConfigured {
		t.Fatalf("expected not_configured for 0-byte mcp_config.json, got obs=%s err=%v", obs, err)
	}
	obs, _, err = ReconcileAntigravityMCP(agCfg, spec, true, false)
	if err != nil || obs != MCPObservedRegistered {
		t.Fatalf("expected registered from 0-byte mcp_config.json, got obs=%s err=%v", obs, err)
	}
}

func TestStableAsterhandMCPEntryMigrationOnStartup(t *testing.T) {
	installRoot := t.TempDir()
	dataDir := t.TempDir()
	v1Dir := filepath.Join(installRoot, "versions", "v1")
	v2Dir := filepath.Join(installRoot, "versions", "v2")
	_ = os.MkdirAll(v1Dir, 0700)
	_ = os.MkdirAll(v2Dir, 0700)
	exeV1 := filepath.Join(v1Dir, "asterhand.exe")
	exeV2 := filepath.Join(v2Dir, "asterhand.exe")
	stableMCP := filepath.Join(installRoot, "asterhand-mcp.exe")
	_ = os.WriteFile(exeV1, []byte("v1"), 0600)
	_ = os.WriteFile(exeV2, []byte("v2"), 0600)

	agCfg := filepath.Join(t.TempDir(), "mcp_config.json")

	// M1 setup: no asterhand-mcp.exe yet, registers legacy versioned asterhand.exe mcp --home <data> --caller antigravity.
	svc1 := isolatedService(t, dataDir, exeV1)
	svc1.AntigravityConfigPath = agCfg
	tr := true
	if _, err := svc1.UpdatePolicy(UpdatePolicyInput{
		HarnessID:           HarnessAntigravity,
		CollabCallerEnabled: &tr,
		ReconcileMCP:        true,
	}); err != nil {
		t.Fatal(err)
	}

	// M2 upgrade: installation.json points to v2 AND asterhand-mcp.exe is published at install root.
	_ = os.WriteFile(stableMCP, []byte("mcp-forwarder"), 0600)
	rec, _ := json.Marshal(map[string]any{"schema": 1, "executable": exeV2, "data": dataDir})
	_ = os.WriteFile(filepath.Join(installRoot, "installation.json"), rec, 0600)

	svc2 := isolatedService(t, dataDir, exeV2)
	svc2.AntigravityConfigPath = agCfg
	svc2.ReconcileManagedOnStartup()

	root, exists, err := readJSONObject(agCfg)
	if err != nil || !exists {
		t.Fatalf("expected mcp_config.json readable: exists=%v err=%v", exists, err)
	}
	entry := root["mcpServers"].(map[string]any)["asterhand"].(map[string]any)
	if !SamePath(entry["command"].(string), stableMCP) {
		t.Fatalf("expected startup reconcile to migrate to stable asterhand-mcp.exe, got %+v", entry["command"])
	}
	rawArgs := entry["args"].([]any)
	if len(rawArgs) != 2 || rawArgs[0] != "--caller" || rawArgs[1] != HarnessAntigravity {
		t.Fatalf("expected stable entry args [--caller antigravity], got %+v", rawArgs)
	}
}
