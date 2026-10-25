package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jackgan90/Asterhand/internal/config"
)

func TestRuntimeCLIRejectsUnknownSelectionAndInvalidOptionsBeforeSave(t *testing.T) {
	m, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(m.Home, "config.json"))
	options := filepath.Join(t.TempDir(), "options.json")
	if err = os.WriteFile(options, []byte(`{"unrecognized":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	previous := os.Args
	defer func() { os.Args = previous }()
	for _, args := range [][]string{{"--agent-runtime", "not-registered"}, {"--runtime-options", options}} {
		os.Args = append([]string{"asterhand", "repair", "--home", m.Home}, args...)
		if run() == nil {
			t.Fatal("invalid CLI runtime selection accepted")
		}
		after, _ := os.ReadFile(filepath.Join(m.Home, "config.json"))
		if string(before) != string(after) {
			t.Fatal("invalid CLI options changed saved configuration")
		}
	}
}

func TestRuntimeCLIRecordsExplicitOptions(t *testing.T) {
	m, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	options := filepath.Join(t.TempDir(), "options.json")
	if err = os.WriteFile(options, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	previous := os.Args
	defer func() { os.Args = previous }()
	os.Args = []string{"asterhand", "repair", "--home", m.Home, "--basic", "--runtime-options", options}
	if err = run(); err != nil {
		t.Fatal(err)
	}
	m, err = config.Open(m.Home)
	if err != nil {
		t.Fatal(err)
	}
	c := m.Get()
	if !c.RuntimeOptionsExplicit[c.AgentRuntime] || c.ModelVerified {
		t.Fatal("CLI provenance/readiness not saved")
	}
	// Returned marker maps must be independent snapshots just like raw options.
	c.RuntimeOptionsExplicit[c.AgentRuntime] = false
	if !m.Get().RuntimeOptionsExplicit[c.AgentRuntime] {
		t.Fatal("snapshot aliases live provenance")
	}
	if err = m.Save(c); err != nil {
		t.Fatal(err)
	}
	c.RuntimeOptionsExplicit[c.AgentRuntime] = true
	if m.Get().RuntimeOptionsExplicit[c.AgentRuntime] {
		t.Fatal("save aliases caller provenance")
	}
}
