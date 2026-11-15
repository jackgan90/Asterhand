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
	page := &wire.ToolActivityPage{Workspace: files.ID(), Authorized: files.Status().Authorized, Items: []wire.ToolActivity{}}
	if !page.Authorized {
		return page, nil
	}
	items, next := files.Activities(request.Conversation, turn, int64(request.Offset))
	for _, op := range items {
		state := op.State
		if state == "started" && op.Phase == workspace.PhaseQueued {
			state = "queued"
		}
		item := wire.ToolActivity{ID: op.ID, Turn: op.Scope.Turn, Tool: op.Tool, State: state, Sequence: op.Sequence}
		for _, step := range op.Progress {
			item.Progress = append(item.Progress, wire.ToolStep{Text: step.Text, Status: step.Status})
		}
		page.Items = append(page.Items, item)
	}
	page.Next = next
	return page, nil
}
