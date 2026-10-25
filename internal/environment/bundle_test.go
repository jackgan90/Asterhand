package environment

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBundleRejectsChangedCatalogBeforeRuntimeExecution(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "packaging"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "packaging", "dependencies.json"), []byte(`{"schema":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	err := CheckBundle(context.Background(), base)
	if err == nil || !strings.Contains(err.Error(), "清单") {
		t.Fatal("changed catalog accepted", err)
	}
	entries, _ := os.ReadDir(base)
	if len(entries) != 1 {
		t.Fatal("bundle diagnosis created node data")
	}
}
