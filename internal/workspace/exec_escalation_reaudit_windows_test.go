//go:build windows

package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/platform/sandbox"
)

type interceptSpawnBackend struct {
	inner   sandbox.Backend
	onStart func(spec sandbox.SpawnSpec)
	specs   []sandbox.SpawnSpec
}

func (b *interceptSpawnBackend) Supported() bool              { return b.inner.Supported() }
func (b *interceptSpawnBackend) Prepare(ws, tmp string) error { return b.inner.Prepare(ws, tmp) }
func (b *interceptSpawnBackend) CleanupTemp(tmp string) error {
	return b.inner.CleanupTemp(tmp)
}
func (b *interceptSpawnBackend) Start(ctx context.Context, spec sandbox.SpawnSpec) (sandbox.SessionProcess, error) {
	b.specs = append(b.specs, spec)
	if b.onStart != nil {
		b.onStart(spec)
	}
	return b.inner.Start(ctx, spec)
}

func TestExecEscalationReauditMultilineAndSubexprNotAutoAllowed(t *testing.T) {
	s, _, _, scope := openEscalationTestService(t)
	spy := &interceptSpawnBackend{inner: s.exec.backend}
	s.exec.backend = spy

	toolDir := t.TempDir()
	toolPath := filepath.Join(toolDir, "mytool.cmd")
	if err := os.WriteFile(toolPath, []byte("@echo off\r\necho from-mytool:%1:%2\r\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", toolDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var persisted []config.TrustRule
	s.SetTrustCallbacks(func(rules []config.TrustRule) error {
		persisted = append(persisted, rules...)
		return nil
	}, nil)

	res0 := runApprovedFlow(t, s, scope, "c-re-base", "allow_always", ExecStartRequest{
		CallID: "c-re-base", Command: "mytool ok", Escalated: true, EscalationReason: "Run harmless test tool",
	})
	if res0.Status != "completed" || len(persisted) != 1 {
		t.Fatalf("expected baseline rule allow_always, res=%+v persisted=%+v", res0, persisted)
	}
	s.ConfigureTrust(config.ExecutionModeStandard, "rev-reaudit-cmd", persisted)

	compoundInputs := []string{
		"mytool ok\nWrite-Output injected",
		"mytool ok\r\nWrite-Output injected",
		"mytool (Write-Output injected)",
		`mytool "$env:PATH"`,
		`mytool "a\" ; Write-Output injected #"`,
	}
	for i, cmd := range compoundInputs {
		cid := "c-re-cmp-" + string(rune('a'+i))
		testBeginTurnCall(t, s, scope, cid, "b-"+cid, 0, "exec_command", nil, "cmp", 0)
		res, err := s.StartExec(context.Background(), ExecStartRequest{
			CallID: cid, Command: cmd, Escalated: true, EscalationReason: "Bypass probe",
		})
		if err != nil || res.Status != "approval_required" {
			t.Fatalf("case %d (%q): expected approval_required, got res=%+v err=%v", i, cmd, res, err)
		}
		_ = s.CancelCall(cid, "cancelled")
		_, _ = s.End(cid, "cancelled", "cancelled")
	}

	// Single benign direct invocation auto-approves and passes canonical DirectExe + DirectArgs.
	testBeginTurnCall(t, s, scope, "c-re-direct", "b-re-direct", 0, "exec_command", nil, "direct", 0)
	resDirect, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "c-re-direct", Command: `mytool --flag "hello world"`, Escalated: true,
	})
	_, _ = s.End("c-re-direct", "completed", "")
	if err != nil || resDirect.Status != "completed" || !strings.Contains(resDirect.Output, "from-mytool") {
		t.Fatalf("expected direct auto-approved execution, got res=%+v err=%v", resDirect, err)
	}
	last := spy.specs[len(spy.specs)-1]
	wantExe, _ := sandbox.CanonicalExistingPath(toolPath)
	if !pathsEqualOS(last.DirectExe, wantExe) || !reflect.DeepEqual(last.DirectArgs, []string{"--flag", "hello world"}) {
		t.Fatalf("expected bound DirectExe=%q DirectArgs=[--flag hello world], got DirectExe=%q DirectArgs=%q",
			wantExe, last.DirectExe, last.DirectArgs)
	}
}

func TestExecEscalationReauditFinalVerifyLockAndPostVerifyRulePublish(t *testing.T) {
	s, _, _, scope := openEscalationTestService(t)
	extDir := t.TempDir()
	extScript := filepath.Join(extDir, "ext_job.ps1")
	if err := os.WriteFile(extScript, []byte("Write-Output 'verified-original'\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var persisted []config.TrustRule
	var writeAttempts atomic.Int32
	s.SetTrustCallbacks(func(rules []config.TrustRule) error {
		writeAttempts.Add(1)
		persisted = append(persisted, rules...)
		return nil
	}, nil)

	counter := &countingEscalationBackend{inner: s.exec.backend}
	s.exec.backend = counter

	// 1. Modifying external script after claim before spawn aborts spawn and writes 0 rules.
	s.exec.mu.Lock()
	s.exec.onAfterClaimBeforeSpawn = func(string) {
		_ = os.WriteFile(extScript, []byte("Write-Output 'tampered-after-claim'\r\n"), 0o600)
	}
	s.exec.mu.Unlock()

	testBeginTurnCall(t, s, scope, "c-re-tamper", "b-re-tamper", 0, "exec_command", nil, "tamper", 0)
	resT, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "c-re-tamper", Command: `powershell -File "` + extScript + `"`,
		Escalated: true, EscalationReason: "Run ext script", ExternalPaths: []string{extDir},
	})
	if err != nil || resT.Status != "approval_required" {
		t.Fatalf("StartExec: res=%+v err=%v", resT, err)
	}
	if err := s.ApproveTool(scope.Conversation, resT.ApprovalID, "allow_always"); err != nil {
		t.Fatal(err)
	}
	waitMs := 2500
	if _, err := s.WaitApproval(context.Background(), ExecApprovalWaitRequest{
		CallID: "c-re-tamper", ApprovalID: resT.ApprovalID, WaitMs: &waitMs,
	}); Code(err) != "approval_tampered" {
		t.Fatalf("expected approval_tampered, got %v", err)
	}
	_, _ = s.End("c-re-tamper", "failed", "approval_tampered")
	if counter.starts.Load() != 0 || writeAttempts.Load() != 0 || len(persisted) != 0 || len(s.Status().AllowedItems) != 0 {
		t.Fatalf("tampered spawn leaked starts=%d writes=%d persisted=%+v", counter.starts.Load(), writeAttempts.Load(), persisted)
	}

	// 2. FILE_SHARE_READ lock prevents overwrite during Backend.Start and persistTrustFn, then releases cleanly.
	testAuditScriptLockedDuringSpawn(t, s, counter.inner, extDir, extScript, scope)
}

func testAuditScriptLockedDuringSpawn(t *testing.T, s *Service, baseBackend sandbox.Backend, extDir, extScript string, scope Scope) {
	t.Helper()
	if err := os.WriteFile(extScript, []byte("Write-Output 'verified-original'\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.exec.mu.Lock()
	s.exec.onAfterClaimBeforeSpawn = nil
	s.exec.mu.Unlock()

	var startWriteErr, persistWriteErr error
	spy := &interceptSpawnBackend{
		inner: baseBackend,
		onStart: func(sandbox.SpawnSpec) {
			startWriteErr = os.WriteFile(extScript, []byte("Write-Output 'hijacked-at-start'\r\n"), 0o600)
		},
	}
	s.exec.backend = spy
	s.SetTrustCallbacks(func([]config.TrustRule) error {
		persistWriteErr = os.WriteFile(extScript, []byte("Write-Output 'hijacked-at-persist'\r\n"), 0o600)
		return nil
	}, nil)

	for _, tc := range []struct {
		cid string
		cmd string
	}{
		{"c-re-lock-file", `powershell -File "` + extScript + `"`},
		{"c-re-lock-cmd", `powershell -Command "& '` + strings.ReplaceAll(extScript, `'`, `''`) + `'"`},
	} {
		startWriteErr = nil
		persistWriteErr = nil
		res := runApprovedFlow(t, s, scope, tc.cid, "allow_always", ExecStartRequest{
			CallID: tc.cid, Command: tc.cmd,
			Escalated: true, EscalationReason: "Run locked script", ExternalPaths: []string{extDir},
		})
		if res.Status != "completed" || !strings.Contains(res.Output, "verified-original") {
			t.Fatalf("%s unexpected locked run result: %+v", tc.cid, res)
		}
		if startWriteErr == nil || persistWriteErr == nil {
			t.Fatalf("%s expected sharing-violation write errors while locked: startErr=%v persistErr=%v", tc.cid, startWriteErr, persistWriteErr)
		}
		if err := os.WriteFile(extScript, []byte("Write-Output 'verified-original'\r\n"), 0o600); err != nil {
			t.Fatalf("%s expected script lock released after session exit, got %v", tc.cid, err)
		}
	}
}

func TestExecEscalationReauditCancelAfterClaimBeforeSpawn(t *testing.T) {
	s, _, _, scope := openEscalationTestService(t)
	counter := &countingEscalationBackend{inner: s.exec.backend}
	s.exec.backend = counter
	extDir := t.TempDir()

	var persisted []config.TrustRule
	s.SetTrustCallbacks(func(rules []config.TrustRule) error {
		persisted = append(persisted, rules...)
		return nil
	}, nil)

	testBeginTurnCall(t, s, scope, "c-re-cancel", "b-re-cancel", 0, "exec_command", nil, "cancel", 0)
	res, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "c-re-cancel", Command: `Get-ChildItem "` + extDir + `"`,
		Escalated: true, EscalationReason: "Cancel handoff test", ExternalPaths: []string{extDir},
	})
	if err != nil || res.Status != "approval_required" {
		t.Fatalf("StartExec: res=%+v err=%v", res, err)
	}

	s.exec.mu.Lock()
	s.exec.onAfterClaimBeforeSpawn = func(callID string) {
		_ = s.CancelCall(callID, "cancelled")
	}
	s.exec.mu.Unlock()

	if err := s.ApproveTool(scope.Conversation, res.ApprovalID, "allow_always"); err != nil {
		t.Fatal(err)
	}
	waitMs := 2500
	if _, err := s.WaitApproval(context.Background(), ExecApprovalWaitRequest{
		CallID: "c-re-cancel", ApprovalID: res.ApprovalID, WaitMs: &waitMs,
	}); Code(err) != "cancelled" {
		t.Fatalf("expected cancelled when CancelCall fires after claim, got %v", err)
	}
	_, _ = s.End("c-re-cancel", "cancelled", "cancelled")
	if counter.starts.Load() != 0 || len(persisted) != 0 {
		t.Fatalf("expected 0 backend starts and 0 rules, got starts=%d persisted=%+v", counter.starts.Load(), persisted)
	}
	settled, err := s.exec.TurnSettled(scope.Turn)
	if err != nil || !settled {
		t.Fatalf("expected TurnSettled=true after cancel handoff, settled=%v err=%v", settled, err)
	}
}

func TestExecEscalationReauditLargeDataFilesNotTreatedAsScripts(t *testing.T) {
	s, _, ws, scope := openEscalationTestService(t)
	extDir := t.TempDir()
	overLimit := int64(MaxVerifiableScriptBytes + 1)

	makeSparseFile := func(name string, size int64) string {
		p := filepath.Join(ws, name)
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(size); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		return p
	}
	bigCSV := makeSparseFile("huge.csv", overLimit)
	bigZIP := makeSparseFile("archive.zip", overLimit)
	bigPS1 := makeSparseFile("huge.ps1", overLimit)

	// Pure data arguments >16 MiB must enter approval normally instead of failing with script_unverifiable.
	testBeginTurnCall(t, s, scope, "c-re-csv", "b-re-csv", 0, "exec_command", nil, "csv", 0)
	resData, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "c-re-csv", Command: `Get-Item "` + bigCSV + `" ; Get-Item "` + bigZIP + `"`,
		Escalated: true, EscalationReason: "Process large dataset", ExternalPaths: []string{extDir},
	})
	_ = s.CancelCall("c-re-csv", "cancelled")
	_, _ = s.End("c-re-csv", "cancelled", "cancelled")
	if err != nil || resData.Status != "approval_required" {
		t.Fatalf("expected large CSV/ZIP data args to pass preflight, got res=%+v err=%v", resData, err)
	}

	// Real >16 MiB script targets AND data files passed to an interpreter still reject with script_unverifiable.
	for i, cmd := range []string{
		`powershell -File "` + bigPS1 + `"`,
		`python "` + bigCSV + `"`,
	} {
		cid := "c-re-code-" + string(rune('a'+i))
		testBeginTurnCall(t, s, scope, cid, "b-"+cid, 0, "exec_command", nil, "code", 0)
		_, err := s.StartExec(context.Background(), ExecStartRequest{
			CallID: cid, Command: cmd, Escalated: true, EscalationReason: "Run oversized code", ExternalPaths: []string{extDir},
		})
		_, _ = s.End(cid, "failed", Code(err))
		if Code(err) != "script_unverifiable" {
			t.Fatalf("case %d (%q): expected script_unverifiable, got %v", i, cmd, err)
		}
	}
}

func TestExecEscalationReauditNearMaxMultilinePreflightLatency(t *testing.T) {
	s, _, _, scope := openEscalationTestService(t)
	extDir := t.TempDir()

	var b strings.Builder
	for i := 0; ; i++ {
		line := fmt.Sprintf("# benign multiline %04d throughput probe C:\\Temp\\ok\\data 'single_%04d'\n", i, i)
		if b.Len()+len(line)+32 >= MaxExecCommandBytes {
			break
		}
		b.WriteString(line)
	}
	b.WriteString(`Write-Output "fast-ok"`)
	bigCmd := b.String()

	startT1 := time.Now()
	testBeginTurnCall(t, s, scope, "c-re-perf-t1", "b-re-perf-t1", 0, "exec_command", nil, "perf1", 0)
	resT1, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "c-re-perf-t1", Command: bigCmd,
	})
	elapsedT1 := time.Since(startT1)
	_, _ = s.End("c-re-perf-t1", "completed", "")
	if err != nil || resT1.Status != "completed" || !strings.Contains(resT1.Output, "fast-ok") {
		t.Fatalf("Tier 1 near-max multiline run failed: res=%+v err=%v", resT1, err)
	}
	if elapsedT1 > 3*time.Second {
		t.Fatalf("Tier 1 near-max multiline execution unexpectedly slow: %v", elapsedT1)
	}

	startPreflight := time.Now()
	testBeginTurnCall(t, s, scope, "c-re-perf-t2", "b-re-perf-t2", 0, "exec_command", nil, "perf2", 0)
	resT2, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "c-re-perf-t2", Command: bigCmd, Escalated: true, EscalationReason: "Multiline latency check", ExternalPaths: []string{extDir},
	})
	elapsedPreflight := time.Since(startPreflight)
	_ = s.CancelCall("c-re-perf-t2", "cancelled")
	_, _ = s.End("c-re-perf-t2", "cancelled", "cancelled")
	if err != nil || resT2.Status != "approval_required" {
		t.Fatalf("Tier 2 near-max multiline StartExec failed: res=%+v err=%v", resT2, err)
	}
	if elapsedPreflight > 500*time.Millisecond {
		t.Fatalf("Tier 2 near-max multiline preflight took too long: %v (bytes=%d)", elapsedPreflight, len(bigCmd))
	}
	t.Logf("near-max multiline (%d bytes): Tier1=%v Tier2-preflight=%v", len(bigCmd), elapsedT1, elapsedPreflight)
}

func TestExecEscalationRound2PowerShellNamedAndSwitchParams(t *testing.T) {
	s, _, ws, scope := openEscalationTestService(t)
	spy := &interceptSpawnBackend{inner: s.exec.backend}
	s.exec.backend = spy

	scriptPath := filepath.Join(ws, "greeting.ps1")
	scriptBody := "param([Parameter(Mandatory=$true)][string]$Name, [switch]$Loud, [switch]$Keep = $true)\r\n" +
		"Write-Output (\"$Name|$($Loud.IsPresent)|$($Keep.IsPresent)\")\r\n"
	if err := os.WriteFile(scriptPath, []byte(scriptBody), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		cid  string
		cmd  string
		want string
	}{
		{"c-r2-ps1-a", `./greeting.ps1 -Name Audit`, "Audit|False|True"},
		{"c-r2-ps1-b", `./greeting.ps1 -Name "Hello Audit" -Loud -Keep:$false`, "Hello Audit|True|False"},
		{"c-r2-ps1-c", `./greeting.ps1 -Name:"Colon Audit" -Loud:$true`, "Colon Audit|True|True"},
	}
	for _, tc := range cases {
		testBeginTurnCall(t, s, scope, tc.cid, "b-"+tc.cid, 0, "exec_command", nil, "ps1", 0)
		res, err := s.StartExec(context.Background(), ExecStartRequest{
			CallID: tc.cid, Command: tc.cmd,
		})
		_, _ = s.End(tc.cid, "completed", "")
		if err != nil || res.Status != "completed" || !strings.Contains(res.Output, tc.want) {
			t.Fatalf("%s (%q) failed: res=%+v err=%v", tc.cid, tc.cmd, res, err)
		}
		last := spy.specs[len(spy.specs)-1]
		if last.DirectExe == "" {
			t.Fatalf("%s (%q) expected DirectExe binding, got empty", tc.cid, tc.cmd)
		}
	}
}

func TestExecEscalationRound2RulePersistFailurePreservesExecResult(t *testing.T) {
	s, _, _, scope := openEscalationTestService(t)
	extDir := t.TempDir()
	marker := filepath.Join(extDir, "marker.txt")

	s.SetTrustCallbacks(func([]config.TrustRule) error {
		return fail("simulated_config_write_error")
	}, nil)

	res := runApprovedFlow(t, s, scope, "c-r2-persist-err", "allow_always", ExecStartRequest{
		CallID:           "c-r2-persist-err",
		Command:          `Set-Content -LiteralPath "` + marker + `" -Value "created"; Write-Output "side-effect-done"`,
		Escalated:        true,
		EscalationReason: "Create output marker",
		ExternalPaths:    []string{extDir},
	})
	if res.Status != "completed" || !strings.Contains(res.Output, "side-effect-done") {
		t.Fatalf("expected completed execution output preserved, got %+v", res)
	}
	if data, err := os.ReadFile(marker); err != nil || !strings.Contains(string(data), "created") {
		t.Fatalf("expected side-effect marker present, data=%q err=%v", string(data), err)
	}
	if res.RulePersistError != "simulated_config_write_error" {
		t.Fatalf("expected RulePersistError=simulated_config_write_error, got %+v", res)
	}
	if !strings.Contains(res.Warning, "trust_persist_failed:simulated_config_write_error") {
		t.Fatalf("expected trust_persist_failed warning, got %+v", res)
	}
	if items := s.Status().AllowedItems; len(items) != 0 {
		t.Fatalf("expected no failed rule in AllowedItems, got %+v", items)
	}
}

type round2CancelBackend struct {
	inner   sandbox.Backend
	onStart func(ctx context.Context, spec sandbox.SpawnSpec) sandbox.SpawnSpec
}

func (b *round2CancelBackend) Supported() bool              { return b.inner.Supported() }
func (b *round2CancelBackend) Prepare(ws, tmp string) error { return b.inner.Prepare(ws, tmp) }
func (b *round2CancelBackend) CleanupTemp(tmp string) error {
	return b.inner.CleanupTemp(tmp)
}
func (b *round2CancelBackend) Start(ctx context.Context, spec sandbox.SpawnSpec) (sandbox.SessionProcess, error) {
	if b.onStart != nil {
		spec = b.onStart(ctx, spec)
	}
	return b.inner.Start(ctx, spec)
}

func TestExecEscalationRound2CancelSerializedWithBackendStartAndResume(t *testing.T) {
	s, _, _, scope := openEscalationTestService(t)
	base := s.exec.backend
	extDir := t.TempDir()
	neverA := filepath.Join(extDir, "never_a.txt")
	neverB := filepath.Join(extDir, "never_b.txt")

	// A. Cancel at Backend.Start entry stops before native CreateProcess starts.
	var ctxWasCanceled atomic.Bool
	s.exec.backend = &round2CancelBackend{
		inner: base,
		onStart: func(ctx context.Context, spec sandbox.SpawnSpec) sandbox.SpawnSpec {
			_ = s.CancelCall("c-r2-cancel-start", "cancelled")
			ctxWasCanceled.Store(ctx.Err() != nil)
			return spec
		},
	}
	runCancelProbeCase(t, s, scope, "c-r2-cancel-start", extDir, neverA)
	if !ctxWasCanceled.Load() {
		t.Fatal("expected launch context canceled synchronously inside CancelCall before Backend.Start")
	}
	if _, err := os.Stat(neverA); !os.IsNotExist(err) {
		t.Fatalf("process ran despite cancel at Start entry: stat=%v", err)
	}

	// B. Cancel inside AdmitResume while process is suspended blocks ResumeThread.
	var resumedCount atomic.Int32
	s.exec.backend = &round2CancelBackend{
		inner: base,
		onStart: func(_ context.Context, spec sandbox.SpawnSpec) sandbox.SpawnSpec {
			orig := spec.AdmitResume
			spec.AdmitResume = func(resume func() error) error {
				_ = s.CancelCall("c-r2-cancel-resume", "cancelled")
				return orig(func() error {
					resumedCount.Add(1)
					return resume()
				})
			}
			return spec
		},
	}
	runCancelProbeCase(t, s, scope, "c-r2-cancel-resume", extDir, neverB)
	if resumedCount.Load() != 0 {
		t.Fatalf("expected ResumeThread never called when canceled at gate, count=%d", resumedCount.Load())
	}
	if _, err := os.Stat(neverB); !os.IsNotExist(err) {
		t.Fatalf("suspended process executed despite AdmitResume gate: stat=%v", err)
	}
}

func runCancelProbeCase(t *testing.T, s *Service, scope Scope, cid, extDir, marker string) {
	t.Helper()
	testBeginTurnCall(t, s, scope, cid, "b-"+cid, 0, "exec_command", nil, "cancel", 0)
	res, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: cid, Command: `Set-Content -LiteralPath "` + marker + `" -Value "ran"`,
		Escalated: true, EscalationReason: "Cancel race test", ExternalPaths: []string{extDir},
	})
	if err != nil || res.Status != "approval_required" {
		t.Fatalf("StartExec(%s): res=%+v err=%v", cid, res, err)
	}
	if err := s.ApproveTool(scope.Conversation, res.ApprovalID, "allow_once"); err != nil {
		t.Fatal(err)
	}
	waitMs := 2500
	if _, err := s.WaitApproval(context.Background(), ExecApprovalWaitRequest{
		CallID: cid, ApprovalID: res.ApprovalID, WaitMs: &waitMs,
	}); Code(err) != "cancelled" {
		t.Fatalf("WaitApproval(%s): expected cancelled, got %v", cid, err)
	}
	_, _ = s.End(cid, "cancelled", "cancelled")
	if settled, sErr := s.exec.TurnSettled(scope.Turn); sErr != nil || !settled {
		t.Fatalf("TurnSettled(%s): settled=%v err=%v", cid, settled, sErr)
	}
}

func TestExecEscalationRound3PowerShellLocalizedAndTypedParams(t *testing.T) {
	s, _, ws, scope := openEscalationTestService(t)
	spy := &interceptSpawnBackend{inner: s.exec.backend}
	s.exec.backend = spy

	locPath := filepath.Join(ws, "localized.ps1")
	locBody := "param([Parameter(Mandatory=$true)][string]$名称)\r\nWrite-Output (\"Name:$名称\")\r\n"
	typedPath := filepath.Join(ws, "typed.ps1")
	typedBody := "param([Parameter(Position=0)][object]$Pos, [object]$Value, [switch]$Keep = $true)\r\n" +
		"Write-Output (\"$($Pos.GetType().Name)|$Pos|$($Value.GetType().Name)|$Value|$($Keep.IsPresent)\")\r\n"
	if err := os.WriteFile(locPath, []byte(locBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(typedPath, []byte(typedBody), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		cid        string
		cmd        string
		want       string
		wantDirect bool
	}{
		{"c-r3-loc", `./localized.ps1 -名称 Audit`, "Name:Audit", true},
		{"c-r3-loc-colon", `./localized.ps1 -名称:"Colon 中文"`, "Name:Colon 中文", true},
		{"c-r3-typed", `./typed.ps1 42 -Value:'$false' -Keep:$false`, "Int32|42|String|$false|False", true},
		{"c-r3-bool-pos", `./typed.ps1 $true -Value:'$false'`, "Boolean|True|String|$false|True", true},
		{"c-r3-expr-t1", `./typed.ps1 (40 + 2) -Value:'ok'`, "Int32|42|String|ok|True", false},
	}
	for _, tc := range cases {
		testBeginTurnCall(t, s, scope, tc.cid, "b-"+tc.cid, 0, "exec_command", nil, "ps1", 0)
		res, err := s.StartExec(context.Background(), ExecStartRequest{CallID: tc.cid, Command: tc.cmd})
		_, _ = s.End(tc.cid, "completed", "")
		if err != nil || res.Status != "completed" || !strings.Contains(res.Output, tc.want) {
			t.Fatalf("%s (%q) failed: res=%+v err=%v", tc.cid, tc.cmd, res, err)
		}
		last := spy.specs[len(spy.specs)-1]
		if tc.wantDirect && last.DirectExe == "" {
			t.Fatalf("%s (%q) expected DirectExe binding, got empty", tc.cid, tc.cmd)
		}
		if !tc.wantDirect && last.DirectExe != "" {
			t.Fatalf("%s (%q) expected native fallback DirectExe=\"\", got %q", tc.cid, tc.cmd, last.DirectExe)
		}
	}
	verifyRound3Tier2CommandBoundary(t, s, scope)
}

func verifyRound3Tier2CommandBoundary(t *testing.T, s *Service, scope Scope) {
	t.Helper()
	toolDir := t.TempDir()
	toolPath := filepath.Join(toolDir, "r3tool.cmd")
	if err := os.WriteFile(toolPath, []byte("@echo off\r\necho r3-tool-%*\r\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var persisted []config.TrustRule
	s.SetTrustCallbacks(func(rules []config.TrustRule) error {
		persisted = append(persisted, rules...)
		return nil
	}, nil)
	t.Setenv("PATH", toolDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if res := runApprovedFlow(t, s, scope, "c-r3-allow", "allow_always", ExecStartRequest{
		CallID: "c-r3-allow", Command: "r3tool", Escalated: true, EscalationReason: "Approve test tool",
	}); res.Status != "completed" {
		t.Fatalf("approve r3tool failed: %+v", res)
	}
	s.ConfigureTrust(config.ExecutionModeStandard, "rev-r3-cmd", persisted)

	testBeginTurnCall(t, s, scope, "c-r3-auto", "b-r3-auto", 0, "exec_command", nil, "auto", 0)
	autoRes, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "c-r3-auto", Command: "r3tool ping", Escalated: true, EscalationReason: "Direct arg auto",
	})
	_, _ = s.End("c-r3-auto", "completed", "")
	if err != nil || autoRes.Status != "completed" || !strings.Contains(autoRes.Output, "r3-tool-ping") {
		t.Fatalf("expected approved direct call auto-allowed, got res=%+v err=%v", autoRes, err)
	}

	for _, tc := range []struct{ cid, cmd string }{
		{"c-r3-expr-t2", "r3tool (40 + 2)"},
		{"c-r3-bracket", "r3tool [int]42"},
		{"c-r3-emdash", "r3tool \u2014Name Audit"},
	} {
		testBeginTurnCall(t, s, scope, tc.cid, "b-"+tc.cid, 0, "exec_command", nil, "ask", 0)
		res, err := s.StartExec(context.Background(), ExecStartRequest{
			CallID: tc.cid, Command: tc.cmd, Escalated: true, EscalationReason: "Non-direct falls back to approval",
		})
		if err != nil || res.Status != "approval_required" {
			t.Fatalf("%s (%q) expected approval_required, got res=%+v err=%v", tc.cid, tc.cmd, res, err)
		}
		_ = s.CancelCall(tc.cid, "cancelled")
		_, _ = s.End(tc.cid, "cancelled", "cancelled")
	}
}

func TestExecEscalationRound4QuotedLiteralAndCurlyApostrophePath(t *testing.T) {
	s, _, ws, scope := openEscalationTestService(t)
	spy := &interceptSpawnBackend{inner: s.exec.backend}
	s.exec.backend = spy

	markerPath := filepath.Join(ws, "literal-executed.txt")
	scriptPath := filepath.Join(ws, "literal.ps1")
	scriptBody := "param([string]$Name = 'default')\r\n" +
		"Set-Content -LiteralPath '" + strings.ReplaceAll(markerPath, "'", "''") + "' -Value $Name -NoNewline\r\n" +
		"Write-Output (\"fixture-script-executed:$Name\")\r\n"
	if err := os.WriteFile(scriptPath, []byte(scriptBody), 0o600); err != nil {
		t.Fatal(err)
	}

	// Standalone quoted strings & expression literals in PowerShell evaluate as values, not commands.
	exprCases := []struct {
		cid, cmd, want string
	}{
		{"c-r4-sq", `'./literal.ps1'`, `./literal.ps1`},
		{"c-r4-dq", `"./literal.ps1"`, `./literal.ps1`},
		{"c-r4-num", `42`, `42`},
		{"c-r4-bool", `$true`, `True`},
	}
	for _, tc := range exprCases {
		testBeginTurnCall(t, s, scope, tc.cid, "b-"+tc.cid, 0, "exec_command", nil, "expr", 0)
		res, err := s.StartExec(context.Background(), ExecStartRequest{CallID: tc.cid, Command: tc.cmd})
		_, _ = s.End(tc.cid, "completed", "")
		if err != nil || res.Status != "completed" || !strings.Contains(res.Output, tc.want) || strings.Contains(res.Output, "fixture-script-executed") {
			t.Fatalf("%s (%q) expected pure literal %q without execution, got res=%+v err=%v", tc.cid, tc.cmd, tc.want, res, err)
		}
		if spy.specs[len(spy.specs)-1].DirectExe != "" {
			t.Fatalf("%s (%q) must not set DirectExe on expression head, got %q", tc.cid, tc.cmd, spy.specs[len(spy.specs)-1].DirectExe)
		}
		if _, statErr := os.Stat(markerPath); !os.IsNotExist(statErr) {
			t.Fatalf("%s (%q) must not execute script marker", tc.cid, tc.cmd)
		}
	}

	// Unquoted command heads and explicit `&` invocations DO bind DirectExe and execute.
	execCases := []struct {
		cid, cmd, want string
	}{
		{"c-r4-unquoted", `./literal.ps1`, "fixture-script-executed:default"},
		{"c-r4-call-sq", `& './literal.ps1'`, "fixture-script-executed:default"},
		{"c-r4-call-arg", `& './literal.ps1' -Name Audit`, "fixture-script-executed:Audit"},
	}
	for _, tc := range execCases {
		_ = os.Remove(markerPath)
		testBeginTurnCall(t, s, scope, tc.cid, "b-"+tc.cid, 0, "exec_command", nil, "call", 0)
		res, err := s.StartExec(context.Background(), ExecStartRequest{CallID: tc.cid, Command: tc.cmd})
		_, _ = s.End(tc.cid, "completed", "")
		if err != nil || res.Status != "completed" || !strings.Contains(res.Output, tc.want) {
			t.Fatalf("%s (%q) failed: res=%+v err=%v", tc.cid, tc.cmd, res, err)
		}
		if spy.specs[len(spy.specs)-1].DirectExe == "" {
			t.Fatalf("%s (%q) expected DirectExe binding, got empty", tc.cid, tc.cmd)
		}
	}
	verifyRound4CurlyApostrophePATH(t, s, scope, spy)
}

func verifyRound4CurlyApostrophePATH(t *testing.T, s *Service, scope Scope, spy *interceptSpawnBackend) {
	t.Helper()
	oneilDir := filepath.Join(t.TempDir(), "O\u2019Neil")
	if err := os.MkdirAll(oneilDir, 0o700); err != nil {
		t.Fatal(err)
	}
	toolPath := filepath.Join(oneilDir, "r4tool.cmd")
	if err := os.WriteFile(toolPath, []byte("@echo off\r\necho from-oneil-%*\r\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", oneilDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var persisted []config.TrustRule
	s.SetTrustCallbacks(func(rules []config.TrustRule) error {
		persisted = append(persisted, rules...)
		return nil
	}, nil)

	if res := runApprovedFlow(t, s, scope, "c-r4-allow", "allow_always", ExecStartRequest{
		CallID: "c-r4-allow", Command: "r4tool allow", Escalated: true, EscalationReason: "Approve O'Neil tool",
	}); res.Status != "completed" || !strings.Contains(res.Output, "from-oneil-allow") {
		t.Fatalf("expected O\u2019Neil tool initial run to succeed, got %+v", res)
	}
	s.ConfigureTrust(config.ExecutionModeStandard, "rev-r4-oneil", persisted)

	for _, tc := range []struct{ cid, cmd string }{
		{"c-r4-oneil-plain", "r4tool ping"},
		{"c-r4-oneil-call", "& 'r4tool' ping"},
	} {
		testBeginTurnCall(t, s, scope, tc.cid, "b-"+tc.cid, 0, "exec_command", nil, "auto", 0)
		res, err := s.StartExec(context.Background(), ExecStartRequest{
			CallID: tc.cid, Command: tc.cmd, Escalated: true, EscalationReason: "Auto-allow bound tool",
		})
		_, _ = s.End(tc.cid, "completed", "")
		if err != nil || res.Status != "completed" || !strings.Contains(res.Output, "from-oneil-ping") {
			t.Fatalf("%s (%q) expected bound auto-allowed execution, got res=%+v err=%v", tc.cid, tc.cmd, res, err)
		}
		if !pathsEqualOS(spy.specs[len(spy.specs)-1].DirectExe, toolPath) {
			t.Fatalf("%s DirectExe mismatch: got %q want %q", tc.cid, spy.specs[len(spy.specs)-1].DirectExe, toolPath)
		}
	}

	testBeginTurnCall(t, s, scope, "c-r4-quoted-ask", "b-r4-quoted-ask", 0, "exec_command", nil, "ask", 0)
	askRes, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "c-r4-quoted-ask", Command: `"r4tool"`, Escalated: true, EscalationReason: "Quoted head without &",
	})
	if err != nil || askRes.Status != "approval_required" {
		t.Fatalf("quoted head without & must exit command auto-allow, got res=%+v err=%v", askRes, err)
	}
	_ = s.CancelCall("c-r4-quoted-ask", "cancelled")
	_, _ = s.End("c-r4-quoted-ask", "cancelled", "cancelled")
}

func TestExecEscalationRound5LiveAuditFixesAndTurnCallLimit(t *testing.T) {
	s, _, _, scope := openEscalationTestService(t)
	if got := s.Status().Limits["turn_calls"]; got != 500 {
		t.Fatalf("expected turn_calls limit 500, got %d", got)
	}

	// Issue 1 + Issue 4: Tier 1 external write error sets escalation_available and leaves escalated=false.
	extDir := t.TempDir()
	outPath := filepath.Join(extDir, "t1-probe.txt")
	for _, tc := range []struct{ cid, cmd string }{
		{"c-r5-t1-set", `Set-Content -LiteralPath "` + outPath + `" -Value "x" -ErrorAction Stop`},
		{"c-r5-t1-out", `"x" | Out-File -FilePath "` + outPath + `" -Encoding utf8`},
	} {
		testBeginTurnCall(t, s, scope, tc.cid, "b-"+tc.cid, 0, "exec_command", nil, "t1", 0)
		res, err := s.StartExec(context.Background(), ExecStartRequest{CallID: tc.cid, Command: tc.cmd})
		_, _ = s.End(tc.cid, "completed", "")
		if err != nil || res.Escalated || res.UserApproved || res.ApprovalMode != "" || !res.EscalationAvailable {
			t.Fatalf("%s unexpected Tier 1 result: res=%+v err=%v", tc.cid, res, err)
		}
	}
	testBeginTurnCall(t, s, scope, "c-r5-t1-ws", "b-r5-t1-ws", 0, "exec_command", nil, "t1-ws", 0)
	wsRes, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "c-r5-t1-ws", Command: `Set-Content -LiteralPath "missing-dir\inner.txt" -Value "x" -ErrorAction Stop`,
	})
	_, _ = s.End("c-r5-t1-ws", "completed", "")
	if err != nil || wsRes.EscalationAvailable {
		t.Fatalf("workspace-local error must not advertise escalation_available: res=%+v err=%v", wsRes, err)
	}

	verifyRound5EscalationResultMeta(t, s, scope, extDir)
	verifyRound5CmdSystemAndUserProfileSubdir(t, s, scope)
	verifyRound5TurnCallLimit500(t)
}

func verifyRound5EscalationResultMeta(t *testing.T, s *Service, scope Scope, extDir string) {
	t.Helper()
	// Approved once: returns UserApproved=true, ApprovalMode=once, ApprovalDecision=allow_once.
	rOnce := runApprovedFlow(t, s, scope, "c-r5-once", "allow_once", ExecStartRequest{
		CallID: "c-r5-once", Command: `Write-Output "once-ok"`,
		Escalated: true, EscalationReason: "Once test", ExternalPaths: []string{extDir},
	})
	if rOnce.Status != "completed" || !rOnce.Escalated || !rOnce.UserApproved || rOnce.ApprovalMode != "once" || rOnce.ApprovalDecision != "allow_once" {
		t.Fatalf("unexpected allow_once metadata: %+v", rOnce)
	}

	// Approved session_allow -> user_approved=true; follow-up -> user_approved=false, approval_mode=session_allow.
	rSess := runApprovedFlow(t, s, scope, "c-r5-sess1", "allow_session", ExecStartRequest{
		CallID: "c-r5-sess1", Command: `Write-Output "sess-1"`,
		Escalated: true, EscalationReason: "Session test", ExternalPaths: []string{extDir},
	})
	if !rSess.Escalated || !rSess.UserApproved || rSess.ApprovalMode != "session_allow" || rSess.ApprovalDecision != "allow_session" {
		t.Fatalf("unexpected allow_session initial metadata: %+v", rSess)
	}
	zeroYield := 0
	testBeginTurnCall(t, s, scope, "c-r5-sess2", "b-r5-sess2", 0, "exec_command", nil, "sess2", 0)
	rAuto, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "c-r5-sess2", Command: `Write-Output "sess-2"`, YieldMs: &zeroYield,
		Escalated: true, EscalationReason: "Session auto", ExternalPaths: []string{extDir},
	})
	_, _ = s.End("c-r5-sess2", "completed", "")
	if err != nil || !rAuto.Escalated || rAuto.UserApproved || rAuto.ApprovalMode != "session_allow" || rAuto.ApprovalDecision != "" {
		t.Fatalf("unexpected auto session_allow metadata: res=%+v err=%v", rAuto, err)
	}
	if rAuto.Status == "running" {
		testBeginTurnCall(t, s, scope, "c-r5-poll", "b-r5-poll", 0, "process_control", nil, "poll", 0)
		defer func() { _, _ = s.End("c-r5-poll", "completed", "") }()
		waitPoll := 2000
		polled, pErr := s.PollExec(context.Background(), ExecPollRequest{CallID: "c-r5-poll", SessionID: rAuto.SessionID, WaitMs: &waitPoll})
		if pErr != nil || !polled.Escalated || polled.UserApproved || polled.ApprovalMode != "session_allow" {
			t.Fatalf("unexpected polled metadata: polled=%+v err=%v", polled, pErr)
		}
		listed, lErr := s.ControlExec(ExecControlRequest{CallID: "c-r5-poll", Action: "list"})
		var autoSummary *ExecSessionSummary
		for i := range listed.Sessions {
			if listed.Sessions[i].SessionID == rAuto.SessionID {
				autoSummary = &listed.Sessions[i]
				break
			}
		}
		if lErr != nil || autoSummary == nil || !autoSummary.Escalated || autoSummary.ApprovalMode != "session_allow" {
			t.Fatalf("unexpected listed summary metadata for %s: listed=%+v err=%v", rAuto.SessionID, listed, lErr)
		}
	}

	// Denied by user: ApprovalMode=denied, ApprovalDecision=deny.
	otherDir := t.TempDir()
	rDeny := runApprovedFlow(t, s, scope, "c-r5-deny", "deny", ExecStartRequest{
		CallID: "c-r5-deny", Command: `Write-Output "no"`,
		Escalated: true, EscalationReason: "Deny test", ExternalPaths: []string{otherDir},
	})
	if rDeny.Status != "denied_by_user" || !rDeny.Escalated || rDeny.UserApproved || rDeny.ApprovalMode != "denied" || rDeny.ApprovalDecision != "deny" || !rDeny.EscalationDenied {
		t.Fatalf("unexpected deny metadata: %+v", rDeny)
	}

	// Turbo auto: ApprovalMode=turbo_auto, UserApproved=false.
	s.ConfigureTrust(config.ExecutionModeTurbo, "rev-r5-turbo", nil)
	testBeginTurnCall(t, s, scope, "c-r5-turbo", "b-r5-turbo", 0, "exec_command", nil, "turbo", 0)
	rTurbo, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "c-r5-turbo", Command: `Write-Output "turbo"`, Escalated: true, EscalationReason: "Turbo test", ExternalPaths: []string{otherDir},
	})
	_, _ = s.End("c-r5-turbo", "completed", "")
	s.ConfigureTrust(config.ExecutionModeStandard, "rev-r5-std", nil)
	if err != nil || rTurbo.Status != "completed" || !rTurbo.Escalated || rTurbo.UserApproved || rTurbo.ApprovalMode != "turbo_auto" {
		t.Fatalf("unexpected turbo metadata: res=%+v err=%v", rTurbo, err)
	}
}

func verifyRound5CmdSystemAndUserProfileSubdir(t *testing.T, s *Service, scope Scope) {
	t.Helper()
	// Issue 2: cmd /c "..." without externalPaths routes to workspace_escalated approval, not broad_scope_denied.
	testBeginTurnCall(t, s, scope, "c-r5-cmd", "b-r5-cmd", 0, "exec_command", nil, "cmd", 0)
	resCmd, err := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "c-r5-cmd", Command: `cmd /c "ver & hostname"`, Escalated: true, EscalationReason: "Run system cmd check",
	})
	if err != nil || resCmd.Status != "approval_required" {
		_ = s.CancelCall("c-r5-cmd", "cancelled")
		_, _ = s.End("c-r5-cmd", "cancelled", "cancelled")
		t.Fatalf("cmd /c without externalPaths expected approval_required, got res=%+v err=%v", resCmd, err)
	}
	opCmd, _ := s.Operation("c-r5-cmd")
	if opCmd.Approval == nil || opCmd.Approval.Kind != "workspace_escalated" || opCmd.Approval.CanAlways {
		_ = s.CancelCall("c-r5-cmd", "cancelled")
		_, _ = s.End("c-r5-cmd", "cancelled", "cancelled")
		t.Fatalf("expected workspace_escalated with CanAlways=false, got %+v", opCmd.Approval)
	}
	if appErr := s.ApproveTool(scope.Conversation, resCmd.ApprovalID, "allow_once"); appErr != nil {
		_ = s.CancelCall("c-r5-cmd", "cancelled")
		_, _ = s.End("c-r5-cmd", "cancelled", "cancelled")
		t.Fatalf("ApproveTool(cmd): %v", appErr)
	}
	waitMs := 5000
	cmdDone, wErr := s.WaitApproval(context.Background(), ExecApprovalWaitRequest{CallID: "c-r5-cmd", ApprovalID: resCmd.ApprovalID, WaitMs: &waitMs})
	_, _ = s.End("c-r5-cmd", "completed", "")
	if wErr != nil || cmdDone.Status != "completed" || !cmdDone.UserApproved || cmdDone.ApprovalMode != "once" {
		t.Fatalf("cmd /c approved execution failed: res=%+v err=%v", cmdDone, wErr)
	}

	// Issue 3: non-existent child subdir under USERPROFILE prompts for narrowed subdir rather than broad_scope_denied.
	fakeHome := t.TempDir()
	t.Setenv("USERPROFILE", fakeHome)
	t.Setenv("HOME", fakeHome)
	if _, err := s.StartExec(context.Background(), ExecStartRequest{}); err == nil {
		t.Fatal("expected empty request error")
	}
	testBeginTurnCall(t, s, scope, "c-r5-home-root", "b-r5-home-root", 0, "exec_command", nil, "hr", 0)
	_, errRoot := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "c-r5-home-root", Command: `Write-Output "x"`, Escalated: true, EscalationReason: "Root", ExternalPaths: []string{fakeHome},
	})
	_, _ = s.End("c-r5-home-root", "failed", Code(errRoot))
	if Code(errRoot) != "broad_scope_denied" {
		t.Fatalf("bare USERPROFILE must fail with broad_scope_denied, got %v", errRoot)
	}
	testBeginTurnCall(t, s, scope, "c-r5-home-loose", "b-r5-home-loose", 0, "exec_command", nil, "hl", 0)
	_, errLoose := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "c-r5-home-loose", Command: `Set-Content -LiteralPath "` + filepath.Join(fakeHome, "loose.txt") + `" -Value "x"`,
		Escalated: true, EscalationReason: "Loose file", ExternalPaths: []string{filepath.Join(fakeHome, "loose.txt")},
	})
	_, _ = s.End("c-r5-home-loose", "failed", Code(errLoose))
	if Code(errLoose) != "broad_scope_denied" {
		t.Fatalf("loose file directly in USERPROFILE root must fail with broad_scope_denied, got %v", errLoose)
	}
	subDir := filepath.Join(fakeHome, "openclaw-escalation-test")
	testBeginTurnCall(t, s, scope, "c-r5-home-sub", "b-r5-home-sub", 0, "exec_command", nil, "hs", 0)
	subRes, errSub := s.StartExec(context.Background(), ExecStartRequest{
		CallID: "c-r5-home-sub", Command: `New-Item -ItemType Directory -Force -Path "` + subDir + `"`,
		Escalated: true, EscalationReason: "Create named subdir", ExternalPaths: []string{subDir},
	})
	opSub, _ := s.Operation("c-r5-home-sub")
	_ = s.CancelCall("c-r5-home-sub", "cancelled")
	_, _ = s.End("c-r5-home-sub", "cancelled", "cancelled")
	if errSub != nil || subRes.Status != "approval_required" || opSub.Approval == nil || !pathsEqualOS(opSub.Approval.Target, subDir) || !opSub.Approval.CanAlways {
		t.Fatalf("non-existent subdir under USERPROFILE should ask narrowed target=%q, res=%+v op=%+v err=%v", subDir, subRes, opSub.Approval, errSub)
	}
}

func verifyRound5TurnCallLimit500(t *testing.T) {
	t.Helper()
	s, _, _, limitScope := openEscalationTestService(t)
	for i := 0; i < 500; i++ {
		cid := fmt.Sprintf("c-lim-%03d", i)
		if _, err := s.Begin(Begin{
			ID: cid, Scope: limitScope, Tool: "memory_get", Path: "MEMORY.md", Fingerprint: "f-" + cid,
		}); err != nil {
			t.Fatalf("call %d unexpectedly failed before limit 500: %v", i, err)
		}
		_, _ = s.End(cid, "completed", "")
	}
	if _, err := s.Begin(Begin{
		ID: "c-lim-500", Scope: limitScope, Tool: "memory_get", Path: "MEMORY.md", Fingerprint: "f-500",
	}); Code(err) != "call_limit" {
		t.Fatalf("expected call_limit on 501st call, got %v", err)
	}
}
