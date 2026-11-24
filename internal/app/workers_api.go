package app

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/workers"
)

// Web Coding Workers panel API backed by the same shared workers.Service used by CLI/MCP.
func (a *App) workersAPI(w http.ResponseWriter, r *http.Request) {
	if a.Workers == nil {
		a.fail(w, r, 503, automation.Fail("unavailable", "Workers service is unavailable"))
		return
	}
	switch r.URL.Path {
	case "/api/workers/list":
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return
		}
		includeClosed := r.URL.Query().Get("include_closed") == "true" || r.URL.Query().Get("include_closed") == "1"
		items, err := a.Workers.List("", "", includeClosed, 100)
		if err != nil {
			a.fail(w, r, 400, err)
			return
		}
		send(w, 200, map[string]any{
			"items":        items,
			"active_turns": a.Workers.ActiveTurnsSnapshot(),
			"interactions": a.Workers.ListAllPendingInteractions(),
		})
	case "/api/workers/get":
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return
		}
		workerID := strings.TrimSpace(r.URL.Query().Get("worker_id"))
		if workerID == "" {
			a.fail(w, r, 400, automation.Fail("invalid_parameters", "worker_id is required"))
			return
		}
		wk, turns, err := a.Workers.Get(workerID, "")
		if err != nil {
			a.fail(w, r, 400, err)
			return
		}
		send(w, 200, map[string]any{"worker": wk, "turns": turns})
	case "/api/workers/logs":
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return
		}
		turnID := strings.TrimSpace(r.URL.Query().Get("turn_id"))
		if turnID == "" {
			a.fail(w, r, 400, automation.Fail("invalid_parameters", "turn_id is required"))
			return
		}
		var afterSeq int64
		if raw := strings.TrimSpace(r.URL.Query().Get("after_seq")); raw != "" {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || n < 0 {
				a.fail(w, r, 400, automation.Fail("invalid_parameters", "after_seq must be a non-negative integer"))
				return
			}
			afterSeq = n
		}
		turn, entries, hasMore, err := a.Workers.ReadLogs(turnID, "", afterSeq, 100)
		if err != nil {
			a.fail(w, r, 400, err)
			return
		}
		resp := map[string]any{
			"turn":     turn,
			"entries":  entries,
			"has_more": hasMore,
		}
		if workers.IsTurnTerminal(turn.State) && turn.AnswerAvailable {
			if page, err := a.Workers.ReadResult(turnID, "", 0, 16<<10); err == nil {
				resp["result"] = page
			}
		}
		send(w, 200, resp)
	case "/api/workers/action":
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		var in struct {
			Action   string `json:"action"` // cancel | reconcile | resolve | close
			TurnID   string `json:"turn_id,omitempty"`
			WorkerID string `json:"worker_id,omitempty"`
			Reason   string `json:"reason,omitempty"`
		}
		if err := read(w, r, &in); err != nil {
			a.fail(w, r, 400, err)
			return
		}
		switch in.Action {
		case "cancel":
			turn, err := a.Workers.Cancel(r.Context(), in.TurnID, "")
			if err != nil {
				a.fail(w, r, 400, err)
				return
			}
			a.notifyWorkers()
			send(w, 200, map[string]any{"turn": turn, "active_turns": a.Workers.ActiveTurnsSnapshot()})
		case "reconcile":
			turn, err := a.Workers.ReconcileTurn(r.Context(), in.TurnID)
			if err != nil {
				a.fail(w, r, 400, err)
				return
			}
			a.notifyWorkers()
			send(w, 200, map[string]any{"turn": turn, "active_turns": a.Workers.ActiveTurnsSnapshot()})
		case "resolve":
			if strings.TrimSpace(in.Reason) == "" {
				in.Reason = "Closed by local human operator"
			}
			turn, err := a.Workers.ResolveTurn(in.TurnID, "human:web", in.Reason)
			if err != nil {
				a.fail(w, r, 400, err)
				return
			}
			a.notifyWorkers()
			send(w, 200, map[string]any{"turn": turn, "active_turns": a.Workers.ActiveTurnsSnapshot()})
		case "close":
			wk, err := a.Workers.CloseWorker(r.Context(), in.WorkerID, "")
			if err != nil {
				a.fail(w, r, 400, err)
				return
			}
			a.notifyWorkers()
			send(w, 200, map[string]any{"worker": wk, "active_turns": a.Workers.ActiveTurnsSnapshot()})
		default:
			a.fail(w, r, 400, automation.Fail("invalid_parameters", "action must be cancel, reconcile, resolve, or close"))
		}
	case "/api/workers/interactions":
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return
		}
		send(w, 200, map[string]any{
			"items": a.Workers.ListHumanInteractions(),
		})
	case "/api/workers/interactions/decide":
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		var in struct {
			RequestID string              `json:"request_id"`
			Decision  string              `json:"decision"`
			Message   string              `json:"message,omitempty"`
			Text      string              `json:"text,omitempty"`
			Answers   map[string][]string `json:"answers,omitempty"`
		}
		if err := read(w, r, &in); err != nil {
			a.fail(w, r, 400, err)
			return
		}
		view, err := a.Workers.DecideInteractionHuman(in.RequestID, workers.InteractionDecision{
			Decision: in.Decision,
			Message:  in.Message,
			Text:     in.Text,
			Answers:  in.Answers,
		})
		if err != nil {
			a.fail(w, r, 400, err)
			return
		}
		send(w, 200, map[string]any{
			"resolved":     view,
			"interactions": a.Workers.ListAllPendingInteractions(),
		})
	case "/api/workers/session-auto-approve":
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		var in struct {
			WorkerID string `json:"worker_id"`
			Enabled  bool   `json:"enabled"`
		}
		if err := read(w, r, &in); err != nil {
			a.fail(w, r, 400, err)
			return
		}
		wk, err := a.Workers.SetSessionAutoApproveHuman(in.WorkerID, in.Enabled)
		if err != nil {
			a.fail(w, r, 400, err)
			return
		}
		a.notifyWorkers()
		send(w, 200, map[string]any{"worker": wk})
	default:
		http.NotFound(w, r)
	}
}
