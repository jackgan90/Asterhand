package app

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/peer"
)

type pendingAnswer struct {
	Actor    string          `json:"actor,omitempty"`
	ID       string          `json:"id"`
	OutboxID string          `json:"outbox_id"`
	Body     json.RawMessage `json:"body"`
	State    string          `json:"state"`
	Error    string          `json:"error,omitempty"`
}

func (a *App) remoteAPI(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/remote/"), "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	var o model.Outbox
	if err := a.Store.Get("outbox", parts[0], &o); err != nil {
		a.fail(w, r, 404, err)
		return
	}
	if parts[1] == "cancel" && r.Method == "POST" {
		err := a.cancelOutbox(o.ID, "")
		if err != nil {
			a.fail(w, r, 500, err)
			return
		}
		send(w, 202, map[string]string{"state": "cancel_registered"})
		return
	}
	if o.Job == nil {
		a.fail(w, r, 409, i18n.Err("err.app.remote_not_accepted_yet"))
		return
	}
	p, err := a.Peers.Get(o.PeerID)
	if err != nil {
		a.fail(w, r, 404, err)
		return
	}
	if p.Revoked {
		a.fail(w, r, 403, i18n.Err("err.app.remote_peer_revoked"))
		return
	}
	if parts[1] == "events" && r.Method == "GET" {
		var result any
		err := peer.Call(r.Context(), p, "GET", "/v1/jobs/"+url.PathEscape(o.Job.ID)+"/events?after="+url.QueryEscape(r.URL.Query().Get("after")), nil, &result)
		if err != nil {
			a.fail(w, r, 502, err)
			return
		}
		send(w, 200, result)
		return
	}
	if parts[1] == "answer" && r.Method == "POST" {
		var input remoteAnswerInput
		if err := read(w, r, &input); err != nil {
			a.fail(w, r, 400, err)
			return
		}
		answer, err := a.queueRemoteAnswer(o, input, "")
		if err != nil {
			a.fail(w, r, 500, err)
			return
		}
		send(w, 202, answer)
		return
	}
	http.NotFound(w, r)
}
