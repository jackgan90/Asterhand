package localcommands

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/model"
)

type managedRun struct {
	graph       *graphRun
	mu          sync.Mutex
	record      RunRecord
	execution   Execution
	file        *logFile
	busy        bool
	started     time.Duration
	stopStarted time.Duration
	done        chan struct{}
}

// RunManager owns accepted work independently of request and UI lifetimes.
// Lock order is registry -> manager -> run; workers only acquire the run lock.
type RunManager struct {
	graphSlots chan struct{}
	mu         sync.Mutex
	store      runStore
	backend    ExecutionBackend
	clock      ExecutionClock
	runs       map[string]*managedRun
	closed     bool
	loadErr    error
}

func NewRunManager(home string, backend ExecutionBackend, clock ExecutionClock) *RunManager {
	m := &RunManager{store: runStore{filepath.Join(home, "command-runs")}, backend: backend, clock: clock, runs: map[string]*managedRun{}, graphSlots: make(chan struct{}, GraphExecutionSlots)}
	if clock == nil {
		m.clock = platformClock{}
	}
	m.loadErr = m.recover()
	return m
}

func (m *RunManager) Available() bool {
	return m != nil && m.backend != nil && m.backend.Available() && m.loadErr == nil
}

func (m *RunManager) recover() error {
	entries, err := os.ReadDir(m.store.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		id := entry.Name()
		if !entry.IsDir() || !wire.ValidID(id) {
			continue
		}
		if _, err := os.Stat(filepath.Join(m.store.directory(id), "deleting")); err == nil {
			if err = m.store.clear(id); err != nil {
				return err
			}
			continue
		}
		record, err := m.store.load(id)
		if err != nil {
			return err
		}
		if record == nil {
			continue
		}
		run := &managedRun{record: *record, done: make(chan struct{})}
		m.runs[id] = run
		if record.Phase != "finished" {
			run.busy = true
			run.record.CleanupPending = true
			run.record.LogComplete = false
			if run.record.Reason == "" {
				run.record.Reason = "unexpected_shutdown"
			}
			go m.reconcile(run)
		} else {
			close(run.done)
		}
		if err = m.store.removeOtherLogs(id, record.ID); err != nil {
			return err
		}
	}
	return nil
}

func (m *RunManager) reconcile(run *managedRun) {
	if run.record.Command.Kind == "graph" {
		m.reconcileGraph(run)
		return
	}
	for {
		if m.backend != nil {
			empty, err := m.backend.Reconcile(run.record.ID)
			if err == nil && empty {
				run.mu.Lock()
				run.record.CleanupPending = false
				m.finishLocked(run, ExitEvidence{Kind: "unknown"})
				run.mu.Unlock()
				return
			}
		}
		time.Sleep(time.Second)
	}
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
	if (run.execution != nil || run.graph != nil) && (record.Phase == "running" || record.Phase == "stopping") {
		record.Elapsed = max(0, (m.clock.Now() - run.started).Seconds())
	}
	return publicRun(record)
}

func (m *RunManager) Current(id string) *RunRecord {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if run == nil {
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
	for key, run := range m.runs {
		if id != "" && id != key {
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

func (m *RunManager) Start(command Command) (*RunRecord, error) {
	if command.Kind == "graph" {
		graph, err := normalizeGraph(command.Graph, commandPolicy)
		if err != nil {
			return nil, err
		}
		if command.ExecutionMode != "managed" {
			return nil, graphProblem("err.graph.managed", "")
		}
		command.Graph = graph
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, problem("unavailable", "err.app.node_stopping")
	}
	if !m.Available() {
		return nil, problem("unsupported", "err.localcmd.managed_unavailable")
	}
	if old := m.runs[command.ID]; old != nil {
		old.mu.Lock()
		busy := old.busy
		old.mu.Unlock()
		if busy {
			return nil, problem("busy", "err.localcmd.running")
		}
	}
	run := &managedRun{record: RunRecord{ID: model.ID(), Command: command, Phase: "starting", Started: time.Now().UTC(), LogComplete: true}, busy: true, done: make(chan struct{})}
	if command.Kind == "graph" {
		initializeGraph(run)
	}
	f, err := m.store.prepare(run.record)
	if err != nil {
		return nil, err
	}
	run.file = f
	m.runs[command.ID] = run
	run.mu.Lock()
	defer run.mu.Unlock()
	// Published before platform creation, including its recovery identity.
	if err = m.store.removeOtherLogs(command.ID, run.record.ID); err != nil {
		run.record.StorageError = true
		run.record.LogComplete = false
		run.record.Reason = "log_io_error"
		m.finishLocked(run, ExitEvidence{Kind: "unknown"})
	} else {
		if run.graph != nil {
			go m.executeGraph(run)
		} else {
			go m.execute(run)
		}
	}
	record := m.snapshotLocked(run)
	return &record, nil
}

func (m *RunManager) execute(run *managedRun) {
	run.mu.Lock()
	if run.record.Reason != "" {
		m.finishLocked(run, ExitEvidence{Kind: "unknown"})
		run.mu.Unlock()
		return
	}
	execution, err := m.backend.Start(run.record.Command, run.record.ID)
	if err != nil && execution == nil {
		run.record.Reason = "start_failed"
		m.finishLocked(run, ExitEvidence{Kind: "unknown"})
		run.mu.Unlock()
		return
	}
	run.execution = execution
	run.started = m.clock.Now()
	run.record.Started = time.Now().UTC()
	run.record.Phase = "running"
	if err != nil {
		m.stopLocked(run, "start_failed")
	}
	if err = m.store.save(run.record); err != nil {
		m.stopLocked(run, "log_io_error")
	}
	run.mu.Unlock()
	var readers sync.WaitGroup
	readers.Add(2)
	go func() { defer readers.Done(); m.capture(run, execution.Stdout(), 0) }()
	go func() { defer readers.Done(); m.capture(run, execution.Stderr(), 1) }()
	ctx, cancel := context.WithCancel(context.Background())
	go m.watch(run, ctx)
	var evidence ExitEvidence
	for {
		evidence, err = execution.Wait(context.Background())
		if err == nil {
			break
		}
		run.mu.Lock()
		run.record.CleanupPending = true
		m.stopLocked(run, "execution_error")
		run.mu.Unlock()
		time.Sleep(100 * time.Millisecond)
	}
	run.mu.Lock()
	// Natural completion is confirmed before the independent log-drain budget.
	run.record.Phase = "draining"
	run.record.Elapsed = (m.clock.Now() - run.started).Seconds()
	run.mu.Unlock()
	cancel()
	drained := make(chan struct{})
	go func() { readers.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		execution.Stdout().Close()
		execution.Stderr().Close()
		<-drained
		run.mu.Lock()
		run.record.LogComplete = false
		if run.record.Reason == "" {
			run.record.Reason = "log_drain_timeout"
		}
		run.mu.Unlock()
	}
	_ = execution.Close()
	run.mu.Lock()
	run.execution = nil
	run.record.CleanupPending = false
	m.finishLocked(run, evidence)
	run.mu.Unlock()
}

func (m *RunManager) capture(run *managedRun, reader io.Reader, stream byte) {
	buffer := make([]byte, logBlockSize)
	for {
		n, err := reader.Read(buffer)
		run.mu.Lock()
		if n > 0 && run.record.LogComplete {
			if writeErr := appendLog(run.file, stream, buffer[:n]); writeErr != nil {
				run.record.LogComplete = false
				run.record.StorageError = true
				m.stopLocked(run, "log_io_error")
			}
		}
		if err != nil && !errors.Is(err, io.EOF) {
			run.record.LogComplete = false
			m.stopLocked(run, "capture_error")
		}
		run.mu.Unlock()
		if err != nil {
			return
		}
	}
}

func (m *RunManager) watch(run *managedRun, ctx context.Context) {
	for range m.clock.Wake(ctx) {
		run.mu.Lock()
		if run.record.Phase == "running" && run.record.Command.TimeoutSeconds != nil && m.clock.Now()-run.started >= time.Duration(*run.record.Command.TimeoutSeconds)*time.Second {
			m.stopLocked(run, "timeout")
		}
		if run.record.Phase == "stopping" && m.clock.Now()-run.stopStarted >= 5*time.Second {
			run.record.CleanupPending = true
		}
		run.mu.Unlock()
	}
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
	if run.execution != nil {
		if err := run.execution.ForceStop(); err != nil {
			run.record.CleanupPending = true
			return err
		}
	}
	return nil
}

func (m *RunManager) Stop(id, runID, reason string) (*RunRecord, error) {
	if !wire.ValidID(id) || !wire.ValidID(runID) {
		return nil, problem("invalid", "err.localcmd.invalid_wire_req")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if run == nil {
		return nil, problem("not_found", "err.localcmd.no_run")
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
	if !run.busy {
		return
	}
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
	if run.record.Command.Kind == "graph" && evidence.Kind == "graph_succeeded" && run.record.Reason == "exit_error" && run.record.LogComplete {
		run.record.Result, run.record.Reason = "succeeded", ""
	}
	if err := m.store.save(run.record); err != nil {
		run.record.StorageError = true
		run.record.LogComplete = false
		if run.record.Reason == "" {
			run.record.Reason = "log_io_error"
			run.record.Result = "failed"
		}
	}
	run.busy = false
	close(run.done)
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
	defer m.mu.Unlock()
	run := m.runs[id]
	if run == nil {
		return LogPage{}, problem("not_found", "err.localcmd.no_run")
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.record.ID != runID {
		return LogPage{}, problem("conflict", "err.localcmd.run_changed")
	}
	if nodeID != "" {
		return m.store.readNode(m.snapshotLocked(run), nodeID, cursor, before)
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
	if run := m.runs[id]; run != nil {
		run.mu.Lock()
		busy := run.busy
		run.mu.Unlock()
		if busy {
			return problem("busy", "err.localcmd.running")
		}
	}
	if err := m.store.clear(id); err != nil {
		delete(m.runs, id)
		return err
	}
	delete(m.runs, id)
	return nil
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
