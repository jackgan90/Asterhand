package launch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jackgan90/Asterhand/internal/i18n"
)

func TestLauncherReadsDataLocaleAndFallsBackSafely(t *testing.T) {
	prevPref := i18n.DefaultPreference()
	t.Cleanup(func() { i18n.SetDefaultPreference(prevPref) })
	restoreSys := i18n.SetSystemLocaleForTest([]string{"zh-CN"}, true)
	t.Cleanup(restoreSys)

	dataDir := t.TempDir()
	settingsPath := filepath.Join(dataDir, "desktop-settings.json")

	// Explicit en-US overrides zh-CN OS language for launcher errors.
	if err := os.WriteFile(settingsPath, []byte(`{"schema_version":1,"locale":"en-US"}`), 0600); err != nil {
		t.Fatal(err)
	}
	applyDataLocale(dataDir)
	if got := i18n.Effective(); got != "en-US" {
		t.Fatalf("expected launcher locale en-US, got %q", got)
	}
	if _, err := ExistingURL(dataDir); err == nil || err.Error() != i18n.T("en-US", "err.launch.not_running") {
		t.Fatalf("expected localized English launcher error, got %v", err)
	}

	// Legacy desktop-settings.json without locale resolves to auto (OS zh-CN).
	if err := os.WriteFile(settingsPath, []byte(`{"schema_version":1,"enabled":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	applyDataLocale(dataDir)
	if got := i18n.Effective(); got != "zh-CN" {
		t.Fatalf("expected legacy auto locale zh-CN from OS, got %q", got)
	}
	if _, err := ExistingURL(dataDir); err == nil || err.Error() != i18n.T("zh-CN", "err.launch.not_running") {
		t.Fatalf("expected localized Chinese launcher error, got %v", err)
	}

	// Corrupt desktop-settings.json is ignored read-only without mutating the file.
	corrupt := []byte("{corrupt")
	if err := os.WriteFile(settingsPath, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	i18n.SetDefaultPreference("auto")
	restoreUndetected := i18n.SetSystemLocaleForTest(nil, false)
	defer restoreUndetected()
	applyDataLocale(dataDir)
	if got := i18n.Effective(); got != "en-US" {
		t.Fatalf("expected undetected OS + corrupt file to fall back to en-US, got %q", got)
	}
	after, err := os.ReadFile(settingsPath)
	if err != nil || string(after) != string(corrupt) {
		t.Fatalf("launcher modified corrupt desktop-settings.json: %q %v", string(after), err)
	}
}
