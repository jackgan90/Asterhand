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

func TestBuildForwardArgsInjectsDataHomeWhenMissing(t *testing.T) {
	inst := launch.Installation{
		Schema:     1,
		Executable: `C:\Apps\Asterhand\versions\v1\asterhand.exe`,
		Data:       `D:\AsterhandData`,
	}
	got := buildForwardArgs(inst, []string{"--caller", "antigravity"})
	want := []string{"mcp", "--home", `D:\AsterhandData`, "--caller", "antigravity"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildForwardArgsRespectsExplicitHome(t *testing.T) {
	inst := launch.Installation{
		Schema:     1,
		Executable: `C:\Apps\Asterhand\versions\v1\asterhand.exe`,
		Data:       `D:\AsterhandData`,
	}
	for _, tc := range [][]string{
		{"--home", `E:\Custom`, "--caller", "codex"},
		{"--home=E:\\Custom", "--caller", "codex"},
		{"-home", `E:\Custom`, "--caller", "claude-code"},
	} {
		got := buildForwardArgs(inst, tc)
		want := append([]string{"mcp"}, tc...)
		if !slices.Equal(got, want) {
			t.Fatalf("for %v: got %v, want %v", tc, got, want)
		}
	}
}

func TestRunForwarderMissingInstallationWritesOnlyStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runForwarder(filepath.Join(t.TempDir(), "installation.json"), []string{"--caller", "antigravity"}, nil, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit code")
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected empty stdout on error, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "asterhand-mcp:") {
		t.Fatalf("expected stderr diagnostic, got %q", stderr.String())
	}
}

func TestRunForwarderRejectsEscapingExecutable(t *testing.T) {
	root := t.TempDir()
	data := t.TempDir()
	outsideExe := filepath.Join(t.TempDir(), "asterhand.exe")
	if err := os.WriteFile(outsideExe, []byte("stub"), 0600); err != nil {
		t.Fatal(err)
	}
	rec := map[string]any{
		"schema":     1,
		"executable": outsideExe,
		"data":       data,
	}
	b, _ := json.Marshal(rec)
	installJSON := filepath.Join(root, "installation.json")
	if err := os.WriteFile(installJSON, b, 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runForwarder(installJSON, nil, nil, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit code for executable outside versions/")
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected zero stdout bytes, got %q", stdout.String())
	}
}
