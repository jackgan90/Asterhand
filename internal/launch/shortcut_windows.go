//go:build windows

package launch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

const (
	shortcutFileName    = "Asterhand.lnk"
	shortcutDescription = "Asterhand - personal assistant across your devices"
	shortcutLockTimeout = 5 * time.Second
)

func platformShortcutSupported() bool {
	return true
}

func defaultDesktopDir() (string, error) {
	dir, err := windows.KnownFolderPath(windows.FOLDERID_Desktop, 0)
	if err != nil || strings.TrimSpace(dir) == "" {
		return "", NewShortcutError(503, "desktop_unavailable", "err.launch.shortcut_desktop_unavailable", nil)
	}
	return filepath.Clean(dir), nil
}

func resolveShortcutTarget(opts ShortcutOptions) (installRoot, launcherPath, desktopDir string, st ShortcutStatus, err *ShortcutError) {
	st = ShortcutStatus{Supported: true}
	exe, exeErr := opts.Executable()
	if exeErr != nil || strings.TrimSpace(exe) == "" {
		st.Reason = "not_installed"
		st.MessageKey = "status.desktop.shortcut_not_installed"
		return "", "", "", st, NewShortcutError(503, "not_installed", "status.desktop.shortcut_not_installed", nil)
	}
	if canonical, cErr := canonicalPath(exe); cErr == nil {
		exe = canonical
	}
	installPath := FindInstallation(exe, opts.Home)
	if installPath == "" {
		st.Reason = "not_installed"
		st.MessageKey = "status.desktop.shortcut_not_installed"
		return "", "", "", st, NewShortcutError(503, "not_installed", "status.desktop.shortcut_not_installed", nil)
	}
	installRoot = filepath.Dir(installPath)
	launcherPath = filepath.Join(installRoot, "AsterhandLauncher.exe")
	info, statErr := os.Stat(launcherPath)
	if statErr != nil || !info.Mode().IsRegular() {
		st.Reason = "not_installed"
		st.MessageKey = "status.desktop.shortcut_not_installed"
		return "", "", "", st, NewShortcutError(503, "not_installed", "status.desktop.shortcut_not_installed", nil)
	}
	desktop, dErr := opts.DesktopDir()
	desktop = strings.TrimSpace(desktop)
	if dErr != nil || desktop == "" || !filepath.IsAbs(desktop) {
		st.Reason = "desktop_unavailable"
		st.MessageKey = "err.launch.shortcut_desktop_unavailable"
		return "", "", "", st, NewShortcutError(503, "desktop_unavailable", "err.launch.shortcut_desktop_unavailable", nil)
	}
	desktopDir = filepath.Clean(desktop)
	st.Available = true
	return installRoot, launcherPath, desktopDir, st, nil
}

func inspectShortcutFile(shortcutPath, installRoot, launcherPath string) (exists, owned bool) {
	info, err := os.Lstat(shortcutPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, false
		}
		return true, false
	}
	if !info.Mode().IsRegular() {
		return true, false
	}
	target, args, readErr := readShellLink(shortcutPath)
	if readErr != nil {
		return true, false
	}
	if pathsEquivalent(target, launcherPath) {
		return true, true
	}
	sysRoot := os.Getenv("SystemRoot")
	if sysRoot == "" {
		sysRoot = `C:\Windows`
	}
	psExe := filepath.Join(sysRoot, `System32\WindowsPowerShell\v1.0\powershell.exe`)
	legacyArgs := `-NoProfile -WindowStyle Hidden -File "` + filepath.Join(installRoot, "start.ps1") + `" -NoBrowser`
	return true, pathsEquivalent(target, psExe) && args == legacyArgs
}

func pathsEquivalent(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if samePath(a, b) {
		return true
	}
	ca, errA := canonicalPath(a)
	cb, errB := canonicalPath(b)
	return errA == nil && errB == nil && samePath(ca, cb)
}

func (m *ShortcutManager) inspectLocked() ShortcutStatus {
	installRoot, launcherPath, desktopDir, st, err := resolveShortcutTarget(m.opts)
	if err != nil {
		return st
	}
	exists, owned := inspectShortcutFile(filepath.Join(desktopDir, shortcutFileName), installRoot, launcherPath)
	switch {
	case exists && owned:
		st.Enabled = true
	case exists && !owned:
		st.Conflict = true
		st.Reason = "conflict"
		st.MessageKey = "err.launch.shortcut_conflict"
	}
	return st
}

func (m *ShortcutManager) applyPlatformLocked(ctx context.Context, enabled bool) (ShortcutStatus, error) {
	installRoot, launcherPath, desktopDir, st, resolveErr := resolveShortcutTarget(m.opts)
	if resolveErr != nil {
		return st, resolveErr
	}
	lockCtx, cancel := context.WithTimeout(ctx, shortcutLockTimeout)
	defer cancel()
	unlock, lockErr := acquireLaunchLock(lockCtx, m.opts.Home)
	if lockErr != nil {
		return m.inspectLocked(), NewShortcutError(503, "busy", "err.launch.mutex_busy", nil)
	}
	defer unlock()

	shortcutPath := filepath.Join(desktopDir, shortcutFileName)
	exists, owned := inspectShortcutFile(shortcutPath, installRoot, launcherPath)
	if enabled {
		if exists && !owned {
			conflict := m.inspectLocked()
			return conflict, NewShortcutError(409, "conflict", "err.launch.shortcut_conflict", nil)
		}
		if mkErr := os.MkdirAll(desktopDir, 0755); mkErr != nil {
			return m.inspectLocked(), NewShortcutError(503, "io_error", "err.launch.shortcut_update_failed", map[string]string{"err": mkErr.Error()})
		}
		if writeErr := writeShellLink(shortcutPath, installRoot, launcherPath); writeErr != nil {
			return m.inspectLocked(), NewShortcutError(503, "io_error", "err.launch.shortcut_update_failed", map[string]string{"err": writeErr.Error()})
		}
		notifyShellPath(shcneCreate|shcneUpdateItem, shortcutPath)
		return m.inspectLocked(), nil
	}

	if exists && owned {
		if rmErr := os.Remove(shortcutPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return m.inspectLocked(), NewShortcutError(503, "io_error", "err.launch.shortcut_update_failed", map[string]string{"err": rmErr.Error()})
		}
		notifyShellPath(shcneDelete, shortcutPath)
	}
	return m.inspectLocked(), nil
}
