package localcommands

import (
	"context"
	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
)

// ConfigureRuns is called once during application assembly, before serving.
func (r *Registry) ConfigureRuns(manager *RunManager) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.managed = manager
	if err := r.ready(); err != nil {
		return err
	}
	if manager.loadErr != nil {
		return manager.loadErr
	}
	if err := r.cleanupPending(); err != nil {
		return err
	}
	valid := map[string]bool{}
	for _, c := range r.catalog.Commands {
		valid[c.ID] = true
	}
	for id := range manager.groups {
		if !valid[id] {
			if err := manager.Clear(id); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Registry) RunStates() map[string]wire.Entry {
	states := map[string]wire.Entry{}
	catalog, err := r.Snapshot()
	if err != nil {
		return states
	}
	for _, command := range catalog.Commands {
		states[command.ID] = r.managed.Summary(command.ID)
	}
	return states
}

func (r *Registry) Runs(id string) RunList          { return r.managed.Runs(id) }
func (r *Registry) Run(id, runID string) *RunRecord { return r.managed.Run(id, runID) }
func (r *Registry) StopRuns(id string, ids []string) error {
	if r.managed == nil {
		return problem("unsupported", "err.localcmd.managed_unavailable")
	}
	return r.managed.StopAll(id, ids)
}

func (r *Registry) ManagedAvailable() bool          { return r.managed.Available() }
func (r *Registry) CurrentRun(id string) *RunRecord { return r.managed.Current(id) }
func (r *Registry) ReadLog(id, run string, cursor int64, node string, before bool) (LogPage, error) {
	if r.managed == nil {
		return LogPage{}, problem("unsupported", "err.localcmd.managed_unavailable")
	}
	if node != "" {
		return r.managed.ReadNode(id, run, node, cursor, before)
	}
	return r.managed.Read(id, run, cursor)
}
func (r *Registry) StopRun(id, run string) (*RunRecord, error) {
	if r.managed == nil {
		return nil, problem("unsupported", "err.localcmd.managed_unavailable")
	}
	return r.managed.Stop(id, run, "user_stop")
}

// BeginStop serializes admission with launch and configuration mutations. The
// callback participates in the application's existing atomic stop decision.
func (r *Registry) BeginStop(confirmed bool, next func() bool) bool {
	if r == nil {
		return next()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !confirmed && r.managed.Busy("") {
		return false
	}
	if !next() {
		return false
	}
	r.closing = true
	return true
}
func (r *Registry) Close(ctx context.Context) error { return r.managed.Close(ctx) }

func (r *Registry) BeginShutdown() {
	if r != nil {
		r.managed.BeginShutdown()
	}
}

// The cleanup intent commits with the configuration. It remains durable until
// every referenced run has been removed, even if publishing a run marker fails.
// Caller holds the registry lock; this also gates switching back to managed.
func (r *Registry) cleanupPending() error {
	if len(r.catalog.PendingCleanup) == 0 {
		return nil
	}
	if r.managed == nil {
		return problem("unavailable", "err.localcmd.managed_unavailable")
	}
	for _, id := range r.catalog.PendingCleanup {
		if err := r.managed.Clear(id); err != nil {
			return err
		}
	}
	next := cloneCatalog(r.catalog)
	next.PendingCleanup = nil
	return r.save(next)
}
