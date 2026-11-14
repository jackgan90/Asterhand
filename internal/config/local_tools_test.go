package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalToolsDefaultAndSavedChoice(t *testing.T) {
	for _, tc := range []struct {
		name, stored string
		enabled      bool
		revision     string
	}{
		{"new node", "", true, "default-tools-v1"},
		{"missing field", `{}`, true, "default-tools-v1"},
		{"old automatic default", `{"local_runtime_tools":{"enabled":false}}`, true, "default-tools-v1"},
		{"explicit off", `{"local_runtime_tools":{"enabled":false,"revision":"user-off"}}`, false, "user-off"},
		{"explicit on", `{"local_runtime_tools":{"enabled":true,"revision":"user-on"}}`, true, "user-on"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if tc.stored != "" {
				if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(tc.stored), 0600); err != nil {
					t.Fatal(err)
				}
			}
			manager, err := Open(home)
			if err != nil {
				t.Fatal(err)
			}
			want := LocalToolPolicy{Enabled: tc.enabled, Revision: tc.revision}
			if got := manager.Get().LocalTools; got != want {
				t.Fatalf("loaded policy: %+v; want %+v", got, want)
			}
			c := manager.Get()
			c.Name = "renamed node"
			if err = manager.Save(c); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(home)
			if err != nil {
				t.Fatal(err)
			}
			if got := reopened.Get().LocalTools; got != want {
				t.Fatalf("unrelated save or restart changed policy: %+v; want %+v", got, want)
			}
		})
	}
}
