// Package hostbundle verifies the input host beside a node executable.
package hostbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/jackgan90/Asterhand/internal/i18n"
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
		return nil, i18n.Wrap("err.quickinput.bundle_manifest_missing", map[string]string{"err": err.Error()}, err)
	}
	defer manifest.Close()
	d := json.NewDecoder(io.LimitReader(manifest, 4097))
	d.DisallowUnknownFields()
	var identity Identity
	if d.Decode(&identity) != nil || d.Decode(new(any)) != io.EOF || identity.Schema != 1 || identity.Version != version || identity.Source != source {
		return nil, i18n.Err("err.quickinput.bundle_version_mismatch")
	}
	expected, err := hex.DecodeString(identity.SHA256)
	if err != nil || len(expected) != sha256.Size {
		return nil, i18n.Err("err.quickinput.bundle_digest_invalid")
	}
	path, err := filepath.EvalSymlinks(filepath.Join(directory, Executable))
	if err != nil {
		return nil, i18n.Wrap("err.quickinput.bundle_exe_missing", map[string]string{"err": err.Error()}, err)
	}
	// Compare filesystem identity, not locale-dependent spelling or casing.
	dirInfo, err := os.Stat(directory)
	if err != nil {
		return nil, err
	}
	parentInfo, err := os.Stat(filepath.Dir(path))
	if err != nil || !os.SameFile(dirInfo, parentInfo) {
		return nil, i18n.Err("err.quickinput.bundle_wrong_dir")
	}
	file, err := openGuard(path)
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil || hex.EncodeToString(hash.Sum(nil)) != identity.SHA256 {
		file.Close()
		return nil, i18n.Err("err.quickinput.bundle_integrity_failed")
	}
	return file, nil
}
