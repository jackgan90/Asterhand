package localcommands

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/operations"
)

type managedRun struct {
	observeCompletion bool
	group             *runGroup
	logBytes          int64
	finalizing        bool
	graph             *graphRun
	mu                sync.Mutex
	record            RunRecord
	file              *logFile
	busy              bool
	started           time.Duration
	stopStarted       time.Duration
	done              chan struct{}
}

// RunManager owns accepted work independently of request and UI lifetimes.
// Lock order is registry -> manager -> run; workers only acquire the run lock.
type RunManager struct {
	completion atomic.Pointer[completionObserver]
	groups     map[string]*runGroup
	recovering atomic.Int32
	graphSlots chan struct{}

	mu            sync.Mutex
	store         runStore
	backend       ExecutionBackend
	clock         ExecutionClock
	runs          map[string]*managedRun
	closed        bool
	loadErr       error
	journal       operations.Journal
	history       *runHistory
	diskBytes     atomic.Int64
	historyFailed atomic.Bool
}

func NewRunManager(home string, backend ExecutionBackend, clock ExecutionClock) *RunManager {
	m := &RunManager{store: runStore{filepath.Join(home, "command-runs")}, backend: backend, clock: clock, runs: map[string]*managedRun{}, groups: map[string]*runGroup{}, graphSlots: make(chan struct{}, GraphExecutionSlots)}
	if clock == nil {
		m.clock = platformClock{}
	}
	m.history, m.loadErr = openRunHistory(m.store.root)
	if m.loadErr == nil {
		m.loadErr = m.recover()
	}
	if m.loadErr == nil {
		m.loadErr = m.restoreHistory()
	}
	for _, run := range m.runs {
		if run.busy {
			go m.reconcileGraph(run)
		}
	}
	return m
}

func (m *RunManager) Available() bool {
	return m != nil && m.backend != nil && m.backend.Available() && m.loadErr == nil && !m.historyFailed.Load()
}

func cloneRecord(record RunRecord) RunRecord {
	record.Nodes = cloneNodeRecords(record.Nodes)
	record.Command = cloneCatalog(Catalog{Commands: []Command{record.Command}}).Commands[0]
	if record.Ended != nil {
		value := *record.Ended
		record.Ended = &value
	}
	if record.Exit.Code != nil {
		value := *record.Exit.Code
		record.Exit.Code = &value
	}
	return record
}

// snapshotLocked derives live duration from the same clock as the timeout.
func (m *RunManager) snapshotLocked(run *managedRun) RunRecord {
	record := cloneRecord(publicRun(run.record))
	if run.graph != nil && (record.Phase == "running" || record.Phase == "stopping") {
		record.Elapsed = max(0, (m.clock.Now() - run.started).Seconds())
	}
	return publicRun(record)
}

func (m *RunManager) Current(id string) *RunRecord { return m.Run(id, "") }

func (m *RunManager) Run(id, runID string) *RunRecord {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if runID == "" {
		if group := m.groups[id]; group != nil {
			runID = group.index.Latest
		}
	}
	run := m.runs[runID]
	if run == nil && runID != "" && wire.ValidID(id) && wire.ValidID(runID) && m.history != nil {
		record, err := m.archivedRun(id, runID)
		if err == nil && record != nil {
			v := publicRun(*record)
			return &v
		}
		return nil
	}
	if run == nil || run.record.Command.ID != id {
		return nil
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	record := m.snapshotLocked(run)
	return &record
}

func (m *RunManager) Busy(id string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, run := range m.runs {
		if id != "" && id != run.record.Command.ID {
			continue
		}
		run.mu.Lock()
		busy := run.busy
		run.mu.Unlock()
		if busy {
			return true
		}
	}
	return false
}

func (m *RunManager) stopLocked(run *managedRun, reason string) error {
	if !run.busy || run.record.Phase == "draining" && (reason == "user_stop" || reason == "timeout" || reason == "node_shutdown") {
		return nil
	}
	if run.record.Reason == "" {
		run.record.Reason = reason
		if run.record.Phase != "draining" {
			run.record.Phase = "stopping"
		}
		run.stopStarted = m.clock.Now()
		if err := m.store.save(run.record); err != nil {
			run.record.StorageError = true
			run.record.LogComplete = false
		}
	}
	if run.graph != nil {
		return m.stopGraphLocked(run)
	}
	return nil
}

func (m *RunManager) Stop(id, runID, reason string) (*RunRecord, error) {
	if !wire.ValidID(id) || !wire.ValidID(runID) {
		return nil, problem("invalid", "err.localcmd.invalid_wire_req")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[runID]
	if run == nil || run.record.Command.ID != id {
		return nil, problem("conflict", "err.localcmd.run_changed")
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.record.ID != runID {
		return nil, problem("conflict", "err.localcmd.run_changed")
	}
	err := m.stopLocked(run, reason)
	record := m.snapshotLocked(run)
	return &record, err
}

func (m *RunManager) finishLocked(run *managedRun, evidence ExitEvidence) {
	if !run.busy || run.finalizing {
		return
	}
	run.finalizing = true
	if run.graph != nil {
		for i := range run.record.Nodes {
			if run.record.Nodes[i].Phase == "waiting" || run.record.Nodes[i].Phase == "ready" {
				finishStructuralNode(&run.record.Nodes[i], "skipped")
			}
		}
	}
	if run.file != nil {
		err := run.file.Sync()
		closeErr := run.file.Close()
		run.file = nil
		if err != nil || closeErr != nil {
			run.record.LogComplete = false
			run.record.StorageError = true
			if run.record.Reason == "" {
				run.record.Reason = "log_io_error"
			}
		}
	}
	run.record.Phase = "finished"
	run.record.Exit = evidence
	ended := time.Now().UTC()
	run.record.Ended = &ended
	run.record.Result, run.record.Reason = executionResult(run.record.Reason, evidence, run.record.LogComplete)
	if evidence.Kind == "graph_succeeded" && run.record.Reason == "exit_error" && run.record.LogComplete {
		run.record.Result, run.record.Reason = "succeeded", ""
	}
	if !m.persistFinalLocked(run) {
		go m.retryFinal(run)
	}
}

func (m *RunManager) persistFinalLocked(run *managedRun) bool {
	run.record.Phase = "finished"
	run.record.CleanupPending = false
	if err := m.store.save(run.record); err != nil {
		run.record.StorageError, run.record.LogComplete = true, false
		run.record.Phase, run.record.CleanupPending = "draining", true
		if run.record.Reason == "" {
			run.record.Reason, run.record.Result = "log_io_error", "failed"
		}
		return false
	}
	if err := m.history.save(run.record); err != nil {
		run.record.StorageError, run.record.CleanupPending = true, true
		run.record.Phase = "draining"
		return false
	}
	run.busy = false
	m.completedLocked(run)
	close(run.done)
	return true
}

func (m *RunManager) retryFinal(run *managedRun) {
	for {
		time.Sleep(250 * time.Millisecond)
		run.mu.Lock()
		done := m.persistFinalLocked(run)
		run.mu.Unlock()
		if done {
			return
		}
	}
}

func (m *RunManager) Read(id, runID string, cursor int64) (LogPage, error) {
	return m.readLog(id, runID, "", cursor, false)
}

func (m *RunManager) ReadNode(id, runID, nodeID string, cursor int64, before bool) (LogPage, error) {
	if !wire.ValidID(nodeID) {
		return LogPage{}, problem("invalid", "err.localcmd.invalid_log_node")
	}
	return m.readLog(id, runID, nodeID, cursor, before)
}

func (m *RunManager) readLog(id, runID, nodeID string, cursor int64, before bool) (LogPage, error) {
	if !wire.ValidID(id) || !wire.ValidID(runID) || cursor < -1 {
		return LogPage{}, problem("invalid", "err.localcmd.invalid_log_cursor")
	}
	m.mu.Lock()
	if m.history != nil {
		s, err := m.history.get(runID)
		if err == nil && s.CommandID == id && s.Details != "retained" {
			m.mu.Unlock()
			return LogPage{}, problem("record_expired", "err.localcmd.run_changed")
		}
	}
	run := m.runs[runID]
	if run == nil && m.history != nil {
		record, err := m.archivedRun(id, runID)
		if err == nil && record != nil && record.Phase == "finished" {
			run = &managedRun{record: *record}
		}
	}
	if run == nil || run.record.Command.ID != id {
		m.mu.Unlock()
		return LogPage{}, problem("conflict", "err.localcmd.run_changed")
	}
	run.mu.Lock()
	m.mu.Unlock()
	defer run.mu.Unlock()
	if run.record.ID != runID {
		return LogPage{}, problem("conflict", "err.localcmd.run_changed")
	}
	if nodeID != "" {
		return m.store.readNode(m.snapshotLocked(run), nodeID, cursor, before)
	}
	if before && cursor >= 0 {
		f, err := os.Open(m.store.logPath(id, runID))
		if err != nil {
			return LogPage{}, err
		}
		cursor, err = previousLogPage(f, cursor)
		f.Close()
		if err != nil {
			return LogPage{}, err
		}
	}
	if cursor == -1 {
		var err error
		cursor, err = m.store.tailCursor(run.record)
		if err != nil {
			cursor = 0
		}
	}
	return m.store.read(m.snapshotLocked(run), cursor)
}

func (m *RunManager) Clear(id string) error {
	if m == nil || id == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	active, _ := m.groupBusyLocked(id)
	if active > 0 {
		return problem("busy", "err.localcmd.running")
	}
	if err := m.reconcilePublicationsLocked(); err != nil {
		return err
	}
	if m.history != nil {
		for _, run := range m.runs {
			if run.record.Command.ID == id {
				run.mu.Lock()
				err := m.history.save(run.record)
				run.mu.Unlock()
				if err != nil {
					return err
				}
			}
		}
		rows, err := m.history.db.Query("SELECT id,round,ended FROM runs WHERE command=? AND admitted=1 AND details='retained'", id)
		if err != nil {
			return err
		}
		var items []retainedRun
		for rows.Next() {
			v := retainedRun{command: id}
			if err = rows.Scan(&v.id, &v.round, &v.ended); err != nil {
				rows.Close()
				return err
			}
			v.bytes = m.detailCharge(id, v.id)
			items = append(items, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, v := range items {
			if err = m.dropDetails(v, "command_deleted"); err != nil {
				return err
			}
		}
	}
	group := m.groups[id]
	// Hide records even on partial deletion; registry retains the cleanup intent.
	if group != nil {
		for _, runID := range group.index.IDs {
			delete(m.runs, runID)
		}
	}
	delete(m.groups, id)
	if err := m.store.clear(id); err != nil {
		return err
	}
	return m.history.pinCurrent(id, nil)
}

func (m *RunManager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	m.closed = true
	var pending []<-chan struct{}
	for _, run := range m.runs {
		run.mu.Lock()
		_ = m.stopLocked(run, "node_shutdown")
		pending = append(pending, run.done)
		run.mu.Unlock()
	}
	m.mu.Unlock()
	for _, done := range pending {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if m.history != nil {
		return m.history.db.Close()
	}
	return nil
}

func (m *RunManager) BeginShutdown() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for _, run := range m.runs {
		run.mu.Lock()
		_ = m.stopLocked(run, "node_shutdown")
		run.mu.Unlock()
	}
}
