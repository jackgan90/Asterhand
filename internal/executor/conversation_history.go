package executor

import (
	"encoding/json"

	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/workspace"
)

// The native transcript determines display order. The workspace journal may
// enrich that same call with an observed result, never add or reorder messages.
func enrichHistoryTools(page *conversation.Page, conversationID string, files *workspace.Service) {
	encoded, err := json.Marshal(page)
	if err != nil {
		return
	}
	// Leave space for the enclosing conversation and automation response.
	remaining := conversation.PageBytes - len(encoded) - 4096
	for i := range page.Messages {
		message := &page.Messages[i]
		for j := range message.Blocks {
			block := &message.Blocks[j]
			if block.Type != "tool" || block.Tool == nil || message.Turn == "" {
				continue
			}
			op, err := files.Operation(block.Tool.ID)
			if err != nil || op.Scope.Conversation != conversationID || op.Scope.Turn != message.Turn || op.Tool != block.Tool.Tool {
				continue
			}
			projected := conversation.ProjectToolActivity(op)
			activity := *block.Tool
			// A failed native callback must not become successful just because its
			// file operation finished before the callback itself failed.
			if !(activity.State == "failed" && projected.State == "completed") {
				activity.State = projected.State
			}
			activity.Sequence = projected.Sequence
			if activity.Summary == "" && projected.Summary != "" {
				activity.Summary = projected.Summary
			}
			activity.ApprovalMode = projected.ApprovalMode
			activity.Detail = projected.Detail
			activity.Approval = projected.Approval
			activity.Progress = projected.Progress
			before, _ := json.Marshal(block.Tool)
			after, _ := json.Marshal(activity)
			if len(after)-len(before) > remaining && activity.Approval != nil && activity.Approval.CommandPreview != "" {
				activity.Approval.CommandPreview = ""
				after, _ = json.Marshal(activity)
			}
			if len(after)-len(before) > remaining {
				activity.Progress = nil
				after, _ = json.Marshal(activity)
			}
			if len(after)-len(before) > remaining {
				activity.Approval = nil
				after, _ = json.Marshal(activity)
			}
			if len(after)-len(before) > remaining {
				activity.ApprovalMode = ""
				activity.Detail = ""
				after, _ = json.Marshal(activity)
			}
			if len(after)-len(before) > remaining {
				activity.Summary = block.Tool.Summary
				after, _ = json.Marshal(activity)
			}
			if len(after)-len(before) <= remaining {
				remaining -= len(after) - len(before)
				block.Tool = &activity
			}
		}
	}
}

func hasHistoryTools(page conversation.Page) bool {
	for _, message := range page.Messages {
		for _, block := range message.Blocks {
			if block.Type == "tool" && block.Tool != nil {
				return true
			}
		}
	}
	return false
}
