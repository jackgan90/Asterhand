package app

import (
	"strings"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/model"
)

func validateNodeName(name string) error {
	if strings.TrimSpace(name) == "" || len(name) > 80 {
		return i18n.Err("err.app.name_required")
	}
	return nil
}

// Caller holds a.mu. Both the human form and public settings operations use
// these admission rules before changing model configuration or ordinary fields.
func (a *App) settingsAdmissionLocked(before, next config.Config, credentialsChanged bool) (func(), error) {
	var jobs []model.Job
	if err := a.Store.List("job", &jobs); err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if !model.Terminal(j.State) {
			return nil, i18n.Err("err.app.busy_tasks_before_settings")
		}
	}
	if a.probeRunning {
		return nil, i18n.Err("err.app.wait_probe")
	}
	if a.stopping {
		return nil, i18n.Err("err.app.node_stopping")
	}
	if credentialsChanged || model.Hash(before.LLM) != model.Hash(next.LLM) || before.AgentRuntime != next.AgentRuntime {
		return a.Chat.BeginConfiguration()
	}
	return func() {}, nil
}
