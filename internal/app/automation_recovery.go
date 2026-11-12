package app

import (
	"errors"
	"time"

	"github.com/jackgan90/Asterhand/internal/operations"
	"github.com/jackgan90/Asterhand/internal/store"
)

// Positive domain evidence repairs an interrupted journal publication. Absence
// of a receipt is never interpreted as permission to execute again.
func (a *App) reconcileAutomationOperation(receipt operations.Receipt) (operations.Receipt, error) {
	if receipt.Terminal() {
		return receipt, nil
	}
	var summary any
	switch receipt.Name {
	case "drafts.commit":
		return a.reconcileEditorCommit(receipt)
	case "conversations.create", "conversations.send":
		conversation, turn, err := a.Chat.OperationStatus(receipt.NativeID())
		if errors.Is(err, store.ErrNotFound) {
			return receipt, nil
		}
		if err != nil {
			return receipt, err
		}
		if receipt.Name == "conversations.create" && conversation != nil {
			summary = map[string]any{"turn_operation_id": receipt.NativeID(), "conversation_id": conversation.ID, "state": conversation.State}
		} else if receipt.Name == "conversations.send" && turn != nil {
			summary = map[string]any{"conversation_id": turn.Conversation, "turn_operation_id": receipt.NativeID(), "delivery": turn.Delivery, "turn_id": turn.ID}
		}
	case "tasks.submit":
		job, err := a.Store.RequestJob(receipt.Node, receipt.NativeID())
		if errors.Is(err, store.ErrNotFound) {
			return receipt, nil
		}
		if err != nil {
			return receipt, err
		}
		summary = map[string]any{"task_id": job.ID, "request_id": receipt.NativeID(), "target_node_id": taskTarget(job), "project_id": job.Request.ProjectID}
	case "diagnostics.run", "batches.submit":
		state, err := a.Store.AutomationState(receipt.Identity)
		if errors.Is(err, store.ErrNotFound) {
			return receipt, nil
		}
		if err != nil {
			return receipt, err
		}
		if receipt.Name == "diagnostics.run" {
			summary = map[string]string{"operation_id": receipt.ID, "state": "running"}
		} else {
			summary = map[string]string{"batch_id": state.ID}
		}
	}
	if summary == nil {
		return receipt, nil
	}
	receipt.State, receipt.Accepted, receipt.Result = "completed", true, compactResult(summary)
	if err := a.Store.Finish(receipt, time.Now()); err != nil {
		if errors.Is(err, operations.ErrConflict) {
			return a.Store.Lookup(receipt.Actor, receipt.Node, receipt.ID)
		}
		return receipt, err
	}
	return receipt, nil
}
