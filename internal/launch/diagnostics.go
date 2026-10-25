package launch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

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
		return errors.New("启动器日志必须是普通文件")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s %v\n", time.Now().Format(time.RFC3339), failure)
	return err
}
