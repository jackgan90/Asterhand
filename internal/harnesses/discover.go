package harnesses

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type CommandRunner func(ctx context.Context, exe string, args ...string) (stdout, stderr string, err error)

type versionCacheEntry struct {
	modUnixNano int64
	size        int64
	version     string
}

func (s *Service) runner() CommandRunner {
	if s.CmdRunner != nil {
		return s.CmdRunner
	}
	return defaultCommandRunner
}

// resolveExecutable checks manual path override -> PATH -> known desktop install directories.
func (s *Service) resolveExecutable(harnessID, manual string) (string, bool) {
	if manual != "" {
		if info, err := os.Stat(manual); err == nil && !info.IsDir() {
			return manual, true
		}
		return manual, false
	}
	pathNames := map[string][]string{
		HarnessAntigravity: {"agy.exe", "agy.cmd", "agy"},
		HarnessCodex:       {"codex.exe", "codex.cmd", "codex"},
		HarnessClaudeCode:  {"claude.exe", "claude.cmd", "claude"},
		HarnessDeepSeek:    {"dsh.exe", "dsh.cmd", "dsh"},
	}[harnessID]
	for _, name := range pathNames {
		if p, err := exec.LookPath(name); err == nil {
			return p, true
		}
	}
	for _, candidate := range knownInstallCandidates(harnessID) {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}
	}
	return "", false
}

func knownInstallCandidates(harnessID string) []string {
	userHome, _ := os.UserHomeDir()
	localAppData := os.Getenv("LOCALAPPDATA")
	appData := os.Getenv("APPDATA")

	if runtime.GOOS != "windows" {
		if userHome == "" {
			return nil
		}
		switch harnessID {
		case HarnessAntigravity:
			return []string{filepath.Join(userHome, ".local", "bin", "agy")}
		case HarnessCodex:
			return []string{filepath.Join(userHome, ".local", "bin", "codex")}
		case HarnessClaudeCode:
			return []string{filepath.Join(userHome, ".local", "bin", "claude")}
		case HarnessDeepSeek:
			return []string{filepath.Join(userHome, ".local", "bin", "dsh")}
		default:
			return nil
		}
	}

	var out []string
	add := func(base string, elems ...string) {
		if base == "" {
			return
		}
		parts := append([]string{base}, elems...)
		out = append(out, filepath.Join(parts...))
	}
	switch harnessID {
	case HarnessAntigravity:
		add(localAppData, "agy", "bin", "agy.exe")
		add(localAppData, "agy", "bin", "agy.cmd")
		add(localAppData, "Programs", "Antigravity", "bin", "antigravity.cmd")
		add(localAppData, "Programs", "Antigravity", "Antigravity.exe")
	case HarnessCodex:
		add(appData, "npm", "codex.cmd")
		add(userHome, ".local", "bin", "codex.exe")
	case HarnessClaudeCode:
		add(userHome, ".local", "bin", "claude.exe")
		add(localAppData, "Programs", "claude-code", "claude.exe")
		add(appData, "npm", "claude.cmd")
	case HarnessDeepSeek:
		add(localAppData, "Programs", "DeepSeek Harness", "resources", "runtime", "cli", "bin", "dsh.cmd")
		add(localAppData, "Programs", "DeepSeek Harness", "resources", "runtime", "cli", "bin", "dsh.exe")
		add(userHome, ".local", "bin", "dsh.exe")
	}
	return out
}

func (s *Service) probeVersion(harnessID, exe string) string {
	if s.DisableVersionProbe || exe == "" {
		return ""
	}
	base := strings.ToLower(filepath.Base(exe))
	// Avoid launching desktop GUI executables during passive discovery.
	if base == "antigravity.exe" || base == "deepseek harness.exe" {
		return ""
	}
	info, err := os.Stat(exe)
	if err != nil || info.IsDir() {
		return ""
	}
	key := harnessID + "|" + strings.ToLower(exe)
	mod := info.ModTime().UnixNano()
	size := info.Size()

	s.cacheMu.Lock()
	if s.versionCache != nil {
		if entry, ok := s.versionCache[key]; ok && entry.modUnixNano == mod && entry.size == size {
			s.cacheMu.Unlock()
			return entry.version
		}
	}
	s.cacheMu.Unlock()

	flag := "--version"
	if harnessID == HarnessDeepSeek {
		flag = "-V"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stdout, stderr, err := s.runner()(ctx, exe, flag)
	ver := ""
	if err == nil {
		ver = firstVersionLine(stdout)
		if ver == "" {
			ver = firstVersionLine(stderr)
		}
	}

	s.cacheMu.Lock()
	if s.versionCache == nil {
		s.versionCache = map[string]versionCacheEntry{}
	}
	s.versionCache[key] = versionCacheEntry{modUnixNano: mod, size: size, version: ver}
	s.cacheMu.Unlock()
	return ver
}

func firstVersionLine(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		s := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if s == "" {
			continue
		}
		if len(s) > 80 {
			s = s[:80]
		}
		return s
	}
	return ""
}
