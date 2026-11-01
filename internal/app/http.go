package app

import (
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/network"
	"github.com/jackgan90/Asterhand/internal/peer"
	"github.com/jackgan90/Asterhand/internal/tasks"
	"github.com/jackgan90/Asterhand/web"
)

func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func requestLocaleFor(r *http.Request, fallback string) string {
	if r != nil {
		if hdr := strings.TrimSpace(r.Header.Get("X-Asterhand-Locale")); i18n.ValidLocale(hdr) {
			return hdr
		}
	}
	if i18n.ValidLocale(fallback) {
		return fallback
	}
	return i18n.Effective()
}

func (a *App) requestLocale(r *http.Request) string {
	fallback := ""
	if a != nil && a.Input != nil {
		fallback = a.Input.Status().EffectiveLocale
	}
	return requestLocaleFor(r, fallback)
}

func errorPayload(locale string, err error) map[string]any {
	msg, key, args := i18n.LocalizeError(locale, err)
	payload := map[string]any{"error": msg}
	if key != "" {
		payload["message_key"] = key
	}
	if len(args) > 0 {
		payload["message_args"] = args
	}
	return payload
}

func failLocale(w http.ResponseWriter, locale string, status int, err error) {
	send(w, status, errorPayload(locale, err))
}

func fail(w http.ResponseWriter, status int, err error) {
	failLocale(w, i18n.Effective(), status, err)
}

func (a *App) fail(w http.ResponseWriter, r *http.Request, status int, err error) {
	failLocale(w, a.requestLocale(r), status, err)
}

func localizeJob(locale string, j model.Job) model.Job {
	j.ErrorArgs = i18n.CloneArgs(j.ErrorArgs)
	if j.ErrorKey != "" {
		j.Error = i18n.LocalizeMessage(locale, j.ErrorKey, j.ErrorArgs, j.Error)
	}
	return j
}

func localizeOutbox(locale string, o model.Outbox) model.Outbox {
	o.ErrorArgs = i18n.CloneArgs(o.ErrorArgs)
	if o.ErrorKey != "" {
		o.Error = i18n.LocalizeMessage(locale, o.ErrorKey, o.ErrorArgs, o.Error)
	}
	if o.Job != nil {
		copyJob := localizeJob(locale, *o.Job)
		o.Job = &copyJob
	}
	return o
}
func read(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	return d.Decode(v)
}
func bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}
func securityHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
}
func (a *App) local(w http.ResponseWriter, r *http.Request) {
	securityHeaders(w)
	if r.Host != a.webAddress {
		a.fail(w, r, 403, i18n.Err("err.http.invalid_host"))
		return
	}
	if r.URL.Path == "/health" && r.Method == "GET" {
		locale := i18n.Effective()
		localeMode := "auto"
		sysDetected := false
		if a.Input != nil {
			st := a.Input.Status()
			if i18n.ValidLocale(st.EffectiveLocale) {
				locale = st.EffectiveLocale
			}
			if st.Desired.Locale != "" {
				localeMode = st.Desired.Locale
			}
			sysDetected = st.SystemLocaleDetected
		} else {
			_, sysDetected = i18n.ResolveLocale("auto")
		}
		send(w, 200, map[string]any{
			"node_id":                a.Config.Get().NodeID,
			"pid":                    os.Getpid(),
			"locale":                 locale,
			"locale_mode":            localeMode,
			"system_locale_detected": sysDetected,
		})
		return
	}
	if r.URL.Path == "/internal/tool" {
		a.tool(w, r)
		return
	}
	if r.URL.Path == "/api/login" {
		a.login(w, r)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".js") || strings.HasSuffix(r.URL.Path, ".mjs") {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		}
		http.FileServer(http.FS(web.Files)).ServeHTTP(w, r)
		return
	}
	s, ok := a.authorize(r)
	if !ok {
		a.fail(w, r, 401, i18n.Err("err.http.login_link_required"))
		return
	}
	if r.Method != "GET" && (r.Header.Get("Origin") != "http://"+a.webAddress || r.Header.Get("X-Asterhand-CSRF") != s.csrf) {
		a.fail(w, r, 403, i18n.Err("err.http.origin_failed"))
		return
	}
	if r.URL.Path == "/api/session" {
		send(w, 200, map[string]string{"csrf": s.csrf})
		return
	}
	a.api(w, r)
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.Header.Get("Origin") != "http://"+a.webAddress {
		a.fail(w, r, 403, i18n.Err("err.http.local_login_required"))
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if err := read(w, r, &input); err != nil {
		a.fail(w, r, 400, err)
		return
	}
	if subtle.ConstantTimeCompare([]byte(input.Token), []byte(a.loginToken)) != 1 {
		a.fail(w, r, 401, i18n.Err("err.http.invalid_token"))
		return
	}
	id := model.ID()
	s := session{expires: time.Now().Add(12 * time.Hour), csrf: model.ID()}
	a.mu.Lock()
	for k, v := range a.sessions {
		if time.Now().After(v.expires) {
			delete(a.sessions, k)
		}
	}
	a.sessions[id] = s
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "asterhand_session", Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	send(w, 200, map[string]string{"csrf": s.csrf})
}
func (a *App) authorize(r *http.Request) (session, bool) {
	c, err := r.Cookie("asterhand_session")
	if err != nil {
		return session{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[c.Value]
	return s, ok && time.Now().Before(s.expires)
}
func (a *App) tool(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if r.Method != "POST" || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		w.WriteHeader(403)
		return
	}
	var call tasks.ToolCall
	if err := read(w, r, &call); err != nil {
		a.fail(w, r, 400, err)
		return
	}
	token := bearer(r)
	a.mu.Lock()
	_, isProbe := a.probeTokens[token]
	if isProbe && call.Tool == "asterhand_probe" {
		a.probeTokens[token] = true
		a.mu.Unlock()
		send(w, 200, map[string]string{"status": "ready", "node_id": a.Config.Get().NodeID})
		return
	}
	a.mu.Unlock()
	result, err := a.Engine.Tool(r.Context(), token, call)
	if err != nil {
		a.fail(w, r, 409, err)
		return
	}
	send(w, 200, result)
}
func (a *App) remote(w http.ResponseWriter, r *http.Request) {
	securityHeaders(w)
	if r.URL.Path == "/v1/pair" && r.Method == "POST" {
		var input peer.PairRequest
		if err := read(w, r, &input); err != nil {
			a.fail(w, r, 400, err)
			return
		}
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() && network.LoopbackEndpoint(input.Node.Endpoint) {
			a.fail(w, r, 400, i18n.Err("err.http.pair_loopback_forbidden"))
			return
		}
		result, err := a.Peers.AcceptPair(input)
		if err != nil {
			a.fail(w, r, 403, err)
			return
		}
		send(w, 200, result)
		return
	}
	p, err := a.Peers.Authenticate(bearer(r))
	if err != nil {
		a.fail(w, r, 401, err)
		return
	}
	if r.URL.Path == "/v1/info" && r.Method == "GET" {
		send(w, 200, a.infoFor(p.Projects, a.requestLocale(r)))
		return
	}
	if r.URL.Path == "/v1/metadata" {
		a.receiveMetadata(w, r, p)
		return
	}
	if r.URL.Path == "/v1/jobs" && r.Method == "POST" {
		var req model.Request
		if err := read(w, r, &req); err != nil {
			a.fail(w, r, 400, err)
			return
		}
		if !contains(p.Projects, req.ProjectID) || req.Mode != "execute" {
			a.fail(w, r, 403, i18n.Err("err.http.peer_unauthorized_project"))
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		j, err := a.Engine.Accept(p.ID, req)
		if err != nil {
			failAdmission(w, a.requestLocale(r), req, err)
			return
		}
		send(w, 202, localizeJob(a.requestLocale(r), j))
		return
	}
	if r.URL.Path == "/v1/cancel-request" && r.Method == "POST" {
		var input struct {
			RequestID string `json:"request_id"`
		}
		if err := read(w, r, &input); err != nil {
			a.fail(w, r, 400, err)
			return
		}
		if len(input.RequestID) < 8 {
			a.fail(w, r, 400, i18n.Err("err.http.invalid_request_id"))
			return
		}
		if err := a.Store.Cancel(p.ID, input.RequestID); err != nil {
			a.fail(w, r, 500, err)
			return
		}
		var jobs []model.Job
		if err := a.Store.List("job", &jobs); err != nil {
			a.fail(w, r, 500, err)
			return
		}
		for _, j := range jobs {
			if j.Origin == p.ID && j.Request.RequestID == input.RequestID && !model.Terminal(j.State) {
				a.Engine.Cancel(j.ID)
			}
		}
		send(w, 200, map[string]bool{"registered": true})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/jobs/") {
		a.jobAPI(w, r, p.ID, false)
		return
	}
	http.NotFound(w, r)
}
func contains(items []string, value string) bool {
	for _, s := range items {
		if s == value {
			return true
		}
	}
	return false
}
func (a *App) jobAPI(w http.ResponseWriter, r *http.Request, actor string, local bool) {
	reqLocale := a.requestLocale(r)
	path := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/jobs/"), "/v1/jobs/")
	parts := strings.Split(path, "/")
	var j model.Job
	if err := a.Store.Get("job", parts[0], &j); err != nil {
		a.fail(w, r, 404, err)
		return
	}
	if !local && j.Origin != actor {
		a.fail(w, r, 403, i18n.Err("err.http.job_origin_only"))
		return
	}
	if len(parts) == 1 && r.Method == "GET" {
		send(w, 200, localizeJob(reqLocale, j))
		return
	}
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	switch parts[1] {
	case "events":
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		if err != nil || after < 0 || after > j.Seq {
			a.fail(w, r, 409, i18n.Err("err.http.invalid_cursor_resync"))
			return
		}
		events, err := a.Store.Events(j.ID, after)
		if err != nil {
			a.fail(w, r, 500, err)
			return
		}
		send(w, 200, map[string]any{"events": events, "watermark": j.Seq})
	case "cancel":
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		j, err := a.Engine.Cancel(j.ID)
		if err != nil {
			a.fail(w, r, 409, err)
			return
		}
		send(w, 202, localizeJob(reqLocale, j))
	case "answer":
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var input struct {
			InteractionID string `json:"interaction_id"`
			Attempt       string `json:"attempt"`
			AnswerID      string `json:"answer_id"`
			Answer        string `json:"answer"`
		}
		if err := read(w, r, &input); err != nil {
			a.fail(w, r, 400, err)
			return
		}
		j, err := a.Engine.Answer(j.ID, input.InteractionID, input.Attempt, input.AnswerID, input.Answer, actor)
		if err != nil {
			a.fail(w, r, 409, err)
			return
		}
		send(w, 200, localizeJob(reqLocale, j))
	case "resolve":
		if !local || r.Method != "POST" {
			w.WriteHeader(403)
			return
		}
		var input struct {
			Reason string `json:"reason"`
		}
		if err := read(w, r, &input); err != nil {
			a.fail(w, r, 400, err)
			return
		}
		j, err := a.Engine.Resolve(j.ID, input.Reason)
		if err != nil {
			a.fail(w, r, 409, err)
			return
		}
		send(w, 200, localizeJob(reqLocale, j))
	default:
		http.NotFound(w, r)
	}
}
