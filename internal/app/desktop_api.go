package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/launch"
	"github.com/jackgan90/Asterhand/internal/quickinput"
)

func desktopErr(status int, key string) *quickinput.Error {
	return &quickinput.Error{Status: status, Message: i18n.T(i18n.Effective(), key), Key: key}
}

// This router inherits local Host, session, Origin and CSRF checks. It never
// routes through general node settings or peer metadata.
func (a *App) desktopAPI(w http.ResponseWriter, r *http.Request) {
	reqLocale := a.requestLocale(r)
	if r.URL.Path == "/api/desktop/settings" && r.Method == http.MethodGet {
		send(w, 200, a.Input.Status().Localize(reqLocale))
		return
	}
	select {
	case a.desktopRequests <- struct{}{}:
		defer func() { <-a.desktopRequests }()
	default:
		a.fail(w, r, 429, i18n.Err("err.app.desktop_rate_limited"))
		return
	}
	if r.URL.Path == "/api/desktop/shortcut" {
		a.desktopShortcutAPI(w, r, reqLocale)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var result any
	var err error
	switch r.URL.Path {
	case "/api/desktop/settings":
		var change quickinput.Change
		if err = readDesktop(w, r, &change); err == nil {
			var st quickinput.Status
			st, err = a.Input.Apply(r.Context(), change)
			if err == nil {
				a.notifyRefresh()
				a.updateDesktopAppearance(st)
				outLocale := reqLocale
				if change.Locale != nil {
					outLocale = st.EffectiveLocale
				}
				result = st.Localize(outLocale)
			}
		}
	case "/api/desktop/locale":
		var change quickinput.LocaleChange
		if err = readDesktop(w, r, &change); err == nil {
			var st quickinput.Status
			st, err = a.Input.SetLocale(r.Context(), change)
			if err == nil {
				a.notifyRefresh()
				a.updateDesktopAppearance(st)
				result = st.Localize(st.EffectiveLocale)
			}
		}
	case "/api/desktop/check":
		var input struct {
			Binding quickinput.Binding `json:"binding"`
		}
		if err = readDesktop(w, r, &input); err == nil {
			err = a.Input.Check(input.Binding)
			result = a.Input.Status().Localize(reqLocale)
		}
	case "/api/desktop/retry":
		var input struct{}
		if err = readDesktop(w, r, &input); err == nil {
			var st quickinput.Status
			st, err = a.Input.Retry()
			if err == nil {
				result = st.Localize(reqLocale)
			}
		}
	case "/api/desktop/recording/start", "/api/desktop/recording/poll", "/api/desktop/recording/cancel":
		result, err = a.desktopRecordingAPI(w, r)
	default:
		w.WriteHeader(404)
		return
	}
	if err != nil {
		a.sendDesktopError(w, reqLocale, err)
		return
	}
	send(w, 200, result)
}

func (a *App) desktopRecordingAPI(w http.ResponseWriter, r *http.Request) (any, error) {
	if r.URL.Path == "/api/desktop/recording/start" {
		var input struct {
			Page string `json:"page_id"`
		}
		if err := readDesktop(w, r, &input); err != nil {
			return nil, err
		}
		s, _ := a.authorize(r)
		if len(input.Page) < 8 || len(input.Page) > 100 {
			return nil, desktopErr(400, "err.app.invalid_page_id")
		}
		return a.Input.StartRecording(s.csrf+":"+input.Page, s.expires)
	}
	var input struct {
		Page string `json:"page_id"`
		ID   string `json:"recording_id"`
	}
	if err := readDesktop(w, r, &input); err != nil {
		return nil, err
	}
	s, _ := a.authorize(r)
	return a.Input.Recording(input.ID, s.csrf+":"+input.Page, r.URL.Path == "/api/desktop/recording/cancel")
}

func (a *App) desktopShortcutAPI(w http.ResponseWriter, r *http.Request, reqLocale string) {
	if r.Method == http.MethodGet {
		send(w, 200, a.shortcutStatus().Localize(reqLocale))
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	if a.Stopping() || a.Shortcut == nil {
		a.sendShortcutError(w, reqLocale, launch.NewShortcutError(503, "node_stopping", "err.app.node_stopping", nil))
		return
	}
	var change launch.ShortcutChange
	if err := readDesktop(w, r, &change); err != nil {
		a.sendShortcutError(w, reqLocale, err)
		return
	}
	st, err := a.Shortcut.Set(r.Context(), change)
	if err != nil {
		a.sendShortcutError(w, reqLocale, err)
		return
	}
	send(w, 200, st.Localize(reqLocale))
}

func (a *App) sendShortcutError(w http.ResponseWriter, reqLocale string, err error) {
	status := 503
	var shortcutErr *launch.ShortcutError
	var inputError *quickinput.Error
	if errors.As(err, &shortcutErr) {
		status = shortcutErr.Status
	} else if errors.As(err, &inputError) {
		status = inputError.Status
	}
	payload := errorPayload(reqLocale, err)
	payload["shortcut"] = a.shortcutStatus().Localize(reqLocale)
	send(w, status, payload)
}

func (a *App) sendDesktopError(w http.ResponseWriter, reqLocale string, err error) {
	status := 503
	var inputError *quickinput.Error
	var registrationError *quickinput.RegistrationError
	if errors.As(err, &inputError) {
		status = inputError.Status
	}
	if errors.As(err, &registrationError) && registrationError.Code == "conflict" {
		status = 409
	}
	payload := errorPayload(reqLocale, err)
	payload["desktop"] = a.Input.Status().Localize(reqLocale)
	send(w, status, payload)
}

func readDesktop(w http.ResponseWriter, r *http.Request, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return &quickinput.Error{Status: 400, Message: err.Error()}
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return desktopErr(400, "err.http.extra_body")
	}
	return nil
}
