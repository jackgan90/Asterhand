package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jackgan90/Asterhand/internal/launch"
)

func TestBuildCLIForwardArgs(t *testing.T) {
	inst := launch.Installation{
		Schema:     1,
		Executable: `C:\Apps\Asterhand\versions\v1\asterhand.exe`,
		Data:       `D:\AsterhandData`,
	}

	// wait injects --home after subcommand unless already supplied.
	got, ok := buildCLIForwardArgs(inst, []string{"wait", "turn_123", "--timeout", "900"})
	want := []string{"wait", "--home", `D:\AsterhandData`, "turn_123", "--timeout", "900"}
	if !ok || !slices.Equal(got, want) {
		t.Fatalf("wait forward args: ok=%v got=%v want=%v", ok, got, want)
	}

	gotExplicit, ok := buildCLIForwardArgs(inst, []string{"wait", "turn_123", "--home=E:\\Custom"})
	wantExplicit := []string{"wait", "turn_123", "--home=E:\\Custom"}
	if !ok || !slices.Equal(gotExplicit, wantExplicit) {
		t.Fatalf("wait explicit home: ok=%v got=%v want=%v", ok, gotExplicit, wantExplicit)
	}

	// automation <op> injects --home AFTER operation name so RunCLI sees op at args[0].
	gotAuto, ok := buildCLIForwardArgs(inst, []string{"automation", "workers_result", "--caller", "claude-code", "--args", `{"turn_id":"turn_1"}`})
	wantAuto := []string{"automation", "workers_result", "--home", `D:\AsterhandData`, "--caller", "claude-code", "--args", `{"turn_id":"turn_1"}`}
	if !ok || !slices.Equal(gotAuto, wantAuto) {
		t.Fatalf("automation op forward args: ok=%v got=%v want=%v", ok, gotAuto, wantAuto)
	}

	// Offline automation helpers remain offline without --home injection.
	for _, offline := range [][]string{
		{"automation", "help"},
		{"automation", "schema", "workers_send"},
		{"automation", "new-id"},
		{"version"},
	} {
		gotOff, ok := buildCLIForwardArgs(inst, offline)
		if !ok || !slices.Equal(gotOff, offline) {
			t.Fatalf("offline %v: ok=%v got=%v", offline, ok, gotOff)
		}
	}

	// status injects --home.
	gotStatus, ok := buildCLIForwardArgs(inst, []string{"status"})
	wantStatus := []string{"status", "--home", `D:\AsterhandData`}
	if !ok || !slices.Equal(gotStatus, wantStatus) {
		t.Fatalf("status forward args: ok=%v got=%v want=%v", ok, gotStatus, wantStatus)
	}

	// Unsafe / unsupported subcommands are rejected.
	for _, bad := range [][]string{nil, {"start"}, {"setup"}, {"repair"}, {"mcp"}, {"doctor"}} {
		if _, ok := buildCLIForwardArgs(inst, bad); ok {
			t.Fatalf("expected unsupported subcommand %v to be rejected", bad)
		}
	}
}

func TestRunCLIForwarderErrorsWriteOnlyStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runCLIForwarder(filepath.Join(t.TempDir(), "installation.json"), []string{"wait", "turn_1"}, nil, &stdout, &stderr)
	if code == 0 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "asterhand-cli:") {
		t.Fatalf("unexpected missing installation behavior: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	codeBad := runCLIForwarder(filepath.Join(t.TempDir(), "installation.json"), []string{"start"}, nil, &stdout, &stderr)
	if codeBad != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "unsupported subcommand") {
		t.Fatalf("expected exit 2 for unsupported start command, got code=%d stdout=%q stderr=%q", codeBad, stdout.String(), stderr.String())
	}

	root := t.TempDir()
	data := t.TempDir()
	outsideExe := filepath.Join(t.TempDir(), "asterhand.exe")
	if err := os.WriteFile(outsideExe, []byte("stub"), 0600); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"schema": 1, "executable": outsideExe, "data": data})
	installJSON := filepath.Join(root, "installation.json")
	if err := os.WriteFile(installJSON, b, 0600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	codeEsc := runCLIForwarder(installJSON, []string{"wait", "turn_1"}, nil, &stdout, &stderr)
	if codeEsc == 0 || stdout.Len() != 0 {
		t.Fatalf("expected escaping executable to fail cleanly with zero stdout, got code=%d stdout=%q", codeEsc, stdout.String())
	}
}
