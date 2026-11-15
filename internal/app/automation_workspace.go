package app

import (
	"context"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/workspace"
)

func workspacePublicError(err error) error {
	if err == nil {
		return nil
	}
	return &automation.Error{Code: workspace.Code(err), Message: "Workspace operation failed", MessageKey: "err.workspace.operation", Args: map[string]string{"code": workspace.Code(err)}}
}

func (a *App) publicWorkspace(p automation.Principal, id string, requireEnabled bool) (*workspace.Service, error) {
	if requireEnabled && id == "" {
		return nil, automation.Fail("workspace_required", "An exact workspace_id is required")
	}
	files, err := a.Chat.Workspace()
	if err != nil {
		return nil, chatError(err)
	}
	if id != "" && files.ID() != id {
		return nil, automation.Fail("wrong_workspace", "Workspace identity changed")
	}
	if err = p.Require("workspaces", files.ID()); err != nil {
		return nil, err
	}
	if requireEnabled && !files.Status().Authorized {
		return nil, automation.Fail("not_authorized", "Workspace tools have not been enabled by the user")
	}
	return files, nil
}

func workspaceOutcome(op workspace.Operation, err error) (automation.Outcome, error) {
	return automation.Outcome{Data: op, Summary: map[string]string{"activity_id": op.ID, "state": op.State}, Accepted: op.ID != "", Unknown: op.State == "unknown" || op.State == "started"}, workspacePublicError(err)
}

func (a *App) registerAutomationWorkspace(r *automation.Registry) {
	for _, name := range []string{"workspaces.get", "conversations.capabilities"} {
		automation.Register(r, automation.Definition{Name: name, Description: "Read supported, authorized and ready workspace capabilities without starting the model", Role: "read"}, nil, nil,
			func(ctx context.Context, call automation.Call, v automation.WorkspaceInput) (automation.Outcome, error) {
				files, err := a.publicWorkspace(call.Principal, v.Workspace, false)
				if err != nil {
					return automation.Outcome{}, err
				}
				return automation.Outcome{Data: files.Status()}, nil
			})
	}
	automation.Register(r, automation.Definition{Name: "workspace_files.list", Description: "List one bounded page of workspace-relative entries", Role: "read"}, nil, nil,
		func(ctx context.Context, call automation.Call, v automation.WorkspaceList) (automation.Outcome, error) {
			files, err := a.publicWorkspace(call.Principal, v.Workspace, true)
			if err != nil {
				return automation.Outcome{}, err
			}
			page, err := files.SearchContext(ctx, v.Path, "", v.Cursor, false, false)
			return automation.Outcome{Data: page}, workspacePublicError(err)
		})
	automation.Register(r, automation.Definition{Name: "workspace_files.search", Description: "Find files by literal filename or content with bounded scanning and pagination", Role: "read"}, nil, nil,
		func(ctx context.Context, call automation.Call, v automation.WorkspaceSearch) (automation.Outcome, error) {
			files, err := a.publicWorkspace(call.Principal, v.Workspace, true)
			if err != nil {
				return automation.Outcome{}, err
			}
			page, err := files.SearchContext(ctx, v.Path, v.Query, v.Cursor, v.Content, true)
			return automation.Outcome{Data: page}, workspacePublicError(err)
		})
	automation.Register(r, automation.Definition{Name: "workspace_files.read", Description: "Read a bounded base64 fragment, checking the expected file revision", Role: "read"}, nil, nil,
		func(ctx context.Context, call automation.Call, v automation.WorkspaceRead) (automation.Outcome, error) {
			files, err := a.publicWorkspace(call.Principal, v.Workspace, true)
			if err != nil {
				return automation.Outcome{}, err
			}
			data, err := files.ReadContext(ctx, v.Path, v.Revision, v.Offset)
			return automation.Outcome{Data: data}, workspacePublicError(err)
		})
	automation.Register(r, automation.Definition{Name: "workspace_files.mutate", Description: "Create, replace, move or remove workspace text files with expected revisions and recoverable versions; a batch may partially complete", Role: "edit", Write: true, Batch: true}, func(v automation.WorkspaceMutate) string { return v.Workspace }, nil,
		func(ctx context.Context, call automation.Call, v automation.WorkspaceMutate) (automation.Outcome, error) {
			files, err := a.publicWorkspace(call.Principal, v.Workspace, true)
			if err != nil {
				return automation.Outcome{}, err
			}
			if err = call.Check(); err != nil {
				return automation.Outcome{}, err
			}
			op, err := files.Mutate(call.Identity.NativeID(), workspace.Scope{Policy: v.Policy}, v.Mutations)
			return workspaceOutcome(op, err)
		})
	automation.Register(r, automation.Definition{Name: "workspace_versions.list", Description: "Read saved before-images and actual outcomes for a workspace file", Role: "read"}, nil, nil,
		func(ctx context.Context, call automation.Call, v automation.WorkspaceVersions) (automation.Outcome, error) {
			files, err := a.publicWorkspace(call.Principal, v.Workspace, true)
			if err != nil {
				return automation.Outcome{}, err
			}
			items, next, err := files.Versions(v.Path, v.After)
			return automation.Outcome{Data: map[string]any{"items": items, "next": next}}, workspacePublicError(err)
		})
	automation.Register(r, automation.Definition{Name: "workspace_versions.restore", Description: "Restore a saved before-image after checking the current revision; records a new recoverable change", Role: "edit", Write: true, Batch: true}, func(v automation.WorkspaceRestore) string { return v.Workspace }, nil,
		func(ctx context.Context, call automation.Call, v automation.WorkspaceRestore) (automation.Outcome, error) {
			files, err := a.publicWorkspace(call.Principal, v.Workspace, true)
			if err != nil {
				return automation.Outcome{}, err
			}
			if err = call.Check(); err != nil {
				return automation.Outcome{}, err
			}
			op, err := files.Restore(call.Identity.NativeID(), workspace.Scope{Policy: v.Policy}, v.Version, v.Path, v.Revision)
			return workspaceOutcome(op, err)
		})
	a.registerAutomationActivities(r)
}
