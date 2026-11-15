//go:build windows

package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

func runInSandbox(t *testing.T, be Backend, ws, tmp, home, cmd string, timeout time.Duration) (SessionProcess, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), maxDuration(timeout, 15*time.Second))
	defer cancel()
	proc, err := be.Start(ctx, SpawnSpec{
		WorkspaceRoot: ws,
		TempRoot:      tmp,
		HomeRoot:      home,
		Command:       cmd,
		Timeout:       timeout,
		ProtectedDirs: DefaultProtectedDirs(home),
	})
	if err != nil {
		t.Fatalf("be.Start(%q) failed: %v", cmd, err)
	}
	<-proc.Done()
	out := proc.OutputSnapshot(0, MaxOutputSessionBytes).Output
	return proc, out
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func TestE1WriteRestrictedCoreAndToolchain(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "workspace")
	tmp := filepath.Join(base, "journal", "sandbox-temp")
	home := filepath.Join(base, "asterhand-home")
	outside := filepath.Join(base, "outside-secret-target")
	if err := os.MkdirAll(ws, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0700); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "blocked.txt")
	userProfileTarget := filepath.Join(os.Getenv("USERPROFILE"), ".asterhand-sandbox-test-must-fail.tmp")
	_ = os.Remove(userProfileTarget)
	t.Cleanup(func() { _ = os.Remove(userProfileTarget) })

	be := NewBackend()
	if !be.Supported() {
		t.Fatal("expected Windows sandbox backend to be supported")
	}
	if err := be.Prepare(ws, tmp); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	// 1. Allowed write in workspace + TEMP/sandbox-temp + cmd > nul + icacls must fail (no WRITE_DAC).
	cmd := `
Set-Content -LiteralPath "hello.txt" -Value "ok-ws" -Encoding UTF8
$t = Join-Path $env:TEMP "sub.tmp"
Set-Content -LiteralPath $t -Value "ok-tmp" -Encoding UTF8
cmd /c "echo nul-ok > nul"
if ($LASTEXITCODE -ne 0) { Write-Output "NUL_FAIL=$LASTEXITCODE"; exit 2 }
cmd /c "icacls hello.txt /grant Everyone:F >nul 2>&1"
Write-Output "ICACLS_EC=$LASTEXITCODE"
`
	proc, out := runInSandbox(t, be, ws, tmp, home, cmd, 10*time.Second)
	defer proc.Close()
	info := proc.ExitInfo()
	if !info.Exited || info.ExitCode != 0 {
		t.Fatalf("expected clean exit, got %+v output:\n%s", info, out)
	}
	if !strings.Contains(out, "ICACLS_EC=5") {
		t.Fatalf("expected icacls access denied (EC=5), output:\n%s", out)
	}
	data, err := os.ReadFile(filepath.Join(ws, "hello.txt"))
	if err != nil || !strings.Contains(string(data), "ok-ws") {
		t.Fatalf("workspace write missing: data=%q err=%v", string(data), err)
	}

	// 2. Writes outside workspace (test outside dir + USERPROFILE) MUST be denied by kernel.
	procOut, out2 := runInSandbox(t, be, ws, tmp, home,
		`try { [IO.File]::WriteAllText('`+outsideFile+`', 'nope'); Write-Output 'UNEXPECTED_ALLOW' } catch { Write-Output ('DENIED:' + $_.Exception.HResult) }`,
		10*time.Second)
	defer procOut.Close()
	if strings.Contains(out2, "UNEXPECTED_ALLOW") || !strings.Contains(out2, "DENIED:") {
		t.Fatalf("expected outside write denial, got:\n%s", out2)
	}
	if _, err := os.Stat(outsideFile); err == nil {
		t.Fatalf("outside file %s should not exist", outsideFile)
	}

	procProf, out3 := runInSandbox(t, be, ws, tmp, home,
		`try { [IO.File]::WriteAllText('`+userProfileTarget+`', 'nope'); Write-Output 'UNEXPECTED_ALLOW_UP' } catch { Write-Output 'DENIED_UP' }`,
		10*time.Second)
	defer procProf.Close()
	if strings.Contains(out3, "UNEXPECTED_ALLOW_UP") || !strings.Contains(out3, "DENIED_UP") {
		t.Fatalf("expected USERPROFILE write denial, got:\n%s", out3)
	}

	verifyOptionalSandboxToolchains(t, be, ws, tmp, home)
}

func verifyOptionalSandboxToolchains(t *testing.T, be Backend, ws, tmp, home string) {
	t.Helper()
	if nodeBin, err := exec.LookPath("node.exe"); err == nil {
		specCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		pNode, err := be.Start(specCtx, SpawnSpec{
			WorkspaceRoot:  ws,
			TempRoot:       tmp,
			HomeRoot:       home,
			Command:        `node -e "console.log('NODE_OK_' + process.env.ASTERHAND_SECRET_SHOULD_NOT_EXIST)"`,
			BundledNodeDir: filepath.Dir(nodeBin),
			Timeout:        10 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		<-pNode.Done()
		nOut := pNode.OutputSnapshot(0, MaxOutputSessionBytes).Output
		nInfo := pNode.ExitInfo()
		_ = pNode.Close()
		if nInfo.ExitCode != 0 || !strings.Contains(nOut, "NODE_OK_undefined") {
			t.Fatalf("node run unexpected: %+v out=%s", nInfo, nOut)
		}
	}
	if _, err := exec.LookPath("python.exe"); err == nil {
		pPy, pyOut := runInSandbox(t, be, ws, tmp, home, `python -c "print('PY_OK_中文')"`, 10*time.Second)
		pyInfo := pPy.ExitInfo()
		_ = pPy.Close()
		if pyInfo.ExitCode != 0 || !strings.Contains(pyOut, "PY_OK_中文") {
			t.Fatalf("python run unexpected: %+v out=%s", pyInfo, pyOut)
		}
	}
	if _, err := exec.LookPath("git.exe"); err == nil {
		pGit, gitOut := runInSandbox(t, be, ws, tmp, home, `git --version`, 10*time.Second)
		gitInfo := pGit.ExitInfo()
		_ = pGit.Close()
		if gitInfo.ExitCode != 0 || !strings.Contains(strings.ToLower(gitOut), "git version") {
			t.Fatalf("git run unexpected: %+v out=%s", gitInfo, gitOut)
		}
	}
}

func TestE2EnvScrubJobKillAndTLSHint(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-secret-must-not-leak")
	t.Setenv("OPENCLAW_GATEWAY_TOKEN", "gw-secret-must-not-leak")
	t.Setenv("ASTERHAND_WORKSPACE_TOKEN", "ws-secret-must-not-leak")

	base := t.TempDir()
	ws := filepath.Join(base, "workspace")
	tmp := filepath.Join(base, "journal", "sandbox-temp")
	home := filepath.Join(base, "asterhand-home")
	_ = os.MkdirAll(ws, 0700)
	_ = os.MkdirAll(home, 0700)

	be := NewBackend()
	proc, out := runInSandbox(t, be, ws, tmp, home,
		`Write-Output ("DS=$($env:DEEPSEEK_API_KEY)|OC=$($env:OPENCLAW_GATEWAY_TOKEN)|AH=$($env:ASTERHAND_WORKSPACE_TOKEN)|GIT=$($env:GIT_SSL_BACKEND)|TMP=$($env:TEMP)")`,
		10*time.Second)
	defer proc.Close()
	if strings.Contains(out, "secret-must-not-leak") || !strings.Contains(out, "DS=|OC=|AH=|GIT=openssl|") {
		t.Fatalf("env scrub mismatch: %s", out)
	}
	if strings.Contains(out, `TMP=\\?\`) {
		t.Fatalf("TEMP must not carry \\\\?\\ prefix: %s", out)
	}

	// Verify TLS hint helper covers both WinHTTP/curl 0x8009030e and PowerShell failures.
	if h := DetectWindowsTLSHint(1, "Invoke-WebRequest : The underlying connection was closed: An unexpected error occurred"); h != "windows_schannel_restricted" {
		t.Fatalf("expected TLS hint for powershell failure, got %q", h)
	}
	if h := DetectWindowsTLSHint(35, "curl: (35) schannel: next InitializeSecurityContext failed: SEC_E_NO_CREDENTIALS (0x8009030E)"); h != "windows_schannel_restricted" {
		t.Fatalf("expected TLS hint for curl 0x8009030E, got %q", h)
	}

	// Spawn process tree with detached grandchild and verify Terminate() empties the Job < 2s.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	treeProc, err := be.Start(ctx, SpawnSpec{
		WorkspaceRoot: ws,
		TempRoot:      tmp,
		HomeRoot:      home,
		Command:       `Start-Process powershell.exe -WindowStyle Hidden -ArgumentList '-NoProfile','-Command','Start-Sleep -Seconds 30'; Write-Output 'TREE_READY'; Start-Sleep -Seconds 30`,
		Timeout:       20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer treeProc.Close()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(treeProc.OutputSnapshot(0, 4096).Output, "TREE_READY") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	stopStart := time.Now()
	treeProc.Terminate("cancelled")
	select {
	case <-treeProc.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("treeProc did not finish within 3s after Terminate")
	}
	if elapsed := time.Since(stopStart); elapsed > 2500*time.Millisecond {
		t.Fatalf("job tree terminate took too long: %v", elapsed)
	}
	stopped, err := treeProc.ProcessesStopped()
	if err != nil || !stopped {
		t.Fatalf("expected JobProcessCount == 0 after terminate, stopped=%v err=%v", stopped, err)
	}
	if ti := treeProc.ExitInfo(); ti.Reason != "cancelled" {
		t.Fatalf("expected reason=cancelled, got %+v", ti)
	}
}

func TestE3PathGuardDangerousEnvAndReinheritACL(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "workspace")
	stg := filepath.Join(base, "staging")
	tmp := filepath.Join(base, "journal", "sandbox-temp")
	home := filepath.Join(base, "asterhand-home")
	extDir := filepath.Join(base, "external-junction-target")
	_ = os.MkdirAll(ws, 0700)
	_ = os.MkdirAll(stg, 0700)
	_ = os.MkdirAll(tmp, 0700)
	_ = os.MkdirAll(home, 0700)
	_ = os.MkdirAll(extDir, 0700)
	extKeep := filepath.Join(extDir, "keep.txt")
	_ = os.WriteFile(extKeep, []byte("must-survive"), 0600)

	// 1. Sensitive path guard blocks ~/.ssh, %USERPROFILE%\.aws, asterhand-home/config.json,
	//    while allowing files inside workspaceRoot even when workspace is nested inside homeRoot.
	nestedHome := base
	protected := DefaultProtectedDirs(nestedHome)
	if _, bad := CheckSensitiveReferences(`type "`+filepath.Join(ws, "ok.txt")+`"`, ws, tmp, protected); bad {
		t.Fatal("must allow path inside workspace even when nested under homeRoot")
	}
	if _, bad := CheckSensitiveReferences(`cat ~/.ssh/id_rsa`, ws, tmp, protected); !bad {
		t.Fatal("expected ~/.ssh to trigger sensitive_path_denied")
	}
	if _, bad := CheckSensitiveReferences(`type %USERPROFILE%\.aws\credentials`, ws, tmp, protected); !bad {
		t.Fatalf("expected %s to trigger sensitive_path_denied", `%USERPROFILE%\.aws`)
	}
	if _, bad := CheckSensitiveReferences(`Get-Content $env:USERPROFILE\.kube\config`, ws, tmp, protected); !bad {
		t.Fatal("expected $env:USERPROFILE\\.kube to trigger sensitive_path_denied")
	}
	if _, bad := CheckSensitiveReferences(`type "`+filepath.Join(nestedHome, "config.json")+`"`, ws, tmp, protected); !bad {
		t.Fatal("expected asterhand-home/config.json to trigger sensitive_path_denied")
	}

	// 2. Workdir junction escape is rejected, dangerous env overrides are rejected.
	be := NewBackend()
	_ = be.Prepare(ws, tmp)
	wsJunc := filepath.Join(ws, "link-out")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", wsJunc, extDir).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J failed: %v (%s)", err, string(out))
	}
	if _, err := be.Start(context.Background(), SpawnSpec{
		WorkspaceRoot: ws, TempRoot: tmp, HomeRoot: home, Command: "echo hi", Workdir: "link-out",
	}); Code(err) != "sensitive_path_denied" {
		t.Fatalf("expected junction workdir escape to fail with sensitive_path_denied, got %v", err)
	}
	if err := os.MkdirAll(filepath.Join(extDir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := be.Start(context.Background(), SpawnSpec{
		WorkspaceRoot: ws, TempRoot: tmp, HomeRoot: home, Command: "echo hi", Workdir: filepath.Join("link-out", "sub"),
	}); Code(err) != "sensitive_path_denied" {
		t.Fatalf("expected nested junction workdir escape to fail with sensitive_path_denied, got %v", err)
	}
	if _, err := be.Start(context.Background(), SpawnSpec{
		WorkspaceRoot: ws, TempRoot: tmp, HomeRoot: home, Command: "echo hi",
		Env: map[string]string{"PYTHONSTARTUP": "evil.py"},
	}); Code(err) != "dangerous_env_denied" {
		t.Fatalf("expected PYTHONSTARTUP override to return dangerous_env_denied, got %v", err)
	}

	// 3. EnsureDirGrant skips junctions without altering external target ACLs.
	extDACLBefore, err := windows.GetNamedSecurityInfo(extDir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	sid, _ := DeriveSandboxSID(ws)
	if err := EnsureDirGrant(ws, sid); err != nil {
		t.Fatalf("EnsureDirGrant failed with junction inside ws: %v", err)
	}
	extDACLAfter, err := windows.GetNamedSecurityInfo(extDir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	dBefore, _, _ := extDACLBefore.DACL()
	dAfter, _, _ := extDACLAfter.DACL()
	if aclHasGrant(dBefore, sid, modifyAccessMask) || aclHasGrant(dAfter, sid, modifyAccessMask) {
		t.Fatal("EnsureDirGrant leaked sandbox ACE through workspace junction to external target")
	}

	verifyReinheritAndTempCleanup(t, be, ws, stg, tmp, home, extDir, extKeep)
}

func verifyReinheritAndTempCleanup(t *testing.T, be Backend, ws, stg, tmp, home, extDir, extKeep string) {
	t.Helper()
	// 4. ReinheritHandleDACL restores S_sandbox Modify after NtSetInformationFile cross-dir rename from staging.
	stgFile := filepath.Join(stg, "stage-item.new")
	pStg, _ := windows.UTF16PtrFromString(stgFile)
	hNew, err := windows.CreateFile(pStg, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE|windows.WRITE_DAC, 0, nil, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	fNew := os.NewFile(uintptr(hNew), stgFile)
	_, _ = fNew.WriteString("from-write-file\n")
	wsDest := filepath.Join(ws, "published-from-staging.txt")
	if err := testNtRenameInto(fNew, ws, "published-from-staging.txt"); err != nil {
		fNew.Close()
		t.Fatalf("nt rename failed: %v", err)
	}
	if err := ReinheritHandleDACL(fNew); err != nil {
		fNew.Close()
		t.Fatalf("ReinheritHandleDACL failed: %v", err)
	}
	fNew.Close()
	procEdit, editOut := runInSandbox(t, be, ws, tmp, home,
		`Add-Content -LiteralPath "published-from-staging.txt" -Value "appended-by-sandbox"; Get-Content -LiteralPath "published-from-staging.txt"`,
		10*time.Second)
	editInfo := procEdit.ExitInfo()
	_ = procEdit.Close()
	if editInfo.ExitCode != 0 || !strings.Contains(editOut, "appended-by-sandbox") {
		t.Fatalf("sandbox could not modify staging-moved file %s: %+v out=%s", wsDest, editInfo, editOut)
	}

	// 5. CleanupTemp deletes 0444 read-only files AND skips junction without deleting extKeep!
	roFile := filepath.Join(tmp, "ro.dat")
	_ = os.WriteFile(roFile, []byte("ro"), 0444)
	tmpJunc := filepath.Join(tmp, "tmp-link-out")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", tmpJunc, extDir).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J tmp failed: %v (%s)", err, string(out))
	}
	if err := be.CleanupTemp(tmp); err != nil {
		t.Fatalf("CleanupTemp failed: %v", err)
	}
	if _, err := os.Stat(roFile); !os.IsNotExist(err) {
		t.Fatalf("expected read-only file %s deleted", roFile)
	}
	if _, err := os.Stat(tmpJunc); !os.IsNotExist(err) {
		t.Fatalf("expected junction itself %s deleted", tmpJunc)
	}
	if got, err := os.ReadFile(extKeep); err != nil || string(got) != "must-survive" {
		t.Fatalf("external junction target was damaged by CleanupTemp: %q err=%v", string(got), err)
	}
}

func testNtRenameInto(file *os.File, parentDir, leaf string) error {
	pPtr, _ := windows.UTF16PtrFromString(parentDir)
	hParent, err := windows.CreateFile(pPtr, syscall.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(hParent)
	name, err := windows.UTF16FromString(leaf)
	if err != nil {
		return err
	}
	name = name[:len(name)-1]
	type renameInfo struct {
		Replace uint32
		Root    windows.Handle
		Length  uint32
		Name    [1]uint16
	}
	offset := unsafe.Offsetof(renameInfo{}.Name)
	buf := make([]byte, int(unsafe.Sizeof(renameInfo{}))+len(name)*2)
	info := (*renameInfo)(unsafe.Pointer(&buf[0]))
	info.Root = hParent
	info.Length = uint32(len(name) * 2)
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&buf[offset])), len(name)), name)
	var iosb windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(windows.Handle(file.Fd()), &iosb, &buf[0], uint32(len(buf)), windows.FileRenameInformation)
}

func TestOutputBufferTruncationAndUTF8Slice(t *testing.T) {
	buf := NewOutputBuffer(MaxOutputSessionBytes)
	head := strings.Repeat("甲", 16000) + "\nHEAD_END\n"
	mid := strings.Repeat("乙", 90000) + "\n"
	tail := "\nTAIL_START\n" + strings.Repeat("丙", 16000)
	buf.Append(head)
	buf.Append(mid)
	buf.Append(tail)

	full, totalBytes, truncated := buf.Materialize()
	if !truncated {
		t.Fatal("expected output exceeding 256 KiB to be truncated")
	}
	if totalBytes != int64(len(head)+len(mid)+len(tail)) {
		t.Fatalf("unexpected totalBytes=%d want=%d", totalBytes, len(head)+len(mid)+len(tail))
	}
	if len(full) > MaxOutputSessionBytes+256 {
		t.Fatalf("materialized buffer exceeded bounded window: %d", len(full))
	}
	if !utf8.ValidString(full) {
		t.Fatal("materialized buffer must stay valid UTF-8")
	}
	if !strings.Contains(full, "HEAD_END") || !strings.Contains(full, "TAIL_START") || !strings.Contains(full, "...[truncated ") {
		t.Fatalf("expected head, marker, and tail in bounded buffer")
	}

	// Read with small non-rune-aligned chunk size across entire buffer and verify every slice is valid UTF-8.
	var rebuilt strings.Builder
	var offset int64
	for {
		slice := buf.ReadSlice(offset, 7) // 7 is not divisible by 3-byte Chinese runes
		if !utf8.ValidString(slice.Output) {
			t.Fatalf("slice at offset %d is not valid UTF-8: %x", offset, []byte(slice.Output))
		}
		rebuilt.WriteString(slice.Output)
		if slice.NextOffset <= offset {
			break
		}
		offset = slice.NextOffset
	}
	if rebuilt.String() != full {
		t.Fatalf("reassembled UTF-8 slices mismatch full output (got len=%d want len=%d)", rebuilt.Len(), len(full))
	}
}

func TestProcessesStoppedWaitsForDoneAndCaseFoldingPathGuard(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "workspace")
	tmp := filepath.Join(base, "sandbox-temp")
	home := filepath.Join(base, "home")
	_ = os.MkdirAll(ws, 0700)
	_ = os.MkdirAll(home, 0700)

	be := NewBackend()
	if err := be.Prepare(ws, tmp); err != nil {
		t.Fatal(err)
	}
	// Cached second Prepare call must succeed without error.
	if err := be.Prepare(ws, tmp); err != nil {
		t.Fatalf("cached Prepare failed: %v", err)
	}

	// Start process, kill job, and verify ProcessesStopped() stays false until Done() closes.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	proc, err := be.Start(ctx, SpawnSpec{
		WorkspaceRoot: ws,
		TempRoot:      tmp,
		HomeRoot:      home,
		Command:       `powershell -NoProfile -Command "Write-Output 'BEFORE_KILL'; Start-Sleep -Seconds 15"`,
		Timeout:       10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	stoppedBefore, err := proc.ProcessesStopped()
	if err != nil || stoppedBefore {
		t.Fatalf("expected ProcessesStopped() == false while Done() still open, got (%v, %v)", stoppedBefore, err)
	}
	proc.Terminate("cancelled")
	<-proc.Done()
	stoppedAfter, err := proc.ProcessesStopped()
	if err != nil || !stoppedAfter {
		t.Fatalf("expected ProcessesStopped() == true after Done(), got (%v, %v)", stoppedAfter, err)
	}
	if err := proc.Close(); err != nil {
		t.Fatalf("first Close failed: %v", err)
	}
	if err := proc.Close(); err != nil {
		t.Fatalf("idempotent second Close failed: %v", err)
	}

	// Verify mixed-case protected directory matching regardless of case.
	macProtected := []ProtectedDir{{Label: "keychains", Path: "/Users/dev/Library/Keychains"}}
	for _, sample := range []string{
		"cat /Users/dev/Library/Keychains/login.keychain",
		"cat /users/dev/library/keychains/login.keychain",
	} {
		if label, bad := CheckSensitiveReferences(sample, ws, tmp, macProtected); !bad || label != "keychains" {
			t.Fatalf("expected %q to match protected dir, got (%q, %v)", sample, label, bad)
		}
	}
}

func TestEnvAllowlistBlocksHostSecretsAndProxyUserInfo(t *testing.T) {
	t.Setenv("LITELLM_MASTER_KEY", "sk-litellm-leak-check")
	t.Setenv("OPENROUTER_MANAGEMENT_KEY", "sk-or-mgmt-leak-check")
	t.Setenv("CUSTOM_SERVICE_SIGNING_KEY", "sig-leak-check")
	t.Setenv("DATABASE_URL", "postgres://admin:supersecret@db.internal:5432/prod")
	t.Setenv("HTTPS_PROXY", "http://corpuser:corppass@proxy.internal:8080")
	t.Setenv("NO_PROXY", "localhost,127.0.0.1")

	base := t.TempDir()
	ws := filepath.Join(base, "workspace")
	tmp := filepath.Join(base, "journal", "sandbox-temp")
	home := filepath.Join(base, "asterhand-home")
	_ = os.MkdirAll(ws, 0700)
	_ = os.MkdirAll(home, 0700)

	be := NewBackend()
	proc, out := runInSandbox(t, be, ws, tmp, home,
		`Write-Output ("LM=$($env:LITELLM_MASTER_KEY)|OM=$($env:OPENROUTER_MANAGEMENT_KEY)|SK=$($env:CUSTOM_SERVICE_SIGNING_KEY)|DB=$($env:DATABASE_URL)|HP=$($env:HTTPS_PROXY)|NP=$($env:NO_PROXY)|HAS_PATH=$($env:PATH.Length -gt 0)")`,
		10*time.Second)
	defer proc.Close()

	for _, secret := range []string{
		"sk-litellm-leak-check",
		"sk-or-mgmt-leak-check",
		"sig-leak-check",
		"supersecret",
		"corpuser",
		"corppass",
	} {
		if strings.Contains(out, secret) {
			t.Fatalf("secret %q leaked into sandbox env output: %s", secret, out)
		}
	}
	if !strings.Contains(out, "LM=|OM=|SK=|DB=|HP=|NP=localhost,127.0.0.1|HAS_PATH=True") {
		t.Fatalf("unexpected sandbox env allowlist output: %s", out)
	}

	// Clean proxy URL without userinfo must still be inherited for toolchain compatibility.
	t.Setenv("HTTPS_PROXY", "http://proxy.internal:8080")
	built, err := BuildEnvironment(SpawnSpec{WorkspaceRoot: ws, TempRoot: tmp, HomeRoot: home})
	if err != nil {
		t.Fatalf("BuildEnvironment: %v", err)
	}
	foundCleanProxy := false
	for _, entry := range built {
		if strings.EqualFold(entry, "HTTPS_PROXY=http://proxy.internal:8080") {
			foundCleanProxy = true
		}
		for _, leaked := range []string{"LITELLM_MASTER_KEY=", "OPENROUTER_MANAGEMENT_KEY=", "CUSTOM_SERVICE_SIGNING_KEY=", "DATABASE_URL="} {
			if strings.HasPrefix(strings.ToUpper(entry), leaked) {
				t.Fatalf("unallowlisted variable leaked into BuildEnvironment: %s", entry)
			}
		}
	}
	if !foundCleanProxy {
		t.Fatalf("expected clean HTTPS_PROXY without userinfo to be inherited")
	}
}

func TestCheckSensitiveReferencesSpacesAndParens(t *testing.T) {
	base := t.TempDir()
	userDir := filepath.Join(base, "John Doe (Admin)")
	home := filepath.Join(userDir, "AppData", "Roaming", "Asterhand")
	rtDir := filepath.Join(home, "conversations", "rt-1")
	ws := filepath.Join(rtDir, "workspace (1)")
	tmp := filepath.Join(rtDir, "file-versions", "sandbox temp (1)")
	extSSH := filepath.Join(userDir, ".ssh")
	for _, dir := range []string{ws, tmp, extSSH} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(extSSH, "id_ed25519"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	wsLink := filepath.Join(ws, "my link (out)")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", wsLink, extSSH).CombinedOutput(); err != nil {
		t.Skipf("mklink /J unavailable: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	protected := append(DefaultProtectedDirs(home), ProtectedDir{Label: "~/.ssh-custom", Path: extSSH})
	wsScript := filepath.Join(ws, "my script (1).ps1")
	tmpCache := filepath.Join(tmp, "cache (1).txt")
	homeCfg := filepath.Join(home, "config.json")
	traversal := ws + `\..\..\..\config.json`

	cases := []struct {
		name string
		text string
		want bool
	}{
		{"double-quoted workspace", `dir "` + wsScript + `"`, false},
		{"single-quoted workspace", `ls '` + wsScript + `'`, false},
		{"escaped quote workspace", `cmd /c \"` + wsScript + `\"`, false},
		{"pwsh backtick quote workspace", "& `\"" + wsScript + "`\"", false},
		{"file URI workspace", `read "file:///` + filepath.ToSlash(wsScript) + `"`, false},
		{"managed temp path", `Get-Content "` + tmpCache + `"`, false},
		{"unquoted workspace env value", wsScript, false},
		{"unquoted multi-arg workspace", `copy ` + wsScript + ` ` + filepath.Join(ws, "copy (2).txt"), false},
		{"home root", `ls "` + home + `"`, true},
		{"home config.json", `type "` + homeCfg + `"`, true},
		{"home conversations", `dir "` + filepath.Join(home, "conversations") + `"`, true},
		{"quoted traversal", `type "` + traversal + `"`, true},
		{"mixed allowed and forbidden", `copy "` + wsScript + `" "` + homeCfg + `"`, true},
		{"quoted workspace junction", `type "` + filepath.Join(wsLink, "id_ed25519") + `"`, true},
		{"quoted workspace junction missing leaf", `type "` + filepath.Join(wsLink, "missing_key") + `"`, true},
		{"unquoted workspace junction", filepath.Join(wsLink, "id_ed25519"), true},
		{"unquoted workspace junction missing leaf", `type ` + filepath.Join(wsLink, "missing_key"), true},
		{"paren-wrapped workspace junction", `echo (` + filepath.Join(wsLink, "id_ed25519") + `)`, true},
		{"paren-wrapped protected path", `echo (` + filepath.Join(extSSH, "id_ed25519") + `)`, true},
		{"unquoted traversal", traversal, true},
		{"unquoted space-subdir traversal", ws + `\sub (1)\..\..\..\..\config.json`, true},
		{"unquoted multi-arg space-subdir traversal", `copy ` + ws + `\sub (1)\..\..\..\..\config.json ` + wsScript, true},
	}
	for _, tc := range cases {
		if label, got := CheckSensitiveReferences(tc.text, ws, tmp, protected); got != tc.want {
			t.Fatalf("%s: got blocked=%v (label=%q), want %v (text=%q)", tc.name, got, label, tc.want, tc.text)
		}
	}
}
