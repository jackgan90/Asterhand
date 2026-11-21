package harnesses

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jackgan90/Asterhand/internal/automation"
)

// CanonicalPath resolves a path to an absolute, cleaned path with existing
// symlinks/junctions evaluated so Windows case/junction aliases match reliably.
func CanonicalPath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.ContainsAny(trimmed, "\x00\r\n") {
		return "", errors.New("invalid path")
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	// If leaf doesn't exist yet, resolve existing parent chain to prevent junction escape.
	if errors.Is(err, os.ErrNotExist) {
		parent := filepath.Dir(abs)
		if parent != abs {
			if pRes, pErr := CanonicalPath(parent); pErr == nil {
				return filepath.Clean(filepath.Join(pRes, filepath.Base(abs))), nil
			}
		}
		return filepath.Clean(abs), nil
	}
	return "", err
}

func NormalizeKey(path string) string {
	clean := filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(clean)
	}
	return clean
}

func SamePath(a, b string) bool {
	return NormalizeKey(a) == NormalizeKey(b)
}

// WithinRoot reports whether target is equal to or strictly inside root.
func WithinRoot(root, target string) bool {
	rk := NormalizeKey(root)
	tk := NormalizeKey(target)
	if rk == tk {
		return true
	}
	if !strings.HasSuffix(rk, string(filepath.Separator)) {
		rk += string(filepath.Separator)
	}
	return strings.HasPrefix(tk, rk)
}

// WorktreeKey returns the canonical Git worktree root for dir if inside a Git
// repository (including linked worktrees with a .git file), or the canonical
// directory path itself for non-Git directories.
func WorktreeKey(dir string) (string, bool) {
	cur, err := CanonicalPath(dir)
	if err != nil {
		return NormalizeKey(dir), false
	}
	for {
		if _, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
			return NormalizeKey(cur), true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	canon, err := CanonicalPath(dir)
	if err != nil {
		return NormalizeKey(dir), false
	}
	return NormalizeKey(canon), false
}

// DeriveWorkspaceID deterministically maps a canonical project directory path
// to a stable workspace identifier so per-project worker aliases and turns stay
// isolated across restarts without manual workspace registration.
func DeriveWorkspaceID(canonicalRoot string) string {
	sum := sha256.Sum256([]byte(NormalizeKey(canonicalRoot)))
	return "ws_" + hex.EncodeToString(sum[:6])
}

// ValidateWorkspacePath resolves rawPath to an existing canonical directory and
// rejects obviously sensitive OS, credential, and Asterhand node data paths.
func ValidateWorkspacePath(rawPath, nodeHome string) (string, error) {
	canon, err := CanonicalPath(rawPath)
	if err != nil {
		return "", automation.Fail("invalid_parameters", "Invalid workspace_root")
	}
	info, err := os.Stat(canon)
	if err != nil || !info.IsDir() {
		return "", automation.Fail("invalid_parameters", "workspace_root must be an existing directory")
	}
	// Reject filesystem roots such as C:\ or /.
	if filepath.Dir(canon) == canon {
		return "", automation.Fail("forbidden", "Filesystem root cannot be used as a worker workspace")
	}
	// Reject Asterhand's own node data directory and anything beneath it.
	if nodeHome != "" {
		if canonHome, err := CanonicalPath(nodeHome); err == nil && WithinRoot(canonHome, canon) {
			return "", automation.Fail("forbidden", "Asterhand node data directory cannot be used as a worker workspace")
		}
	}
	if tempDir, err := CanonicalPath(os.TempDir()); err == nil {
		if SamePath(tempDir, canon) {
			return "", automation.Fail("forbidden", "System temp root cannot be used directly as a worker workspace")
		}
		// Allow dedicated subdirectories inside OS temp (e.g. test dirs / scratch repos)
		// even though Windows places Temp inside AppData\Local.
		if WithinRoot(tempDir, canon) {
			return canon, nil
		}
	}
	if homeDir, err := os.UserHomeDir(); err == nil && homeDir != "" {
		if canonUser, err := CanonicalPath(homeDir); err == nil {
			if SamePath(canonUser, canon) {
				return "", automation.Fail("forbidden", "User home directory itself cannot be used as a worker workspace")
			}
			sensitiveUserDirs := []string{
				"AppData",
				".ssh",
				".aws",
				".gnupg",
				".kube",
				".azure",
				filepath.Join(".config", "gcloud"),
				".gemini",
				".codex",
				".claude",
				".dsh",
			}
			for _, rel := range sensitiveUserDirs {
				if WithinRoot(filepath.Join(canonUser, rel), canon) {
					return "", automation.Fail("forbidden", "Sensitive user or credential directory cannot be used as a worker workspace")
				}
			}
		}
	}
	if runtime.GOOS == "windows" {
		sysDirs := []string{
			os.Getenv("SystemRoot"),
			os.Getenv("WINDIR"),
			os.Getenv("ProgramFiles"),
			os.Getenv("ProgramFiles(x86)"),
			os.Getenv("ProgramData"),
			os.Getenv("APPDATA"),
			os.Getenv("LOCALAPPDATA"),
		}
		for _, dir := range sysDirs {
			if strings.TrimSpace(dir) == "" {
				continue
			}
			if canonSys, err := CanonicalPath(dir); err == nil && WithinRoot(canonSys, canon) {
				return "", automation.Fail("forbidden", "System or application data directory cannot be used as a worker workspace")
			}
		}
	}
	return canon, nil
}
