package localcommands

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/operations"
)

// Metadata is charged at its maximum size while retained. This conservative
// reservation lets a running graph persist its final record even at capacity.
func (m *RunManager) detailCharge(command, id string) int64 {
	var n int64
	if _, err := os.Stat(m.store.recordPath(command, id)); err == nil {
		n = maxRunRecordBytes
	}
	if st, err := os.Stat(m.store.logPath(command, id)); err == nil {
		n += st.Size()
	}
	return n
}

func (m *RunManager) restoreHistory() error {
	if m.history == nil {
		return m.loadErr
	}
	if _, err := m.history.db.Exec("UPDATE runs SET pinned=0"); err != nil {
		return err
	}
	for _, run := range m.runs {
		if err := m.history.save(run.record); err != nil {
			return err
		}
	}
	for id, group := range m.groups {
		if err := m.history.pinCurrent(id, group.index.IDs); err != nil {
			return err
		}
	}
	// Prepared rows left without the current admission index are not executions.
	if _, err := m.history.db.Exec("DELETE FROM runs WHERE admitted=0"); err != nil {
		return err
	}
	entries, err := os.ReadDir(m.store.root)
	if err != nil {
		return err
	}
	var bytes int64
	for _, entry := range entries {
		if !entry.IsDir() || !wire.ValidID(entry.Name()) {
			continue
		}
		files, err := os.ReadDir(m.store.directory(entry.Name()))
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, file := range files {
			id := strings.TrimSuffix(file.Name(), filepath.Ext(file.Name()))
			if file.IsDir() || !recognizedRunFile(file.Name()) || seen[id] {
				continue
			}
			seen[id] = true
			meta, lookupErr := m.history.get(id)
			if lookupErr != nil {
				err = lookupErr
				if !errors.Is(err, operations.ErrMissing) {
					return err
				}
				if err = m.removeDetails(entry.Name(), id); err != nil {
					return err
				}
				continue
			}
			if meta.Details != "retained" {
				if err = m.removeDetails(entry.Name(), id); err != nil {
					return err
				}
				continue
			}
			bytes += m.detailCharge(entry.Name(), id)
		}
	}
	m.diskBytes.Store(bytes)
	return m.pruneHistoryLocked()
}

type retainedRun struct {
	command, id, round string
	ended, bytes       int64
}

func (m *RunManager) pruneHistoryLocked() error {
	rows, err := m.history.db.Query("SELECT command,id,round,ended FROM runs WHERE admitted=1 AND ended>0 AND details='retained' ORDER BY ended DESC,id DESC")
	if err != nil {
		return err
	}
	items := []retainedRun{}
	for rows.Next() {
		var v retainedRun
		if err = rows.Scan(&v.command, &v.id, &v.round, &v.ended); err != nil {
			rows.Close()
			return err
		}
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rounds := map[string][]string{}
	var kept int64
	cutoff := time.Now().Add(-7 * 24 * time.Hour).UnixMilli()
	for _, v := range items {
		list := rounds[v.command]
		if !slices.Contains(list, v.round) {
			list = append(list, v.round)
			rounds[v.command] = list
		}
		v.bytes = m.detailCharge(v.command, v.id)
		reason := ""
		if v.ended < cutoff {
			reason = "expired"
		} else if slices.Index(list, v.round) >= 10 {
			reason = "round_limit"
		} else if kept+v.bytes > MaxHistoryBytes {
			reason = "capacity"
		}
		if reason == "" {
			kept += v.bytes
			continue
		}
		if err = m.dropDetails(v, reason); err != nil {
			return err
		}
	}
	return m.history.expire()
}

func (m *RunManager) dropDetails(v retainedRun, reason string) error {
	// Publish absence before removal so an interrupted cleanup never presents
	// a truncated log as a successful empty result.
	if _, err := m.history.db.Exec("UPDATE runs SET details=? WHERE id=? AND ended>0", reason, v.id); err != nil {
		return err
	}
	if err := m.removeDetails(v.command, v.id); err != nil {
		return err
	}
	m.diskBytes.Add(-v.bytes)
	return nil
}

func (m *RunManager) removeDetails(command, id string) error {
	for _, p := range []string{m.store.logPath(command, id), m.store.recordPath(command, id)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (m *RunManager) archivedRun(command, id string) (*RunRecord, error) {
	s, err := m.history.get(id)
	if err != nil {
		return nil, err
	}
	if s.CommandID != command {
		return nil, operations.ErrMissing
	}
	if s.Details == "retained" {
		r, err := m.store.loadRun(command, id)
		if err != nil {
			return nil, err
		}
		if r != nil {
			return r, nil
		}
		return nil, operations.ErrMissing
	}
	r := s.record()
	return &r, nil
}

func (r *Registry) RunHistory(id, before string, limit int) ([]RunSummary, string, error) {
	m := r.managed
	if m == nil {
		return nil, "", operations.ErrMissing
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.history == nil {
		return nil, "", m.loadErr
	}
	items, next, err := m.history.page(id, before, limit)
	for i := range items {
		if run := m.runs[items[i].ID]; run != nil {
			run.mu.Lock()
			details := items[i].Details
			items[i] = summarizeRun(m.snapshotLocked(run), details)
			run.mu.Unlock()
		}
	}
	return items, next, err
}

func (r *Registry) RunDetails(id, runID string) (RunSummary, error) {
	m := r.managed
	if m == nil {
		return RunSummary{}, operations.ErrMissing
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.history.get(runID)
	if err == nil && s.CommandID != id {
		return RunSummary{}, operations.ErrMissing
	}
	return s, err
}

// Keep only recognized files; current indices are managed separately.
func recognizedRunFile(name string) bool {
	ext := filepath.Ext(name)
	return (ext == ".json" || ext == ".log") && wire.ValidID(strings.TrimSuffix(name, ext))
}
