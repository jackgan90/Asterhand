package localcommands

import (
	"time"

	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
)

type NodeRecord struct {
	NodeID      string       `json:"node_id"`
	Kind        string       `json:"kind"`
	Name        string       `json:"name"`
	RunID       string       `json:"run_id,omitempty"`
	Phase       string       `json:"phase"`
	Result      string       `json:"result,omitempty"`
	Reason      string       `json:"reason,omitempty"`
	Started     *time.Time   `json:"started_at,omitempty"`
	Ended       *time.Time   `json:"ended_at,omitempty"`
	Elapsed     float64      `json:"elapsed_seconds"`
	Exit        ExitEvidence `json:"exit"`
	LogComplete bool         `json:"log_complete"`
}

func cloneNodeRecords(nodes []NodeRecord) []NodeRecord {
	if nodes == nil {
		return nil
	}
	result := append([]NodeRecord{}, nodes...)
	for i := range result {
		n := &result[i]
		if n.Started != nil {
			value := *n.Started
			n.Started = &value
		}
		if n.Ended != nil {
			value := *n.Ended
			n.Ended = &value
		}
		if n.Exit.Code != nil {
			value := *n.Exit.Code
			n.Exit.Code = &value
		}
	}
	return result
}

// Configuration is durable privately; IPC carries only the bounded UI summary.
func publicRun(record RunRecord) RunRecord {
	record.Command = Command{ID: record.Command.ID, Name: record.Command.Name,
		Revision: record.Command.Revision, TimeoutSeconds: record.Command.TimeoutSeconds, AllowConcurrent: record.Command.AllowConcurrent}
	return record
}

// Log pages retain graph identity and state, without private execution IDs.
func logRecord(record RunRecord) RunRecord {
	record = publicRun(record)
	if len(record.Nodes) > 0 {
		record.Nodes = cloneNodeRecords(record.Nodes)
		for i := range record.Nodes {
			record.Nodes[i].RunID = ""
		}
	}
	return record
}

func validateGraphRecord(r RunRecord) error {
	invalid := func() error { return problem("configuration", "err.localcmd.invalid_run") }
	graph, err := normalizeGraph(r.Command.Graph, commandPolicy)
	if err != nil || r.LogFormat != 2 || len(r.Nodes) != len(graph.Nodes) {
		return invalid()
	}
	identities := map[string]bool{}
	for i, n := range r.Nodes {
		if n.NodeID != graph.Nodes[i].ID || n.Kind != graph.Nodes[i].Kind || !cleanText(n.Name, 128) {
			return invalid()
		}
		if n.RunID != "" {
			if !wire.ValidID(n.RunID) || identities[n.RunID] || n.Kind != "script" {
				return invalid()
			}
			identities[n.RunID] = true
		}
	}
	return nil
}

func logFrameSource(r RunRecord, value byte) (string, byte, error) {
	if r.LogFormat == 2 {
		index := int(value / 2)
		if index < len(r.Nodes) && r.Nodes[index].Kind == "script" {
			return r.Nodes[index].NodeID, value % 2, nil
		}

	}
	return "", 0, problem("invalid", "err.localcmd.invalid_log_cursor")
}

func appendGraphLog(f *logFile, index int, stream byte, data []byte) error {
	if f.nodes == nil {
		f.nodes = map[byte]*logContext{}
	}
	key := byte(index)
	context := f.nodes[key]
	if context == nil {
		context = &logContext{}
		f.nodes[key] = context
	}
	return appendLogWithContext(f, context, key*2+stream, data)
}

func executionResult(reason string, evidence ExitEvidence, logComplete bool) (string, string) {
	switch reason {
	case "user_stop", "node_shutdown", "unexpected_shutdown", "graph_aborted":
		return "interrupted", reason
	case "":
		if evidence.Kind == "exited" && evidence.Code != nil && *evidence.Code == 0 && logComplete {
			return "succeeded", ""
		}
		return "failed", "exit_error"
	default:
		return "failed", reason
	}
}
