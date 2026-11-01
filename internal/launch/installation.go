// Package launch implements the short-lived desktop entry point. It never owns
// task execution and never restarts or kills a node whose state is uncertain.
package launch

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jackgan90/Asterhand/internal/i18n"
)

type Installation struct {
	Schema     int    `json:"schema"`
	Executable string `json:"executable"`
	Data       string `json:"data"`
}

// ReadInstallation accepts additional installer metadata, but only absolute
// paths to an executable inside this installation's versions directory.
func ReadInstallation(path string) (Installation, error) {
	var result Installation
	if err := readJSON(path, &result); err != nil {
		return result, i18n.Wrap("err.launch.read_install_failed", nil, err)
	}
	if result.Schema != 1 {
		return result, i18n.Err("err.launch.unsupported_install_schema")
	}
	for _, p := range []string{result.Executable, result.Data} {
		if !filepath.IsAbs(p) || strings.ContainsAny(p, "\x00\r\n\"") {
			return result, i18n.Err("err.launch.invalid_abs_path")
		}
	}
	root, err := canonicalPath(filepath.Dir(path))
	if err != nil {
		return result, err
	}
	versions, err := canonicalPath(filepath.Join(root, "versions"))
	if err != nil {
		return result, err
	}
	result.Executable, err = canonicalPath(result.Executable)
	if err != nil {
		return result, i18n.Wrap("err.launch.exe_unavailable", nil, err)
	}
	if !within(root, versions) || !within(versions, result.Executable) || !samePath(filepath.Base(result.Executable), "asterhand.exe") {
		return result, i18n.Err("err.launch.exe_outside_versions")
	}
	info, err := os.Stat(result.Executable)
	if err != nil || !info.Mode().IsRegular() {
		return result, i18n.Err("err.launch.exe_missing")
	}
	result.Data, err = canonicalPath(result.Data)
	if err != nil {
		return result, i18n.Wrap("err.launch.data_dir_invalid", nil, err)
	}
	user, _ := os.UserHomeDir()
	if samePath(result.Data, filepath.Dir(result.Data)) || samePath(result.Data, user) || samePath(root, result.Data) || within(root, result.Data) || within(result.Data, root) {
		return result, i18n.Err("err.launch.data_dir_overlap")
	}
	return result, nil
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func within(parent, child string) bool {
	if runtime.GOOS == "windows" {
		parent, child = strings.ToLower(parent), strings.ToLower(child)
	}
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func readJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
	if err != nil {
		return err
	}
	if len(b) > 64*1024 {
		return i18n.Err("err.launch.state_oversized")
	}
	b = bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf})
	d := json.NewDecoder(bytes.NewReader(b))
	if err = d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return i18n.Err("err.launch.state_extra")
	}
	return nil
}

// The node, not the launcher, initializes identity and credentials. Before
// opening logs, only accept an existing node home or the installer's empty home.
func prepareHome(home string) error {
	if err := os.MkdirAll(home, 0700); err != nil {
		return err
	}
	var cfg struct {
		NodeID string `json:"node_id"`
	}
	err := readJSON(filepath.Join(home, "config.json"), &cfg)
	if err == nil {
		if cfg.NodeID == "" {
			return i18n.Err("err.launch.missing_node_id")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return i18n.Wrap("err.launch.invalid_node_config", nil, err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "startup.log", "error.log", "launcher.log", "startup.txt", "error.txt", "service.lock", "launch-state.json":
			if entry.IsDir() {
				return i18n.Err("err.launch.path_not_file")
			}
		default:
			return i18n.Err("err.launch.data_dir_not_empty")
		}
	}
	return nil
}
