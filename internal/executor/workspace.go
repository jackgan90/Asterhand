package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jackgan90/Asterhand/internal/config"
)

func ValidateWorkspace(c config.Config, home, root string) error {
	adapter, err := RuntimeFor(c.AgentRuntime, nil)
	if err != nil {
		return err
	}
	return validateWorkspace(adapter, c, home, root)
}

func validateWorkspace(adapter Runtime, c config.Config, home, root string) error {
	paths, err := adapter.ProtectedPaths(c)
	if err != nil {
		return err
	}
	canonical, err := canonicalPath(root)
	if err != nil {
		return err
	}
	for _, path := range append(paths, home) {
		protected, err := canonicalPath(path)
		if err != nil {
			return fmt.Errorf("运行时保护目录无效: %w", err)
		}
		if pathContains(canonical, protected) || pathContains(protected, canonical) {
			return fmt.Errorf("授权目录必须与节点数据和运行时保护目录分离")
		}
	}
	return nil
}

// Resolve an existing ancestor before restoring a not-yet-created suffix.
// Existing paths use the OS resolver, including junctions on Windows.
func canonicalPath(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("需要绝对目录路径")
	}
	path = filepath.Clean(path)
	resolved, err := resolveExistingPath(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	// A broken symlink must not be treated as an absent ordinary directory.
	if _, statErr := os.Lstat(path); statErr == nil {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolved, err = canonicalPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}

func pathContains(parent, child string) bool {
	if runtime.GOOS == "windows" {
		parent, child = strings.ToLower(parent), strings.ToLower(child)
	}
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
