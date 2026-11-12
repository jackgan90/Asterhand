package app

import (
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/operations"
)

// Ordinary and sensitive fields remain in the existing human form. Only a
// digest and publication reference enter the journal, never credential text.
func (a *App) beginHumanSettings(id string, value any) (*operations.Identity, func(error), error) {
	noop := func(error) {}
	if id == "" {
		return nil, noop, nil
	}
	i := operations.Identity{Actor: "human:web", Node: a.Config.Get().NodeID, ID: id, Name: "human.settings", Digest: operations.Hash(value), Target: a.Config.Get().NodeID}
	receipt, fresh, err := a.Store.Begin(i, time.Now())
	if err != nil {
		return nil, noop, err
	}
	if !fresh {
		return nil, noop, automation.Fail("result_unknown", "Inspect the original settings receipt")
	}
	return &i, func(rejected error) {
		current, e := a.Store.Lookup(i.Actor, i.Node, i.ID)
		if e == nil && !current.Terminal() {
			receipt.State = "unknown"
			if rejected != nil {
				receipt.State, receipt.Code = "rejected", automation.PublicError(rejected).Code
			}
			_ = a.Store.Finish(receipt, time.Now())
		}
	}, nil
}
