//go:build windows

package quickinput

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// The host does not set an AppUserModelID. WebView2 therefore selects policy by
// executable name and then wildcard. External browser arguments/profile paths
// could disable isolation; reject them instead of changing the user's policy.
func checkHostRuntimePolicy() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		for _, view := range []uint32{registry.WOW64_64KEY, registry.WOW64_32KEY} {
			for _, option := range []string{"AdditionalBrowserArguments", "UserDataFolder", "BrowserExecutableFolder"} {
				key, err := registry.OpenKey(root, `Software\Policies\Microsoft\Edge\WebView2\`+option, registry.QUERY_VALUE|view)
				if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
					continue
				}
				if err != nil {
					return hostFailure("runtime_override")
				}
				err = checkHostPolicyValues(key, []string{filepath.Base(executable), "*"})
				key.Close()
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func checkHostPolicyValues(key registry.Key, names []string) error {
	for _, name := range names {
		value, _, err := key.GetStringValue(name)
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			continue
		}
		if err != nil || value != "" {
			return hostFailure("runtime_override")
		}
	}
	return nil
}
