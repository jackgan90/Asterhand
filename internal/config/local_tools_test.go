package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
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
			want := LocalToolPolicy{
				Enabled:       tc.enabled,
				Revision:      tc.revision,
				ExecutionMode: ExecutionModeStandard,
				TrustRevision: "default-trust-v1",
			}
			if got := manager.Get().LocalTools; !reflect.DeepEqual(got, want) {
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
			if got := reopened.Get().LocalTools; !reflect.DeepEqual(got, want) {
				t.Fatalf("unrelated save or restart changed policy: %+v; want %+v", got, want)
			}
		})
	}
}

func TestLocalToolsTrustUpdatePreservesToolRevision(t *testing.T) {
	manager, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	before := manager.Get().LocalTools
	updated, err := manager.UpdateLocalToolsTrust(func(cur LocalToolPolicy) (LocalToolPolicy, error) {
		cur.ExecutionMode = ExecutionModeTurbo
		cur.TrustRules = append(cur.TrustRules, TrustRule{
			ID:      "rule-1",
			Kind:    "folder",
			Pattern: `D:\Data`,
			Action:  "allow",
			Label:   `D:\Data`,
		})
		return cur, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enabled != before.Enabled || updated.Revision != before.Revision {
		t.Fatalf("trust update altered tool policy revision: before=%+v after=%+v", before, updated)
	}
	if updated.ExecutionMode != ExecutionModeTurbo || updated.TrustRevision == before.TrustRevision || len(updated.TrustRules) != 1 {
		t.Fatalf("unexpected trust update result: %+v", updated)
	}
}

func TestConcurrentTrustUpdateAndGeneralConfigSavePreservesRevocation(t *testing.T) {
	manager, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpdateLocalToolsTrust(func(cur LocalToolPolicy) (LocalToolPolicy, error) {
		cur.ExecutionMode = ExecutionModeTurbo
		cur.TrustRules = []TrustRule{
			{ID: "keep-rule", Kind: "folder", Pattern: `D:\Keep`, Action: "allow", Label: `D:\Keep`},
			{ID: "drop-rule", Kind: "folder", Pattern: `D:\Drop`, Action: "allow", Label: `D:\Drop`},
		}
		return cur, nil
	}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	// Concurrent trust revocation: remove drop-rule and revert turbo -> standard.
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := manager.UpdateLocalToolsTrust(func(cur LocalToolPolicy) (LocalToolPolicy, error) {
			cur.ExecutionMode = ExecutionModeStandard
			filtered := cur.TrustRules[:0]
			for _, r := range cur.TrustRules {
				if r.ID != "drop-rule" {
					filtered = append(filtered, r)
				}
			}
			cur.TrustRules = filtered
			return cur, nil
		}); err != nil {
			t.Errorf("UpdateLocalToolsTrust: %v", err)
		}
	}()

	// Concurrent ordinary settings updates preserving LocalTools.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if _, err := manager.Update(func(cur Config) (Config, error) {
				cur.Name = fmt.Sprintf("node-%d", idx)
				return cur, nil
			}); err != nil {
				t.Errorf("Update(%d): %v", idx, err)
			}
		}(i)
	}
	wg.Wait()

	got := manager.Get().LocalTools
	if got.ExecutionMode != ExecutionModeStandard {
		t.Fatalf("lost ExecutionModeStandard revert under concurrent save: %+v", got)
	}
	if len(got.TrustRules) != 1 || got.TrustRules[0].ID != "keep-rule" {
		t.Fatalf("lost rule revocation under concurrent save: %+v", got.TrustRules)
	}
}
