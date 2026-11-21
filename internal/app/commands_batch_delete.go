package app

import (
	"net/http"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/operations"
)

type commandDeleteItem struct {
	ID        string `json:"command_id"`
	Revision  string `json:"expected_revision"`
	Operation string `json:"operation_id"`
}

// A bounded human adapter, not a second deletion implementation. Each item's
// durable identity survives a lost HTTP reply; replay goes through the journal.
func (a *App) deleteCommandBatch(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Items []commandDeleteItem `json:"items"`
	}
	if err := readCommand(w, r, &input); err != nil {
		a.fail(w, r, http.StatusBadRequest, err)
		return
	}
	ids, operationsSeen := map[string]bool{}, map[string]bool{}
	valid := len(input.Items) > 0 && len(input.Items) <= 32
	for _, item := range input.Items {
		valid = valid && wire.ValidID(item.ID) && item.Revision != "" && len(item.Revision) <= 128 &&
			operations.CheckName(item.Operation, "commands_delete") == nil && !ids[item.ID] && !operationsSeen[item.Operation]
		ids[item.ID], operationsSeen[item.Operation] = true, true
	}
	if !valid {
		a.fail(w, r, http.StatusBadRequest, automation.Fail("invalid_parameters", "Expected 1-32 distinct commands with revisions and operation identities"))
		return
	}
	results := make([]automation.Response, 0, len(input.Items))
	for _, item := range input.Items {
		response := a.Automation.Registry.DispatchHuman(r.Context(), automation.Request{
			Version: automation.APIVersion, Node: a.Config.Get().NodeID,
			Operation: "commands_delete", ID: item.Operation,
			Arguments: compactResult(automation.CommandVersion{ID: item.ID, Revision: item.Revision}),
		})
		results = append(results, response)
	}
	send(w, http.StatusOK, map[string]any{"results": results})
}
