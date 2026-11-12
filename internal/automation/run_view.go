package automation

import (
	"strconv"
	"time"

	"github.com/jackgan90/Asterhand/internal/localcommands"
)

type RunView struct {
	ID             string                     `json:"run_id"`
	Command        string                     `json:"command_id"`
	Name           string                     `json:"name"`
	Revision       string                     `json:"command_revision"`
	Round          string                     `json:"round_id"`
	Sequence       string                     `json:"sequence"`
	Phase          string                     `json:"phase"`
	Result         string                     `json:"result,omitempty"`
	Reason         string                     `json:"reason,omitempty"`
	Started        time.Time                  `json:"started_at"`
	Ended          *time.Time                 `json:"ended_at,omitempty"`
	Elapsed        float64                    `json:"elapsed_seconds"`
	Exit           localcommands.ExitEvidence `json:"exit"`
	LogComplete    bool                       `json:"log_complete"`
	StorageError   bool                       `json:"storage_error"`
	CleanupPending bool                       `json:"cleanup_pending"`
	Steps          []RunStep                  `json:"steps"`
}
type RunStep struct {
	ID          string                     `json:"step_id"`
	Kind        string                     `json:"kind"`
	Name        string                     `json:"name"`
	Phase       string                     `json:"phase"`
	Result      string                     `json:"result,omitempty"`
	Reason      string                     `json:"reason,omitempty"`
	Started     *time.Time                 `json:"started_at,omitempty"`
	Ended       *time.Time                 `json:"ended_at,omitempty"`
	Elapsed     float64                    `json:"elapsed_seconds"`
	Exit        localcommands.ExitEvidence `json:"exit"`
	LogComplete bool                       `json:"log_complete"`
}

func RunFrom(r *localcommands.RunRecord) RunView {
	v := RunView{ID: r.ID, Command: r.Command.ID, Name: r.Command.Name, Revision: r.Command.Revision, Round: r.RoundID, Sequence: strconv.FormatUint(r.Sequence, 10), Phase: r.Phase, Result: r.Result, Reason: r.Reason, Started: r.Started, Ended: r.Ended, Elapsed: r.Elapsed, Exit: r.Exit, LogComplete: r.LogComplete, StorageError: r.StorageError, CleanupPending: r.CleanupPending, Steps: []RunStep{}}
	for _, step := range r.Nodes {
		v.Steps = append(v.Steps, RunStep{ID: step.NodeID, Kind: step.Kind, Name: step.Name, Phase: step.Phase, Result: step.Result, Reason: step.Reason, Started: step.Started, Ended: step.Ended, Elapsed: step.Elapsed, Exit: step.Exit, LogComplete: step.LogComplete})
	}
	return v
}
