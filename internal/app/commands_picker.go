package app

import (
	"context"
	"net/http"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/localcommands"
	"github.com/jackgan90/Asterhand/internal/quickinput"
)

func (a *App) browseCommandScript(w http.ResponseWriter, r *http.Request) {
	var input struct{}
	if err := readCommand(w, r, &input); err != nil {
		a.fail(w, r, http.StatusBadRequest, err)
		return
	}
	a.mu.Lock()
	busy, stopping, lifetime := a.commandPicking, a.stopping, a.ctx
	if !busy && !stopping {
		a.commandPicking = true
	}
	a.mu.Unlock()
	if busy || stopping {
		a.fail(w, r, http.StatusConflict, i18n.Err("err.app.picker_busy"))
		return
	}
	defer func() { a.mu.Lock(); a.commandPicking = false; a.mu.Unlock() }()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if lifetime != nil {
		stop := context.AfterFunc(lifetime, cancel)
		defer stop()
	}
	choose := a.pickScript
	if choose == nil {
		choose = quickinput.PickLocalCommandScript
	}
	path, err := choose(ctx)
	if err == nil && path != "" {
		path, err = localcommands.ScriptPath(path)
	}
	if err != nil {
		a.fail(w, r, http.StatusBadRequest, err)
		return
	}
	send(w, http.StatusOK, map[string]any{"path": path, "cancelled": path == ""})
}
