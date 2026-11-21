package app

import (
	"net/http"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/harnesses"
	"github.com/jackgan90/Asterhand/internal/workers"
)

// Harness onboarding, external MCP registration, permission switches, and active
// turn overrides remain on the authenticated human Web settings surface per AUTO-19.
func (a *App) harnessesAPI(w http.ResponseWriter, r *http.Request) {
	if a.Harnesses == nil || a.Workers == nil {
		a.fail(w, r, 503, automation.Fail("unavailable", "Harness service is unavailable"))
		return
	}
	switch r.URL.Path {
	case "/api/harnesses/status":
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return
		}
		st := a.Harnesses.Status()
		send(w, 200, map[string]any{
			"revision":     st.Revision,
			"harnesses":    st.Harnesses,
			"active_turns": a.Workers.ActiveTurnsSnapshot(),
		})
	case "/api/harnesses/policy":
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		var in harnesses.UpdatePolicyInput
		if err := read(w, r, &in); err != nil {
			a.fail(w, r, 400, err)
			return
		}
		st, err := a.Harnesses.UpdatePolicy(in)
		if err != nil {
			a.fail(w, r, 400, err)
			return
		}
		send(w, 200, map[string]any{
			"revision":     st.Revision,
			"harnesses":    st.Harnesses,
			"active_turns": a.Workers.ActiveTurnsSnapshot(),
		})
	case "/api/harnesses/turn-action":
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		var in struct {
			Action string `json:"action"` // cancel | reconcile | resolve
			TurnID string `json:"turn_id"`
			Reason string `json:"reason,omitempty"`
		}
		if err := read(w, r, &in); err != nil {
			a.fail(w, r, 400, err)
			return
		}
		var (
			turn workers.Turn
			err  error
		)
		switch in.Action {
		case "cancel":
			turn, err = a.Workers.Cancel(r.Context(), in.TurnID, "")
		case "reconcile":
			turn, err = a.Workers.ReconcileTurn(r.Context(), in.TurnID)
		case "resolve":
			if in.Reason == "" {
				in.Reason = "Closed by local human operator"
			}
			turn, err = a.Workers.ResolveTurn(in.TurnID, "human:web", in.Reason)
		default:
			a.fail(w, r, 400, automation.Fail("invalid_parameters", "action must be cancel, reconcile, or resolve"))
			return
		}
		if err != nil {
			a.fail(w, r, 400, err)
			return
		}
		send(w, 200, map[string]any{"turn": turn, "active_turns": a.Workers.ActiveTurnsSnapshot()})
	default:
		http.NotFound(w, r)
	}
}
