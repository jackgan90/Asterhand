package app

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/peer"
	"github.com/jackgan90/Asterhand/internal/store"
	"github.com/jackgan90/Asterhand/internal/tasks"
)

func (a *App) submit(j model.Job, input tasks.Submission) (model.Outbox, error) {
	if input.Target == "" {
		input.Target = j.Request.Target
	}
	if input.Project == "" {
		input.Project = j.Request.ProjectID
	}
	if j.Request.TargetExplicit && input.Target != j.Request.Target {
		return model.Outbox{}, errors.New("目标与显式选择器冲突，请用户重新选择后提交")
	}
	if input.Target != a.Config.Get().NodeID {
		p, err := a.Peers.Get(input.Target)
		if err != nil || p.Revoked {
			return model.Outbox{}, errors.New("目标节点尚未配对或已撤销")
		}
	}
	id := j.ID
	var old model.Outbox
	err := a.Store.Get("outbox", id, &old)
	if err == nil {
		if old.Request.Goal != input.Goal || old.PeerID != input.Target || old.Request.ProjectID != input.Project {
			return old, store.ErrConflict
		}
		return old, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return old, err
	}
	r := model.Request{RequestID: model.ID(), ProjectID: input.Project, Goal: input.Goal, Mode: "execute", StartBefore: j.Request.StartBefore}
	o := model.Outbox{ID: id, PeerID: input.Target, Request: r, State: "pending", Updated: model.Now()}
	return o, a.Store.Put("outbox", id, o)
}
func (a *App) outboxLoop(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		var out []model.Outbox
		if a.Store.List("outbox", &out) != nil {
			continue
		}
		for _, o := range out {
			if ctx.Err() != nil {
				return
			}
			if o.State == "rejected" || o.State == "cancelled" {
				continue
			}
			if o.Job != nil && model.Terminal(o.Job.State) {
				continue
			}
			a.refreshOutbox(ctx, o)
		}
		a.deliverAnswers(ctx)
	}
}
func (a *App) deliverAnswers(ctx context.Context) {
	var answers []pendingAnswer
	if a.Store.List("answer-outbox", &answers) != nil {
		return
	}
	for _, answer := range answers {
		if answer.State != "saved" {
			continue
		}
		var o model.Outbox
		if a.Store.Get("outbox", answer.OutboxID, &o) != nil || o.Job == nil {
			continue
		}
		p, peerErr := a.Peers.Get(o.PeerID)
		if peerErr != nil || p.Revoked {
			continue
		}
		var result model.Job
		err := peer.Call(ctx, p, "POST", "/v1/jobs/"+url.PathEscape(o.Job.ID)+"/answer", answer.Body, &result)
		if err != nil {
			answer.Error = err.Error()
		} else {
			answer.State = "delivered"
			answer.Error = ""
		}
		a.Store.Put("answer-outbox", answer.ID, answer)
	}
}
func (a *App) refreshOutbox(ctx context.Context, o model.Outbox) {
	var j model.Job
	var err error
	if o.PeerID == a.Config.Get().NodeID {
		if o.Job == nil {
			if o.Cancel {
				err = a.Store.Cancel(a.Config.Get().NodeID, o.Request.RequestID)
			}
			if err == nil {
				j, err = a.Engine.Accept(a.Config.Get().NodeID, o.Request)
			}
		} else {
			err = a.Store.Get("job", o.Job.ID, &j)
			if err == nil && o.Cancel && !model.Terminal(j.State) {
				j, err = a.Engine.Cancel(j.ID)
			}
		}
	} else {
		var p model.Peer
		p, err = a.Peers.Get(o.PeerID)
		if err == nil && p.Revoked {
			err = errors.New("此 peer 已撤销")
		}
		if err == nil && o.Cancel {
			err = peer.Call(ctx, p, "POST", "/v1/cancel-request", map[string]string{"request_id": o.Request.RequestID}, nil)
		}
		if err == nil && o.Job == nil {
			err = peer.Call(ctx, p, "POST", "/v1/jobs", o.Request, &j)
		} else if err == nil {
			err = peer.Call(ctx, p, "GET", "/v1/jobs/"+url.PathEscape(o.Job.ID), nil, &j)
		}
	}
	if err != nil {
		o.Error = err.Error()
		o.State = "delivery_unknown"
	} else {
		o.Job = &j
		o.Error = ""
		o.State = "delivered"
		o.Updated = model.Now()
	}
	// Preserve a concurrently registered cancel; network refresh cannot erase user intent.
	a.Store.Modify("outbox", o.ID, func(raw []byte) (any, error) {
		var current model.Outbox
		if err := decode(raw, &current); err != nil {
			return nil, err
		}
		o.Cancel = o.Cancel || current.Cancel
		return o, nil
	})
}
