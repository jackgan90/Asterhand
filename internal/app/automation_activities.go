package app

import (
	"context"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/workspace"
)

func (a *App) workspaceActivityFilter(p automation.Principal, v automation.WorkspaceActivities) (string, error) {
	if v.After < 0 {
		return "", automation.Fail("invalid_cursor", "Negative activity sequence")
	}
	if err := p.Require("conversations", v.Conversation); err != nil {
		return "", err
	}
	if v.Conversation == "" {
		if v.Turn != "" {
			return "", automation.Fail("invalid_parameters", "A conversation is required for a turn")
		}
		return "", nil
	}
	return a.Chat.WorkspaceTurn(v.Conversation, v.Turn)
}

func activityAccess(p automation.Principal, op workspace.Operation) error {
	if op.Scope.Conversation != "" {
		return p.Require("conversations", op.Scope.Conversation)
	}
	return nil
}

func (a *App) registerAutomationActivities(r *automation.Registry) {
	automation.Register(r, automation.Definition{Name: "turn_activities_list", Description: "Read bounded durable tool metadata and model-declared progress after a sequence", Role: "read"}, nil, nil,
		func(ctx context.Context, call automation.Call, v automation.WorkspaceActivities) (automation.Outcome, error) {
			files, err := a.publicWorkspace(call.Principal, v.Workspace, true)
			if err != nil {
				return automation.Outcome{}, err
			}
			turn, err := a.workspaceActivityFilter(call.Principal, v)
			if err != nil {
				return automation.Outcome{}, err
			}
			items, next := files.Activities(v.Conversation, turn, v.After)
			return automation.Outcome{Data: map[string]any{"items": items, "next": next}}, nil
		})
	for _, name := range []string{"turn_activities_get", "workspace_versions_get", "workspace_operations_get"} {
		automation.Register(r, automation.Definition{Name: name, Description: "Read one durable operation and its actual per-file outcomes without file contents", Role: "read"}, nil, nil,
			func(ctx context.Context, call automation.Call, v automation.WorkspaceObject) (automation.Outcome, error) {
				files, err := a.publicWorkspace(call.Principal, v.Workspace, true)
				if err != nil {
					return automation.Outcome{}, err
				}
				op, err := files.Operation(v.ID)
				if err != nil {
					return automation.Outcome{}, workspacePublicError(err)
				}
				if err = activityAccess(call.Principal, op); err != nil {
					return automation.Outcome{}, err
				}
				return automation.Outcome{Data: op}, nil
			})
	}
	automation.Register(r, automation.Definition{Name: "workspace_operations_resolve", Description: "Record explicit reconciliation evidence for an unknown operation after its owned runtime stopped; does not replay or restore files", Role: "edit", Write: true, Control: true}, func(v automation.WorkspaceResolve) string { return v.Workspace }, nil,
		func(ctx context.Context, call automation.Call, v automation.WorkspaceResolve) (automation.Outcome, error) {
			files, err := a.publicWorkspace(call.Principal, v.Workspace, true)
			if err != nil {
				return automation.Outcome{}, err
			}
			op, err := files.Operation(v.ID)
			if err != nil {
				return automation.Outcome{}, workspacePublicError(err)
			}
			if err = activityAccess(call.Principal, op); err != nil {
				return automation.Outcome{}, err
			}
			if err = call.Check(); err != nil {
				return automation.Outcome{}, err
			}
			op, err = files.ResolveRequest(call.Identity.NativeID(), v.ID, v.Evidence)
			return workspaceOutcome(op, err)
		})
	automation.Register(r, automation.Definition{Name: "artifacts_list", Description: "List stable references to changed files, bound to the operation and content revision", Role: "read"}, nil, nil,
		func(ctx context.Context, call automation.Call, v automation.WorkspaceActivities) (automation.Outcome, error) {
			files, err := a.publicWorkspace(call.Principal, v.Workspace, true)
			if err != nil {
				return automation.Outcome{}, err
			}
			turn, err := a.workspaceActivityFilter(call.Principal, v)
			if err != nil {
				return automation.Outcome{}, err
			}
			items, next := files.Artifacts(turn, v.After)
			filtered := []workspace.Artifact{}
			for _, item := range items {
				if (v.Conversation == "" || v.Conversation == item.Scope.Conversation) && call.Principal.Allows("conversations", item.Scope.Conversation) {
					filtered = append(filtered, item)
				}
			}
			return automation.Outcome{Data: map[string]any{"items": filtered, "next": next}}, nil
		})
	for _, name := range []string{"artifacts_get", "artifacts_read"} {
		automation.Register(r, automation.Definition{Name: name, Description: "Inspect or read a bounded fragment of an artifact after revalidating authorization and its saved revision", Role: "read"}, nil, nil,
			func(ctx context.Context, call automation.Call, v automation.ArtifactRead) (automation.Outcome, error) {
				files, err := a.publicWorkspace(call.Principal, v.Workspace, true)
				if err != nil {
					return automation.Outcome{}, err
				}
				item, err := files.Artifact(v.ID)
				if err != nil {
					return automation.Outcome{}, workspacePublicError(err)
				}
				if item.Scope.Conversation != "" {
					if err = call.Principal.Require("conversations", item.Scope.Conversation); err != nil {
						return automation.Outcome{}, err
					}
				}
				fragment, err := files.ReadContext(ctx, item.Path, item.Revision, v.Offset)
				if err != nil {
					return automation.Outcome{}, workspacePublicError(err)
				}
				if name == "artifacts_get" {
					return automation.Outcome{Data: item}, nil
				}
				return automation.Outcome{Data: fragment}, nil
			})
	}
}
