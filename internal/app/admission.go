package app

import (
	"errors"
	"net/http"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/model"
)

func failAdmission(w http.ResponseWriter, locale string, request model.Request, err error) {
	var rejection *model.TaskRejection
	if errors.As(err, &rejection) && rejection.Matches(request) {
		out := *rejection
		out.MessageArgs = i18n.CloneArgs(rejection.MessageArgs)
		if out.MessageKey != "" {
			out.Message = i18n.LocalizeMessage(locale, out.MessageKey, out.MessageArgs, out.Message)
		}
		send(w, http.StatusConflict, &out)
		return
	}
	failLocale(w, locale, http.StatusConflict, err)
}

// The loopback outbox shares the configuration transaction lock with HTTP
// admission and settings. Engine/Store never reach back into this lock.
func (a *App) acceptLocal(request model.Request) (model.Job, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Engine.Accept(a.Config.Get().NodeID, request)
}
