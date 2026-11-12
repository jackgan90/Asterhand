package app

import (
	"context"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/executor"
	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/operations"
)

func (a *App) probeSnapshot(locale string) map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	state, detail := a.probeStatus, a.probeError
	key, args := a.probeErrorKey, i18n.CloneArgs(a.probeErrorArgs)
	if !a.probeRunning && state != "failed" {
		state, detail, key, args = string(a.Config.Get().DiagnosticStatus()), "", "", nil
	}
	if key != "" {
		detail = i18n.LocalizeMessage(locale, key, args, detail)
	}
	return map[string]any{"state": state, "error": detail, "message_key": key, "message_args": args}
}

// The human button and automation share admission and execution. A durable
// acceptance record precedes the model call; recovery only reads that record.
func (a *App) startProbe(identity *operations.Identity, check func() error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.probeRunning {
		return i18n.Err("err.app.probe_running")
	}
	if a.stopping {
		return i18n.Err("err.app.node_stopping")
	}
	if check != nil {
		if err := check(); err != nil {
			return err
		}
	}
	var state *operations.State
	if identity != nil {
		state = &operations.State{Identity: *identity, Instance: a.Automation.Registry.Instance, Updated: time.Now().UnixMilli(), Data: compactResult(map[string]string{"state": "running"})}
		if err := a.Store.SaveAutomationState(*state); err != nil {
			return err
		}
	}
	a.probeRunning, a.probeStatus, a.probeError, a.probeErrorKey, a.probeErrorArgs = true, "running", "", "", nil
	token := model.ID()
	a.probeTokens[token] = false
	a.wg.Add(1)
	go a.executeProbe(token, state)
	return nil
}

func (a *App) executeProbe(token string, state *operations.State) {
	defer a.wg.Done()
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Minute)
	defer cancel()
	_, err := a.Engine.Executor.Execute(ctx, executor.Run{ID: model.ID(), Mode: "probe", Callback: a.Engine.Callback, Token: token, Goal: "Call asterhand_probe exactly once to check this node. This tool has no side effects. Then reply with a short readiness confirmation. Do not use any other tools."})
	a.mu.Lock()
	called := a.probeTokens[token]
	delete(a.probeTokens, token)
	if err == nil && !called {
		err = i18n.Err("err.app.probe_tool_not_called")
	}
	c := a.Config.Get()
	c.ModelVerified, c.ModelDiagnostic = err == nil, config.DiagnosticFailed
	if err == nil {
		c.ModelDiagnostic, c.VerifiedBundle, c.VerifiedExecution = config.DiagnosticPassed, executor.BundleIdentity(), executor.ExecutionIdentity(c)
	}
	if saveErr := a.Config.Save(c); saveErr != nil {
		err = saveErr
	}
	a.probeRunning, a.probeStatus, a.probeError, a.probeErrorKey, a.probeErrorArgs = false, "passed", "", "", nil
	if err != nil {
		a.probeStatus = "failed"
		a.probeError, a.probeErrorKey, a.probeErrorArgs = i18n.LocalizeError(i18n.Effective(), err)
	}
	if state != nil {
		// Store stable status and translation metadata, not runtime error text
		// which can contain paths or a provider's response.
		state.Data = compactResult(map[string]string{"state": a.probeStatus, "message_key": a.probeErrorKey})
		state.Complete, state.Updated = true, time.Now().UnixMilli()
		_ = a.Store.SaveAutomationState(*state)
	}
	a.mu.Unlock()
}
