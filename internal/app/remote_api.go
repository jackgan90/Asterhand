package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/peer"
)

type pendingAnswer struct {
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
		fail(w, 404, err)
		return
	}
	if parts[1] == "cancel" && r.Method == "POST" {
		err := a.Store.Modify("outbox", o.ID, func(raw []byte) (any, error) {
			var current model.Outbox
			if err := decode(raw, &current); err != nil {
				return nil, err
			}
			current.Cancel = true
			return current, nil
		})
		if err != nil {
			fail(w, 500, err)
			return
		}
		send(w, 202, map[string]string{"state": "cancel_registered"})
		return
	}
	if o.Job == nil {
		fail(w, 409, errors.New("目标节点尚未确认接收"))
		return
	}
	p, err := a.Peers.Get(o.PeerID)
	if err != nil {
		fail(w, 404, err)
		return
	}
	if p.Revoked {
		fail(w, 403, errors.New("peer 已撤销"))
		return
	}
	if parts[1] == "events" && r.Method == "GET" {
		var result any
		err := peer.Call(r.Context(), p, "GET", "/v1/jobs/"+url.PathEscape(o.Job.ID)+"/events?after="+url.QueryEscape(r.URL.Query().Get("after")), nil, &result)
		if err != nil {
			fail(w, 502, err)
			return
		}
		send(w, 200, result)
		return
	}
	if parts[1] == "answer" && r.Method == "POST" {
		var input struct {
			InteractionID string `json:"interaction_id"`
			Attempt       string `json:"attempt"`
			AnswerID      string `json:"answer_id"`
			Answer        string `json:"answer"`
		}
		if err := read(w, r, &input); err != nil {
			fail(w, 400, err)
			return
		}
		if len(input.AnswerID) < 8 || input.Answer == "" || len(input.Answer) > 4000 {
			fail(w, 400, errors.New("回答无效"))
			return
		}
		body, _ := json.Marshal(input)
		key := model.Hash([]string{o.ID, input.InteractionID, input.AnswerID})
		var existing pendingAnswer
		if err := a.Store.Get("answer-outbox", key, &existing); err == nil {
			if string(existing.Body) != string(body) {
				fail(w, 409, errors.New("同一回答 ID 内容不同"))
				return
			}
			send(w, 200, existing)
			return
		}
		answer := pendingAnswer{ID: key, OutboxID: o.ID, Body: body, State: "saved"}
		if err := a.Store.Put("answer-outbox", key, answer); err != nil {
			fail(w, 500, err)
			return
		}
		send(w, 202, answer)
		return
	}
	http.NotFound(w, r)
}
