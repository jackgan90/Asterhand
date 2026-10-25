// Package launch implements the short-lived desktop entry point. It never owns
// task execution and never restarts or kills a node whose state is uncertain.
package launch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
		return result, fmt.Errorf("读取安装记录失败，请重新运行安装器：%w", err)
	}
	if result.Schema != 1 {
		return result, errors.New("安装记录版本不受支持，请重新运行安装器")
	}
	for _, p := range []string{result.Executable, result.Data} {
		if !filepath.IsAbs(p) || strings.ContainsAny(p, "\x00\r\n\"") {
			return result, errors.New("安装记录包含无效的绝对路径")
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
		return result, fmt.Errorf("节点程序不可用：%w", err)
	}
	if !within(root, versions) || !within(versions, result.Executable) || !samePath(filepath.Base(result.Executable), "asterhand.exe") {
		return result, errors.New("节点程序必须位于本安装目录的 versions 内")
	}
	info, err := os.Stat(result.Executable)
	if err != nil || !info.Mode().IsRegular() {
		return result, errors.New("节点程序缺失或不是普通文件，请重新安装")
	}
	result.Data, err = canonicalPath(result.Data)
	if err != nil {
		return result, fmt.Errorf("数据目录无效：%w", err)
	}
	user, _ := os.UserHomeDir()
	if samePath(result.Data, filepath.Dir(result.Data)) || samePath(result.Data, user) || samePath(root, result.Data) || within(root, result.Data) || within(result.Data, root) {
		return result, errors.New("数据目录必须独立于程序目录、磁盘根目录和用户主目录")
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
		return errors.New("状态文件过大")
	}
	b = bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf})
	d := json.NewDecoder(bytes.NewReader(b))
	if err = d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return errors.New("状态文件包含额外内容")
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
			return errors.New("已有配置缺少节点身份，请检查数据目录")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("已有节点配置无效：%w", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "startup.log", "error.log", "launcher.log", "startup.txt", "error.txt", "service.lock", "launch-state.json":
			if entry.IsDir() {
				return errors.New("数据目录中的状态或日志路径不是文件")
			}
		default:
			return errors.New("数据目录已有其他内容，请使用空目录或已有 Asterhand 节点目录")
		}
	}
	return nil
}
