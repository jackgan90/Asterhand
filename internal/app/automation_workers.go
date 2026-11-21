package app

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/workers"
)

type workerCreateInput struct {
	HarnessID       string `json:"harness_id" description:"Target worker harness identifier (e.g. \"codex\")"`
	WorkspaceRoot   string `json:"workspace_root" description:"Absolute path to the local project directory"`
	Alias           string `json:"alias,omitempty" description:"Optional stable worker alias unique within the project (e.g. \"codex-reviewer\")"`
	AccessMode      string `json:"access_mode,omitempty" enum:"read_only,workspace_write" description:"Worker access mode: \"read_only\" (default) or \"workspace_write\" (requires enabling workspace write for the target harness in Asterhand settings)"`
	RoleHint        string `json:"role_hint,omitempty" description:"Optional role instructions passed to the worker (e.g. reviewer persona)"`
	Model           string `json:"model,omitempty" description:"Optional default model name for this worker"`
	ReasoningEffort string `json:"reasoning_effort,omitempty" description:"Optional default reasoning effort / thinking level (e.g. low, medium, high, xhigh)"`
}

type workerListInput struct {
	WorkspaceRoot string                 `json:"workspace_root,omitempty" description:"Optional absolute project directory path to filter workers"`
	WorkspaceID   string                 `json:"workspace_id,omitempty" description:"Optional derived workspace_id to filter workers"`
	IncludeClosed bool                   `json:"include_closed,omitempty" description:"Include closed workers in results (default false)"`
	Limit         automation.OptionalInt `json:"limit,omitempty,omitzero" description:"Maximum number of workers to return (1..200, default 50)"`
}

type workerGetInput struct {
	WorkerID    string `json:"worker_id" description:"Worker identifier returned by workers_create or workers_list"`
	WorkspaceID string `json:"workspace_id,omitempty" description:"Optional expected workspace_id to guard against cross-project mismatch"`
}

type workerSendInput struct {
	WorkerID        string   `json:"worker_id" description:"Target worker identifier returned by workers_create or workers_list"`
	WorkspaceID     string   `json:"workspace_id,omitempty" description:"Optional expected workspace_id to guard against cross-project mismatch"`
	Prompt          string   `json:"prompt" description:"Task instruction or review prompt for this turn"`
	Model           string   `json:"model,omitempty" description:"Optional per-turn model override (defaults to worker model)"`
	ReasoningEffort string   `json:"reasoning_effort,omitempty" description:"Optional per-turn reasoning effort / thinking level override (e.g. low, medium, high, xhigh)"`
	ArtifactPaths   []string `json:"artifact_paths,omitempty" description:"Optional list of project-relative or absolute file paths within the project to highlight"`
	GitRef          string   `json:"git_ref,omitempty" description:"Optional git commit/branch reference for context"`
	DiffBase        string   `json:"diff_base,omitempty" description:"Optional git diff base reference for review context"`
	OutputContract  string   `json:"output_contract,omitempty" description:"Optional expected output structure or format constraint"`
}

type workerWaitInput struct {
	TurnID      string                 `json:"turn_id" description:"Turn identifier returned by workers_send"`
	WorkspaceID string                 `json:"workspace_id,omitempty" description:"Optional expected workspace_id to guard against cross-project mismatch"`
	WaitSeconds automation.OptionalInt `json:"wait_seconds,omitempty,omitzero" description:"Seconds to wait for progress or completion (1..30, default 30)"`
}

type workerResultInput struct {
	TurnID      string                 `json:"turn_id" description:"Turn identifier returned by workers_send"`
	WorkspaceID string                 `json:"workspace_id,omitempty" description:"Optional expected workspace_id to guard against cross-project mismatch"`
	Cursor      string                 `json:"cursor,omitempty" description:"Byte offset cursor returned by a previous truncated workers_wait or workers_result call (must align to a UTF-8 character boundary)"`
	MaxBytes    automation.OptionalInt `json:"max_bytes,omitempty,omitzero" description:"Maximum UTF-8 bytes to return per page (1..32768, default 24576)"`
}

type workerLogsInput struct {
	TurnID      string                 `json:"turn_id" description:"Turn identifier returned by workers_send"`
	WorkspaceID string                 `json:"workspace_id,omitempty" description:"Optional expected workspace_id to guard against cross-project mismatch"`
	AfterSeq    int64                  `json:"after_seq,omitempty" description:"Return log entries with seq greater than this value"`
	Limit       automation.OptionalInt `json:"limit,omitempty,omitzero" description:"Maximum log entries to return (1..200, default 50)"`
}

type workerCancelInput struct {
	TurnID      string `json:"turn_id" description:"Specific active turn_id to cancel"`
	WorkspaceID string `json:"workspace_id,omitempty" description:"Optional expected workspace_id to guard against cross-project mismatch"`
}

type workerCloseInput struct {
	WorkerID    string `json:"worker_id" description:"Idle worker_id to close and archive"`
	WorkspaceID string `json:"workspace_id,omitempty" description:"Optional expected workspace_id to guard against cross-project mismatch"`
}

type workerResolveInput struct {
	TurnID string `json:"turn_id" description:"Turn identifier currently in reconciliation_required state"`
	Reason string `json:"reason" description:"Audit reason for manually closing the unresolved turn"`
}

type workerGetView struct {
	Worker workers.Worker `json:"worker"`
	Turns  []workers.Turn `json:"turns"`
}

type workerWaitView struct {
	Turn            workers.Turn `json:"turn"`
	WorkerID        string       `json:"worker_id"`
	TurnID          string       `json:"turn_id"`
	State           string       `json:"state"`
	AnswerAvailable bool         `json:"answer_available"`
	AnswerSource    string       `json:"answer_source,omitempty"`
	Answer          string       `json:"answer,omitempty"`
	PartialOutput   string       `json:"partial_output,omitempty"`
	Truncated       bool         `json:"truncated,omitempty"`
	ResultBytes     int          `json:"result_bytes"`
}

type workerLogsView struct {
	TurnID   string             `json:"turn_id"`
	WorkerID string             `json:"worker_id"`
	State    string             `json:"state"`
	Entries  []workers.LogEntry `json:"entries"`
	HasMore  bool               `json:"has_more"`
}

func optionalIntRange(opt automation.OptionalInt, name string, def, min, max int) (int, error) {
	if !opt.Set {
		return def, nil
	}
	if opt.Value < min || opt.Value > max {
		return 0, automation.Fail("invalid_parameters", fmt.Sprintf("%s must be between %d and %d", name, min, max))
	}
	return opt.Value, nil
}

func (a *App) registerAutomationWorkers(r *automation.Registry) {
	scopeWorkers := func(p automation.Principal, target string) error {
		return p.Require("workers", target)
	}

	automation.Register(r, automation.Definition{
		Name:        "workers_create",
		Description: "Create a persistent coding harness worker bound to a project directory and stable alias (e.g. codex-reviewer); optional model and reasoning_effort (e.g. low, medium, high, xhigh)",
		Role:        "execute",
		Toolset:     automation.ToolsetCollab,
		Write:       true,
	}, func(v workerCreateInput) string { return v.HarnessID },
		func(p automation.Principal, v workerCreateInput) error { return scopeWorkers(p, v.HarnessID) },
		func(ctx context.Context, call automation.Call, v workerCreateInput) (automation.Outcome, error) {
			if a.Workers == nil {
				return automation.Outcome{}, automation.Fail("unavailable", "Workers service is unavailable")
			}
			w, err := a.Workers.Create(ctx, workers.CreateInput{
				OperationID:     call.Identity.ID,
				HarnessID:       v.HarnessID,
				WorkspaceRoot:   v.WorkspaceRoot,
				Alias:           v.Alias,
				AccessMode:      v.AccessMode,
				RoleHint:        v.RoleHint,
				Model:           v.Model,
				ReasoningEffort: v.ReasoningEffort,
			})
			if err != nil {
				return automation.Outcome{}, err
			}
			return automation.Outcome{
				Accepted: true,
				Data:     w,
				Summary:  map[string]any{"worker_id": w.ID, "workspace_id": w.WorkspaceID, "alias": w.Alias, "state": w.State},
			}, nil
		})

	automation.Register(r, automation.Definition{
		Name:        "workers_list",
		Description: "List coding harness workers, optionally filtered by project workspace_root or workspace_id",
		Role:        "read",
		Toolset:     automation.ToolsetCollab,
	}, nil,
		func(p automation.Principal, _ workerListInput) error { return scopeWorkers(p, "*") },
		func(ctx context.Context, call automation.Call, v workerListInput) (automation.Outcome, error) {
			if a.Workers == nil {
				return automation.Outcome{}, automation.Fail("unavailable", "Workers service is unavailable")
			}
			limit, err := optionalIntRange(v.Limit, "limit", 50, 1, 200)
			if err != nil {
				return automation.Outcome{}, err
			}
			items, err := a.Workers.List(v.WorkspaceRoot, v.WorkspaceID, v.IncludeClosed, limit)
			if err != nil {
				return automation.Outcome{}, err
			}
			return automation.Outcome{Data: map[string]any{"items": items}}, nil
		})

	automation.Register(r, automation.Definition{
		Name:        "workers_get",
		Description: "Read a worker's metadata, persistent session binding, and recent turns",
		Role:        "read",
		Toolset:     automation.ToolsetCollab,
	}, func(v workerGetInput) string { return v.WorkerID },
		func(p automation.Principal, v workerGetInput) error { return scopeWorkers(p, v.WorkerID) },
		func(ctx context.Context, call automation.Call, v workerGetInput) (automation.Outcome, error) {
			if a.Workers == nil {
				return automation.Outcome{}, automation.Fail("unavailable", "Workers service is unavailable")
			}
			w, turns, err := a.Workers.Get(v.WorkerID, v.WorkspaceID)
			if err != nil {
				return automation.Outcome{}, err
			}
			return automation.Outcome{Data: workerGetView{Worker: w, Turns: turns}}, nil
		})

	automation.Register(r, automation.Definition{
		Name:        "workers_send",
		Description: "Dispatch a new turn to an existing worker (optional per-turn model and reasoning_effort override) and return turn_id immediately; follow up with workers_wait(turn_id)",
		Role:        "execute",
		Toolset:     automation.ToolsetCollab,
		Write:       true,
	}, func(v workerSendInput) string { return v.WorkerID },
		func(p automation.Principal, v workerSendInput) error { return scopeWorkers(p, v.WorkerID) },
		func(ctx context.Context, call automation.Call, v workerSendInput) (automation.Outcome, error) {
			if a.Workers == nil {
				return automation.Outcome{}, automation.Fail("unavailable", "Workers service is unavailable")
			}
			t, err := a.Workers.Send(ctx, workers.SendInput{
				OperationID:         call.Identity.ID,
				CallerID:            call.Principal.CallerID,
				WorkerID:            v.WorkerID,
				ExpectedWorkspaceID: v.WorkspaceID,
				Prompt:              v.Prompt,
				Model:               v.Model,
				ReasoningEffort:     v.ReasoningEffort,
				ArtifactPaths:       v.ArtifactPaths,
				GitRef:              v.GitRef,
				DiffBase:            v.DiffBase,
				OutputContract:      v.OutputContract,
			})
			if err != nil {
				return automation.Outcome{}, err
			}
			return automation.Outcome{
				Accepted: true,
				Data:     t,
				Summary:  map[string]any{"worker_id": t.WorkerID, "turn_id": t.ID, "workspace_id": t.WorkspaceID, "state": t.State},
			}, nil
		})

	automation.Register(r, automation.Definition{
		Name:        "workers_wait",
		Description: "Wait up to 30 seconds for a worker turn to progress or finish; returns status, final answer preview, or next-step guidance",
		Role:        "read",
		Toolset:     automation.ToolsetCollab,
		Wait:        true,
	}, func(v workerWaitInput) string { return v.TurnID },
		func(p automation.Principal, v workerWaitInput) error { return scopeWorkers(p, v.TurnID) },
		func(ctx context.Context, call automation.Call, v workerWaitInput) (automation.Outcome, error) {
			if a.Workers == nil {
				return automation.Outcome{}, automation.Fail("unavailable", "Workers service is unavailable")
			}
			waitSec, err := optionalIntRange(v.WaitSeconds, "wait_seconds", 30, 1, 30)
			if err != nil {
				return automation.Outcome{}, err
			}
			dur := time.Duration(waitSec) * time.Second
			t, preview, trunc, err := a.Workers.Wait(ctx, v.TurnID, v.WorkspaceID, dur)
			if err != nil {
				return automation.Outcome{}, err
			}
			return automation.Outcome{Data: workerWaitView{
				Turn:            t,
				WorkerID:        t.WorkerID,
				TurnID:          t.ID,
				State:           t.State,
				AnswerAvailable: t.AnswerAvailable,
				AnswerSource:    t.AnswerSource,
				Answer:          preview,
				PartialOutput:   t.PartialOutput,
				Truncated:       trunc,
				ResultBytes:     t.ResultBytes,
			}}, nil
		})

	automation.Register(r, automation.Definition{
		Name:        "workers_result",
		Description: "Read paginated full final review/output text for a completed worker turn",
		Role:        "read",
		Toolset:     automation.ToolsetCollab,
	}, func(v workerResultInput) string { return v.TurnID },
		func(p automation.Principal, v workerResultInput) error { return scopeWorkers(p, v.TurnID) },
		func(ctx context.Context, call automation.Call, v workerResultInput) (automation.Outcome, error) {
			if a.Workers == nil {
				return automation.Outcome{}, automation.Fail("unavailable", "Workers service is unavailable")
			}
			offset := 0
			if v.Cursor != "" {
				n, err := strconv.Atoi(v.Cursor)
				if err != nil || n < 0 {
					return automation.Outcome{}, automation.Fail("invalid_cursor", "Cursor must be a non-negative byte offset")
				}
				offset = n
			}
			maxBytes, err := optionalIntRange(v.MaxBytes, "max_bytes", 24<<10, 1, 32<<10)
			if err != nil {
				return automation.Outcome{}, err
			}
			page, err := a.Workers.ReadResult(v.TurnID, v.WorkspaceID, offset, maxBytes)
			if err != nil {
				return automation.Outcome{}, err
			}
			return automation.Outcome{Data: page}, nil
		})

	automation.Register(r, automation.Definition{
		Name:        "workers_logs",
		Description: "Read bounded structured progress and tool activity logs for a worker turn",
		Role:        "read",
		Toolset:     automation.ToolsetCollab,
	}, func(v workerLogsInput) string { return v.TurnID },
		func(p automation.Principal, v workerLogsInput) error { return scopeWorkers(p, v.TurnID) },
		func(ctx context.Context, call automation.Call, v workerLogsInput) (automation.Outcome, error) {
			if a.Workers == nil {
				return automation.Outcome{}, automation.Fail("unavailable", "Workers service is unavailable")
			}
			limit, err := optionalIntRange(v.Limit, "limit", 50, 1, 200)
			if err != nil {
				return automation.Outcome{}, err
			}
			t, entries, hasMore, err := a.Workers.ReadLogs(v.TurnID, v.WorkspaceID, v.AfterSeq, limit)
			if err != nil {
				return automation.Outcome{}, err
			}
			return automation.Outcome{Data: workerLogsView{
				TurnID:   t.ID,
				WorkerID: t.WorkerID,
				State:    t.State,
				Entries:  entries,
				HasMore:  hasMore,
			}}, nil
		})

	automation.Register(r, automation.Definition{
		Name:        "workers_cancel",
		Description: "Cancel a specific active worker turn by explicit turn_id",
		Role:        "execute",
		Toolset:     automation.ToolsetCollab,
		Write:       true,
		Control:     true,
	}, func(v workerCancelInput) string { return v.TurnID },
		func(p automation.Principal, v workerCancelInput) error { return scopeWorkers(p, v.TurnID) },
		func(ctx context.Context, call automation.Call, v workerCancelInput) (automation.Outcome, error) {
			if a.Workers == nil {
				return automation.Outcome{}, automation.Fail("unavailable", "Workers service is unavailable")
			}
			t, err := a.Workers.Cancel(ctx, v.TurnID, v.WorkspaceID)
			if err != nil {
				return automation.Outcome{}, err
			}
			return automation.Outcome{
				Accepted: true,
				Data:     t,
				Summary:  map[string]any{"worker_id": t.WorkerID, "turn_id": t.ID, "state": t.State},
			}, nil
		})

	automation.Register(r, automation.Definition{
		Name:        "workers_close",
		Description: "Close and archive an idle worker and release its resident process",
		Role:        "execute",
		Toolset:     automation.ToolsetCollab,
		Write:       true,
	}, func(v workerCloseInput) string { return v.WorkerID },
		func(p automation.Principal, v workerCloseInput) error { return scopeWorkers(p, v.WorkerID) },
		func(ctx context.Context, call automation.Call, v workerCloseInput) (automation.Outcome, error) {
			if a.Workers == nil {
				return automation.Outcome{}, automation.Fail("unavailable", "Workers service is unavailable")
			}
			w, err := a.Workers.CloseWorker(ctx, v.WorkerID, v.WorkspaceID)
			if err != nil {
				return automation.Outcome{}, err
			}
			return automation.Outcome{
				Accepted: true,
				Data:     w,
				Summary:  map[string]any{"worker_id": w.ID, "state": w.State},
			}, nil
		})

	automation.Register(r, automation.Definition{
		Name:        "workers_resolve",
		Description: "Explicitly resolve a reconciliation_required turn with an audit reason without faking success",
		Role:        "execute",
		Toolset:     automation.ToolsetCollab,
		Write:       true,
		Control:     true,
	}, func(v workerResolveInput) string { return v.TurnID },
		func(p automation.Principal, v workerResolveInput) error { return scopeWorkers(p, v.TurnID) },
		func(ctx context.Context, call automation.Call, v workerResolveInput) (automation.Outcome, error) {
			if a.Workers == nil {
				return automation.Outcome{}, automation.Fail("unavailable", "Workers service is unavailable")
			}
			t, err := a.Workers.ResolveTurn(v.TurnID, call.Principal.ID, v.Reason)
			if err != nil {
				return automation.Outcome{}, err
			}
			return automation.Outcome{
				Accepted: true,
				Data:     t,
				Summary:  map[string]any{"worker_id": t.WorkerID, "turn_id": t.ID, "state": t.State},
			}, nil
		})
}

func describeWorkerAutomationResults(r *automation.Registry) {
	r.DescribeData("workers_create", automation.Schema[workers.Worker]())
	r.DescribeData("workers_list", resultObject("", "", "", map[string]any{"items": arrayOf(automation.Schema[workers.Worker]())}))
	r.DescribeData("workers_get", automation.Schema[workerGetView]())
	r.DescribeData("workers_send", automation.Schema[workers.Turn]())
	r.DescribeData("workers_wait", automation.Schema[workerWaitView]())
	r.DescribeData("workers_result", automation.Schema[workers.ResultPage]())
	r.DescribeData("workers_logs", automation.Schema[workerLogsView]())
	r.DescribeData("workers_cancel", automation.Schema[workers.Turn]())
	r.DescribeData("workers_close", automation.Schema[workers.Worker]())
	r.DescribeData("workers_resolve", automation.Schema[workers.Turn]())
}
