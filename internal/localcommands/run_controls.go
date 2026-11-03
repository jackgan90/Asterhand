package localcommands

import (
	"errors"

	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
)

func (m *RunManager) StopAll(id string, ids []string) error {
	if !wire.ValidID(id) || len(ids) == 0 || len(ids) > MaxActiveCommandRuns {
		return problem("invalid", "err.localcmd.invalid_wire_req")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	selected := make([]*managedRun, 0, len(ids))
	seen := map[string]bool{}
	for _, runID := range ids {
		run := m.runs[runID]
		if !wire.ValidID(runID) || seen[runID] {
			return problem("invalid", "err.localcmd.invalid_wire_req")
		}
		if run == nil || run.record.Command.ID != id {
			return problem("conflict", "err.localcmd.run_changed")
		}
		seen[runID] = true
		selected = append(selected, run)
	}
	var result error
	for _, run := range selected {
		run.mu.Lock()
		result = errors.Join(result, m.stopLocked(run, "user_stop"))
		run.mu.Unlock()
	}
	return result
}

// The run mutex protects its byte count; CAS reserves the shared round budget
// without acquiring the manager lock from inside a running graph.
func (m *RunManager) appendRunLog(run *managedRun, index int, stream byte, data []byte) error {
	size := int64(logFrameOverhead + len(data))
	if run.logBytes+size > MaxRunLogBytes {
		return m.logLimitLocked(run, index)
	}
	for {
		used := run.group.bytes.Load()
		if used+size > MaxRoundLogBytes {
			return m.logLimitLocked(run, index)
		}
		if run.group.bytes.CompareAndSwap(used, used+size) {
			break
		}
	}
	// Reservations remain charged on partial IO failures, conservatively.
	run.logBytes += size
	return appendGraphLog(run.file, index, stream, data)
}

func (m *RunManager) logLimitLocked(run *managedRun, index int) error {
	run.record.LogComplete = false
	run.record.Nodes[index].LogComplete = false
	m.failGraphNodeLocked(run, index, "log_limit")
	return problem("limit", "err.localcmd.log_limit")
}
