//go:build windows

package launch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type shortcutFixture struct {
	installRoot string
	homeDir     string
	desktopDir  string
	exePath     string
	launcher    string
	manager     *ShortcutManager
}

func newShortcutFixture(t *testing.T) shortcutFixture {
	t.Helper()
	base := t.TempDir()
	installRoot := filepath.Join(base, "星伴 Program")
	homeDir := filepath.Join(base, "星伴 Data")
	desktopDir := filepath.Join(base, "星伴 Desktop")
	exeDir := filepath.Join(installRoot, "versions", "v-test")
	if err := os.MkdirAll(exeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(homeDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(desktopDir, 0755); err != nil {
		t.Fatal(err)
	}
	exePath := filepath.Join(exeDir, "asterhand.exe")
	launcher := filepath.Join(installRoot, "AsterhandLauncher.exe")
	if err := os.WriteFile(exePath, []byte("exe-stub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher, []byte("launcher-stub"), 0755); err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(Installation{
		Schema:     1,
		Executable: exePath,
		Data:       homeDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installRoot, "installation.json"), record, 0600); err != nil {
		t.Fatal(err)
	}
	mgr := NewShortcutManagerWith(ShortcutOptions{
		Home:       homeDir,
		Executable: func() (string, error) { return exePath, nil },
		DesktopDir: func() (string, error) { return desktopDir, nil },
	})
	return shortcutFixture{
		installRoot: installRoot,
		homeDir:     homeDir,
		desktopDir:  desktopDir,
		exePath:     exePath,
		launcher:    launcher,
		manager:     mgr,
	}
}

func runPowerShellEnv(t *testing.T, script string, env map[string]string) string {
	t.Helper()
	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", script)
	cmd.Env = append([]string{}, os.Environ()...)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("powershell failed: %v\n%s", err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func TestShortcutLifecycleAndWScriptInterop(t *testing.T) {
	fx := newShortcutFixture(t)
	ctx := context.Background()

	st := fx.manager.Status()
	if !st.Supported || !st.Available || st.Enabled || st.Conflict {
		t.Fatalf("unexpected initial status: %+v", st)
	}

	st, err := fx.manager.Set(ctx, ShortcutChange{Operation: "op-enable-1", Enabled: true})
	if err != nil {
		t.Fatalf("enable failed: %v", err)
	}
	if !st.Available || !st.Enabled || st.Conflict || st.LastOperation != "op-enable-1" {
		t.Fatalf("unexpected enabled status: %+v", st)
	}
	lnkPath := filepath.Join(fx.desktopDir, shortcutFileName)
	if _, statErr := os.Stat(lnkPath); statErr != nil {
		t.Fatalf("expected shortcut file at %s: %v", lnkPath, statErr)
	}

	// Verify generated .lnk with Windows WScript.Shell COM reader.
	psRead := `$s = (New-Object -ComObject WScript.Shell).CreateShortcut($env:LNK_PATH); [pscustomobject]@{TargetPath=$s.TargetPath; Arguments=$s.Arguments; WorkingDirectory=$s.WorkingDirectory; IconLocation=$s.IconLocation; Description=$s.Description; WindowStyle=$s.WindowStyle} | ConvertTo-Json -Compress`
	raw := runPowerShellEnv(t, psRead, map[string]string{"LNK_PATH": lnkPath})
	var info struct {
		TargetPath       string `json:"TargetPath"`
		Arguments        string `json:"Arguments"`
		WorkingDirectory string `json:"WorkingDirectory"`
		IconLocation     string `json:"IconLocation"`
		Description      string `json:"Description"`
		WindowStyle      int    `json:"WindowStyle"`
	}
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatalf("parse ps shortcut json %q: %v", raw, err)
	}
	if !samePath(info.TargetPath, fx.launcher) || !samePath(info.WorkingDirectory, fx.installRoot) {
		t.Fatalf("unexpected WScript shortcut paths: %+v", info)
	}
	if info.Arguments != "" || info.Description != shortcutDescription || info.WindowStyle != 7 {
		t.Fatalf("unexpected WScript shortcut metadata: %+v", info)
	}
	if !samePath(info.IconLocation, fx.launcher+",0") {
		t.Fatalf("unexpected WScript shortcut icon location: %q", info.IconLocation)
	}

	// Manual deletion on desktop is reflected immediately without cache drift.
	if err := os.Remove(lnkPath); err != nil {
		t.Fatal(err)
	}
	st = fx.manager.Status()
	if st.Enabled || st.Conflict {
		t.Fatalf("expected disabled status after manual delete, got: %+v", st)
	}

	// Legacy start.ps1 shortcut created via WScript.Shell is recognized as owned and removable.
	psCreateLegacy := `$s = (New-Object -ComObject WScript.Shell).CreateShortcut($env:LNK_PATH); $s.TargetPath = (Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'); $s.Arguments = '-NoProfile -WindowStyle Hidden -File "' + (Join-Path $env:INSTALL_ROOT 'start.ps1') + '" -NoBrowser'; $s.WorkingDirectory = $env:INSTALL_ROOT; $s.Save()`
	runPowerShellEnv(t, psCreateLegacy, map[string]string{"LNK_PATH": lnkPath, "INSTALL_ROOT": fx.installRoot})
	st = fx.manager.Status()
	if !st.Enabled || st.Conflict {
		t.Fatalf("expected legacy shortcut recognized as owned enabled, got: %+v", st)
	}
	st, err = fx.manager.Set(ctx, ShortcutChange{Operation: "op-disable-1", Enabled: false})
	if err != nil {
		t.Fatalf("disable failed: %v", err)
	}
	if st.Enabled || st.Conflict || st.LastOperation != "op-disable-1" {
		t.Fatalf("unexpected disabled status: %+v", st)
	}
	if _, err := os.Stat(lnkPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected shortcut removed, got err=%v", err)
	}
}

func TestShortcutExternalConflictAndNotInstalledAndIdempotency(t *testing.T) {
	fx := newShortcutFixture(t)
	ctx := context.Background()
	lnkPath := filepath.Join(fx.desktopDir, shortcutFileName)

	// External .lnk pointing elsewhere causes conflict on inspect and 409 on enable, never deleted on disable.
	psExternal := `$s = (New-Object -ComObject WScript.Shell).CreateShortcut($env:LNK_PATH); $s.TargetPath = (Join-Path $env:SystemRoot 'System32\notepad.exe'); $s.Save()`
	runPowerShellEnv(t, psExternal, map[string]string{"LNK_PATH": lnkPath})
	originalBytes, err := os.ReadFile(lnkPath)
	if err != nil {
		t.Fatal(err)
	}

	st := fx.manager.Status()
	if !st.Available || st.Enabled || !st.Conflict || st.Reason != "conflict" {
		t.Fatalf("expected conflict status, got: %+v", st)
	}
	st, err = fx.manager.Set(ctx, ShortcutChange{Operation: "op-conflict-on", Enabled: true})
	var se *ShortcutError
	if !errors.As(err, &se) || se.Status != 409 || se.Code != "conflict" {
		t.Fatalf("expected 409 conflict error, got st=%+v err=%v", st, err)
	}
	afterBytes, err := os.ReadFile(lnkPath)
	if err != nil || !bytes.Equal(originalBytes, afterBytes) {
		t.Fatalf("external shortcut modified after conflict enable: err=%v", err)
	}

	// Disabling during external conflict is a safe no-op that preserves the external shortcut.
	st, err = fx.manager.Set(ctx, ShortcutChange{Operation: "op-conflict-off", Enabled: false})
	if err != nil {
		t.Fatalf("expected disable no-op on external conflict, got: %v", err)
	}
	if !st.Conflict || st.Enabled {
		t.Fatalf("expected conflict preserved, got: %+v", st)
	}
	afterBytes, err = os.ReadFile(lnkPath)
	if err != nil || !bytes.Equal(originalBytes, afterBytes) {
		t.Fatalf("external shortcut deleted or altered on disable")
	}

	// Operation ID idempotency and mismatch conflict.
	if err := os.Remove(lnkPath); err != nil {
		t.Fatal(err)
	}
	first, err := fx.manager.Set(ctx, ShortcutChange{Operation: "op-idemp-1", Enabled: true})
	if err != nil || !first.Enabled {
		t.Fatalf("first op failed: %+v %v", first, err)
	}
	if err := os.Remove(lnkPath); err != nil {
		t.Fatal(err)
	}
	replay, err := fx.manager.Set(ctx, ShortcutChange{Operation: "op-idemp-1", Enabled: true})
	if err != nil || !replay.Enabled {
		t.Fatalf("idempotent replay did not return cached outcome: %+v %v", replay, err)
	}
	_, err = fx.manager.Set(ctx, ShortcutChange{Operation: "op-idemp-1", Enabled: false})
	if !errors.As(err, &se) || se.Status != 409 {
		t.Fatalf("expected 409 on reused operation_id with different enabled, got: %v", err)
	}

	// Uninstalled / missing launcher returns not_installed.
	if err := os.Remove(fx.launcher); err != nil {
		t.Fatal(err)
	}
	uninstalled := fx.manager.Status()
	if !uninstalled.Supported || uninstalled.Available || uninstalled.Reason != "not_installed" {
		t.Fatalf("expected not_installed status, got: %+v", uninstalled)
	}
	_, err = fx.manager.Set(ctx, ShortcutChange{Operation: "op-uninstalled-1", Enabled: true})
	if !errors.As(err, &se) || se.Code != "not_installed" {
		t.Fatalf("expected not_installed error, got: %v", err)
	}
}
