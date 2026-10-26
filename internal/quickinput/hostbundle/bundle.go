// Package hostbundle verifies the input host beside a node executable.
package hostbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const Executable = "AsterhandInputHost.exe"
const Manifest = "input-host.json"

type Identity struct {
	Schema  int    `json:"schema"`
	Version string `json:"version"`
	Source  string `json:"source_sha256"`
	SHA256  string `json:"sha256"`
}

// Verify opens the fixed sibling path, checks its digest, and returns a read
// guard. On Windows the guard denies replacement until process creation ends.
func Verify(directory, version, source string) (*os.File, error) {
	directory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, err
	}
	manifest, err := os.Open(filepath.Join(directory, Manifest))
	if err != nil {
		return nil, fmt.Errorf("缺少快捷输入宿主清单，请重新运行安装器: %w", err)
	}
	defer manifest.Close()
	d := json.NewDecoder(io.LimitReader(manifest, 4097))
	d.DisallowUnknownFields()
	var identity Identity
	if d.Decode(&identity) != nil || d.Decode(new(any)) != io.EOF || identity.Schema != 1 || identity.Version != version || identity.Source != source {
		return nil, fmt.Errorf("快捷输入宿主版本或构建不匹配，请重新构建/安装配套宿主")
	}
	expected, err := hex.DecodeString(identity.SHA256)
	if err != nil || len(expected) != sha256.Size {
		return nil, fmt.Errorf("快捷输入宿主摘要无效")
	}
	path, err := filepath.EvalSymlinks(filepath.Join(directory, Executable))
	if err != nil {
		return nil, fmt.Errorf("缺少快捷输入宿主，请重新运行安装器: %w", err)
	}
	// Compare filesystem identity, not locale-dependent spelling or casing.
	dirInfo, err := os.Stat(directory)
	if err != nil {
		return nil, err
	}
	parentInfo, err := os.Stat(filepath.Dir(path))
	if err != nil || !os.SameFile(dirInfo, parentInfo) {
		return nil, fmt.Errorf("快捷输入宿主不在当前版本目录")
	}
	file, err := openGuard(path)
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil || hex.EncodeToString(hash.Sum(nil)) != identity.SHA256 {
		file.Close()
		return nil, fmt.Errorf("快捷输入宿主完整性校验失败")
	}
	return file, nil
}
