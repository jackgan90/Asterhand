package automation

import "github.com/jackgan90/Asterhand/internal/workspace"

type WorkspaceInput struct {
	Workspace string `json:"workspace_id,omitempty"`
}
type WorkspaceList struct {
	Workspace string `json:"workspace_id"`
	Path      string `json:"path,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
}
type WorkspaceSearch struct {
	Workspace string `json:"workspace_id"`
	Path      string `json:"path,omitempty"`
	Query     string `json:"query"`
	Content   bool   `json:"content,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
}
type WorkspaceRead struct {
	Workspace string `json:"workspace_id"`
	Path      string `json:"path"`
	Revision  string `json:"expected_revision,omitempty"`
	Offset    int64  `json:"offset,omitempty"`
}
type WorkspaceMutate struct {
	Workspace string               `json:"workspace_id"`
	Policy    string               `json:"policy_revision"`
	Mutations []workspace.Mutation `json:"mutations"`
}
type WorkspaceVersions struct {
	Workspace string `json:"workspace_id"`
	Path      string `json:"path"`
	After     int64  `json:"after,omitempty"`
}
type WorkspaceRestore struct {
	Workspace string `json:"workspace_id"`
	Policy    string `json:"policy_revision"`
	Version   string `json:"version_id"`
	Path      string `json:"path"`
	Revision  string `json:"expected_revision"`
}
type WorkspaceActivities struct {
	Workspace    string `json:"workspace_id"`
	Conversation string `json:"conversation_id,omitempty"`
	Turn         string `json:"turn_operation_id,omitempty"`
	After        int64  `json:"after,omitempty"`
}
type WorkspaceObject struct {
	Workspace string `json:"workspace_id"`
	ID        string `json:"id"`
}
type WorkspaceResolve struct {
	Workspace string `json:"workspace_id"`
	ID        string `json:"id"`
	Evidence  string `json:"evidence"`
}
type ArtifactRead struct {
	Workspace string `json:"workspace_id"`
	ID        string `json:"id"`
	Offset    int64  `json:"offset,omitempty"`
}
