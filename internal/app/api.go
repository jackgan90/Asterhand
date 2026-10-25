package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/executor"
	"github.com/jackgan90/Asterhand/internal/llm"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/network"
)

func decode(b []byte, v any) error { return json.Unmarshal(b, v) }
func (a *App) info(projects []string) map[string]any {
	c := a.Config.Get()
	visible := []config.Project{}
	for _, p := range c.Projects {
		if projects == nil || contains(projects, p.ID) {
			visible = append(visible, p)
		}
	}
	return map[string]any{"node_id": c.NodeID, "name": c.Name, "version": config.Version, "ready": c.ModelVerified, "projects": visible, "endpoint": c.Endpoint, "model": c.LLM.Model, "agent_runtime": c.AgentRuntime, "source_sha256": config.SourceDigest, "bundle_identity": executor.BundleIdentity(), "network_warning": network.ConnectionWarning(c.Endpoint, c.PeerListen, a.peerAddress), "restart_required": !network.ListenerMatches(c.PeerListen, a.peerAddress)}
}
func (a *App) api(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/info":
		send(w, 200, a.info(nil))
	case "/api/stop":
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		send(w, 202, map[string]bool{"stopping": true})
		time.AfterFunc(100*time.Millisecond, a.cancel)
	case "/api/network":
		_, port, _ := net.SplitHostPort(a.Config.Get().PeerListen)
		send(w, 200, network.Inspect(r.Context(), port))
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
			fail(w, 400, err)
			return
		}
		send(w, 200, map[string]string{"code": code})
	case "/api/pair":
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		if !network.ListenerMatches(a.Config.Get().PeerListen, a.peerAddress) {
			fail(w, 409, errors.New("节点监听配置尚未生效，请重启本机节点后再配对"))
			return
		}
		var input struct {
			Code string `json:"code"`
		}
		if err := read(w, r, &input); err != nil {
			fail(w, 400, err)
			return
		}
		p, err := a.Peers.Pair(r.Context(), input.Code)
		if err != nil {
			fail(w, 400, err)
			return
		}
		a.requestPeerSync()
		send(w, 200, map[string]string{"id": p.ID, "name": p.Name})
	case "/api/jobs":
		a.jobsAPI(w, r)
	case "/api/outbox":
		var out []model.Outbox
		if err := a.Store.List("outbox", &out); err != nil {
			fail(w, 500, err)
			return
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
		fail(w, 400, err)
		return
	}
	if strings.TrimSpace(input.Name) == "" || len(input.Name) > 80 {
		fail(w, 400, errors.New("请填写节点名称"))
		return
	}
	if input.PeerListen != nil {
		c.PeerListen = strings.TrimSpace(*input.PeerListen)
		if err := network.ValidateListener(c.PeerListen); err != nil {
			fail(w, 400, err)
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
		fail(w, 400, err)
		return
	}
	if !filepath.IsAbs(input.Root) {
		fail(w, 400, errors.New("授权目录必须为绝对路径"))
		return
	}
	root, err := filepath.Abs(input.Root)
	if err != nil {
		fail(w, 400, err)
		return
	}
	if err = executor.ValidateWorkspace(c, a.Config.Home, root); err != nil {
		fail(w, 400, err)
		return
	}
	var jobs []model.Job
	if err = a.Store.List("job", &jobs); err != nil {
		fail(w, 500, err)
		return
	}
	for _, j := range jobs {
		if !model.Terminal(j.State) {
			fail(w, 409, errors.New("请先结束或处置现有任务，再修改节点设置"))
			return
		}
	}
	if a.probeRunning {
		fail(w, 409, errors.New("请等待当前模型检查完成"))
		return
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		fail(w, 400, err)
		return
	}
	if input.Endpoint != "" {
		u, parseErr := url.Parse(input.Endpoint)
		if parseErr != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			fail(w, 400, errors.New("节点地址格式为 https://IP:17433"))
			return
		}
	}
	c.Name = input.Name
	c.Endpoint = input.Endpoint

	c.Projects = []config.Project{{ID: "workspace", Name: "授权目录", Root: root}}
	c.Configured = true
	if input.Key != "" || input.DeleteKey {
		if input.DeleteKey {
			input.Key = ""
		}
		if err = a.Config.SetCredential(c.LLM.CredentialRef, input.Key); err != nil {
			fail(w, 500, errors.New("凭据保存失败"))
			return
		}
	}
	workspaceChanged := len(before.Projects) != 1 || before.Projects[0].ID != "workspace" || before.Projects[0].Root != root
	requiresProbe := input.Key != "" || input.DeleteKey || workspaceChanged || executor.ExecutionIdentity(before) != executor.ExecutionIdentity(c)
	if requiresProbe {
		c.ModelVerified = false
	}
	if err = a.Config.Save(c); err != nil {
		fail(w, 500, err)
		return
	}
	if requiresProbe {
		a.probeStatus, a.probeError = "not_run", ""
	}
	a.requestPeerSync()
	send(w, 200, map[string]bool{"saved": true, "ready": c.ModelVerified, "restart_required": !network.ListenerMatches(c.PeerListen, a.peerAddress)})
}
func (a *App) probe(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	if r.Method == "GET" {
		state, detail := a.probeStatus, a.probeError
		if !a.probeRunning {
			if a.Config.Get().ModelVerified {
				state, detail = "passed", ""
			} else if state == "passed" {
				state, detail = "not_run", ""
			}
		}
		a.mu.Unlock()
		send(w, 200, map[string]string{"state": state, "error": detail})
		return
	}
	if r.Method != "POST" {
		a.mu.Unlock()
		w.WriteHeader(405)
		return
	}
	if a.probeRunning {
		a.mu.Unlock()
		fail(w, 409, errors.New("模型检查正在执行"))
		return
	}
	a.probeRunning = true
	a.probeStatus = "running"
	a.probeError = ""
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
			err = errors.New("模型已返回，但未调用无副作用检查工具")
		}
		c := a.Config.Get()
		c.ModelVerified = err == nil
		if err == nil {
			c.VerifiedBundle = executor.BundleIdentity()
			c.VerifiedExecution = executor.ExecutionIdentity(c)
		}
		if saveErr := a.Config.Save(c); saveErr != nil {
			err = saveErr
		}
		a.probeRunning = false
		a.probeStatus = "passed"
		if err != nil {
			a.probeStatus = "failed"
			a.probeError = err.Error()
		}
		a.mu.Unlock()
	}()
	send(w, 202, map[string]string{"state": "running"})
}
func (a *App) peersAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == "DELETE" {
		var input struct {
			ID string `json:"id"`
		}
		if err := read(w, r, &input); err != nil {
			fail(w, 400, err)
			return
		}
		var p model.Peer
		if err := a.Store.Get("peer", input.ID, &p); err != nil {
			fail(w, 404, err)
			return
		}
		p.Revoked = true
		if err := a.Store.Put("peer", p.ID, p); err != nil {
			fail(w, 500, err)
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
		fail(w, 500, err)
		return
	}
	result := []map[string]any{}
	for _, p := range peers {
		row := map[string]any{"id": p.ID, "name": p.Name, "endpoint": p.Endpoint, "revoked": p.Revoked, "online": false}
		if !p.Revoked {
			var cached struct {
				Info    map[string]any `json:"info"`
				Checked string         `json:"checked"`
			}
			if a.Store.Get("peer-info", p.ID, &cached) == nil {
				row["info"] = cached.Info
				row["checked"] = cached.Checked
			}
			ctx, cancel := context.WithTimeout(r.Context(), peerProbeTimeout)
			info, err := a.Peers.Refresh(ctx, p.ID, "")
			cancel()
			if err == nil {
				row["name"], row["endpoint"] = info["name"], info["endpoint"]
				row["online"] = true
				row["info"] = info
				row["checked"] = model.Now()
				cached.Info = info
				cached.Checked = row["checked"].(string)
				a.Store.Put("peer-info", p.ID, cached)
			} else {
				row["error"] = err.Error()
			}
		}
		result = append(result, row)
	}
	send(w, 200, result)
}
func (a *App) jobsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		var jobs []model.Job
		if err := a.Store.List("job", &jobs); err != nil {
			fail(w, 500, err)
			return
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
	if !a.Config.Get().ModelVerified {
		fail(w, 409, errors.New("请先完成本机模型和工具检查"))
		return
	}
	var req model.Request
	if err := read(w, r, &req); err != nil {
		fail(w, 400, err)
		return
	}
	if req.Target == "" {
		req.Target = a.Config.Get().NodeID
	}
	if req.Target != a.Config.Get().NodeID {
		var p model.Peer
		if err := a.Store.Get("peer", req.Target, &p); err != nil || p.Revoked {
			fail(w, 400, errors.New("请先配对目标节点"))
			return
		}
	}
	req.Mode = "route"
	j, err := a.Engine.Accept(a.Config.Get().NodeID, req)
	if err != nil {
		fail(w, 409, err)
		return
	}
	send(w, 202, j)
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
		http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := fmt.Fprintf(w, "event: refresh\ndata: %s\n\n", model.Now()); err != nil {
			return
		}
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
	}
}
