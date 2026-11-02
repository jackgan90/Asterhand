package localcommands

import (
	"context"
	"errors"
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
)

// All graph state, including node executions, is owned by managedRun.mu.
// A slot is reserved before a worker starts and released only after cleanup.
type graphRun struct {
	nodes        []graphNodeRun
	predecessors [][]int
	active       int
	wake         chan struct{}
}

type graphNodeRun struct {
	execution Execution
	started   time.Duration
}

func initializeGraph(run *managedRun) {
	definition := run.record.Command.Graph
	g := &graphRun{nodes: make([]graphNodeRun, len(definition.Nodes)), predecessors: make([][]int, len(definition.Nodes)), wake: make(chan struct{}, 1)}
	indices := map[string]int{}
	for i, n := range definition.Nodes {
		indices[n.ID] = i
		run.record.Nodes = append(run.record.Nodes, NodeRecord{NodeID: n.ID, Kind: n.Kind, Name: n.Name, Phase: "waiting", LogComplete: true})
	}
	for _, edge := range definition.Edges {
		g.predecessors[indices[edge.To]] = append(g.predecessors[indices[edge.To]], indices[edge.From])
	}
	run.graph, run.record.LogFormat = g, 2
}

func notifyGraph(run *managedRun) {
	select {
	case run.graph.wake <- struct{}{}:
	default:
	}
}

func (m *RunManager) saveGraphLocked(run *managedRun) bool {
	if err := m.store.save(run.record); err != nil {
		run.record.StorageError, run.record.LogComplete = true, false
		if run.record.Reason == "" {
			run.record.Reason = "log_io_error"
			run.stopStarted = m.clock.Now()
		}
		run.record.Phase = "stopping"
		return false
	}
	return true
}

func (m *RunManager) executeGraph(run *managedRun) {
	run.mu.Lock()
	run.started = m.clock.Now()
	run.record.Started = time.Now().UTC()
	if run.record.Reason == "" {
		run.record.Phase = "running"
		for i := range run.record.Nodes {
			if run.record.Nodes[i].Kind == "start" {
				finishStructuralNode(&run.record.Nodes[i], "succeeded")
			}
		}
	}
	m.saveGraphLocked(run)
	run.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := m.clock.Wake(ctx)
	for {
		run.mu.Lock()
		m.checkGraphTimeoutsLocked(run)
		if run.record.Reason != "" {
			_ = m.stopGraphLocked(run)
		}
		progress := false
		if run.record.Reason == "" {
			progress = m.scheduleGraphLocked(run)
		}
		if run.graph.active == 0 && (run.record.Reason != "" || graphComplete(run)) {
			run.record.Phase = "draining"
			run.record.Elapsed = max(0, (m.clock.Now() - run.started).Seconds())
			evidence := ExitEvidence{Kind: "unknown"}
			if run.record.Reason == "" {
				evidence.Kind = "graph_succeeded"
			}
			run.record.CleanupPending = false
			m.finishLocked(run, evidence)
			run.mu.Unlock()
			return
		}
		run.mu.Unlock()
		if progress {
			continue
		}
		select {
		case <-run.graph.wake:
		case <-ticks:
		}
	}
}

func finishStructuralNode(n *NodeRecord, result string) {
	now := time.Now().UTC()
	n.Phase, n.Result, n.Ended = "finished", result, &now
}

func graphComplete(run *managedRun) bool {
	for _, n := range run.record.Nodes {
		if n.Result != "succeeded" {
			return false
		}
	}
	return true
}

func (m *RunManager) scheduleGraphLocked(run *managedRun) bool {
	for i := range run.record.Nodes {
		n := &run.record.Nodes[i]
		if n.Phase != "waiting" && n.Phase != "ready" {
			continue
		}
		ready := true
		for _, previous := range run.graph.predecessors[i] {
			if run.record.Nodes[previous].Phase != "finished" || run.record.Nodes[previous].Result != "succeeded" {
				ready = false
				break
			}
		}
		if !ready {
			continue
		}
		if n.Kind == "end" {
			finishStructuralNode(n, "succeeded")
			return true
		}
		n.Phase = "ready"
		if run.graph.active >= run.record.Command.Graph.Parallelism {
			continue
		}
		select {
		case m.graphSlots <- struct{}{}:
		default:
			return false
		}
		n.RunID, n.Phase = model.ID(), "starting"
		if !m.saveGraphLocked(run) {
			<-m.graphSlots
			finishStructuralNode(n, "failed")
			n.Reason = "log_io_error"
			return true
		}
		run.graph.active++
		go m.executeGraphNode(run, i)
		return true
	}
	return false
}

func (m *RunManager) checkGraphTimeoutsLocked(run *managedRun) {
	now := m.clock.Now()
	if run.record.Reason == "" && run.record.Command.TimeoutSeconds != nil && now-run.started >= time.Duration(*run.record.Command.TimeoutSeconds)*time.Second {
		_ = m.stopLocked(run, "timeout")
	}
	for i := range run.record.Nodes {
		n, state := &run.record.Nodes[i], &run.graph.nodes[i]
		if n.Phase != "running" {
			continue
		}
		n.Elapsed = max(0, (now - state.started).Seconds())
		limit := run.record.Command.Graph.Nodes[i].TimeoutSeconds
		if limit != nil && now-state.started >= time.Duration(*limit)*time.Second {
			m.failGraphNodeLocked(run, i, "timeout")
		}
	}
	if run.record.Phase == "stopping" && now-run.stopStarted >= 5*time.Second {
		run.record.CleanupPending = true
	}
}

func (m *RunManager) failGraphNodeLocked(run *managedRun, index int, reason string) {
	n := &run.record.Nodes[index]
	if n.Reason == "" {
		n.Reason, n.Result = reason, "failed"
	}
	if run.record.Reason == "" && abortsGraph(run.record.Command.Graph.Nodes[index].FailurePolicy, n.Result) {
		run.record.FailedNodeID = n.NodeID
		_ = m.stopLocked(run, "node_failed")
		return
	}
	if run.record.Reason != "" {
		_ = m.stopGraphLocked(run)
	}
}

func (m *RunManager) stopGraphLocked(run *managedRun) error {
	var result error
	changed := false
	for i := range run.record.Nodes {
		n := &run.record.Nodes[i]
		switch n.Phase {
		case "finished", "draining":
			continue
		case "waiting", "ready":
			finishStructuralNode(n, "skipped")
			n.Reason = "graph_aborted"
			changed = true
		default:
			if run.record.StorageError {
				n.LogComplete = false
			}
			if n.Reason == "" {
				n.Reason = "graph_aborted"
				changed = true
			}
			if n.Phase != "stopping" {
				n.Phase = "stopping"
				changed = true
			}
			if execution := run.graph.nodes[i].execution; execution != nil {
				if err := execution.ForceStop(); err != nil {
					result = errors.Join(result, err)
					run.record.CleanupPending = true
				}
			}
		}
	}
	if changed {
		m.saveGraphLocked(run)
		notifyGraph(run)
	}
	return result
}

func (m *RunManager) reconcileGraph(run *managedRun) {
	for {
		empty := m.backend != nil
		for _, n := range run.record.Nodes {
			if n.RunID == "" || n.Phase == "finished" {
				continue
			}
			if m.backend == nil {
				empty = false
				break
			}
			confirmed, err := m.backend.Reconcile(n.RunID)
			if !confirmed || err != nil {
				empty = false
			}
		}
		if empty {
			run.mu.Lock()
			for i := range run.record.Nodes {
				n := &run.record.Nodes[i]
				if n.Phase == "finished" {
					continue
				}
				result := "skipped"
				if n.RunID != "" {
					result = "interrupted"
					n.LogComplete = false
				}
				if n.Result != "" {
					result = n.Result
				}
				if n.Reason == "" {
					n.Reason = "unexpected_shutdown"
				}
				finishStructuralNode(n, result)
			}
			run.record.CleanupPending = false
			m.finishLocked(run, ExitEvidence{Kind: "unknown"})
			run.mu.Unlock()
			return
		}
		time.Sleep(time.Second)
	}
}
