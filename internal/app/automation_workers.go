package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/harnesses"
	"github.com/jackgan90/Asterhand/internal/workers"
)

type workerCreateInput struct {
	HarnessID       string `json:"harness_id" enum:"codex,claude-code,deepseek-harness" description:"Target worker harness identifier: \"codex\", \"claude-code\", or \"deepseek-harness\""`
	WorkspaceRoot   string `json:"workspace_root" description:"Absolute path to the local project directory"`
	Alias           string `json:"alias,omitempty" description:"Optional stable worker alias unique within the project (e.g. \"codex-reviewer\")"`
	AccessMode      string `json:"access_mode,omitempty" enum:"read_only,workspace_write" description:"Worker access mode: \"read_only\" (default) or \"workspace_write\" (requires enabling workspace write for the target harness in Asterhand settings)"`
	ApprovalMode    string `json:"approval_mode,omitempty" enum:"auto,ask_human,strict_deny" description:"Approval policy for writable workers: \"auto\" (default: uses harness native auto-approve when supported, falls back to ask_human otherwise), \"ask_human\" (routes escalations to Asterhand human popup/Web), or \"strict_deny\""`
	RoleHint        string `json:"role_hint,omitempty" description:"Optional role instructions passed to the worker (e.g. reviewer persona)"`
	Model           string `json:"model,omitempty" description:"Optional default model name for this worker"`
	ReasoningEffort string `json:"reasoning_effort,omitempty" description:"Optional default reasoning effort / thinking level (e.g. off, low, medium, high, xhigh, max)"`
}

type workerListInput struct {
	WorkspaceRoot string                 `json:"workspace_root,omitempty" description:"Optional absolute project directory path to filter workers"`
	WorkspaceID   string                 `json:"workspace_id,omitempty" description:"Optional derived workspace_id to filter workers"`
	IncludeClosed bool                   `json:"include_closed,omitempty" description:"Include closed workers in results (default false)"`
	Cursor        string                 `json:"cursor,omitempty" description:"Optional pagination cursor returned as next_cursor by a previous workers_list call"`
	Limit         automation.OptionalInt `json:"limit,omitempty,omitzero" description:"Maximum number of workers to return (1..200, default 50)"`
}

type workerGetInput struct {
	WorkerID    string `json:"worker_id" description:"Worker identifier returned by workers_create or workers_list"`
	WorkspaceID string `json:"workspace_id,omitempty" description:"Optional expected workspace_id to guard against cross-project mismatch"`
}

type workerSendInput struct {
	WorkerID         string                 `json:"worker_id" description:"Target worker identifier returned by workers_create or workers_list"`
	WorkspaceID      string                 `json:"workspace_id,omitempty" description:"Optional expected workspace_id to guard against cross-project mismatch"`
	Prompt           string                 `json:"prompt" description:"Task instruction or review prompt for this turn"`
	Model            string                 `json:"model,omitempty" description:"Optional per-turn model override (defaults to worker model)"`
	ReasoningEffort  string                 `json:"reasoning_effort,omitempty" description:"Optional per-turn reasoning effort / thinking level override (e.g. off, low, medium, high, xhigh, max)"`
	ArtifactPaths    []string               `json:"artifact_paths,omitempty" description:"Optional list of project-relative or absolute file paths within the project to highlight"`
	GitRef           string                 `json:"git_ref,omitempty" description:"Optional git commit/branch reference for context"`
	DiffBase         string                 `json:"diff_base,omitempty" description:"Optional git diff base reference for review context"`
	OutputContract   string                 `json:"output_contract,omitempty" description:"Optional expected output structure or format constraint"`
	WallClockSeconds automation.OptionalInt `json:"wall_clock_seconds,omitempty,omitzero" description:"Optional per-turn wall-clock timeout in seconds (30..3600, default 1800)"`
}

type workerWaitInput struct {
	TurnID             string                   `json:"turn_id" description:"Turn identifier returned by workers_send"`
	WorkspaceID        string                   `json:"workspace_id,omitempty" description:"Optional expected workspace_id to guard against cross-project mismatch"`
	WaitSeconds        automation.OptionalInt   `json:"wait_seconds,omitempty,omitzero" description:"Seconds to wait for progress or completion (1..30, default 30)"`
	AfterCommentarySeq automation.OptionalInt64 `json:"after_commentary_seq,omitempty,omitzero" description:"Return only mid-turn commentary entries with seq greater than this value; omit for the latest 2 tail entries, or pass an explicit non-negative seq (including 0 or a previous delivered_commentary_seq when commentaries_truncated is true) to paginate buffered entries in ascending order"`
	WakeOnCommentary   bool                     `json:"wake_on_commentary,omitempty" description:"When true, return immediately whenever the worker emits a new user-facing commentary update instead of waiting until wait_seconds expires"`
	Verbose            bool                     `json:"verbose,omitempty" description:"When true, include the full nested turn metadata object (default false returns a compact view; use workers_get for full turn metadata)"`
}

type workerWaitAnyInput struct {
	TurnIDs     []string               `json:"turn_ids" description:"List of 1..16 turn identifiers returned by workers_send to monitor concurrently"`
	WorkspaceID string                 `json:"workspace_id,omitempty" description:"Optional expected workspace_id to guard against cross-project mismatch"`
	WaitSeconds automation.OptionalInt `json:"wait_seconds,omitempty,omitzero" description:"Seconds to wait until any watched turn reaches terminal/reconciliation state (1..30, default 30)"`
}

type workerResultInput struct {
	TurnID      string                 `json:"turn_id" description:"Turn identifier returned by workers_send"`
	WorkspaceID string                 `json:"workspace_id,omitempty" description:"Optional expected workspace_id to guard against cross-project mismatch"`
	Part        string                 `json:"part,omitempty" enum:"answer,prompt" description:"Which text to read: \"answer\" (default, the worker's final or partial output) or \"prompt\" (the caller's original prompt for this turn)"`
	Cursor      string                 `json:"cursor,omitempty" description:"Byte offset cursor returned by a previous truncated workers_wait or workers_result call (must align to a UTF-8 character boundary)"`
	MaxBytes    automation.OptionalInt `json:"max_bytes,omitempty,omitzero" description:"Maximum UTF-8 bytes to return per page (1..32768, default 24576)"`
}

type workerTurnsInput struct {
	WorkerID    string                 `json:"worker_id" description:"Worker identifier returned by workers_create or workers_list"`
	WorkspaceID string                 `json:"workspace_id,omitempty" description:"Optional expected workspace_id to guard against cross-project mismatch (must be passed unchanged across cursor pages when specified on the first page)"`
	Filter      string                 `json:"filter,omitempty" enum:"all,attention" description:"\"all\" (default) or \"attention\" to return only failed, cancelled, interrupted, reconciliation_required, blocked, or waiting turns"`
	Cursor      string                 `json:"cursor,omitempty" description:"Opaque cursor returned as next_cursor by a previous workers_turns call; pass with the same worker_id, workspace_id (if specified), and filter to read older turns"`
	Limit       automation.OptionalInt `json:"limit,omitempty,omitzero" description:"Maximum number of turns to return (1..50, default 20)"`
}

type workerTurnsView struct {
	WorkerID    string                 `json:"worker_id"`
	WorkspaceID string                 `json:"workspace_id,omitempty"`
	Filter      string                 `json:"filter,omitempty"`
	Cursor      string                 `json:"cursor,omitempty" description:"Echoes the request cursor passed for this page when paging older turns"`
	Items       []workers.TurnListItem `json:"items"`
	HasMore     bool                   `json:"has_more,omitempty" description:"True when older turns exist after this page"`
	NextCursor  string                 `json:"next_cursor,omitempty" description:"Opaque cursor bound to worker_id, workspace_id, and filter; pass with the same worker_id, workspace_id (if specified), and filter to read older turns"`
	Truncated   bool                   `json:"truncated,omitempty" description:"True when the page was shortened or an oversized item field was compacted to fit the response budget"`
	NextStep    string                 `json:"next_step,omitempty"`
}

type workerLogsInput struct {
	TurnID      string                 `json:"turn_id" description:"Turn identifier returned by workers_send"`
	WorkspaceID string                 `json:"workspace_id,omitempty" description:"Optional expected workspace_id to guard against cross-project mismatch"`
	AfterSeq    int64                  `json:"after_seq,omitempty" description:"Return log entries with seq greater than this value (cannot be combined with tail=true or before_seq)"`
	BeforeSeq   int64                  `json:"before_seq,omitempty" description:"Return up to limit log entries with seq less than this value in oldest-to-newest order (for paging backward from a tail window; cannot be combined with after_seq)"`
	Limit       automation.OptionalInt `json:"limit,omitempty,omitzero" description:"Maximum log entries to return (1..200, default 50)"`
	Tail        bool                   `json:"tail,omitempty" description:"When true, return the latest limit log entries (or entries before before_seq when specified, ordered oldest-to-newest); cannot be combined with after_seq"`
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

type workerSteerInput struct {
	TurnID      string `json:"turn_id" description:"Currently running turn_id returned by workers_send"`
	WorkspaceID string `json:"workspace_id,omitempty" description:"Optional expected workspace_id to guard against cross-project mismatch"`
	Text        string `json:"text" description:"Mid-turn steering guidance or correction to inject into the running turn"`
}

type workerSteerView struct {
	Turn         workers.Turn `json:"turn"`
	Steered      bool         `json:"steered"`
	DeliveryMode string       `json:"delivery_mode,omitempty"`
	NextStep     string       `json:"next_step,omitempty"`
}

type workerReplyInput struct {
	TurnID          string                 `json:"turn_id" description:"Turn identifier currently in waiting_input state"`
	RequestID       string                 `json:"request_id,omitempty" description:"Optional specific pending interaction request_id when multiple questions are queued in the same turn"`
	WorkspaceID     string                 `json:"workspace_id,omitempty" description:"Optional expected workspace_id to guard against cross-project mismatch"`
	Text            string                 `json:"text,omitempty" description:"Plain-text answer to the worker's business question (omit if using answers, defer_seconds, or escalate_to_human)"`
	Answers         map[string][]string    `json:"answers,omitempty" description:"Optional structured map of question_id to selected answer strings"`
	EscalateToHuman bool                   `json:"escalate_to_human,omitempty" description:"Set true when caller cannot decide and wants to escalate the question immediately to the human desktop/Web prompt"`
	DeferSeconds    automation.OptionalInt `json:"defer_seconds,omitempty,omitzero" description:"Defer automatic escalation to human UI by 30..300 seconds while caller verifies code or runs checks (repeatable; cannot combine with text/answers/escalate_to_human)"`
	Note            string                 `json:"note,omitempty" description:"Optional verification summary or decision context attached when deferring or escalating to the human UI"`
}

type workerReplyView struct {
	Turn       workers.Turn `json:"turn"`
	RequestID  string       `json:"request_id,omitempty"`
	Escalated  bool         `json:"escalated,omitempty"`
	Deferred   bool         `json:"deferred,omitempty"`
	EscalateAt *time.Time   `json:"escalate_at,omitempty"`
}

type workerGetView struct {
	Worker           workers.Worker       `json:"worker"`
	Turns            []workers.Turn       `json:"turns"`
	Summary          *workers.TurnSummary `json:"summary,omitempty" description:"Counts and durations aggregated over all retained turns of this worker"`
	TurnsHasMore     bool                 `json:"turns_has_more,omitempty" description:"True when the worker has more retained turns than the recent turns listed here; page through them with workers_turns"`
	HistoryTruncated bool                 `json:"history_truncated,omitempty" description:"True when turn history fields were compacted to fit the response budget"`
	NextStep         string               `json:"next_step,omitempty"`
}

type workerListView struct {
	Items      []workers.Worker `json:"items"`
	HasMore    bool             `json:"has_more,omitempty" description:"True when more matching workers exist after this page"`
	NextCursor string           `json:"next_cursor,omitempty" description:"Opaque pagination cursor; pass as cursor to workers_list to read the next page"`
	Truncated  bool             `json:"truncated,omitempty" description:"True when worker metadata was compacted or entries were split to fit the response budget"`
	NextStep   string           `json:"next_step,omitempty"`
}

type workerWaitView struct {
	Turn                   *workers.Turn             `json:"turn,omitempty"`
	WorkerID               string                    `json:"worker_id"`
	TurnID                 string                    `json:"turn_id"`
	State                  string                    `json:"state"`
	Progress               string                    `json:"progress,omitempty"`
	AnswerAvailable        bool                      `json:"answer_available"`
	AnswerSource           string                    `json:"answer_source,omitempty"`
	Answer                 string                    `json:"answer,omitempty"`
	PartialOutput          string                    `json:"partial_output,omitempty"`
	Truncated              bool                      `json:"truncated,omitempty"`
	Expired                bool                      `json:"expired,omitempty" description:"True when the completed turn's stored answer body has been evicted by retention or capacity limits while the summary remains readable"`
	ResultTruncated        bool                      `json:"result_truncated,omitempty" description:"True when the worker's raw final answer exceeded the 2 MiB storage cap and was truncated before persistence"`
	ResultBytes            int                       `json:"result_bytes,omitempty"`
	NextCursor             string                    `json:"next_cursor,omitempty"`
	ErrorCode              string                    `json:"error_code,omitempty"`
	ErrorMessage           string                    `json:"error_message,omitempty"`
	AfterVCS               *workers.VCSSnapshot      `json:"after_vcs,omitempty"`
	ElapsedSeconds         int                       `json:"elapsed_seconds,omitempty"`
	IdleSeconds            int                       `json:"idle_seconds,omitempty"`
	StreamEvents           int64                     `json:"stream_events,omitempty"`
	StreamBytes            int64                     `json:"stream_bytes,omitempty"`
	LatestCommentarySeq    int64                     `json:"latest_commentary_seq,omitempty" description:"Highest commentary sequence number generated by this turn"`
	DeliveredCommentarySeq *int64                    `json:"delivered_commentary_seq,omitempty" description:"Highest commentary sequence number delivered completely in this response; pass this value as after_commentary_seq to workers_wait to resume without skipping omitted or truncated commentaries"`
	CommentariesTruncated  bool                      `json:"commentaries_truncated,omitempty" description:"True when one or more requested commentaries were omitted or shortened to fit response budget"`
	Commentaries           []workers.CommentaryEntry `json:"commentaries,omitempty"`
	PendingInteraction     *workers.InteractionView  `json:"pending_interaction,omitempty"`
	NextStep               string                    `json:"next_step,omitempty"`
}

type workerWaitAnyView struct {
	TriggeredTurnID string                `json:"triggered_turn_id,omitempty"`
	Items           []workers.WaitAnyItem `json:"items"`
}

type workerLogsView struct {
	TurnID           string             `json:"turn_id"`
	WorkerID         string             `json:"worker_id"`
	State            string             `json:"state"`
	Entries          []workers.LogEntry `json:"entries"`
	HasMore          bool               `json:"has_more"`
	NextAfterSeq     int64              `json:"next_after_seq,omitempty" description:"Pass as after_seq to workers_logs (without tail=true) to read the next forward log page"`
	EarlierBeforeSeq int64              `json:"earlier_before_seq,omitempty" description:"When tail/before_seq omitted earlier entries, pass as before_seq (with tail=true) to page backward or use after_seq=0 to read forward from the start"`
	Truncated        bool               `json:"truncated,omitempty" description:"True when the page was shortened or a log entry summary was truncated to fit the response budget"`
	NextStep         string             `json:"next_step,omitempty"`
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
				OperationKey:    call.Identity.NativeID(),
				ActorID:         call.Principal.ID,
				HarnessID:       v.HarnessID,
				WorkspaceRoot:   v.WorkspaceRoot,
				Alias:           v.Alias,
				AccessMode:      v.AccessMode,
				ApprovalMode:    v.ApprovalMode,
				RoleHint:        v.RoleHint,
				Model:           v.Model,
				ReasoningEffort: v.ReasoningEffort,
			})
			if err != nil {
				return automation.Outcome{}, err
			}
			return automation.Outcome{
				Accepted: true,
				Data:     fitWorkerViewBudget(w),
				Summary: map[string]any{
					"worker_id":               w.ID,
					"workspace_id":            w.WorkspaceID,
					"alias":                   w.Alias,
					"state":                   w.State,
					"approval_mode":           w.ApprovalMode,
					"effective_approval_mode": w.EffectiveApprovalMode,
				},
			}, nil
		})

	automation.Register(r, automation.Definition{
		Name:        "workers_list",
		Description: "List coding harness workers, optionally filtered by project workspace_root or workspace_id, with cursor pagination",
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
			binding := []any{"workers_list", call.Principal.ID, v.WorkspaceRoot, v.WorkspaceID, v.IncludeClosed}
			cursor, err := automation.ParseCursor(v.Cursor, binding)
			if err != nil {
				return automation.Outcome{}, err
			}
			items, hasMore, rev, err := a.Workers.ListPage(v.WorkspaceRoot, v.WorkspaceID, v.IncludeClosed, cursor.Revision, cursor.After, limit)
			if err != nil {
				return automation.Outcome{}, err
			}
			return automation.Outcome{Data: buildWorkerListView(items, hasMore, binding, rev)}, nil
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
			summary, err := a.Workers.TurnSummary(w)
			if err != nil {
				return automation.Outcome{}, err
			}
			return automation.Outcome{Data: withWorkerGetSummary(buildWorkerGetView(w, turns), w.ID, v.WorkspaceID, summary)}, nil
		})

	automation.Register(r, automation.Definition{
		Name:        "workers_turns",
		Description: "Page through all retained turns of a worker, newest first, with turn number, state, prompt preview, timing, and result size; filter=attention lists only turns that failed, were cancelled or interrupted, need reconciliation, are blocked, or are waiting",
		Role:        "read",
		Toolset:     automation.ToolsetCollab,
	}, func(v workerTurnsInput) string { return v.WorkerID },
		func(p automation.Principal, v workerTurnsInput) error { return scopeWorkers(p, v.WorkerID) },
		func(ctx context.Context, call automation.Call, v workerTurnsInput) (automation.Outcome, error) {
			if a.Workers == nil {
				return automation.Outcome{}, automation.Fail("unavailable", "Workers service is unavailable")
			}
			limit, err := optionalIntRange(v.Limit, "limit", 20, 1, 50)
			if err != nil {
				return automation.Outcome{}, err
			}
			filter := strings.TrimSpace(v.Filter)
			if filter == "" {
				filter = workers.TurnFilterAll
			}
			binding := []any{"workers_turns", call.Principal.ID, v.WorkerID, v.WorkspaceID, filter}
			cursor, err := automation.ParseCursor(v.Cursor, binding)
			if err != nil {
				return automation.Outcome{}, err
			}
			items, hasMore, _, err := a.Workers.ListTurns(v.WorkerID, v.WorkspaceID, cursor.After, limit, filter)
			if err != nil {
				return automation.Outcome{}, err
			}
			return automation.Outcome{Data: buildWorkerTurnsView(v.WorkerID, v.WorkspaceID, filter, v.Cursor, items, hasMore, binding)}, nil
		})

	automation.Register(r, automation.Definition{
		Name:        "workers_send",
		Description: "Dispatch a new turn to an existing worker (optional per-turn model and reasoning_effort override) and return turn_id immediately; follow up with workers_wait(turn_id). At most one active workspace_write turn is allowed per physical worktree (concurrent writes return worktree_busy); read_only turns may run concurrently",
		Role:        "execute",
		Toolset:     automation.ToolsetCollab,
		Write:       true,
	}, func(v workerSendInput) string { return v.WorkerID },
		func(p automation.Principal, v workerSendInput) error { return scopeWorkers(p, v.WorkerID) },
		func(ctx context.Context, call automation.Call, v workerSendInput) (automation.Outcome, error) {
			if a.Workers == nil {
				return automation.Outcome{}, automation.Fail("unavailable", "Workers service is unavailable")
			}
			wallSec, err := optionalIntRange(v.WallClockSeconds, "wall_clock_seconds", 0, workers.MinWallClockSeconds, workers.MaxWallClockSeconds)
			if err != nil {
				return automation.Outcome{}, err
			}
			t, err := a.Workers.Send(ctx, workers.SendInput{
				OperationID:         call.Identity.ID,
				OperationKey:        call.Identity.NativeID(),
				ActorID:             call.Principal.ID,
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
				WallClockSeconds:    wallSec,
			})
			if err != nil {
				return automation.Outcome{}, err
			}
			return automation.Outcome{
				Accepted: true,
				Data:     fitTurnViewBudget(t, workerViewMaxPayloadJSONBytes),
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
			if v.AfterCommentarySeq.Set && v.AfterCommentarySeq.Value < 0 {
				return automation.Outcome{}, automation.Fail("invalid_parameters", "after_commentary_seq must be non-negative")
			}
			dur := time.Duration(waitSec) * time.Second
			res, err := a.Workers.WaitSnapshot(ctx, v.TurnID, v.WorkspaceID, workers.WaitOptions{
				WaitDuration:          dur,
				AfterCommentarySeq:    v.AfterCommentarySeq.Value,
				HasAfterCommentarySeq: v.AfterCommentarySeq.Set,
				WakeOnCommentary:      v.WakeOnCommentary,
			})
			if err != nil {
				return automation.Outcome{}, err
			}
			view := buildWorkerWaitView(res.Turn, res.Preview, res.Truncated, res.Expired, res.Commentaries, v.Verbose, v.AfterCommentarySeq.Set, v.AfterCommentarySeq.Value)
			return automation.Outcome{Data: view}, nil
		})

	automation.Register(r, automation.Definition{
		Name:        "workers_wait_any",
		Description: "Monitor up to 16 worker turns concurrently in one bounded wait (up to 30 seconds); returns immediately when any turn finishes or enters reconciliation, avoiding sequential wait overhead across multiple workers",
		Role:        "read",
		Toolset:     automation.ToolsetCollab,
		Wait:        true,
	}, func(v workerWaitAnyInput) string {
		if len(v.TurnIDs) == 1 {
			return v.TurnIDs[0]
		}
		return "*"
	},
		func(p automation.Principal, v workerWaitAnyInput) error { return scopeWorkers(p, "*") },
		func(ctx context.Context, call automation.Call, v workerWaitAnyInput) (automation.Outcome, error) {
			if a.Workers == nil {
				return automation.Outcome{}, automation.Fail("unavailable", "Workers service is unavailable")
			}
			waitSec, err := optionalIntRange(v.WaitSeconds, "wait_seconds", 30, 1, 30)
			if err != nil {
				return automation.Outcome{}, err
			}
			items, triggered, err := a.Workers.WaitAny(ctx, v.TurnIDs, v.WorkspaceID, time.Duration(waitSec)*time.Second)
			if err != nil {
				return automation.Outcome{}, err
			}
			return automation.Outcome{Data: workerWaitAnyView{
				TriggeredTurnID: triggered,
				Items:           items,
			}}, nil
		})

	automation.Register(r, automation.Definition{
		Name:        "workers_result",
		Description: "Read paginated final review/output text for a completed worker turn, or retained partial output (marked partial=true, answer_available=false) for an incomplete/non-success turn; pass part=\"prompt\" to read the caller's original prompt for the turn instead",
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
			var page workers.ResultPage
			switch strings.TrimSpace(v.Part) {
			case "", "answer":
				page, err = a.Workers.ReadResult(v.TurnID, v.WorkspaceID, offset, maxBytes)
			case "prompt":
				page, err = a.Workers.ReadPrompt(v.TurnID, v.WorkspaceID, offset, maxBytes)
			default:
				return automation.Outcome{}, automation.Fail("invalid_parameters", "part must be \"answer\" or \"prompt\"")
			}
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
			if v.BeforeSeq < 0 {
				return automation.Outcome{}, automation.Fail("invalid_parameters", "before_seq must be non-negative")
			}
			if (v.Tail || v.BeforeSeq > 0) && v.AfterSeq > 0 {
				return automation.Outcome{}, automation.Fail("invalid_parameters", "tail and before_seq cannot be combined with after_seq")
			}
			var (
				t        workers.Turn
				entries  []workers.LogEntry
				hasMore  bool
				backward = v.Tail || v.BeforeSeq > 0
			)
			if backward {
				t, entries, hasMore, err = a.Workers.ReadLogsBefore(v.TurnID, v.WorkspaceID, v.BeforeSeq, limit)
			} else {
				t, entries, hasMore, err = a.Workers.ReadLogs(v.TurnID, v.WorkspaceID, v.AfterSeq, limit)
			}
			if err != nil {
				return automation.Outcome{}, err
			}
			return automation.Outcome{Data: buildWorkerLogsView(t, entries, hasMore, backward)}, nil
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
				Data:     fitTurnViewBudget(t, workerViewMaxPayloadJSONBytes),
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
				Data:     fitWorkerViewBudget(w),
				Summary:  map[string]any{"worker_id": w.ID, "state": w.State},
			}, nil
		})

	automation.Register(r, automation.Definition{
		Name:        "workers_steer",
		Description: "Inject mid-turn guidance or corrections into an actively running worker turn for harnesses with supports_steer=true (Codex turn/steer RPC; Claude Code stream-json stdin applied at the next tool boundary); never cancels or restarts turns on unsupported harnesses",
		Role:        "execute",
		Toolset:     automation.ToolsetCollab,
		Write:       true,
		Control:     true,
	}, func(v workerSteerInput) string { return v.TurnID },
		func(p automation.Principal, v workerSteerInput) error { return scopeWorkers(p, v.TurnID) },
		func(ctx context.Context, call automation.Call, v workerSteerInput) (automation.Outcome, error) {
			if a.Workers == nil {
				return automation.Outcome{}, automation.Fail("unavailable", "Workers service is unavailable")
			}
			t, err := a.Workers.SteerTurn(ctx, workers.SteerInput{
				TurnID:      v.TurnID,
				WorkspaceID: v.WorkspaceID,
				Text:        v.Text,
			})
			if err != nil {
				return automation.Outcome{}, err
			}
			a.notifyWorkers()
			deliveryMode := "rpc"
			nextStep := fmt.Sprintf("Steering message injected into running turn. Continue calling workers_wait(turn_id=%q) to observe worker progress and response.", t.ID)
			if t.HarnessID == harnesses.HarnessClaudeCode {
				deliveryMode = "tool_boundary"
				nextStep = fmt.Sprintf("Steering message queued on Claude stream-json stdin (takes effect at the next tool boundary; if Claude has already entered final text generation with no remaining tool calls, check the final answer and follow up with workers_send if needed). Continue calling workers_wait(turn_id=%q).", t.ID)
			}
			return automation.Outcome{
				Accepted: true,
				Data:     fitSteerViewBudget(workerSteerView{Turn: t, Steered: true, DeliveryMode: deliveryMode, NextStep: nextStep}),
				Summary:  map[string]any{"worker_id": t.WorkerID, "turn_id": t.ID, "state": t.State, "steered": true, "delivery_mode": deliveryMode},
			}, nil
		})

	automation.Register(r, automation.Definition{
		Name:        "workers_reply",
		Description: "Answer a worker turn's waiting_input business question, defer human escalation (defer_seconds=30..300) while verifying code, or escalate to human UI with an optional verification note (cannot approve waiting_approval)",
		Role:        "execute",
		Toolset:     automation.ToolsetCollab,
		Write:       true,
		Control:     true,
	}, func(v workerReplyInput) string { return v.TurnID },
		func(p automation.Principal, v workerReplyInput) error { return scopeWorkers(p, v.TurnID) },
		func(ctx context.Context, call automation.Call, v workerReplyInput) (automation.Outcome, error) {
			if a.Workers == nil {
				return automation.Outcome{}, automation.Fail("unavailable", "Workers service is unavailable")
			}
			deferSec, err := optionalIntRange(v.DeferSeconds, "defer_seconds", 0, workers.MinDeferSeconds, workers.MaxDeferSeconds)
			if err != nil {
				return automation.Outcome{}, err
			}
			res, err := a.Workers.ReplyTurnWithReceipt(workers.ReplyInput{
				OperationID:     call.Identity.ID,
				TurnID:          v.TurnID,
				RequestID:       v.RequestID,
				WorkspaceID:     v.WorkspaceID,
				Text:            v.Text,
				Answers:         v.Answers,
				EscalateToHuman: v.EscalateToHuman,
				DeferSeconds:    deferSec,
				Note:            v.Note,
			})
			if err != nil {
				return automation.Outcome{}, err
			}
			summary := map[string]any{
				"worker_id":  res.Turn.WorkerID,
				"turn_id":    res.Turn.ID,
				"request_id": res.RequestID,
				"state":      res.Turn.State,
				"escalated":  res.Escalated,
				"deferred":   res.Deferred,
			}
			if res.EscalateAt != nil {
				summary["escalate_at"] = res.EscalateAt.UTC()
			}
			return automation.Outcome{
				Accepted: true,
				Data: fitReplyViewBudget(workerReplyView{
					Turn:       res.Turn,
					RequestID:  res.RequestID,
					Escalated:  res.Escalated,
					Deferred:   res.Deferred,
					EscalateAt: res.EscalateAt,
				}),
				Summary: summary,
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
				Data:     fitTurnViewBudget(t, workerViewMaxPayloadJSONBytes),
				Summary:  map[string]any{"worker_id": t.WorkerID, "turn_id": t.ID, "state": t.State},
			}, nil
		})
}

func workerWaitNextStep(t workers.Turn, truncated bool, nextCursor string, newCommentaries int) string {
	return workerWaitNextStepWithExpiry(t, truncated, nextCursor, newCommentaries, false)
}

func workerWaitNextStepWithExpiry(t workers.Turn, truncated bool, nextCursor string, newCommentaries int, expired bool) string {
	switch t.State {
	case workers.TurnRunning, workers.TurnPrepared:
		if newCommentaries > 0 {
			return fmt.Sprintf("Turn is still %s with %d commentary update(s) (latest_commentary_seq=%d). Review commentaries, optionally call workers_steer(turn_id=%q, text=...) if supported, and continue calling workers_wait(turn_id=%q, after_commentary_seq=%d).", t.State, newCommentaries, t.LatestCommentarySeq, t.ID, t.ID, t.LatestCommentarySeq)
		}
		if t.StreamEvents > 0 {
			return fmt.Sprintf("Turn is still %s (elapsed=%ds, idle=%ds, stream_events=%d). Continue calling workers_wait(turn_id=%q).", t.State, t.ElapsedSeconds, t.IdleSeconds, t.StreamEvents, t.ID)
		}
		return fmt.Sprintf("Turn is still %s. Continue calling workers_wait(turn_id=%q).", t.State, t.ID)
	case workers.TurnWaitingApproval:
		return fmt.Sprintf("Turn is waiting for human permission approval in Asterhand desktop/Web UI. Caller cannot approve on the user's behalf; do not cancel—inform the user if helpful and continue calling workers_wait(turn_id=%q).", t.ID)
	case workers.TurnWaitingInput:
		if t.PendingInteraction != nil && t.PendingInteraction.VisibleToHuman {
			return fmt.Sprintf("Turn is waiting for human input in Asterhand desktop/Web UI (already escalated). Continue calling workers_wait(turn_id=%q).", t.ID)
		}
		return fmt.Sprintf("Worker asked a business question (see pending_interaction.questions). Answer via workers_reply(turn_id=%q, text=...), defer escalation while verifying via workers_reply(turn_id=%q, defer_seconds=120), or escalate via workers_reply(turn_id=%q, escalate_to_human=true, note=...).", t.ID, t.ID, t.ID)
	case workers.TurnCompleted:
		if truncated {
			if nextCursor != "" {
				return fmt.Sprintf("Turn completed with truncated preview. Call workers_result(turn_id=%q, cursor=%q) to read the rest.", t.ID, nextCursor)
			}
			return fmt.Sprintf("Turn completed with truncated preview. Call workers_result(turn_id=%q) with cursor to read the rest.", t.ID)
		}
		if !t.AnswerAvailable {
			return fmt.Sprintf("Turn completed without a candidate final answer; inspect workers_logs(turn_id=%q).", t.ID)
		}
	case workers.TurnBlockedApproval, workers.TurnBlockedUserInput:
		return appendWaitPartialAndChangedFilesNote("Worker requested an unsupported interactive approval or user question and stopped cleanly; report the reason to the user.", t, expired)
	case workers.TurnInterrupted, workers.TurnCancelled, workers.TurnCancelledRevoked, workers.TurnFailed:
		return appendWaitPartialAndChangedFilesNote(fmt.Sprintf("Turn ended with non-success state %q; inspect workers_logs(turn_id=%q).", t.State, t.ID), t, expired)
	case workers.TurnReconcile:
		return fmt.Sprintf("Turn requires reconciliation before continuing; do not resend automatically. Inspect workers_get or workers_logs(turn_id=%q).", t.ID)
	}
	return ""
}

func appendWaitPartialAndChangedFilesNote(base string, t workers.Turn, expired bool) string {
	if !t.AnswerAvailable && t.ResultBytes > 0 && (t.ResultBytes > 4096 || t.ResultBytes > len(t.PartialOutput)) {
		if expired {
			base += fmt.Sprintf(" Stored full partial output (%d bytes) has expired under retention or capacity limits; only the 4 KiB partial_output preview remains.", t.ResultBytes)
		} else {
			base += fmt.Sprintf(" Retained partial output (%d bytes) exceeds preview; call workers_result(turn_id=%q, cursor=\"0\") to paginate the partial output (not a completed final answer).", t.ResultBytes, t.ID)
		}
	}
	return appendWaitChangedFilesNote(base, t)
}

func appendWaitChangedFilesNote(base string, t workers.Turn) string {
	if t.AfterVCS == nil || len(t.AfterVCS.ChangedFiles) == 0 {
		return base
	}
	var names []string
	for i, f := range t.AfterVCS.ChangedFiles {
		if i >= 8 {
			names = append(names, fmt.Sprintf("... (+%d more)", len(t.AfterVCS.ChangedFiles)-8))
			break
		}
		if f != "" {
			names = append(names, f)
		}
	}
	if len(names) == 0 {
		return base
	}
	return base + " Worktree currently has changed files on disk: " + strings.Join(names, ", ") + "."
}

func describeWorkerAutomationResults(r *automation.Registry) {
	r.DescribeData("workers_create", automation.Schema[workers.Worker]())
	r.DescribeData("workers_list", automation.Schema[workerListView]())
	r.DescribeData("workers_get", automation.Schema[workerGetView]())
	r.DescribeData("workers_turns", automation.Schema[workerTurnsView]())
	r.DescribeData("workers_send", automation.Schema[workers.Turn]())
	r.DescribeData("workers_wait", automation.Schema[workerWaitView]())
	r.DescribeData("workers_wait_any", automation.Schema[workerWaitAnyView]())
	r.DescribeData("workers_steer", automation.Schema[workerSteerView]())
	r.DescribeData("workers_reply", automation.Schema[workerReplyView]())
	r.DescribeData("workers_result", automation.Schema[workers.ResultPage]())
	r.DescribeData("workers_logs", automation.Schema[workerLogsView]())
	r.DescribeData("workers_cancel", automation.Schema[workers.Turn]())
	r.DescribeData("workers_close", automation.Schema[workers.Worker]())
	r.DescribeData("workers_resolve", automation.Schema[workers.Turn]())
}
