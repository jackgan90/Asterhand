package app

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackgan90/Asterhand/internal/operations"
	"github.com/jackgan90/Asterhand/internal/store"
	"github.com/jackgan90/Asterhand/internal/workers"
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

func (a *App) workerServiceHooks() workers.ServiceHooks {
	return workers.ServiceHooks{
		BeforePruneTerminalTurns: a.reconcileReceiptsBeforeTurnPrune,
	}
}

// reconcileReceiptsBeforeTurnPrune reconciles pending non-terminal workers_send
// receipts using the expiring turn summaries before 30-day summary pruning runs.
// Any turn whose matching receipt cannot yet be persisted as terminal is
// returned in keepTurnIDs so its summary stays available for future recovery.
func (a *App) reconcileReceiptsBeforeTurnPrune(svc *workers.Service, expired []workers.Turn, now time.Time) ([]string, error) {
	if a == nil || a.Store == nil || svc == nil || len(expired) == 0 {
		return nil, nil
	}
	pending, err := a.Store.Pending()
	if err != nil {
		return nil, err
	}
	if len(pending) == 0 {
		return nil, nil
	}
	expiredIDs := make(map[string]bool, len(expired))
	for _, t := range expired {
		expiredIDs[t.ID] = true
	}
	keepSet := map[string]bool{}
	for _, r := range pending {
		if r.Terminal() || r.Name != "workers_send" {
			continue
		}
		summary, found, err := svc.OperationLookup(r.Name, r.NativeID())
		if err != nil {
			return nil, err
		}
		if !found && strings.Contains(r.ID, "_") {
			summary, found, err = svc.LegacyOperationLookup(r.Name, r.ID, r.Actor, r.Target)
			if err != nil {
				return nil, err
			}
		}
		if !found {
			continue
		}
		m, _ := summary.(map[string]any)
		turnID, _ := m["turn_id"].(string)
		reconciled := r
		reconciled.State, reconciled.Accepted, reconciled.Result = "completed", true, compactResult(summary)
		if fErr := a.Store.Finish(reconciled, now); fErr != nil {
			if errors.Is(fErr, operations.ErrConflict) {
				if latest, lErr := a.Store.Lookup(r.Actor, r.Node, r.ID); lErr == nil && latest.Terminal() {
					continue
				}
			}
			if turnID != "" && expiredIDs[turnID] {
				keepSet[turnID] = true
			}
		}
	}
	if len(keepSet) == 0 {
		return nil, nil
	}
	keep := make([]string, 0, len(keepSet))
	for id := range keepSet {
		keep = append(keep, id)
	}
	return keep, nil
}
