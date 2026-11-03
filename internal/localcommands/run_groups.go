package localcommands

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"time"

	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/model"
)

type runGroup struct {
	index   runIndex
	bytes   atomic.Int64
	cleanup bool
}

// RunList is a bounded list of compact summaries. Node details are fetched for
// selected runs, never multiplied by all retained instances in one IPC frame.
type RunList struct {
	CleanupPending bool        `json:"cleanup_pending,omitempty"`
	RoundID        string      `json:"round_id"`
	LatestID       string      `json:"latest_run_id"`
	Runs           []RunRecord `json:"runs"`
}

func (m *RunManager) groupBusyLocked(id string) (active int, uncertain bool) {
	group := m.groups[id]
	if group == nil {
		return
	}
	for _, runID := range group.index.IDs {
		run := m.runs[runID]
		run.mu.Lock()
		if run.busy {
			active++
		}
		uncertain = uncertain || run.record.CleanupPending
		run.mu.Unlock()
	}
	return
}

func (m *RunManager) Start(command Command) (*RunRecord, error) {
	graph, err := normalizeGraph(command.Graph, commandPolicy)
	if err != nil {
		return nil, err
	}
	command.Graph = graph
	if !wire.ValidID(command.ID) {
		return nil, problem("invalid", "err.localcmd.invalid_wire_req")
	}
	if err = validateTimeout(command.TimeoutSeconds); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, problem("unavailable", "err.app.node_stopping")
	}
	if !m.Available() {
		return nil, problem("unsupported", "err.localcmd.managed_unavailable")
	}
	active, uncertain := m.groupBusyLocked(command.ID)
	if uncertain || (!command.AllowConcurrent && active > 0) {
		return nil, problem("busy", "err.localcmd.running")
	}
	total := 0
	for id := range m.groups {
		count, _ := m.groupBusyLocked(id)
		total += count
	}
	if active >= MaxActiveCommandRuns || total >= MaxActiveRuns {
		return nil, problem("busy", "err.localcmd.concurrent_limit")
	}
	previous := m.groups[command.ID]
	if previous != nil && previous.cleanup {
		if err = m.store.collect(command.ID, previous.index); err != nil {
			return nil, err
		}
		previous.cleanup = false
	}
	if active > 0 && (len(previous.index.IDs) >= MaxRoundRuns || previous.bytes.Load() >= MaxRoundLogBytes) {
		return nil, problem("busy", "err.localcmd.round_limit")
	}
	return m.admitLocked(command, previous, active == 0)
}

// All checks precede prepare/publish. Publication is the only admission point.
func (m *RunManager) admitLocked(command Command, previous *runGroup, newRound bool) (*RunRecord, error) {
	id := model.ID()
	group := previous
	sequence := uint64(1)
	if previous != nil {
		sequence = previous.index.Sequence + 1
	}
	if sequence == 0 {
		return nil, problem("unavailable", "err.localcmd.limit_exceeded")
	}
	if group == nil || newRound {
		group = &runGroup{index: runIndex{Version: 1, RoundID: id}}
	}
	index := group.index
	index.IDs = append(slices.Clone(index.IDs), id)
	index.Latest, index.Sequence = id, sequence
	run := &managedRun{record: RunRecord{Version: 1, ID: id, RoundID: index.RoundID, Sequence: sequence, Command: command,
		Phase: "starting", Started: time.Now().UTC(), LogComplete: true}, busy: true, done: make(chan struct{}), group: group}
	initializeGraph(run)
	f, err := m.store.prepare(run.record)
	if err != nil {
		return nil, err
	}
	if err = m.store.publish(command.ID, index); err != nil {
		f.Close()
		// Failed publication never makes this attempt observable as a run.
		// Remove its private preparation files so repeated failures stay bounded.
		_ = os.Remove(m.store.recordPath(command.ID, id))
		_ = os.Remove(m.store.logPath(command.ID, id))
		return nil, err
	}
	run.file = f
	if newRound && previous != nil {
		for _, oldID := range previous.index.IDs {
			delete(m.runs, oldID)
		}
	}
	group.index = index
	m.groups[command.ID], m.runs[id] = group, run
	group.cleanup = m.store.collect(command.ID, index) != nil
	run.mu.Lock()
	record := m.snapshotLocked(run)
	run.mu.Unlock()
	go m.executeGraph(run)
	return &record, nil
}

func (m *RunManager) Runs(id string) RunList {
	list := RunList{Runs: []RunRecord{}}
	if m == nil {
		return list
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	group := m.groups[id]
	if group == nil {
		return list
	}
	list.RoundID, list.LatestID = group.index.RoundID, group.index.Latest
	list.CleanupPending = group.cleanup
	for _, runID := range group.index.IDs {
		run := m.runs[runID]
		run.mu.Lock()
		record := m.snapshotLocked(run)
		run.mu.Unlock()
		record.Nodes = nil
		record.Command = Command{ID: id}
		list.Runs = append(list.Runs, record)
	}
	return list
}

func (m *RunManager) Summary(id string) wire.Entry {
	result := wire.Entry{ID: id}
	list := m.Runs(id)
	for _, record := range list.Runs {
		if record.Phase != "finished" {
			result.Active++
			result.ActiveRunIDs = append(result.ActiveRunIDs, record.ID)
		}
		if record.Result == "failed" {
			result.Failed++
		}
		if record.Result == "interrupted" {
			result.Interrupted++
		}
		if record.ID == list.LatestID {
			result.RunID, result.Phase, result.Result, result.Reason = record.ID, record.Phase, record.Result, record.Reason
		}
	}
	return result
}

func (m *RunManager) restoreGroup(id string, index runIndex) error {
	group := &runGroup{index: index}
	var result error
	for i, runID := range index.IDs {
		record, err := m.store.loadRun(id, runID)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if record == nil || record.RoundID != index.RoundID || record.Sequence != index.Sequence-uint64(len(index.IDs)-1-i) {
			result = errors.Join(result, problem("configuration", "err.localcmd.invalid_run"))
			continue
		}
		var logBytes int64
		info, err := os.Stat(m.store.logPath(id, runID))
		if err != nil {
			result = errors.Join(result, err)
			record.LogComplete, record.StorageError = false, true
		} else {
			logBytes = info.Size()
		}
		// A missing log does not invalidate a verified process identity.
		run := &managedRun{record: *record, done: make(chan struct{}), group: group, logBytes: logBytes}
		group.bytes.Add(logBytes)
		m.runs[runID] = run
		if record.Phase != "finished" {
			run.busy, run.record.CleanupPending = true, true
			run.record.LogComplete = false
			if run.record.Reason == "" {
				run.record.Reason = "unexpected_shutdown"
			}
			m.recovering.Add(1)
		} else {
			close(run.done)
		}
	}
	if result != nil {
		// Keep the complete published index and all files as recovery evidence.
		// Partially loaded groups must never become eligible for a new round.
		return result
	}
	group.cleanup = m.store.collect(id, index) != nil
	m.groups[id] = group
	return nil
}

func (m *RunManager) recover() error {
	entries, err := os.ReadDir(m.store.root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var result error
	for _, entry := range entries {
		id := entry.Name()
		if !entry.IsDir() || !wire.ValidID(id) {
			continue
		}
		if _, err = os.Stat(filepath.Join(m.store.directory(id), "deleting")); err == nil {
			if err = m.store.clear(id); err != nil {
				result = errors.Join(result, err)
			}
			continue
		}
		index, err := m.store.loadIndex(id)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if index == nil {
			if err = m.store.collect(id, runIndex{}); err != nil {
				result = errors.Join(result, err)
			}
			continue
		}
		if err = m.restoreGroup(id, *index); err != nil {
			result = errors.Join(result, err)
		}
	}
	// Count all uncertain scopes before permitting any fresh graph scheduling.
	for _, run := range m.runs {
		if run.busy {
			go m.reconcileGraph(run)
		}
	}
	return result
}
