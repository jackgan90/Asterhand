//go:build windows

package launch

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInstallerLauncherMutexInterop(t *testing.T) {
	installer, err := filepath.Abs(filepath.Join("..", "..", "install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, shell := range []string{"powershell.exe", "pwsh.exe"} {
		t.Run(shell, func(t *testing.T) {
			program, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("PowerShell host unavailable: %s", shell)
			}
			cases := []struct {
				name     string
				junction bool
			}{
				{"ASCII-Data", false}, {"中文 空格-data", false},
				{"unicode-İ-data", false}, {"unicode-𐐀-data", false},
				{"junction-İ-data", true},
			}
			for _, test := range cases {
				t.Run(test.name, func(t *testing.T) {
					home := filepath.Join(t.TempDir(), test.name)
					if err := os.Mkdir(home, 0700); err != nil {
						t.Fatal(err)
					}
					home, err := canonicalPath(home)
					if err != nil {
						t.Fatal(err)
					}
					releaseInstaller := holdInstallerMutex(t, program, installer, home, test.junction)
					ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
					defer cancel()
					unlock, err := acquireLaunchLock(ctx, home)
					if err == nil {
						unlock()
						t.Fatal("launcher acquired lock while installer held the same data directory")
					}
					releaseInstaller()
					ctx, cancel = context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					unlock, err = acquireLaunchLock(ctx, home)
					if err != nil {
						t.Fatal(err)
					}
					unlock()
				})
			}
		})
	}
}

func holdInstallerMutex(t *testing.T, shell, installer, home string, junction bool) func() {
	t.Helper()
	// Load the real installer functions without running installation or touching
	// Desktop/Startup. Keep the owning PowerShell thread alive until released.
	const script = `param([string]$Installer, [string]$Data, [switch]$Junction)
$ErrorActionPreference = 'Stop'
$tokens = $null; $errors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($Installer, [ref]$tokens, [ref]$errors)
if ($errors.Count) { throw 'Installer syntax is invalid' }
foreach ($name in @('Initialize-InstallNative', 'Get-LaunchMutexName')) {
    $fn = $ast.Find({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $name}, $true)
    if (-not $fn) { throw "Missing installer function: $name" }
    . ([scriptblock]::Create($fn.Extent.Text))
}
$alias = $null
if ($Junction) {
    $alias = Join-Path ([IO.Path]::GetDirectoryName($Data)) 'install-alias'
    New-Item -ItemType Junction -Path $alias -Target $Data | Out-Null
    $Data = $alias
}
$mutex = New-Object Threading.Mutex($false, (Get-LaunchMutexName $Data))
if (-not $mutex.WaitOne(1000)) { throw 'Isolated installer mutex is already held' }
try { [Console]::WriteLine('ready'); [Console]::In.ReadLine() | Out-Null }
finally {
    $mutex.ReleaseMutex(); $mutex.Dispose()
    if ($alias) { Remove-Item -LiteralPath $alias -Force }
}
`
	path := filepath.Join(t.TempDir(), "hold-installer-mutex.ps1")
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	cmd := exec.CommandContext(ctx, shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", path, "-Installer", installer, "-Data", home)
	if junction {
		cmd.Args = append(cmd.Args, "-Junction")
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		cancel()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		cancel()
		t.Fatal(err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		stdin.Close()
		err := cmd.Wait()
		cancel()
		if err != nil {
			t.Errorf("installer lock helper: %v: %s", err, stderr.String())
		}
	}
	t.Cleanup(release)
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		release()
		t.Fatalf("installer lock helper did not become ready: %v: %s", scanner.Err(), stderr.String())
	}
	return release
}
