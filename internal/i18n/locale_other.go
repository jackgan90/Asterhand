//go:build !windows

package i18n

import (
	"os"
	"strings"
)

func detectSystemLanguages() ([]string, bool) {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		val := strings.TrimSpace(os.Getenv(key))
		if val == "" || strings.EqualFold(val, "C") || strings.HasPrefix(strings.ToUpper(val), "C.") || strings.EqualFold(val, "POSIX") {
			continue
		}
		return []string{val}, true
	}
	return nil, false
}
