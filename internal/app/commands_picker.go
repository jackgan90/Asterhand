package app

import (
	"context"
	"net/http"

	"github.com/jackgan90/Asterhand/internal/localcommands"
	"github.com/jackgan90/Asterhand/internal/quickinput"
)

func (a *App) browseCommandScript(w http.ResponseWriter, r *http.Request) {
	var input struct{}
	if err := readCommand(w, r, &input); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	a.mu.Lock()
	busy, stopping, lifetime := a.commandPicking, a.stopping, a.ctx
	if !busy && !stopping {
		a.commandPicking = true
	}
	a.mu.Unlock()
	if busy || stopping {
		send(w, http.StatusConflict, map[string]string{"error": "文件选择窗口已打开或节点正在退出"})
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
		fail(w, http.StatusBadRequest, err)
		return
	}
	send(w, http.StatusOK, map[string]any{"path": path, "cancelled": path == ""})
}
