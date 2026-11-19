package conversation

import (
	"context"

	"github.com/jackgan90/Asterhand/internal/conversation/wire"
	"github.com/jackgan90/Asterhand/internal/workspace"
)

// WorkspaceRuntime is the optional capability contract. It is independent of
// runtime/provider names; ordinary text-only Runtime implementations need not
// expose files or permission management.
type WorkspaceRuntime interface {
	Workspace() (*workspace.Service, error)
	SetWorkspacePolicy(context.Context, bool, string) (workspace.Status, error)
}

// WorkspaceTrustRuntime extends WorkspaceRuntime with live trust mode/rule updates
// and user approval attention notifications without restarting the chat Gateway.
type WorkspaceTrustRuntime interface {
	SetWorkspaceTrustPolicy(ctx context.Context, executionMode, removeRuleID string) (workspace.Status, error)
	SetApprovalHandler(func())
}

func (s *Service) Workspace() (*workspace.Service, error) {
	provider, ok := s.runtime.(WorkspaceRuntime)
	if !ok {
		return nil, FailKey("unsupported", "err.workspace.unsupported")
	}
	return provider.Workspace()
}

func (s *Service) SetWorkspacePolicy(ctx context.Context, enabled bool, expected string) (workspace.Status, error) {
	provider, ok := s.runtime.(WorkspaceRuntime)
	if !ok {
		return workspace.Status{}, FailKey("unsupported", "err.workspace.unsupported")
	}
	release, err := s.BeginConfiguration()
	if err != nil {
		return workspace.Status{}, err
	}
	defer release()
	return provider.SetWorkspacePolicy(ctx, enabled, expected)
}

func (s *Service) SetWorkspaceTrustPolicy(ctx context.Context, executionMode, removeRuleID string) (workspace.Status, error) {
	provider, ok := s.runtime.(WorkspaceTrustRuntime)
	if !ok {
		return workspace.Status{}, FailKey("unsupported", "err.workspace.unsupported")
	}
	return provider.SetWorkspaceTrustPolicy(ctx, executionMode, removeRuleID)
}

func (s *Service) SetApprovalHandler(handler func()) {
	if provider, ok := s.runtime.(WorkspaceTrustRuntime); ok {
		provider.SetApprovalHandler(handler)
	}
}

func (s *Service) WorkspaceTurn(conversationID, operation string) (string, error) {
	if _, err := s.catalog.conversation(conversationID); err != nil {
		return "", err
	}
	if operation == "" {
		return "", nil
	}
	record, err := s.catalog.turn(operation)
	if err != nil || record.Turn.Conversation != conversationID {
		return "", FailKey("not_found", "err.conv.op_wrong_conversation")
	}
	return record.RuntimeRequest, nil
}

func (s *Service) activities(request Request) (*wire.ToolActivityPage, error) {
	turn, err := s.WorkspaceTurn(request.Conversation, request.Operation)
	if err != nil {
		return nil, err
	}
	files, err := s.Workspace()
	if err != nil {
		return nil, err
	}
	status := files.Status()
	page := &wire.ToolActivityPage{
		Workspace:     files.ID(),
		Authorized:    status.Authorized,
		ExecutionMode: status.ExecutionMode,
		Items:         []wire.ToolActivity{},
	}
	if !page.Authorized {
		return page, nil
	}
	items, next := files.Activities(request.Conversation, turn, int64(request.Offset))
	seen := make(map[string]bool, len(items))
	for _, op := range items {
		seen[op.ID] = true
		page.Items = append(page.Items, ProjectToolActivity(op))
	}
	for _, pendingOp := range files.PendingApprovals(request.Conversation, turn) {
		if !seen[pendingOp.ID] {
			seen[pendingOp.ID] = true
			page.Items = append(page.Items, ProjectToolActivity(pendingOp))
		}
	}
	page.Next = next
	return page, nil
}

func (s *Service) approveTool(request Request) (*wire.ToolActivityPage, error) {
	if _, err := s.catalog.conversation(request.Conversation); err != nil {
		return nil, err
	}
	files, err := s.Workspace()
	if err != nil {
		return nil, err
	}
	callID, err := files.ApproveToolCall(request.Conversation, request.ApprovalID, request.Decision)
	if err != nil {
		code := workspace.Code(err)
		return nil, FailKey(code, "err.workspace.operation", map[string]string{"code": code})
	}
	page, err := s.activities(Request{Conversation: request.Conversation, Operation: request.Operation})
	if err != nil || callID == "" {
		return page, err
	}
	for _, item := range page.Items {
		if item.ID == callID {
			return page, nil
		}
	}
	if op, opErr := files.Operation(callID); opErr == nil {
		page.Items = append(page.Items, ProjectToolActivity(op))
	}
	return page, nil
}

// ProjectToolActivity projects a workspace operation onto the sanitized wire
// activity view shared by live reconciliation and message history enrichment.
func ProjectToolActivity(op workspace.Operation) wire.ToolActivity {
	item := wire.ToolActivity{
		ID:           op.ID,
		Turn:         op.Scope.Turn,
		Tool:         op.Tool,
		State:        mapWorkspaceActivityState(op),
		Summary:      op.Summary,
		Sequence:     op.Sequence,
		ApprovalMode: op.ApprovalMode,
		Detail:       publicActivityDetail(op.Detail),
		Approval:     toWireToolApproval(op.Approval),
	}
	for _, step := range op.Progress {
		item.Progress = append(item.Progress, wire.ToolStep{Text: step.Text, Status: step.Status})
	}
	return item
}

func publicActivityDetail(detail string) string {
	switch detail {
	case "denied_by_user", "policy_denied", "approval_timeout", "approval_tampered",
		"elevation_verb_denied", "sensitive_path_denied", "broad_scope_denied", "undeclared_external_workdir":
		return detail
	default:
		return ""
	}
}

func mapWorkspaceActivityState(op workspace.Operation) string {
	if op.State == "started" {
		if op.Phase == workspace.PhaseQueued {
			return "queued"
		}
		if op.Phase == workspace.PhaseAwaitingApproval {
			return "awaiting_approval"
		}
		return "started"
	}
	if op.Detail == "denied_by_user" || op.Detail == "policy_denied" || op.ApprovalMode == "denied" || (op.Approval != nil && op.Approval.Status == "denied") {
		return "denied"
	}
	if op.Detail == "approval_timeout" || op.ApprovalMode == "timeout" || (op.Approval != nil && op.Approval.Status == "expired") {
		return "failed"
	}
	return op.State
}

func toWireToolApproval(a *workspace.ApprovalView) *wire.ToolApproval {
	if a == nil {
		return nil
	}
	return &wire.ToolApproval{
		ID:             a.ID,
		Status:         a.Status,
		Kind:           a.Kind,
		Target:         a.Target,
		Targets:        append([]string(nil), a.Targets...),
		Reason:         a.Reason,
		CommandPreview: a.CommandPreview,
		CanSession:     a.CanSession,
		CanAlways:      a.CanAlways,
	}
}
