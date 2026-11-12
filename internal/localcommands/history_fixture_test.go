package localcommands

import "testing"

func testRunManager(t *testing.T, home string, backend ExecutionBackend, clock ExecutionClock) *RunManager {
	t.Helper()
	m := NewRunManager(home, backend, clock)
	// Some recovery fixtures deliberately leave uncertain scopes unresolved.
	// Close their new observation database even when a shutdown cannot finish.
	t.Cleanup(func() {
		if m.history != nil {
			_ = m.history.db.Close()
		}
	})
	return m
}
