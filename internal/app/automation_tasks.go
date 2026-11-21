package app

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/operations"
	"github.com/jackgan90/Asterhand/internal/store"
)

func projectScope(target, project string) string { return target + ":" + project }
func taskAccess(p automation.Principal, ref automation.TaskRef) error {
	return p.Require("projects", projectScope(ref.Target, ref.Project))
}
func taskError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrNotFound) {
		return automation.Fail("not_found", "Task is not available")
	}
	if errors.Is(err, store.ErrConflict) {
		return automation.Fail("conflict", "Task changed")
	}
	var rejection *model.TaskRejection
	if errors.As(err, &rejection) {
		return &automation.Error{Code: rejection.Code, Message: rejection.Message, MessageKey: rejection.MessageKey, Args: rejection.MessageArgs}
	}
	return err
}

func (a *App) registerAutomationTasks(r *automation.Registry) {
	automation.Register(r, automation.Definition{Name: "tasks_events", Description: "Read immutable task events with a bounded JSON fragment for an oversized event", Role: "read"}, func(v automation.TaskEvents) string { return projectScope(v.Task.Target, v.Task.Project) }, func(p automation.Principal, v automation.TaskEvents) error { return taskAccess(p, v.Task) }, a.automationTaskEvents)
	automation.Register(r, automation.Definition{Name: "tasks_submit", Description: "Submit a local or paired-node task through the existing interpretation and routing service", Role: "execute", Write: true, Batch: true}, func(v automation.TaskSubmit) string { return projectScope(v.Target, v.Project) }, func(p automation.Principal, v automation.TaskSubmit) error {
		return p.Require("projects", projectScope(v.Target, v.Project))
	}, a.automationTaskSubmit)
	automation.Register(r, automation.Definition{Name: "tasks_list", Description: "Read bounded task or outbox summaries visible to this subject", Role: "read"}, nil, nil, a.automationTaskList)
	automation.Register(r, automation.Definition{Name: "tasks_get", Description: "Read task metadata or a bounded slice of its goal, result, interaction or artifacts", Role: "read"}, func(v automation.TaskGet) string { return projectScope(v.Task.Target, v.Task.Project) }, func(p automation.Principal, v automation.TaskGet) error { return taskAccess(p, v.Task) }, a.automationTaskGet)
	automation.Register(r, automation.Definition{Name: "tasks_cancel", Description: "Request cancellation of the exact local job or original remote outbox request", Role: "execute", Write: true, Batch: true, Control: true}, func(v automation.TaskRef) string { return projectScope(v.Target, v.Project) }, taskAccess, a.automationTaskCancel)
	automation.Register(r, automation.Definition{Name: "tasks_answer", Description: "Answer an ordinary task question using its exact attempt and interaction identities", Role: "execute", Write: true, Batch: true, Control: true}, func(v automation.TaskAnswer) string { return projectScope(v.Task.Target, v.Task.Project) }, func(p automation.Principal, v automation.TaskAnswer) error { return taskAccess(p, v.Task) }, a.automationTaskAnswer)
	automation.Register(r, automation.Definition{Name: "tasks_reconcile", Description: "Refresh the original task or outbox state; never restart execution or redirect a request", Role: "read", Control: true}, func(v automation.TaskRef) string { return projectScope(v.Target, v.Project) }, taskAccess, a.automationTaskReconcile)
	automation.Register(r, automation.Definition{Name: "tasks_resolve", Description: "Close the unknown outcome of a local task with a reason, using the same action as the Web UI", Role: "execute", Write: true, Control: true}, func(v automation.TaskResolve) string { return projectScope(v.Task.Target, v.Task.Project) }, func(p automation.Principal, v automation.TaskResolve) error { return taskAccess(p, v.Task) }, a.automationTaskResolve)
}

func (a *App) automationTaskSubmit(ctx context.Context, call automation.Call, v automation.TaskSubmit) (automation.Outcome, error) {
	if v.Target == "" || v.Project == "" {
		return automation.Outcome{}, automation.Fail("invalid_parameters", "An explicit target node and project are required")
	}
	deadline := v.StartBefore
	if deadline == "" {
		created, _ := operations.IDTime(call.Identity.ID)
		deadline = created.Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	}
	req := model.Request{RequestID: call.Identity.NativeID(), ProjectID: v.Project, Goal: v.Goal, StartBefore: deadline, Mode: "route", Target: v.Target, TargetExplicit: true}
	if err := call.Check(); err != nil {
		return automation.Outcome{}, err
	}
	job, err := a.submitTask(req, "automation:"+call.Principal.ID)
	ref := map[string]any{"task_id": job.ID, "request_id": req.RequestID, "target_node_id": v.Target, "project_id": v.Project}
	return automation.Outcome{Data: taskSummary(job), Summary: ref, Accepted: job.ID != "", Unknown: job.ID != "" && err != nil}, taskError(err)
}

func taskTarget(j model.Job) string {
	if j.Request.Target != "" {
		return j.Request.Target
	}
	return j.Owner
}
func taskSummary(j model.Job) map[string]any {
	return map[string]any{"id": j.ID, "origin": j.Origin, "owner": j.Owner, "target_node_id": taskTarget(j), "project_id": j.Request.ProjectID, "request_id": j.Request.RequestID, "submitted_by": j.SubmittedBy, "state": j.State, "attempt": j.Attempt, "created": j.Created, "updated": j.Updated, "sequence": strconv.FormatInt(j.Seq, 10), "execution_outcome": j.Execution, "verification_outcome": j.Verification, "user_acceptance": j.Acceptance, "artifact_count": len(j.Artifacts), "interaction": interactionSummary(j.Interaction), "remote_id": j.RemoteID, "remote_owner": j.RemoteOwner}
}
func interactionSummary(i *model.Interaction) any {
	if i == nil {
		return nil
	}
	return map[string]string{"id": i.ID, "attempt": i.Attempt, "kind": i.Kind, "state": i.State, "expires": i.Expires, "answer_id": i.AnswerID, "answered_by": i.AnsweredBy}
}
func outboxSummary(o model.Outbox) map[string]any {
	result := map[string]any{"id": o.ID, "kind": "outbox", "target_node_id": o.PeerID, "project_id": o.Request.ProjectID, "state": o.State, "updated": o.Updated, "cancel_requested": o.Cancel, "request_id": o.Request.RequestID}
	if o.Job != nil {
		result["job"] = taskSummary(*o.Job)
	}
	return result
}

func (a *App) automationTaskList(ctx context.Context, call automation.Call, v automation.TaskList) (automation.Outcome, error) {
	kind := v.Kind
	if kind == "" {
		kind = "job"
	}
	if kind != "job" && kind != "outbox" {
		return automation.Outcome{}, automation.Fail("invalid_parameters", "Task kind must be job or outbox")
	}
	limit, err := automation.PageLimit(v.Limit)
	if err != nil {
		return automation.Outcome{}, err
	}
	limit = min(limit, 25)
	binding := []any{"tasks", kind, call.Principal.ID, call.Principal.Scopes["projects"]}
	cursor, err := automation.ParseCursor(v.Cursor, binding)
	if err != nil {
		return automation.Outcome{}, err
	}
	var before int64
	if cursor.After != "" {
		before, err = strconv.ParseInt(cursor.After, 10, 64)
		if err != nil {
			return automation.Outcome{}, automation.Fail("invalid_cursor", "Invalid task cursor")
		}
	}
	rows, next, err := a.Store.Page(kind, before, limit)
	if err != nil {
		return automation.Outcome{}, err
	}
	items := []any{}
	for _, row := range rows {
		if kind == "job" {
			var j model.Job
			if err = json.Unmarshal(row, &j); err != nil {
				return automation.Outcome{}, err
			}
			if call.Principal.Allows("projects", projectScope(taskTarget(j), j.Request.ProjectID)) {
				items = append(items, taskSummary(j))
			}
		} else {
			var o model.Outbox
			if err = json.Unmarshal(row, &o); err != nil {
				return automation.Outcome{}, err
			}
			if call.Principal.Allows("projects", projectScope(o.PeerID, o.Request.ProjectID)) {
				items = append(items, outboxSummary(o))
			}
		}
	}
	nextCursor := ""
	if next != 0 {
		nextCursor = automation.EncodeCursor(binding, "", strconv.FormatInt(next, 10))
	}
	return automation.Outcome{Data: map[string]any{"items": items, "next": nextCursor}}, nil
}

func (a *App) loadAutomationTask(p automation.Principal, ref automation.TaskRef) (model.Job, *model.Outbox, error) {
	if err := taskAccess(p, ref); err != nil {
		return model.Job{}, nil, err
	}
	if ref.Kind == "job" {
		var j model.Job
		if err := a.Store.Get("job", ref.ID, &j); err != nil {
			return j, nil, taskError(err)
		}
		if taskTarget(j) != ref.Target || j.Request.ProjectID != ref.Project {
			return model.Job{}, nil, automation.Fail("forbidden", "Task does not match the granted target and project")
		}
		return j, nil, nil
	}
	if ref.Kind == "outbox" {
		var o model.Outbox
		if err := a.Store.Get("outbox", ref.ID, &o); err != nil {
			return model.Job{}, nil, taskError(err)
		}
		if o.PeerID != ref.Target || o.Request.ProjectID != ref.Project {
			return model.Job{}, nil, automation.Fail("forbidden", "Outbox does not match the granted target and project")
		}
		j := model.Job{Request: o.Request, Owner: o.PeerID}
		if o.Job != nil {
			j = *o.Job
		}
		return j, &o, nil
	}
	return model.Job{}, nil, automation.Fail("invalid_parameters", "Task kind must be job or outbox")
}

func (a *App) automationTaskGet(ctx context.Context, call automation.Call, v automation.TaskGet) (automation.Outcome, error) {
	j, outbox, err := a.loadAutomationTask(call.Principal, v.Task)
	if err != nil {
		return automation.Outcome{}, err
	}
	part := v.Part
	if part == "" {
		part = "summary"
	}
	if part == "summary" {
		if v.Cursor != "" {
			return automation.Outcome{}, automation.Fail("invalid_cursor", "Summary has no cursor")
		}
		if outbox != nil {
			return automation.Outcome{Data: outboxSummary(*outbox)}, nil
		}
		return automation.Outcome{Data: taskSummary(j)}, nil
	}
	binding := []any{"task-content", v.Task, part, call.Principal.ID}
	cursor, err := automation.ParseCursor(v.Cursor, binding)
	if err != nil {
		return automation.Outcome{}, err
	}
	revision := strconv.FormatInt(j.Seq, 10)
	if outbox != nil {
		revision = outbox.Updated
	}
	if cursor.Revision != "" && cursor.Revision != revision {
		return automation.Outcome{}, automation.Fail("conflict", "Task content changed; read from the beginning")
	}
	offset := 0
	if cursor.After != "" {
		offset, err = strconv.Atoi(cursor.After)
		if err != nil || offset < 0 {
			return automation.Outcome{}, automation.Fail("invalid_cursor", "Invalid content offset")
		}
	}
	if part == "artifacts" {
		if offset > len(j.Artifacts) {
			return automation.Outcome{}, automation.Fail("invalid_cursor", "Artifact offset is beyond the list")
		}
		end := min(offset+8, len(j.Artifacts))
		next := ""
		if end < len(j.Artifacts) {
			next = automation.EncodeCursor(binding, revision, strconv.Itoa(end))
		}
		return automation.Outcome{Data: map[string]any{"items": j.Artifacts[offset:end], "next": next}}, nil
	}
	text := ""
	switch part {
	case "goal":
		text = j.Request.Goal
	case "result":
		text = j.Result
	case "error":
		text = j.Error
		if outbox != nil {
			text = outbox.Error
		}
	case "question":
		if j.Interaction != nil {
			text = j.Interaction.Question
		}
	case "answer":
		if j.Interaction != nil {
			text = j.Interaction.Answer
		}
	default:
		return automation.Outcome{}, automation.Fail("invalid_parameters", "Unknown task content part")
	}
	fragment, end, err := textFragment(text, offset, 8192)
	if err != nil {
		return automation.Outcome{}, err
	}
	next := ""
	if end < len(text) {
		next = automation.EncodeCursor(binding, revision, strconv.Itoa(end))
	}
	return automation.Outcome{Data: map[string]any{"text": fragment, "offset": strconv.Itoa(offset), "next": next, "complete": end == len(text)}}, nil
}

func textFragment(text string, offset, limit int) (string, int, error) {
	if offset < 0 || offset > len(text) || offset < len(text) && !utf8.RuneStart(text[offset]) {
		return "", 0, automation.Fail("invalid_cursor", "Text cursor is not a UTF-8 boundary")
	}
	end := min(offset+limit, len(text))
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[offset:end], end, nil
}
