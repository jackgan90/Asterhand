package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/jackgan90/Asterhand/internal/quickinput"
)

// This router inherits local Host, session, Origin and CSRF checks. It never
// routes through general node settings or peer metadata.
func (a *App) desktopAPI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/desktop/settings" && r.Method == http.MethodGet {
		send(w, 200, a.Input.Status())
		return
	}
	select {
	case a.desktopRequests <- struct{}{}:
		defer func() { <-a.desktopRequests }()
	default:
		fail(w, 429, errors.New("桌面设置操作过多，请稍后重试"))
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
			result, err = a.Input.Apply(r.Context(), change)
		}
	case "/api/desktop/check":
		var input struct {
			Binding quickinput.Binding `json:"binding"`
		}
		if err = readDesktop(w, r, &input); err == nil {
			err = a.Input.Check(input.Binding)
			result = a.Input.Status()
		}
	case "/api/desktop/retry":
		var input struct{}
		if err = readDesktop(w, r, &input); err == nil {
			result, err = a.Input.Retry()
		}
	case "/api/desktop/recording/start":
		var input struct {
			Page string `json:"page_id"`
		}
		if err = readDesktop(w, r, &input); err == nil {
			s, _ := a.authorize(r)
			if len(input.Page) < 8 || len(input.Page) > 100 {
				err = &quickinput.Error{Status: 400, Message: "页面 ID 无效"}
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
		status := 503
		var inputError *quickinput.Error
		var registrationError *quickinput.RegistrationError
		if errors.As(err, &inputError) {
			status = inputError.Status
		}
		if errors.As(err, &registrationError) && registrationError.Code == "conflict" {
			status = 409
		}
		send(w, status, map[string]any{"error": err.Error(), "desktop": a.Input.Status()})
		return
	}
	send(w, 200, result)
}

func readDesktop(w http.ResponseWriter, r *http.Request, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return &quickinput.Error{Status: 400, Message: err.Error()}
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return &quickinput.Error{Status: 400, Message: "请求包含多余内容"}
	}
	return nil
}
