package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/jackgan90/Asterhand/internal/i18n"
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
	case "/api/desktop/recording/start":
		var input struct {
			Page string `json:"page_id"`
		}
		if err = readDesktop(w, r, &input); err == nil {
			s, _ := a.authorize(r)
			if len(input.Page) < 8 || len(input.Page) > 100 {
				err = desktopErr(400, "err.app.invalid_page_id")
			} else {
				result, err = a.Input.StartRecording(s.csrf+":"+input.Page, s.expires)
			}
		}
	case "/api/desktop/recording/poll", "/api/desktop/recording/cancel":
		var input struct {
			Page string `json:"page_id"`
			ID   string `json:"recording_id"`
		}
		if err = readDesktop(w, r, &input); err == nil {
			s, _ := a.authorize(r)
			result, err = a.Input.Recording(input.ID, s.csrf+":"+input.Page, r.URL.Path == "/api/desktop/recording/cancel")
		}
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
