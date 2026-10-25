package app

import (
	"errors"
	"net/http"
	"sync"
)

// requestGate closes admission before waiting. No handler may add background
// work after Wait returns, so database closure cannot race a late request.
type requestGate struct {
	mu      sync.Mutex
	stopped bool
	wg      sync.WaitGroup
}

func (g *requestGate) wrap(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		if g.stopped {
			g.mu.Unlock()
			fail(w, http.StatusServiceUnavailable, errors.New("节点正在退出，请稍后重新启动"))
			return
		}
		g.wg.Add(1)
		g.mu.Unlock()
		defer g.wg.Done()
		next(w, r)
	}
}

func (g *requestGate) stop() {
	g.mu.Lock()
	g.stopped = true
	g.mu.Unlock()
}

// RequestStop atomically checks for active work and closes admission. A tray
// caller first passes false, then asks the user and retries with true if busy.
// Web, signals and session shutdown already convey the user's intent to stop.
func (a *App) RequestStop(confirmed bool) bool {
	a.mu.Lock()
	if a.stopping {
		a.mu.Unlock()
		return true
	}
	if (!confirmed && a.probeRunning) || !a.Engine.BeginStop(confirmed) {
		a.mu.Unlock()
		return false
	}
	a.stopping = true
	a.requests.stop()
	cancel := a.cancel
	a.mu.Unlock()
	if a.Input != nil {
		a.Input.BeginStop()
	}
	if cancel != nil {
		cancel()
	}
	return true
}

func (a *App) Stopping() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stopping
}
