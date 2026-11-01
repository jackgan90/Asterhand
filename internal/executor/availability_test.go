package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/testutil/runtimefixture"
)

func TestPackageManifestBudgetSupportsLockedRuntimeAndRejectsIncompleteData(t *testing.T) {
	manifest := `{"version":"2026.10.7","exports":"` + strings.Repeat("x", 150*1024) + `"}`
	for _, tc := range []struct {
		name, content string
		valid         bool
	}{
		{"large-valid-manifest", manifest, true},
		{"truncated", manifest[:len(manifest)-1], false},
		{"wrong-version", strings.Replace(manifest, "2026.10.7", "2026.1.1", 1), false},
		{"trailing-json", manifest + "{}", false},
		{"over-budget", manifest + strings.Repeat(" ", 512*1024), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "package.json")
			if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			if packageVersion(path, "2026.10.7") != tc.valid {
				t.Fatal("incorrect manifest availability", tc.name)
			}
		})
	}
}

func TestLocalConfigurationDoesNotRequireProbeOrTaskWorkspace(t *testing.T) {
	manager, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := runtimefixture.Configure(t, manager)
	c := manager.Get()
	if c.ModelVerified || len(c.Projects) != 0 {
		t.Fatal("fixture unexpectedly verified or authorized")
	}
	for range 3 {
		status := ConfiguredCapabilities(manager, c)
		if !status.Chat.Available || !status.ModelTasks.Available {
			t.Fatalf("local prerequisites rejected: %+v", status)
		}
	}
	// The dummy node.exe cannot execute. A successful status proves no process
	// is needed; no task run directory or Gateway state may appear.
	for _, name := range []string{"runs", "conversations"} {
		if _, err := os.Stat(filepath.Join(manager.Home, name)); !os.IsNotExist(err) {
			t.Fatal("configuration check started runtime", name, err)
		}
	}
	if err := os.Remove(filepath.Join(root, "tools", "index.js")); err != nil {
		t.Fatal(err)
	}
	status := ConfiguredCapabilities(manager, c)
	if !status.Chat.Available || status.ModelTasks.Available || status.ModelTasks.Code != TaskToolsMissing {
		t.Fatalf("task tool installation blocked chat: %+v", status)
	}
	runtime := NewConversationRuntime(manager)
	defer runtime.Close()
	descriptor, err := runtime.Describe()
	if err != nil || !descriptor.CanSend {
		t.Fatal("chat descriptor still depends on task probe/tools", descriptor, err)
	}
}

func TestConfigurationReportsMissingUnreadableAndDriftedInputs(t *testing.T) {
	for _, kind := range []string{"missing-key", "empty-key", "unreadable-key", "missing-node", "directory-entry", "provider-version", "selection"} {
		t.Run(kind, func(t *testing.T) {
			manager, err := config.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root := runtimefixture.Configure(t, manager)
			c := manager.Get()
			expected := CredentialMissing
			switch kind {
			case "missing-key":
				err = manager.SetKey("")
			case "empty-key":
				err = manager.SetKey("   ")
			case "unreadable-key":
				path := filepath.Join(manager.Home, c.LLM.CredentialRef+".secret")
				err = os.Remove(path)
				if err == nil {
					err = os.Mkdir(path, 0700)
				}
				expected = CredentialUnreadable
			case "missing-node":
				err = os.Remove(filepath.Join(root, "node.exe"))
				expected = RuntimeMissing
			case "directory-entry":
				options, _ := clawOptions(c)
				options.EntryPath = root
				setClawOptions(&c, options)
				expected = RuntimeMissing
			case "provider-version":
				err = os.WriteFile(filepath.Join(root, "provider", "package.json"), []byte(`{"version":"old"}`), 0600)
				expected = RuntimeIncompatible
			case "selection":
				c.AgentRuntime = "unsupported"
				expected = InvalidConfiguration
			}
			if err != nil {
				t.Fatal(err)
			}
			status := ConfiguredCapabilities(manager, c)
			if status.Chat.Available || status.ModelTasks.Available || status.ModelTasks.Code != expected || status.ModelTasks.Detail == "" {
				t.Fatalf("wrong local status: %+v", status)
			}
		})
	}
}
