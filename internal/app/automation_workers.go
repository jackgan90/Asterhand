package app

import (
	"context"
	"strconv"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/workers"
)

type workerCreateInput struct {
	HarnessID       string `json:"harness_id" description:"Target worker harness identifier (e.g. \"codex\")"`
	WorkspaceRoot   string `json:"workspace_root" description:"Absolute path to the local project directory"`
	Alias           string `json:"alias,omitempty" description:"Optional stable worker alias unique within the project (e.g. \"codex-reviewer\")"`
	AccessMode      string `json:"access_mode,omitempty" description:"Worker access mode: \"read_only\" (default)"`
	RoleHint        string `json:"role_hint,omitempty" description:"Optional role instructions passed to the worker (e.g. reviewer persona)"`
	Model           string `json:"model,omitempty" description:"Optional default model name for this worker"`
	ReasoningEffort string `json:"reasoning_effort,omitempty" description:"Optional default reasoning effort / thinking level (e.g. low, medium, high, xhigh)"`
}

type workerListInput struct {
	WorkspaceRoot string `json:"workspace_root,omitempty" description:"Optional absolute project directory path to filter workers"`
	WorkspaceID   string `json:"workspace_id,omitempty" description:"Optional derived workspace_id to filter workers"`
	IncludeClosed bool   `json:"include_closed,omitempty" description:"Include closed workers in results (default false)"`
	Limit         int    `json:"limit,omitempty" description:"Maximum number of workers to return (default 50)"`
}

type workerGetInput struct {
	WorkerID    string `json:"worker_id" description:"Worker identifier returned by workers.create or workers.list"`
	WorkspaceID string `json:"workspace_id,omitempty" description:"Optional expected workspace_id to guard against cross-project mismatch"`
}

type workerSendInput struct {
	WorkerID        string   `json:"worker_id" description:"Target worker identifier returned by workers.create or workers.list"`
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
	TurnID      string `json:"turn_id" description:"Turn identifier returned by workers.send"`
	WorkspaceID string `json:"workspace_id,omitempty" description:"Optional expected workspace_id to guard against cross-project mismatch"`
	WaitSeconds int    `json:"wait_seconds,omitempty" description:"Seconds to wait for progress or completion (1..30, default 30)"`
}

type workerResultInput struct {
	TurnID      string `json:"turn_id" description:"Turn identifier returned by workers.send"`
	WorkspaceID string `json:"workspace_id,omitempty" description:"Optional expected workspace_id to guard against cross-project mismatch"`
	Cursor      string `json:"cursor,omitempty" description:"Byte offset cursor returned by a previous truncated workers.wait or workers.result call"`
	MaxBytes    int    `json:"max_bytes,omitempty" description:"Maximum UTF-8 bytes to return per page (up to 32768, default 24576)"`
}

type workerLogsInput struct {
	TurnID      string `json:"turn_id" description:"Turn identifier returned by workers.send"`
	WorkspaceID string `json:"workspace_id,omitempty" description:"Optional expected workspace_id to guard against cross-project mismatch"`
	AfterSeq    int64  `json:"after_seq,omitempty" description:"Return log entries with seq greater than this value"`
	Limit       int    `json:"limit,omitempty" description:"Maximum log entries to return (default 50)"`
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

func (a *App) registerAutomationWorkers(r *automation.Registry) {
	scopeWorkers := func(p automation.Principal, target string) error {
		return p.Require("workers", target)
	}

	automation.Register(r, automation.Definition{
		Name:        "workers.create",
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
		Name:        "workers.list",
		Description: "List coding harness workers, optionally filtered by project workspace_root or workspace_id",
		Role:        "read",
		Toolset:     automation.ToolsetCollab,
	}, nil,
		func(p automation.Principal, _ workerListInput) error { return scopeWorkers(p, "*") },
		func(ctx context.Context, call automation.Call, v workerListInput) (automation.Outcome, error) {
			if a.Workers == nil {
				return automation.Outcome{}, automation.Fail("unavailable", "Workers service is unavailable")
			}
			items, err := a.Workers.List(v.WorkspaceRoot, v.WorkspaceID, v.IncludeClosed, v.Limit)
			if err != nil {
				return automation.Outcome{}, err
			}
			return automation.Outcome{Data: map[string]any{"items": items}}, nil
		})

	automation.Register(r, automation.Definition{
		Name:        "workers.get",
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
		Name:        "workers.send",
		Description: "Dispatch a new turn to an existing worker (optional per-turn model and reasoning_effort override) and return turn_id immediately; follow up with workers.wait(turn_id)",
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
		Name:        "workers.wait",
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
			dur, err := boundedWait(v.WaitSeconds)
			if err != nil {
				return automation.Outcome{}, err
			}
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
		Name:        "workers.result",
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
			page, err := a.Workers.ReadResult(v.TurnID, v.WorkspaceID, offset, v.MaxBytes)
			if err != nil {
				return automation.Outcome{}, err
			}
			return automation.Outcome{Data: page}, nil
		})

	automation.Register(r, automation.Definition{
		Name:        "workers.logs",
		Description: "Read bounded structured progress and tool activity logs for a worker turn",
		Role:        "read",
		Toolset:     automation.ToolsetCollab,
	}, func(v workerLogsInput) string { return v.TurnID },
		func(p automation.Principal, v workerLogsInput) error { return scopeWorkers(p, v.TurnID) },
		func(ctx context.Context, call automation.Call, v workerLogsInput) (automation.Outcome, error) {
			if a.Workers == nil {
				return automation.Outcome{}, automation.Fail("unavailable", "Workers service is unavailable")
			}
			t, entries, hasMore, err := a.Workers.ReadLogs(v.TurnID, v.WorkspaceID, v.AfterSeq, v.Limit)
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
		Name:        "workers.cancel",
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
		Name:        "workers.close",
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
		Name:        "workers.resolve",
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
	r.DescribeData("workers.create", automation.Schema[workers.Worker]())
	r.DescribeData("workers.list", resultObject("", "", "", map[string]any{"items": arrayOf(automation.Schema[workers.Worker]())}))
	r.DescribeData("workers.get", automation.Schema[workerGetView]())
	r.DescribeData("workers.send", automation.Schema[workers.Turn]())
	r.DescribeData("workers.wait", automation.Schema[workerWaitView]())
	r.DescribeData("workers.result", automation.Schema[workers.ResultPage]())
	r.DescribeData("workers.logs", automation.Schema[workerLogsView]())
	r.DescribeData("workers.cancel", automation.Schema[workers.Turn]())
	r.DescribeData("workers.close", automation.Schema[workers.Worker]())
	r.DescribeData("workers.resolve", automation.Schema[workers.Turn]())
}
