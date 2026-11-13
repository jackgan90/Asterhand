//go:build !windows

package launch

import "context"

func platformShortcutSupported() bool {
	return false
}

func defaultDesktopDir() (string, error) {
	return "", NewShortcutError(400, "unsupported", "err.launch.shortcut_unsupported", nil)
}

func (m *ShortcutManager) inspectLocked() ShortcutStatus {
	return ShortcutStatus{Supported: false}
}

func (m *ShortcutManager) applyPlatformLocked(ctx context.Context, enabled bool) (ShortcutStatus, error) {
	return ShortcutStatus{Supported: false}, NewShortcutError(400, "unsupported", "err.launch.shortcut_unsupported", nil)
}
