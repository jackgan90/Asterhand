package launch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jackgan90/Asterhand/internal/i18n"
)

// ApplyInstallationLocale reads an existing node's desktop-settings.json (read-only)
// to align launcher error dialogs with the user's saved language preference.
func ApplyInstallationLocale(installationPath string) {
	if installation, err := ReadInstallation(installationPath); err == nil {
		applyDataLocale(installation.Data)
	}
}

func applyDataLocale(dataDir string) {
	var prefs struct {
		Schema int    `json:"schema_version"`
		Locale string `json:"locale"`
	}
	if err := readJSON(filepath.Join(dataDir, "desktop-settings.json"), &prefs); err == nil && prefs.Schema == 1 {
		if prefs.Locale == "" {
			prefs.Locale = "auto"
		}
		if i18n.ValidPreference(prefs.Locale) {
			i18n.SetDefaultPreference(prefs.Locale)
		}
	}
}

// LogError returns the directory offered by the native error dialog. Invalid
// installation or home records fall back to the application directory, never
// to an unvalidated data path from the record.
func LogError(installationPath string, failure error) string {
	root := filepath.Dir(installationPath)
	if installation, err := ReadInstallation(installationPath); err == nil {
		if prepareHome(installation.Data) == nil && appendDiagnostic(installation.Data, failure) == nil {
			return installation.Data
		}
	}
	_ = appendDiagnostic(root, failure)
	return root
}

func appendDiagnostic(directory string, failure error) error {
	path := filepath.Join(directory, "launcher.log")
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && !info.Mode().IsRegular() {
		return i18n.Err("err.launch.log_regular_file")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s %v\n", time.Now().Format(time.RFC3339), failure)
	return err
}
