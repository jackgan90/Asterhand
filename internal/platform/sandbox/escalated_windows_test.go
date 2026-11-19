//go:build windows

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestEscalatedOrdinaryTokenAllowsExternalWriteAndEnforcesMediumIL(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "workspace")
	tmp := filepath.Join(base, "journal", "sandbox-temp")
	home := filepath.Join(base, "asterhand-home")
	ext := filepath.Join(base, "external-data")
	for _, dir := range []string{ws, home, ext} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	be := NewBackend()
	if err := be.Prepare(ws, tmp); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(ext, "report.txt")
	cmd := `whoami /groups | findstr /C:"S-1-16-8192" && Set-Content -LiteralPath "` + target + `" -Value "tier2-ok" -Encoding UTF8`
	// PowerShell accepts semicolon-separated commands.
	cmd = `whoami /groups; Set-Content -LiteralPath "` + target + `" -Value "tier2-ok" -Encoding UTF8`
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	proc, err := be.Start(ctx, SpawnSpec{
		WorkspaceRoot: ws,
		TempRoot:      tmp,
		HomeRoot:      home,
		Command:       cmd,
		Workdir:       ext,
		Timeout:       15 * time.Second,
		ProtectedDirs: DefaultProtectedDirs(home),
		Escalated:     true,
		ExternalDirs:  []string{ext},
		JobName:       `Local\Asterhand.Exec.TestMediumIL`,
	})
	if err != nil {
		t.Fatalf("be.Start escalated failed: %v", err)
	}
	defer proc.Close()
	<-proc.Done()

	info := proc.ExitInfo()
	out := proc.OutputSnapshot(0, MaxOutputSessionBytes).Output
	if !info.Exited || info.ExitCode != 0 {
		t.Fatalf("unexpected escalated exit: %+v out=%q", info, out)
	}
	if !strings.Contains(out, "S-1-16-8192") {
		t.Fatalf("expected Medium Mandatory Level S-1-16-8192 in whoami output, got: %q", out)
	}
	data, err := os.ReadFile(target)
	if err != nil || !strings.Contains(string(data), "tier2-ok") {
		t.Fatalf("expected external file written by Tier 2, got data=%q err=%v", string(data), err)
	}
}

func TestEscalatedHardBlocksProtectedHomeElevationVerbsAndUnlistedWorkdir(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "workspace")
	tmp := filepath.Join(base, "journal", "sandbox-temp")
	home := filepath.Join(base, "asterhand-home")
	extAllowed := filepath.Join(base, "allowed")
	extOther := filepath.Join(base, "other")
	for _, dir := range []string{ws, home, extAllowed, extOther} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	be := NewBackend()

	cases := []struct {
		name string
		spec SpawnSpec
		want string
	}{
		{
			name: "runas-verb",
			spec: SpawnSpec{WorkspaceRoot: ws, TempRoot: tmp, HomeRoot: home, Command: `Start-Process cmd -Verb RunAs`, Escalated: true, ExternalDirs: []string{extAllowed}},
			want: "elevation_verb_denied",
		},
		{
			name: "sudo-cmd",
			spec: SpawnSpec{WorkspaceRoot: ws, TempRoot: tmp, HomeRoot: home, Command: `echo ok; sudo whoami`, Escalated: true, ExternalDirs: nil},
			want: "elevation_verb_denied",
		},
		{
			name: "asterhand-home-secret",
			spec: SpawnSpec{WorkspaceRoot: ws, TempRoot: tmp, HomeRoot: home, Command: `Get-Content "` + filepath.Join(home, "config.json") + `"`, Escalated: true, ExternalDirs: nil},
			want: "sensitive_path_denied",
		},
		{
			name: "ssh-dir",
			spec: SpawnSpec{WorkspaceRoot: ws, TempRoot: tmp, HomeRoot: home, Command: `Get-ChildItem ~/.ssh`, Escalated: true, ExternalDirs: nil},
			want: "sensitive_path_denied",
		},
		{
			name: "unlisted-workdir",
			spec: SpawnSpec{WorkspaceRoot: ws, TempRoot: tmp, HomeRoot: home, Command: `Get-Location`, Workdir: extOther, Escalated: true, ExternalDirs: []string{extAllowed}},
			want: "sensitive_path_denied",
		},
		{
			name: "dangerous-env",
			spec: SpawnSpec{WorkspaceRoot: ws, TempRoot: tmp, HomeRoot: home, Command: `Write-Output ok`, Env: map[string]string{"NODE_OPTIONS": "--require evil.js"}, Escalated: true, ExternalDirs: []string{extAllowed}},
			want: "dangerous_env_denied",
		},
	}

	for _, tc := range cases {
		tc.spec.ProtectedDirs = DefaultProtectedDirs(home)
		tc.spec.Timeout = 10 * time.Second
		_, err := be.Start(context.Background(), tc.spec)
		if got := Code(err); got != tc.want {
			t.Fatalf("%s: Code(err)=%q (%v), want %q", tc.name, got, err, tc.want)
		}
	}
}

func TestEscalatedEnvironmentStripsSecretsAndPreservesNativeTempAndSystemTLS(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "workspace")
	tmp := filepath.Join(base, "journal", "sandbox-temp")
	home := filepath.Join(base, "asterhand-home")
	for _, dir := range []string{ws, home, tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DEEPSEEK_API_KEY", "secret-host-deepseek")
	t.Setenv("OPENCLAW_GATEWAY_TOKEN", "secret-host-gateway")
	t.Setenv("ASTERHAND_NODE_KEY", "secret-host-node")

	be := NewBackend()
	cmd := `Write-Output ("TMP=" + $env:TEMP + ";GIT_SSL=" + $env:GIT_SSL_BACKEND + ";DS=" + $env:DEEPSEEK_API_KEY + ";OC=" + $env:OPENCLAW_GATEWAY_TOKEN + ";AH=" + $env:ASTERHAND_NODE_KEY)`
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	proc, err := be.Start(ctx, SpawnSpec{
		WorkspaceRoot: ws,
		TempRoot:      tmp,
		HomeRoot:      home,
		Command:       cmd,
		Timeout:       15 * time.Second,
		ProtectedDirs: DefaultProtectedDirs(home),
		Escalated:     true,
		ExternalDirs:  []string{},
	})
	if err != nil {
		t.Fatalf("be.Start escalated failed: %v", err)
	}
	defer proc.Close()
	<-proc.Done()

	out := proc.OutputSnapshot(0, MaxOutputSessionBytes).Output
	if strings.Contains(out, "secret-host-") {
		t.Fatalf("secret env leaked into Tier 2 child: %q", out)
	}
	if strings.Contains(out, filepath.Clean(tmp)) {
		t.Fatalf("Tier 2 should preserve native TEMP instead of redirecting to sandbox-temp: %q", out)
	}
	if strings.Contains(out, "GIT_SSL=openssl") {
		t.Fatalf("Tier 2 should not force GIT_SSL_BACKEND=openssl: %q", out)
	}
	_ = windows.S_OK
}

func TestEscalatedNamedJobTerminatesChildTree(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "workspace")
	tmp := filepath.Join(base, "journal", "sandbox-temp")
	home := filepath.Join(base, "asterhand-home")
	for _, dir := range []string{ws, home, tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	be := NewBackend()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	proc, err := be.Start(ctx, SpawnSpec{
		WorkspaceRoot: ws,
		TempRoot:      tmp,
		HomeRoot:      home,
		Command:       `Start-Process -NoNewWindow -FilePath powershell -ArgumentList "-NoProfile -Command Start-Sleep -Seconds 30"; Start-Sleep -Seconds 30`,
		Timeout:       30 * time.Second,
		ProtectedDirs: DefaultProtectedDirs(home),
		Escalated:     true,
		ExternalDirs:  []string{},
		JobName:       `Local\Asterhand.Exec.TestTreeKill`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Close()
	time.Sleep(350 * time.Millisecond)
	proc.Terminate("cancelled")
	select {
	case <-proc.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("escalated job tree did not terminate after Terminate()")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		stopped, err := proc.ProcessesStopped()
		if err == nil && stopped {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("escalated job child processes did not drain")
}
