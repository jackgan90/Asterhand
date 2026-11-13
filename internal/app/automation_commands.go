package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/localcommands"
	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
)

func commandError(err error) error {
	var e *wire.Error
	if errors.As(err, &e) {
		return &automation.Error{Code: e.Code, Message: e.Error(), MessageKey: e.MessageKey, Args: e.MessageArgs}
	}
	return err
}

func (a *App) registerAutomationCommands(r *automation.Registry) {
	for _, name := range []string{"commands.list", "commands.search"} {
		automation.Register(r, automation.Definition{Name: name, Description: "Search permitted commands by name or alias; return bounded summaries", Role: "read"}, nil, nil, a.automationCommandList)
	}
	automation.Register(r, automation.Definition{Name: "commands.get", Description: "Read a complete command and its object revision; large objects support json_fragments", Role: "read", Large: true}, func(p automation.CommandGet) string { return p.ID }, func(p automation.Principal, v automation.CommandGet) error { return p.Require("commands", v.ID) },
		func(ctx context.Context, call automation.Call, v automation.CommandGet) (automation.Outcome, error) {
			c, err := a.Commands.Get(v.ID)
			if err != nil {
				return automation.Outcome{}, commandError(err)
			}
			return automationObject(c, v.Format, v.Cursor, []any{"command", call.Principal.ID, v.ID, c.Revision})
		})
	automation.Register(r, automation.Definition{Name: "commands.validate", Description: "Normalize and validate a candidate command and DAG without saving or executing it (missing script files on disk are allowed at save time; returns empty id/revision)", Role: "read", Large: true}, nil, func(p automation.Principal, v automation.CommandValidate) error { return p.Require("commands", v.ID) },
		func(ctx context.Context, call automation.Call, v automation.CommandValidate) (automation.Outcome, error) {
			c, err := a.Commands.Validate(v.Candidate.Command())
			return automation.Outcome{Data: c}, commandError(err)
		})
	automation.Register(r, automation.Definition{Name: "commands.create", Description: "Atomically create a command with a complete DAG; script file availability transitions asynchronously from checking to ready", Role: "edit", Write: true, Batch: true, Large: true}, nil, func(p automation.Principal, v automation.CommandInput) error { return p.Require("commands", "") },
		func(ctx context.Context, call automation.Call, v automation.CommandInput) (automation.Outcome, error) {
			return a.saveAutomationCommand(call, v.Command(), "")
		})
	automation.Register(r, automation.Definition{Name: "commands.patch", Description: "Atomically apply command metadata and full-graph or step-level edits against expected_revision (rejected while any instance is running)", Role: "edit", Write: true, Batch: true}, func(v automation.CommandPatch) string { return v.ID }, func(p automation.Principal, v automation.CommandPatch) error { return p.Require("commands", v.ID) }, a.automationCommandPatch)
	automation.Register(r, automation.Definition{Name: "commands.clone", Description: "Copy configuration with new command and step identities", Role: "edit", Write: true, Batch: true}, func(v automation.CommandClone) string { return v.ID }, func(p automation.Principal, v automation.CommandClone) error {
		if err := p.Require("commands", v.ID); err != nil {
			return err
		}
		return p.Require("commands", "")
	}, a.automationCommandClone)
	automation.Register(r, automation.Definition{Name: "commands.delete", Description: "Delete a command and removable detailed run data; preserve operation receipts", Role: "edit", Write: true, Batch: true}, func(v automation.CommandVersion) string { return v.ID }, func(p automation.Principal, v automation.CommandVersion) error { return p.Require("commands", v.ID) },
		func(ctx context.Context, call automation.Call, v automation.CommandVersion) (automation.Outcome, error) {
			if err := call.Check(); err != nil {
				return automation.Outcome{}, err
			}
			ref, err := a.Commands.DeleteOperation(v.ID, v.Revision, call.Identity)
			return automation.Outcome{Data: ref, Summary: ref, Accepted: ref.ID != "", Unknown: ref.ID != "" && err != nil}, commandError(err)
		})
	automation.Register(r, automation.Definition{Name: "commands.run", Description: "Start one graph instance at expected_revision and return its run_id; Windows runs each script step non-interactively in a non-breakaway Job object (holds 1 of 8 node slots until all child processes exit)", Role: "execute", Write: true, Batch: true}, func(v automation.CommandVersion) string { return v.ID }, func(p automation.Principal, v automation.CommandVersion) error { return p.Require("commands", v.ID) },
		func(ctx context.Context, call automation.Call, v automation.CommandVersion) (automation.Outcome, error) {
			record, err := a.Commands.LaunchOperation(ctx, v.ID, v.Revision, &call.Identity, call.Check)
			if record == nil {
				return automation.Outcome{}, commandError(err)
			}
			ref := localcommands.RunReference{CommandID: v.ID, RunID: record.ID}
			return automation.Outcome{Data: automation.RunFrom(record), Summary: ref, Accepted: true, Unknown: err != nil}, commandError(err)
		})
}

func (a *App) automationCommandList(ctx context.Context, call automation.Call, v automation.CommandList) (automation.Outcome, error) {
	limit, err := automation.PageLimit(v.Limit)
	if err != nil {
		return automation.Outcome{}, err
	}
	query := strings.TrimPrefix(strings.TrimSpace(v.Query), "/")
	binding := []any{"commands", call.Principal.ID, call.Principal.Scopes["commands"], query}
	cursor, err := automation.ParseCursor(v.Cursor, binding)
	if err != nil {
		return automation.Outcome{}, err
	}
	page, err := a.Commands.Page(query, cursor.Revision, cursor.After, limit, func(id string) bool { return call.Principal.Allows("commands", id) })
	page.Next = automation.EncodeCursor(binding, page.Revision, page.Next)
	return automation.Outcome{Data: page}, commandError(err)
}

func (a *App) saveAutomationCommand(call automation.Call, c localcommands.Command, revision string) (automation.Outcome, error) {
	if err := call.Check(); err != nil {
		return automation.Outcome{}, err
	}
	ref, err := a.Commands.SaveOperation(c, revision, call.Identity)
	return automation.Outcome{Data: ref, Summary: ref, Accepted: ref.ID != "", Unknown: ref.ID != "" && err != nil}, commandError(err)
}

func (a *App) automationCommandPatch(ctx context.Context, call automation.Call, v automation.CommandPatch) (automation.Outcome, error) {
	c, err := a.Commands.Get(v.ID)
	if err != nil {
		return automation.Outcome{}, commandError(err)
	}
	if c.Revision != v.Revision {
		return automation.Outcome{}, automation.Fail("conflict", "Command revision changed")
	}
	c, err = automation.PatchCommand(c, v)
	if err != nil {
		return automation.Outcome{}, err
	}
	return a.saveAutomationCommand(call, c, v.Revision)
}

func (a *App) automationCommandClone(ctx context.Context, call automation.Call, v automation.CommandClone) (automation.Outcome, error) {
	c, err := a.Commands.Get(v.ID)
	if err != nil {
		return automation.Outcome{}, commandError(err)
	}
	if c.Revision != v.Revision {
		return automation.Outcome{}, automation.Fail("conflict", "Source command revision changed")
	}
	c = automation.CloneCommand(c, v.Name, v.Aliases)
	return a.saveAutomationCommand(call, c, "")
}

func (a *App) registerAutomationRuns(r *automation.Registry) {
	automation.Register(r, automation.Definition{Name: "runs.get", Description: "Read the exact graph instance and its steps", Role: "read"}, func(v automation.RunID) string { return v.CommandID }, func(p automation.Principal, v automation.RunID) error { return p.Require("commands", v.CommandID) },
		func(ctx context.Context, call automation.Call, v automation.RunID) (automation.Outcome, error) {
			return a.automationRun(v)
		})
	automation.Register(r, automation.Definition{Name: "runs.stop", Description: "Request stopping only the explicit instance IDs; later runs are unaffected", Role: "execute", Write: true, Batch: true, Control: true}, func(v automation.RunStop) string { return v.CommandID }, func(p automation.Principal, v automation.RunStop) error { return p.Require("commands", v.CommandID) },
		func(ctx context.Context, call automation.Call, v automation.RunStop) (automation.Outcome, error) {
			if err := call.Check(); err != nil {
				return automation.Outcome{}, err
			}
			err := a.Commands.StopRuns(v.CommandID, v.IDs)
			summary := map[string]any{"command_id": v.CommandID, "run_ids": v.IDs, "stop_requested": err == nil}
			return automation.Outcome{Data: summary, Summary: summary, Accepted: err == nil, Unknown: err != nil}, commandError(err)
		})
	automation.Register(r, automation.Definition{Name: "runs.wait", Description: "Observe one run for at most 30 seconds; cancelling observation does not stop it", Role: "read", Wait: true}, func(v automation.RunWait) string { return v.CommandID }, func(p automation.Principal, v automation.RunWait) error { return p.Require("commands", v.CommandID) }, a.automationRunWait)
}

func (a *App) automationRun(v automation.RunID) (automation.Outcome, error) {
	if v.ID == "" {
		return automation.Outcome{}, automation.Fail("invalid_parameters", "Explicit run_id is required")
	}
	run := a.Commands.Run(v.CommandID, v.ID)
	if run == nil {
		return automation.Outcome{}, automation.Fail("not_found", "Run is not retained")
	}
	return automation.Outcome{Data: automation.RunFrom(run)}, nil
}

func (a *App) automationRunWait(ctx context.Context, call automation.Call, v automation.RunWait) (automation.Outcome, error) {
	if v.Seconds < 0 || v.Seconds > 30 {
		return automation.Outcome{}, automation.Fail("invalid_parameters", "Wait must be between 0 and 30 seconds")
	}
	seconds := v.Seconds
	if seconds == 0 {
		seconds = 30
	}
	deadline := time.NewTimer(time.Duration(seconds) * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := call.Check(); err != nil {
			return automation.Outcome{}, err
		}
		outcome, err := a.automationRun(automation.RunID{CommandID: v.CommandID, ID: v.ID})
		if err != nil {
			return outcome, err
		}
		if outcome.Data.(automation.RunView).Phase == "finished" {
			return outcome, nil
		}
		select {
		case <-ctx.Done():
			return automation.Outcome{}, ctx.Err()
		case <-deadline.C:
			return outcome, nil
		case <-ticker.C:
		}
	}
}
