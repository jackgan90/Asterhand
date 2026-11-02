package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	"github.com/jackgan90/Asterhand/internal/desktop"
	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/localcommands"
	commandswire "github.com/jackgan90/Asterhand/internal/localcommands/wire"
)

// commandsAPI is only reachable after the normal local session/Origin/CSRF
// checks. There is deliberately no HTTP launch route or remote API equivalent.
func (a *App) commandsAPI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/local-commands/browse" && r.Method == http.MethodPost {
		a.browseCommandScript(w, r)
		return
	}
	var result any
	var err error
	switch {
	case r.URL.Path == "/api/local-commands" && r.Method == http.MethodGet:
		result, err = a.Commands.Snapshot()
	case r.URL.Path == "/api/local-commands/state" && r.Method == http.MethodGet:
		result = map[string]any{"runs": a.Commands.RunStates(), "managed_available": a.Commands.ManagedAvailable()}
	case (r.URL.Path == "/api/local-commands/stop" || r.URL.Path == "/api/local-commands/logs") && r.Method == http.MethodPost:
		var input struct {
			ID    string `json:"id"`
			RunID string `json:"run_id,omitempty"`
		}
		if err = readCommand(w, r, &input); err == nil {
			if r.URL.Path == "/api/local-commands/stop" {
				result, err = a.Commands.StopRun(input.ID, input.RunID)
			} else {
				err = a.openCommandLogs(r.Context(), input.ID)
				result = map[string]bool{"opened": err == nil}
			}
		}
	case r.URL.Path == "/api/local-commands" && r.Method == http.MethodPost:
		var input localcommands.Mutation
		if err = readCommand(w, r, &input); err == nil {
			result, err = a.Commands.Save(input)
		}
	case r.URL.Path == "/api/local-commands/delete" && r.Method == http.MethodPost:
		var input struct {
			ID       string `json:"id"`
			Revision string `json:"revision"`
		}
		if err = readCommand(w, r, &input); err == nil {
			result, err = a.Commands.Delete(input.ID, input.Revision)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err != nil {
		public := localcommands.Failure(err).Error
		status := http.StatusBadRequest
		if public.Code == "conflict" || public.Code == "name_conflict" {
			status = http.StatusConflict
		}
		if public.Code == "configuration" || public.Code == "unavailable" {
			status = http.StatusServiceUnavailable
		}
		msg := i18n.LocalizeMessage(a.requestLocale(r), public.MessageKey, public.MessageArgs, public.Message)
		payload := map[string]any{"error": msg, "code": public.Code}
		if public.MessageKey != "" {
			payload["message_key"] = public.MessageKey
		}
		if len(public.MessageArgs) > 0 {
			payload["message_args"] = i18n.CloneArgs(public.MessageArgs)
		}
		send(w, status, payload)
		return
	}
	send(w, http.StatusOK, result)
}

func readCommand(w http.ResponseWriter, r *http.Request, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 32*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return i18n.Err("err.http.extra_body")
	}
	return nil
}

func (a *App) commandCall(ctx context.Context, request commandswire.Request) commandswire.Response {
	if err := request.Validate(); err != nil {
		return localcommands.Failure(err)
	}
	a.mu.Lock()
	stopping := a.stopping
	address := a.webAddress
	a.mu.Unlock()
	if stopping {
		return localcommands.Failure(i18n.Err("err.app.node_stopping"))
	}
	switch request.Action {
	case "list":
		return a.Commands.List(request)
	case "launch":
		return a.Commands.Launch(ctx, request.ID, request.Revision)
	case "status":
		data, _ := json.Marshal(a.Commands.CurrentRun(request.ID))
		return commandswire.Response{Run: data}
	case "stop":
		run, err := a.Commands.StopRun(request.ID, request.RunID)
		if err != nil {
			return localcommands.Failure(err)
		}
		data, _ := json.Marshal(run)
		return commandswire.Response{Run: data}
	case "read_log", "tail_log":
		cursor := request.Cursor
		if request.Action == "tail_log" {
			cursor = -1
		}
		page, err := a.Commands.ReadLog(request.ID, request.RunID, cursor)
		if err != nil {
			return localcommands.Failure(err)
		}
		data, _ := json.Marshal(page)
		return commandswire.Response{Log: data}
	case "logs":
		if err := a.openCommandLogs(ctx, request.ID); err != nil {
			return localcommands.Failure(err)
		}
		return commandswire.Response{}
	case "settings":
		if address == "" {
			return localcommands.Failure(i18n.Err("err.app.web_not_ready"))
		}
		fragment := url.Values{"login": {a.loginToken}, "commands": {request.ID}}
		if err := desktop.Open("http://" + address + "/#" + fragment.Encode()); err != nil {
			return localcommands.Failure(err)
		}
	}
	return commandswire.Response{}
}

func (a *App) openCommandLogs(ctx context.Context, id string) error {
	if !commandswire.ValidID(id) {
		return i18n.Err("err.localcmd.invalid_wire_req")
	}
	if a.Commands.CurrentRun(id) == nil {
		return i18n.Err("err.localcmd.no_run")
	}
	if a.CommandLogs == nil {
		return i18n.Err("err.localcmd.log_window_failed")
	}
	return a.CommandLogs.Open(ctx, id)
}
