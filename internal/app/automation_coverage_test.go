package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAutomationCoverage(t *testing.T) {
	var manifest struct {
		Version int `json:"api_version"`
		Groups  []struct {
			ID         string   `json:"id"`
			Operations []string `json:"operations"`
			Tests      []string `json:"tests"`
		} `json:"groups"`
	}
	root := filepath.Join("..", "..")
	b, err := os.ReadFile(filepath.Join(root, "contracts", "automation-coverage.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != 1 {
		t.Fatal("unsupported manifest")
	}
	names := map[string]string{}
	for _, group := range manifest.Groups {
		for _, name := range group.Operations {
			if names[name] != "" {
				t.Fatal("duplicate coverage", name)
			}
			names[name] = group.ID
		}
		for _, file := range group.Tests {
			if _, err := os.Stat(filepath.Join(root, file)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, d := range AutomationDefinitions() {
		if names[d.Name] == "" {
			t.Errorf("operation %s lacks coverage and validation evidence", d.Name)
		}
		delete(names, d.Name)
		if d.Schema["type"] != "object" || d.OutputSchema["type"] != "object" {
			t.Errorf("%s lacks shared schemas", d.Name)
		}
		data, ok := d.OutputSchema["properties"].(map[string]any)["data"].(map[string]any)
		if !ok || len(data) == 0 {
			t.Errorf("%s lacks an explicit result schema", d.Name)
		}
	}
	for name := range names {
		t.Errorf("coverage advertises missing operation %s", name)
	}
}
