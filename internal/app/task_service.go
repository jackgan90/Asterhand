package app

import (
	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/model"
)

// submitTask preserves the existing local interpretation/route step. An agent
// cannot use this entry point to turn a request into a privileged execute call.
func (a *App) submitTask(req model.Request, actor string) (model.Job, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if req.Target == "" {
		req.Target = a.Config.Get().NodeID
	}
	if req.Target != a.Config.Get().NodeID {
		var p model.Peer
		if err := a.Store.Get("peer", req.Target, &p); err != nil || p.Revoked {
			return model.Job{}, i18n.Err("err.app.pair_target_first")
		}
	}
	req.Mode = "route"
	return a.Engine.AcceptAs(a.Config.Get().NodeID, req, actor)
}
