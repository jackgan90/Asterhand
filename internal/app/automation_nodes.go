package app

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/executor"
	"github.com/jackgan90/Asterhand/internal/model"
)

func (a *App) registerAutomationNodes(r *automation.Registry) {
	automation.Register(r, automation.Definition{Name: "node.start", Description: "Explicitly start the human-granted installed node, or acknowledge the already running instance; never installs or changes configuration", Role: "execute", Write: true}, func(v automation.Empty) string { return r.Node }, func(p automation.Principal, v automation.Empty) error { return p.Require("node", r.Node) },
		func(ctx context.Context, call automation.Call, v automation.Empty) (automation.Outcome, error) {
			data := map[string]any{"running": true, "node_id": r.Node, "instance_id": r.Instance}
			return automation.Outcome{Data: data, Summary: data, Accepted: true}, nil
		})
	automation.Register(r, automation.Definition{Name: "nodes.list", Description: "Read permitted local and paired nodes from bounded cached metadata", Role: "read"}, nil, nil, a.automationNodesList)
	for _, name := range []string{"nodes.get", "nodes.capabilities"} {
		automation.Register(r, automation.Definition{Name: name, Description: "Read the selected node's available capabilities and metadata timestamp", Role: "read"}, func(v automation.NodeGet) string { return v.ID }, func(p automation.Principal, v automation.NodeGet) error { return p.Require("node", v.ID) },
			func(ctx context.Context, call automation.Call, v automation.NodeGet) (automation.Outcome, error) {
				data, err := a.automationNode(call.Principal, v.ID)
				return automation.Outcome{Data: data}, err
			})
	}
	automation.Register(r, automation.Definition{Name: "node.status", Description: "Read this node instance's status without starting it or making a model call", Role: "read"}, nil, func(p automation.Principal, v automation.Empty) error { return p.Require("node", r.Node) },
		func(ctx context.Context, call automation.Call, v automation.Empty) (automation.Outcome, error) {
			return automation.Outcome{Data: map[string]any{"node_id": r.Node, "instance_id": r.Instance, "running": true, "stopping": a.Stopping(), "version": config.Version}}, nil
		})
	automation.Register(r, automation.Definition{Name: "node.stop", Description: "Stop this exact node instance; active work requires explicit stop_active_work", Role: "execute", Write: true, Control: true}, func(v automation.NodeStop) string { return r.Node }, func(p automation.Principal, v automation.NodeStop) error { return p.Require("node", r.Node) },
		func(ctx context.Context, call automation.Call, v automation.NodeStop) (automation.Outcome, error) {
			if v.Instance != r.Instance {
				return automation.Outcome{}, automation.Fail("instance_changed", "The requested node instance has changed")
			}
			if err := call.Check(); err != nil {
				return automation.Outcome{}, err
			}
			if !a.RequestStop(v.StopActive) {
				return automation.Outcome{}, automation.Fail("busy", "The node has active work or queued input; stopping it requires an explicit choice")
			}
			summary := map[string]any{"node_id": r.Node, "instance_id": r.Instance, "stopping": true}
			return automation.Outcome{Data: summary, Summary: summary, Accepted: true}, nil
		})
}

func (a *App) automationNode(p automation.Principal, id string) (map[string]any, error) {
	if id == a.Config.Get().NodeID {
		c := a.Config.Get()
		projects := []map[string]string{}
		for _, project := range c.Projects {
			if p.Allows("projects", projectScope(c.NodeID, project.ID)) {
				projects = append(projects, map[string]string{"id": project.ID, "name": project.Name, "root": project.Root})
			}
		}
		return map[string]any{"node_id": c.NodeID, "name": c.Name, "version": config.Version, "online": true, "checked_at": model.Now(), "capabilities": executor.ConfiguredCapabilities(a.Config, c), "projects": projects, "automation_api": automation.APIVersion}, nil
	}
	peer, err := a.Peers.Get(id)
	if err != nil || peer.Revoked {
		return nil, automation.Fail("not_found", "The permitted node is not paired or has been revoked")
	}
	var cached struct {
		Info    map[string]any `json:"info"`
		Checked string         `json:"checked"`
	}
	_ = a.Store.Get("peer-info", id, &cached)
	data := map[string]any{"node_id": id, "name": peer.Name, "checked_at": cached.Checked, "online": "unknown", "cached": true, "task_projects": []string{}}
	projects := []string{}
	for _, project := range peer.Projects {
		if p.Allows("projects", projectScope(id, project)) {
			projects = append(projects, project)
		}
	}
	data["task_projects"] = projects
	if cached.Info != nil {
		data["name"], data["version"], data["capabilities"] = cached.Info["name"], cached.Info["version"], cached.Info["capabilities"]
	}
	if checked, e := time.Parse(time.RFC3339Nano, cached.Checked); e == nil {
		data["age_seconds"] = int64(time.Since(checked).Seconds())
	}
	return data, nil
}

func (a *App) automationNodesList(ctx context.Context, call automation.Call, v automation.NodeList) (automation.Outcome, error) {
	limit, err := automation.PageLimit(v.Limit)
	if err != nil {
		return automation.Outcome{}, err
	}
	limit = min(limit, 25)
	binding := []any{"nodes", call.Principal.ID, call.Principal.Scopes}
	cursor, err := automation.ParseCursor(v.Cursor, binding)
	if err != nil {
		return automation.Outcome{}, err
	}
	before := int64(0)
	if cursor.After != "" {
		before, err = strconv.ParseInt(cursor.After, 10, 64)
		if err != nil {
			return automation.Outcome{}, automation.Fail("invalid_cursor", "Invalid node cursor")
		}
	}
	items := []any{}
	if v.Cursor == "" && call.Principal.Allows("node", a.Config.Get().NodeID) {
		local, err := a.automationNode(call.Principal, a.Config.Get().NodeID)
		if err != nil {
			return automation.Outcome{}, err
		}
		items = append(items, local)
	}
	rows, next, err := a.Store.Page("peer", before, limit)
	if err != nil {
		return automation.Outcome{}, err
	}
	for _, raw := range rows {
		var peer model.Peer
		if err = json.Unmarshal(raw, &peer); err != nil {
			return automation.Outcome{}, err
		}
		if !peer.Revoked && call.Principal.Allows("node", peer.ID) {
			data, err := a.automationNode(call.Principal, peer.ID)
			if err == nil {
				items = append(items, data)
			}
		}
	}
	nextCursor := ""
	if next != 0 {
		nextCursor = automation.EncodeCursor(binding, "", strconv.FormatInt(next, 10))
	}
	return automation.Outcome{Data: map[string]any{"items": items, "next": nextCursor}}, nil
}
