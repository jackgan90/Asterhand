package localcommands

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
)

const (
	MaxActiveCommandRuns       = 8
	MaxActiveRuns              = 32
	MaxRoundRuns               = 64
	MaxRunLogBytes       int64 = 256 * 1024 * 1024
	MaxRoundLogBytes     int64 = 1024 * 1024 * 1024
)

// The index is the admission authority; unreferenced files are never executed.
type runIndex struct {
	Version  int      `json:"schema_version"`
	RoundID  string   `json:"round_id"`
	Latest   string   `json:"latest_run_id"`
	Sequence uint64   `json:"last_sequence"`
	IDs      []string `json:"run_ids"`
}

func (s runStore) indexPath(id string) string { return filepath.Join(s.directory(id), "current.json") }
func (s runStore) recordPath(id, run string) string {
	return filepath.Join(s.directory(id), run+".json")
}

func (s runStore) publish(id string, index runIndex) error {
	b, err := json.Marshal(index)
	if err != nil {
		return err
	}
	return config.AtomicFile(s.indexPath(id), b, 0600)
}

func (s runStore) loadIndex(id string) (*runIndex, error) {
	f, err := os.Open(s.indexPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 16*1024+1))
	if err != nil {
		return nil, err
	}
	var index runIndex
	if len(b) > 16*1024 || json.Unmarshal(b, &index) != nil || index.Version != 1 || !wire.ValidID(index.RoundID) ||
		len(index.IDs) == 0 || len(index.IDs) > MaxRoundRuns || index.Latest != index.IDs[len(index.IDs)-1] || index.Sequence < uint64(len(index.IDs)) {
		return nil, problem("configuration", "err.localcmd.invalid_run")
	}
	seen := map[string]bool{}
	for _, runID := range index.IDs {
		if !wire.ValidID(runID) || seen[runID] {
			return nil, problem("configuration", "err.localcmd.invalid_run")
		}
		seen[runID] = true
	}
	return &index, nil
}

// Only recognized run filenames are collected. Unknown files are never treated
// as execution evidence or removed during recovery.
func (s runStore) collect(id string, index runIndex) error {
	keep := map[string]bool{}
	for _, runID := range index.IDs {
		keep[runID] = true
	}
	files, err := os.ReadDir(s.directory(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, file := range files {
		ext := filepath.Ext(file.Name())
		runID := strings.TrimSuffix(file.Name(), ext)
		if file.IsDir() || (ext != ".log" && ext != ".json") || !wire.ValidID(runID) || keep[runID] {
			continue
		}
		if err = os.Remove(filepath.Join(s.directory(id), file.Name())); err != nil {
			return err
		}
	}
	return nil
}
