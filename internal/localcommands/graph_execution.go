package localcommands

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

func (m *RunManager) executeGraphNode(run *managedRun, index int) {
	command := nodeScript(run.record.Command.Graph.Nodes[index])
	status := inspectScript(command)
	run.mu.Lock()
	n := &run.record.Nodes[index]
	if run.record.Reason != "" || status.state != "ready" {
		if run.record.Reason == "" {
			m.failGraphNodeLocked(run, index, "start_failed")
		}
		m.finishGraphNodeLocked(run, index, ExitEvidence{Kind: "unknown"})
		run.mu.Unlock()
		return
	}
	// The final stop check and platform handoff share the graph's state lock.
	execution, err := m.backend.Start(command, n.RunID)
	if execution == nil {
		m.failGraphNodeLocked(run, index, "start_failed")
		m.finishGraphNodeLocked(run, index, ExitEvidence{Kind: "unknown"})
		run.mu.Unlock()
		return
	}
	state := &run.graph.nodes[index]
	state.execution, state.started = execution, m.clock.Now()
	now := time.Now().UTC()
	n.Started, n.Phase = &now, "running"
	if err != nil {
		m.failGraphNodeLocked(run, index, "start_failed")
	}
	if !m.saveGraphLocked(run) {
		_ = m.stopGraphLocked(run)
	}
	run.mu.Unlock()
	m.waitGraphNode(run, index, execution)
}

func (m *RunManager) waitGraphNode(run *managedRun, index int, execution Execution) {
	var readers sync.WaitGroup
	readers.Add(2)
	go func() { defer readers.Done(); m.captureGraph(run, index, execution.Stdout(), 0) }()
	go func() { defer readers.Done(); m.captureGraph(run, index, execution.Stderr(), 1) }()
	var evidence ExitEvidence
	for {
		var err error
		evidence, err = execution.Wait(context.Background())
		if err == nil {
			break
		}
		run.mu.Lock()
		run.record.CleanupPending = true
		m.failGraphNodeLocked(run, index, "execution_error")
		run.mu.Unlock()
		time.Sleep(100 * time.Millisecond)
	}
	run.mu.Lock()
	n := &run.record.Nodes[index]
	n.Phase, n.Elapsed = "draining", max(0, (m.clock.Now()-run.graph.nodes[index].started).Seconds())
	if result, reason := executionResult(n.Reason, evidence, n.LogComplete); result == "failed" {
		m.failGraphNodeLocked(run, index, reason)
	}
	run.mu.Unlock()
	drained := make(chan struct{})
	go func() { readers.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		execution.Stdout().Close()
		execution.Stderr().Close()
		<-drained
		run.mu.Lock()
		n.LogComplete, run.record.LogComplete = false, false
		m.failGraphNodeLocked(run, index, "log_drain_timeout")
		run.mu.Unlock()
	}
	closeErr := execution.Close()
	run.mu.Lock()
	if closeErr != nil {
		m.failGraphNodeLocked(run, index, "execution_error")
	}
	run.graph.nodes[index].execution = nil
	m.finishGraphNodeLocked(run, index, evidence)
	run.mu.Unlock()
}

func (m *RunManager) finishGraphNodeLocked(run *managedRun, index int, evidence ExitEvidence) {
	n := &run.record.Nodes[index]
	if n.Phase == "finished" {
		return
	}
	if run.file != nil {
		if err := run.file.Sync(); err != nil {
			n.LogComplete, run.record.LogComplete, run.record.StorageError = false, false, true
			m.failGraphNodeLocked(run, index, "log_io_error")
		}
	}
	n.Exit = evidence
	n.Result, n.Reason = executionResult(n.Reason, evidence, n.LogComplete)
	if n.Started == nil && n.Reason == "graph_aborted" {
		// A reserved slot and persisted identity do not mean a process started.
		n.Result = "skipped"
	}
	finishStructuralNode(n, n.Result)
	run.graph.active--
	<-m.graphSlots
	if abortsGraph(run.record.Command.Graph.Nodes[index].FailurePolicy, n.Result) {
		m.failGraphNodeLocked(run, index, n.Reason)
	}
	if !m.saveGraphLocked(run) {
		_ = m.stopGraphLocked(run)
	}
	notifyGraph(run)
}

func (m *RunManager) captureGraph(run *managedRun, index int, reader io.Reader, stream byte) {
	buffer := make([]byte, logBlockSize)
	for {
		n, err := reader.Read(buffer)
		run.mu.Lock()
		if n > 0 && (run.record.StorageError || run.record.Reason == "log_limit") {
			// Draining after a capture failure still consumes bytes. Each node
			// whose output is discarded must expose that loss in its own pane.
			run.record.Nodes[index].LogComplete, run.record.LogComplete = false, false
		}
		if n > 0 && !run.record.StorageError && run.record.Reason != "log_limit" {
			if writeErr := m.appendRunLog(run, index, stream, buffer[:n]); writeErr != nil {
				run.record.Nodes[index].LogComplete, run.record.LogComplete = false, false
				if run.record.Reason != "log_limit" {
					run.record.StorageError = true
					m.failGraphNodeLocked(run, index, "log_io_error")
				}
			}
		}
		if err != nil && !errors.Is(err, io.EOF) {
			run.record.Nodes[index].LogComplete, run.record.LogComplete = false, false
			m.failGraphNodeLocked(run, index, "capture_error")
		}
		run.mu.Unlock()
		if err != nil {
			return
		}
	}
}
