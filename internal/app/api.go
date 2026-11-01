package app

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/executor"
	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/llm"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/network"
)

func decode(b []byte, v any) error { return json.Unmarshal(b, v) }

func applySettingsNames(before, c config.Config, name string, nameEdited *bool, root string) config.Config {
	if before.NameDefault && ((nameEdited != nil && !*nameEdited) || (nameEdited == nil && name == before.Name)) {
		c.Name = before.Name
		c.NameDefault = true
	} else {
		c.Name = name
		c.NameDefault = false
	}
	c.Projects = nil
	if root != "" {
		proj := config.DefaultWorkspaceProject(root)
		for _, prev := range before.Projects {
			if prev.ID == "workspace" {
				proj.Name = prev.Name
				proj.NameCustom = prev.NameCustom
				break
			}
		}
		c.Projects = []config.Project{proj}
	}
	return c
}
func (a *App) info(projects []string) map[string]any {
	return a.infoFor(projects, i18n.Effective())
}
func (a *App) infoFor(projects []string, locale string) map[string]any {
	if !i18n.ValidLocale(locale) {
		locale = i18n.Effective()
	}
	c := a.Config.Get()
	visible := []config.Project{}
	for _, p := range c.Projects {
		if projects == nil || contains(projects, p.ID) {
			visible = append(visible, p)
		}
	}
	return map[string]any{
		"node_id":             c.NodeID,
		"name":                c.Name,
		"name_default":        c.NameDefault,
		"version":             config.Version,
		"ready":               c.ModelVerified,
		"capabilities":        executor.ConfiguredCapabilities(a.Config, c).Localize(locale),
		"projects":            visible,
		"endpoint":            c.Endpoint,
		"model":               c.LLM.Model,
		"agent_runtime":       c.AgentRuntime,
		"source_sha256":       config.SourceDigest,
		"bundle_identity":     executor.BundleIdentity(),
		"network_warning":     network.ConnectionWarningFor(locale, c.Endpoint, c.PeerListen, a.peerAddress),
		"network_warning_key": network.ConnectionWarningKey(c.Endpoint, c.PeerListen, a.peerAddress),
		"restart_required":    !network.ListenerMatches(c.PeerListen, a.peerAddress),
	}
}
func (a *App) api(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/local-commands") {
		a.commandsAPI(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/desktop/") {
		a.desktopAPI(w, r)
		return
	}
	reqLocale := a.requestLocale(r)
	switch r.URL.Path {
	case "/api/info":
		send(w, 200, a.infoFor(nil, reqLocale))
	case "/api/stop":
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		send(w, 202, map[string]bool{"stopping": true})
		time.AfterFunc(100*time.Millisecond, func() { a.RequestStop(true) })
	case "/api/network":
		_, port, _ := net.SplitHostPort(a.Config.Get().PeerListen)
		send(w, 200, network.Inspect(r.Context(), port).Localize(reqLocale))
	case "/api/settings":
		a.settings(w, r)
	case "/api/probe":
		a.probe(w, r)
	case "/api/peers":
		a.peersAPI(w, r)
	case "/api/invite":
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		code, err := a.Peers.Invite()
		if err != nil {
			a.fail(w, r, 400, err)
			return
		}
		send(w, 200, map[string]string{"code": code})
	case "/api/pair":
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		if !network.ListenerMatches(a.Config.Get().PeerListen, a.peerAddress) {
			a.fail(w, r, 409, i18n.Err("err.app.listen_pending_restart"))
			return
		}
		var input struct {
			Code string `json:"code"`
		}
		if err := read(w, r, &input); err != nil {
			a.fail(w, r, 400, err)
			return
		}
		p, err := a.Peers.Pair(r.Context(), input.Code)
		if err != nil {
			a.fail(w, r, 400, err)
			return
		}
		a.requestPeerSync()
		send(w, 200, map[string]string{"id": p.ID, "name": p.Name})
	case "/api/jobs":
		a.jobsAPI(w, r)
	case "/api/outbox":
		var out []model.Outbox
		if err := a.Store.List("outbox", &out); err != nil {
			a.fail(w, r, 500, err)
			return
		}
		for idx := range out {
			out[idx] = localizeOutbox(reqLocale, out[idx])
		}
		send(w, 200, out)
	case "/api/stream":
		a.stream(w, r)
	default:
		if strings.HasPrefix(r.URL.Path, "/api/jobs/") {
			a.jobAPI(w, r, "local", true)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/remote/") {
			a.remoteAPI(w, r)
			return
		}
		http.NotFound(w, r)
	}
}
func (a *App) settings(w http.ResponseWriter, r *http.Request) {
	c := a.Config.Get()
	if r.Method == "GET" {
		_, err := a.Config.Key()
		send(w, 200, map[string]any{"config": c, "has_key": err == nil, "home": a.Config.Home, "providers": llm.Available(), "runtimes": executor.Available()})
		return
	}
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	c = a.Config.Get()
	before := c
	var input struct {
		Name         string         `json:"name"`
		NameEdited   *bool          `json:"name_edited,omitempty"`
		Endpoint     string         `json:"endpoint"`
		PeerListen   *string        `json:"peer_listen"`
		Model        string         `json:"model"` // Legacy clients.
		LLM          *llm.Selection `json:"llm"`
		AgentRuntime string         `json:"agent_runtime"`
		Root         string         `json:"root"`
		Key          string         `json:"key"`
		DeleteKey    bool           `json:"delete_key"`
	}
	if err := read(w, r, &input); err != nil {
		a.fail(w, r, 400, err)
		return
	}
	if strings.TrimSpace(input.Name) == "" || len(input.Name) > 80 {
		a.fail(w, r, 400, i18n.Err("err.app.name_required"))
		return
	}
	if input.PeerListen != nil {
		c.PeerListen = strings.TrimSpace(*input.PeerListen)
		if err := network.ValidateListener(c.PeerListen); err != nil {
			a.fail(w, r, 400, err)
			return
		}
	}
	if input.LLM != nil {
		c.LLM = *input.LLM
	} else if input.Model != "" {
		c.LLM.Model = input.Model
	}
	if input.AgentRuntime != "" {
		c.AgentRuntime = input.AgentRuntime
	}
	if err := executor.ValidateSelection(c); err != nil {
		a.fail(w, r, 400, err)
		return
	}
	if input.Root != "" && !filepath.IsAbs(input.Root) {
		a.fail(w, r, 400, i18n.Err("err.app.root_must_be_abs"))
		return
	}
	root := ""
	var err error
	if input.Root != "" {
		root, err = filepath.Abs(input.Root)
		if err == nil {
			err = executor.ValidateWorkspace(c, a.Config.Home, root)
		}
		if err != nil {
			a.fail(w, r, 400, err)
			return
		}
	}
	var jobs []model.Job
	if err = a.Store.List("job", &jobs); err != nil {
		a.fail(w, r, 500, err)
		return
	}
	for _, j := range jobs {
		if !model.Terminal(j.State) {
			a.fail(w, r, 409, i18n.Err("err.app.busy_tasks_before_settings"))
			return
		}
	}
	if a.probeRunning {
		a.fail(w, r, 409, i18n.Err("err.app.wait_probe"))
		return
	}
	if a.stopping {
		a.fail(w, r, 503, i18n.Err("err.app.node_stopping"))
		return
	}
	chatChanged := input.Key != "" || input.DeleteKey || model.Hash(before.LLM) != model.Hash(c.LLM) || before.AgentRuntime != c.AgentRuntime
	if chatChanged {
		release, guardErr := a.Chat.BeginConfiguration()
		if guardErr != nil {
			a.fail(w, r, 409, guardErr)
			return
		}
		defer release()
	}
	if root != "" {
		if err = os.MkdirAll(root, 0700); err != nil {
			a.fail(w, r, 400, err)
			return
		}
	}
	if input.Endpoint != "" {
		u, parseErr := url.Parse(input.Endpoint)
		if parseErr != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			a.fail(w, r, 400, i18n.Err("err.app.endpoint_format"))
			return
		}
	}
	c = applySettingsNames(before, c, input.Name, input.NameEdited, root)
	c.Endpoint = input.Endpoint
	c.Configured = true
	if input.Key != "" || input.DeleteKey {
		if input.DeleteKey {
			input.Key = ""
		}
		if err = a.Config.SetCredential(c.LLM.CredentialRef, input.Key); err != nil {
			a.fail(w, r, 500, i18n.Err("err.app.credential_save_failed"))
			return
		}
	}
	workspaceChanged := len(before.Projects) != len(c.Projects) || len(before.Projects) == 1 && (before.Projects[0].ID != "workspace" || before.Projects[0].Root != root)
	diagnosticChanged := input.Key != "" || input.DeleteKey || workspaceChanged || executor.ExecutionIdentity(before) != executor.ExecutionIdentity(c)
	if diagnosticChanged {
		c.InvalidateModelDiagnostic()
	}
	if err = a.Config.Save(c); err != nil {
		a.fail(w, r, 500, err)
		return
	}
	if diagnosticChanged {
		a.probeStatus, a.probeError, a.probeErrorKey, a.probeErrorArgs = string(c.DiagnosticStatus()), "", "", nil
	}
	a.requestPeerSync()
	send(w, 200, map[string]any{"saved": true, "ready": c.ModelVerified, "capabilities": executor.ConfiguredCapabilities(a.Config, c).Localize(a.requestLocale(r)), "restart_required": !network.ListenerMatches(c.PeerListen, a.peerAddress)})
}
func (a *App) probe(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	if r.Method == "GET" {
		state, detail := a.probeStatus, a.probeError
		errKey, errArgs := a.probeErrorKey, i18n.CloneArgs(a.probeErrorArgs)
		if !a.probeRunning && state != "failed" {
			state, detail, errKey, errArgs = string(a.Config.Get().DiagnosticStatus()), "", "", nil
		}
		a.mu.Unlock()
		if errKey != "" {
			detail = i18n.LocalizeMessage(a.requestLocale(r), errKey, errArgs, detail)
		}
		payload := map[string]any{"state": state, "error": detail}
		if errKey != "" {
			payload["message_key"] = errKey
		}
		if len(errArgs) > 0 {
			payload["message_args"] = errArgs
		}
		send(w, 200, payload)
		return
	}
	if r.Method != "POST" {
		a.mu.Unlock()
		w.WriteHeader(405)
		return
	}
	if a.probeRunning {
		a.mu.Unlock()
		a.fail(w, r, 409, i18n.Err("err.app.probe_running"))
		return
	}
	if a.stopping {
		a.mu.Unlock()
		a.fail(w, r, 503, i18n.Err("err.app.node_stopping"))
		return
	}
	a.probeRunning = true
	a.probeStatus = "running"
	a.probeError = ""
	a.probeErrorKey = ""
	a.probeErrorArgs = nil
	token := model.ID()
	a.probeTokens[token] = false
	a.mu.Unlock()
	a.wg.Add(1)
	go func() {
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
		c.ModelVerified = err == nil
		c.ModelDiagnostic = config.DiagnosticFailed
		if err == nil {
			c.ModelDiagnostic = config.DiagnosticPassed
			c.VerifiedBundle = executor.BundleIdentity()
			c.VerifiedExecution = executor.ExecutionIdentity(c)
		}
		if saveErr := a.Config.Save(c); saveErr != nil {
			err = saveErr
		}
		a.probeRunning = false
		a.probeStatus = "passed"
		a.probeError = ""
		a.probeErrorKey = ""
		a.probeErrorArgs = nil
		if err != nil {
			msg, key, args := i18n.LocalizeError(i18n.Effective(), err)
			a.probeStatus = "failed"
			a.probeError = msg
			a.probeErrorKey = key
			a.probeErrorArgs = args
		}
		a.mu.Unlock()
	}()
	send(w, 202, map[string]string{"state": "running"})
}
func localizePeerInfo(locale string, info map[string]any) map[string]any {
	if info == nil {
		return nil
	}
	out := maps.Clone(info)
	if warnKey, ok := out["network_warning_key"].(string); ok && warnKey != "" {
		fallback, _ := out["network_warning"].(string)
		out["network_warning"] = i18n.LocalizeMessage(locale, warnKey, nil, fallback)
	}
	if caps, ok := out["capabilities"].(map[string]any); ok {
		capsCopy := maps.Clone(caps)
		for _, field := range []string{"chat", "model_tasks"} {
			entry, ok := capsCopy[field].(map[string]any)
			if !ok {
				continue
			}
			entryCopy := maps.Clone(entry)
			if msgKey, ok := entryCopy["message_key"].(string); ok && msgKey != "" {
				var args map[string]string
				if rawArgs, ok := entryCopy["message_args"].(map[string]any); ok {
					args = make(map[string]string, len(rawArgs))
					for k, v := range rawArgs {
						if s, ok := v.(string); ok {
							args[k] = s
						}
					}
				}
				fallback, _ := entryCopy["detail"].(string)
				entryCopy["detail"] = i18n.LocalizeMessage(locale, msgKey, args, fallback)
			}
			capsCopy[field] = entryCopy
		}
		out["capabilities"] = capsCopy
	}
	return out
}
func (a *App) peersAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == "DELETE" {
		var input struct {
			ID string `json:"id"`
		}
		if err := read(w, r, &input); err != nil {
			a.fail(w, r, 400, err)
			return
		}
		var p model.Peer
		if err := a.Store.Get("peer", input.ID, &p); err != nil {
			a.fail(w, r, 404, err)
			return
		}
		p.Revoked = true
		if err := a.Store.Put("peer", p.ID, p); err != nil {
			a.fail(w, r, 500, err)
			return
		}
		send(w, 200, map[string]bool{"revoked": true})
		return
	}
	if r.Method != "GET" {
		w.WriteHeader(405)
		return
	}
	peers, err := a.Peers.List()
	if err != nil {
		a.fail(w, r, 500, err)
		return
	}
	reqLocale := a.requestLocale(r)
	result := []map[string]any{}
	for _, p := range peers {
		row := map[string]any{"id": p.ID, "name": p.Name, "endpoint": p.Endpoint, "revoked": p.Revoked, "online": false}
		if !p.Revoked {
			var cached struct {
				Info    map[string]any `json:"info"`
				Checked string         `json:"checked"`
			}
			if a.Store.Get("peer-info", p.ID, &cached) == nil {
				row["info"] = localizePeerInfo(reqLocale, cached.Info)
				row["checked"] = cached.Checked
			}
			ctx, cancel := context.WithTimeout(r.Context(), peerProbeTimeout)
			info, err := a.Peers.Refresh(ctx, p.ID, "")
			cancel()
			if err == nil {
				row["name"], row["endpoint"] = info["name"], info["endpoint"]
				row["online"] = true
				row["info"] = localizePeerInfo(reqLocale, info)
				row["checked"] = model.Now()
				cached.Info = info
				cached.Checked = row["checked"].(string)
				a.Store.Put("peer-info", p.ID, cached)
			} else {
				msg, key, args := i18n.LocalizeError(reqLocale, err)
				row["error"] = msg
				if key != "" {
					row["error_key"] = key
				}
				if len(args) > 0 {
					row["error_args"] = args
				}
			}
		}
		result = append(result, row)
	}
	send(w, 200, result)
}
func (a *App) jobsAPI(w http.ResponseWriter, r *http.Request) {
	reqLocale := a.requestLocale(r)
	if r.Method == "GET" {
		var jobs []model.Job
		if err := a.Store.List("job", &jobs); err != nil {
			a.fail(w, r, 500, err)
			return
		}
		for idx := range jobs {
			jobs[idx] = localizeJob(reqLocale, jobs[idx])
		}
		send(w, 200, jobs)
		return
	}
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var req model.Request
	if err := read(w, r, &req); err != nil {
		a.fail(w, r, 400, err)
		return
	}
	if req.Target == "" {
		req.Target = a.Config.Get().NodeID
	}
	if req.Target != a.Config.Get().NodeID {
		var p model.Peer
		if err := a.Store.Get("peer", req.Target, &p); err != nil || p.Revoked {
			a.fail(w, r, 400, i18n.Err("err.app.pair_target_first"))
			return
		}
	}
	req.Mode = "route"
	j, err := a.Engine.Accept(a.Config.Get().NodeID, req)
	if err != nil {
		failAdmission(w, reqLocale, req, err)
		return
	}
	send(w, 202, localizeJob(reqLocale, j))
}
func (a *App) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		w.WriteHeader(500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		refresh := a.refreshDone()
		http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := fmt.Fprintf(w, "event: refresh\ndata: %s\n\n", model.Now()); err != nil {
			return
		}
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-refresh:
		case <-tick.C:
		}
	}
}
