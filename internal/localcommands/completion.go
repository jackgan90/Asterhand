package localcommands

import "time"

// Completion is a bounded fact about one newly accepted run. It deliberately
// excludes script paths, parameters, graph definitions and captured output.
type Completion struct {
	CommandID, RunID, Name, Result, Reason string
	Started, Ended                         time.Time
	Elapsed                                float64
}

type completionObserver struct{ notify func(Completion) }

// SetCompletionObserver installs an in-memory observer, never replays history.
// notify must only hand off bounded data; it must not perform I/O or call back
// into the run manager (the finalization lock is held during this handoff).
func (m *RunManager) SetCompletionObserver(notify func(Completion)) {
	if notify == nil {
		m.completion.Store(nil)
		return
	}
	m.completion.Store(&completionObserver{notify: notify})
}

func (m *RunManager) completedLocked(run *managedRun) {
	if !run.observeCompletion {
		return
	}
	run.observeCompletion = false
	r := &run.record
	observer := m.completion.Load()
	if observer == nil || !r.Command.NotifyOnCompletion.Enabled() || r.Ended == nil ||
		(r.Result != "succeeded" && r.Result != "failed") {
		return
	}
	observer.notify(Completion{CommandID: r.Command.ID, RunID: r.ID, Name: r.Command.Name,
		Result: r.Result, Reason: r.Reason, Started: r.Started, Ended: *r.Ended, Elapsed: r.Elapsed})
}
