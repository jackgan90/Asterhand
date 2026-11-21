package app

import (
	"encoding/json"
	"net/http"

	"github.com/jackgan90/Asterhand/internal/automation"
)

// This facade runs only after the existing browser cookie, origin and CSRF
// checks. Its small allowlist contains ordinary editor submissions;
// it exposes neither authorization controls nor a generic human impersonation.
func (a *App) automationHumanCall(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name      string          `json:"operation"`
		ID        string          `json:"operation_id,omitempty"`
		Node      string          `json:"node_id,omitempty"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := read(w, r, &input); err != nil {
		a.fail(w, r, 400, err)
		return
	}
	if input.Node != "" && input.Node != a.Config.Get().NodeID {
		send(w, 200, automation.Response{Version: automation.APIVersion, Node: a.Config.Get().NodeID, Instance: a.Automation.Registry.Instance, Error: automation.Fail("wrong_node", "The editor belongs to another node")})
		return
	}
	switch input.Name {
	case "commands_create", "commands_patch", "settings_patch", "tasks_answer":
	case "workspaces_get", "workspace_files_list", "workspace_files_search", "workspace_files_read",
		"workspace_versions_list", "workspace_versions_get", "workspace_versions_restore", "turn_activities_list", "turn_activities_get",
		"workspace_operations_get", "workspace_operations_resolve", "artifacts_list", "artifacts_get", "artifacts_read":
	default:
		a.fail(w, r, 403, automation.Fail("forbidden", "Operation is not part of the human editor facade"))
		return
	}
	response := a.Automation.Registry.DispatchHuman(r.Context(), automation.Request{Version: automation.APIVersion, Node: a.Config.Get().NodeID, Operation: input.Name, ID: input.ID, Arguments: input.Arguments})
	// A business rejection is part of the structured response. Losing an HTTP
	// response therefore never looks like a definite domain rejection to the UI.
	send(w, 200, response)
}
