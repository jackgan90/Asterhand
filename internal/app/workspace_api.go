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
		Enabled  bool   `json:"enabled"`
		Revision string `json:"expected_revision"`
	}
	if err := read(w, r, &input); err != nil {
		a.fail(w, r, 400, err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	state, err := a.Chat.SetWorkspacePolicy(r.Context(), input.Enabled, input.Revision)
	if err != nil {
		a.fail(w, r, 409, err)
		return
	}
	send(w, 200, state)
}
