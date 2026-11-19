package app

import "net/http"

// Permission changes remain on the authenticated human settings surface. The
// public CLI/MCP registry exposes status and ordinary business operations only.
func (a *App) workspacePolicyAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		files, err := a.Chat.Workspace()
		if err != nil {
			a.fail(w, r, 400, err)
			return
		}
		send(w, 200, files.Status())
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var input struct {
		Enabled       *bool  `json:"enabled,omitempty"`
		Revision      string `json:"expected_revision,omitempty"`
		ExecutionMode string `json:"execution_mode,omitempty"`
		RemoveRuleID  string `json:"remove_rule_id,omitempty"`
	}
	if err := read(w, r, &input); err != nil {
		a.fail(w, r, 400, err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if input.Enabled == nil && (input.ExecutionMode != "" || input.RemoveRuleID != "") {
		state, err := a.Chat.SetWorkspaceTrustPolicy(r.Context(), input.ExecutionMode, input.RemoveRuleID)
		if err != nil {
			a.fail(w, r, 400, err)
			return
		}
		send(w, 200, state)
		return
	}
	enabled := false
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	state, err := a.Chat.SetWorkspacePolicy(r.Context(), enabled, input.Revision)
	if err != nil {
		a.fail(w, r, 409, err)
		return
	}
	send(w, 200, state)
}
