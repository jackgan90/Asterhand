package localcommands

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackgan90/Asterhand/internal/operations"
)

type RunReference struct {
	CommandID string `json:"command_id"`
	RunID     string `json:"run_id"`
}

func (m *RunManager) ReconcilePublications(j operations.Journal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.journal = j
	return m.reconcilePublicationsLocked()
}

func (m *RunManager) reconcilePublicationsLocked() error {
	for _, run := range m.runs {
		p := run.record.Operation // Immutable from preparation onward.
		if p == nil {
			continue
		}
		if m.journal == nil {
			return operations.ErrMissing
		}
		receipt, err := m.journal.Lookup(p.Actor, p.Node, p.ID)
		if errors.Is(err, operations.ErrMissing) {
			if at, e := operations.IDTime(p.ID); e == nil && time.Since(at) > operations.Retention {
				continue
			}
		}
		if err != nil {
			return err
		}
		if receipt.Identity != *p {
			return operations.ErrConflict
		}
		if receipt.Terminal() {
			continue
		}
		receipt.State, receipt.Accepted = "completed", true
		receipt.Result, _ = json.Marshal(RunReference{CommandID: run.record.Command.ID, RunID: run.record.ID})
		if err = m.journal.Finish(receipt, time.Now()); err != nil {
			return err
		}
	}
	return nil
}

func (r *Registry) LaunchOperation(ctx context.Context, id, revision string, identity *operations.Identity, authorize func() error) (*RunRecord, error) {
	command, err := r.Get(id)
	if err != nil {
		if Failure(err).Error.Code == "not_found" {
			return nil, problem("not_found", "err.localcmd.deleted_refresh_list")
		}
		return nil, err
	}
	if command.Revision != revision {
		return nil, problem("conflict", "err.localcmd.changed_reselect")
	}
	checkContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err = r.check(checkContext, command); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	current := false
	for _, c := range r.catalog.Commands {
		current = current || c.ID == id && c.Revision == revision
	}
	if !current {
		return nil, problem("conflict", "err.localcmd.changed_reselect")
	}
	if ctx.Err() != nil {
		return nil, problem("unavailable", "err.localcmd.precheck_timeout")
	}
	if r.closing {
		return nil, problem("unavailable", "err.app.node_stopping")
	}
	if r.managed == nil {
		return nil, problem("unsupported", "err.localcmd.managed_unavailable")
	}
	if authorize != nil {
		if err = authorize(); err != nil {
			return nil, err
		}
	}
	return r.managed.StartOperation(command, identity)
}
