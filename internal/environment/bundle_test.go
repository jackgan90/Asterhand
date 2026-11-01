package environment

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackgan90/Asterhand/internal/i18n"
)

func TestBundleRejectsChangedCatalogBeforeRuntimeExecution(t *testing.T) {
	i18n.SetDefaultPreference("zh-CN")
	t.Cleanup(func() { i18n.SetDefaultPreference("auto") })
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
	if msg, key, _ := i18n.LocalizeError("en-US", err); key != "err.env.manifest_mismatch" || !strings.Contains(msg, "manifest") {
		t.Fatalf("unexpected english localization: %q %q", key, msg)
	}
	entries, _ := os.ReadDir(base)
	if len(entries) != 1 {
		t.Fatal("bundle diagnosis created node data")
	}
}
