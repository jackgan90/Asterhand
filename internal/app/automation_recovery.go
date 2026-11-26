package app

import (
	"encoding/json"
	"errors"
	"strings"
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
	case "drafts_commit":
		return a.reconcileEditorCommit(receipt)
	case "workspace_files_mutate", "workspace_versions_restore", "workspace_operations_resolve":
		files, err := a.Chat.Workspace()
		if err != nil {
			return receipt, err
		}
		op, err := files.Operation(receipt.NativeID())
		if receipt.Name == "workspace_operations_resolve" {
			op, err = files.Resolution(receipt.NativeID())
		}
		if err != nil {
			return receipt, nil
		}
		if op.State == "started" || op.State == "unknown" {
			return receipt, nil
		}
		summary = map[string]string{"activity_id": op.ID, "state": op.State}
	case "conversations_create", "conversations_send":
		conversation, turn, err := a.Chat.OperationStatus(receipt.NativeID())
		if errors.Is(err, store.ErrNotFound) {
			return receipt, nil
		}
		if err != nil {
			return receipt, err
		}
		if receipt.Name == "conversations_create" && conversation != nil {
			summary = map[string]any{"turn_operation_id": receipt.NativeID(), "conversation_id": conversation.ID, "state": conversation.State}
		} else if receipt.Name == "conversations_send" && turn != nil {
			summary = map[string]any{"conversation_id": turn.Conversation, "turn_operation_id": receipt.NativeID(), "delivery": turn.Delivery, "turn_id": turn.ID}
		}
	case "workers_create", "workers_send":
		if a.Workers == nil {
			return receipt, nil
		}
		s, found, err := a.Workers.OperationLookup(receipt.Name, receipt.NativeID())
		if err != nil {
			return receipt, err
		}
		if !found && strings.Contains(receipt.ID, "_") {
			s, found, err = a.Workers.LegacyOperationLookup(receipt.Name, receipt.ID, receipt.Actor, receipt.Target)
			if err != nil {
				return receipt, err
			}
		}
		if !found {
			return receipt, nil
		}
		summary = s
	case "tasks_submit":
		job, err := a.Store.RequestJob(receipt.Node, receipt.NativeID())
		if errors.Is(err, store.ErrNotFound) {
			return receipt, nil
		}
		if err != nil {
			return receipt, err
		}
		summary = map[string]any{"task_id": job.ID, "request_id": receipt.NativeID(), "target_node_id": taskTarget(job), "project_id": job.Request.ProjectID}
	case "diagnostics_run", "batches_submit":
		state, err := a.Store.AutomationState(receipt.Identity)
		if errors.Is(err, store.ErrNotFound) {
			return receipt, nil
		}
		if err != nil {
			return receipt, err
		}
		if receipt.Name == "diagnostics_run" {
			summary = map[string]string{"operation_id": receipt.ID, "state": "running"}
		} else {
			summary = map[string]string{"batch_id": state.ID}
		}
	case "settings_desktop_shortcut":
		state, err := a.Store.AutomationState(receipt.Identity)
		if errors.Is(err, store.ErrNotFound) {
			return receipt, nil
		}
		if err != nil {
			return receipt, err
		}
		var desired struct {
			Enabled bool `json:"enabled"`
		}
		if err = json.Unmarshal(state.Data, &desired); err != nil {
			return receipt, nil
		}
		st := a.shortcutStatus()
		if st.Supported && st.Available && !st.Conflict && st.Enabled == desired.Enabled {
			summary = map[string]bool{"enabled": st.Enabled, "conflict": false}
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
