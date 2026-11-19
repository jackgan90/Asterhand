package executor

import (
	"context"
	"embed"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/workspace"
)

const workspacePlugin = "asterhand-workspace"

//go:embed workspaceplugin/*
var workspacePluginFiles embed.FS

type workspaceTransportKey struct{}

func workspaceUnavailable() error { return errors.New("workspace service is unavailable") }
func workspaceError(err error) error {
	return &conversation.Error{Code: workspace.Code(err), Message: "Workspace operation failed", MessageKey: "err.workspace.operation", MessageArgs: map[string]string{"code": workspace.Code(err)}}
}

func (r *ConversationRuntime) Workspace() (*workspace.Service, error) {
	r.workspaceMu.Lock()
	c := r.config.Get()
	if r.workspace == nil {
		root := filepath.Join(r.config.Home, "conversations", "runtime-"+chatBindingID())
		var err error
		r.workspace, err = workspace.Open(filepath.Join(root, "workspace"), filepath.Join(root, "file-versions"), model.Digest([]byte(c.NodeID + ":" + chatBindingID()))[:32])
		if err != nil {
			r.workspaceMu.Unlock()
			return nil, err
		}
		r.workspace.SetHomeRoot(r.config.Home)
		r.workspace.SetTrustCallbacks(r.persistTrustRules, r.fireApprovalAttention)
		r.workspace.ConfigureTrust(c.LocalTools.ExecutionMode, c.LocalTools.TrustRevision, c.LocalTools.TrustRules)
	}
	files := r.workspace
	state := files.Status()
	if state.Authorized != c.LocalTools.Enabled || state.Policy != c.LocalTools.Revision {
		if err := files.Configure(c.LocalTools.Enabled, c.LocalTools.Revision); err != nil {
			r.workspaceMu.Unlock()
			return nil, err
		}
	}
	r.workspaceMu.Unlock()

	r.syncWorkspaceTrust(files)
	return files, nil
}

func (r *ConversationRuntime) syncWorkspaceTrust(files *workspace.Service) {
	if files == nil {
		return
	}
	r.trustApplyMu.Lock()
	defer r.trustApplyMu.Unlock()
	lt := r.config.Get().LocalTools
	state := files.Status()
	if state.ExecutionMode != lt.ExecutionMode || state.TrustRevision != lt.TrustRevision {
		files.ConfigureTrust(lt.ExecutionMode, lt.TrustRevision, lt.TrustRules)
	}
}

func (r *ConversationRuntime) persistTrustRules(rules []config.TrustRule) error {
	if len(rules) == 0 {
		return nil
	}
	r.workspaceMu.Lock()
	files := r.workspace
	r.workspaceMu.Unlock()

	r.trustApplyMu.Lock()
	defer r.trustApplyMu.Unlock()
	saved, err := r.config.UpdateLocalToolsTrust(func(cur config.LocalToolPolicy) (config.LocalToolPolicy, error) {
		cur.TrustRules = append(cur.TrustRules, rules...)
		return cur, nil
	})
	if err != nil {
		return err
	}
	if files != nil {
		files.ConfigureTrust(saved.ExecutionMode, saved.TrustRevision, saved.TrustRules)
	}
	return nil
}

// SetWorkspaceTrustPolicy updates confirmation mode or removes a persistent
// trust rule immediately without stopping or restarting the chat Gateway.
func (r *ConversationRuntime) SetWorkspaceTrustPolicy(_ context.Context, executionMode, removeRuleID string) (workspace.Status, error) {
	mode := strings.ToLower(strings.TrimSpace(executionMode))
	removeID := strings.TrimSpace(removeRuleID)
	if (mode != "" && mode != config.ExecutionModeStandard && mode != config.ExecutionModeTurbo) || (mode == "" && removeID == "") {
		return workspace.Status{}, conversation.FailKey("invalid_request", "err.workspace.operation", map[string]string{"code": "invalid_request"})
	}
	files, err := r.Workspace()
	if err != nil {
		return workspace.Status{}, err
	}
	r.trustApplyMu.Lock()
	defer r.trustApplyMu.Unlock()
	saved, err := r.config.UpdateLocalToolsTrust(func(cur config.LocalToolPolicy) (config.LocalToolPolicy, error) {
		if mode != "" {
			cur.ExecutionMode = mode
		}
		if removeID != "" && len(cur.TrustRules) > 0 {
			filtered := cur.TrustRules[:0]
			for _, rule := range cur.TrustRules {
				if rule.ID != removeID {
					filtered = append(filtered, rule)
				}
			}
			cur.TrustRules = filtered
		}
		return cur, nil
	})
	if err != nil {
		return workspace.Status{}, err
	}
	files.ConfigureTrust(saved.ExecutionMode, saved.TrustRevision, saved.TrustRules)
	return files.Status(), nil
}

func (r *ConversationRuntime) startWithWorkspace(ctx context.Context, c config.Config, options openClawOptions, provider providerBridge, key, root string) (*chatProcess, error) {
	if !c.LocalTools.Enabled {
		return startConversationProcess(ctx, c, options, provider, key, root)
	}
	files, err := r.Workspace()
	if err != nil {
		return nil, err
	}
	files.SetHomeRoot(r.config.Home)
	if options.NodePath != "" {
		files.SetBundledNodeDir(filepath.Dir(options.NodePath))
	}
	if !files.Status().Supported {
		return nil, workspaceUnavailable()
	}
	if err := seedNativeWorkspace(files, options.EntryPath, c.LocalTools.Revision); err != nil {
		return nil, err
	}
	access, err := workspace.Serve(files)
	if err != nil {
		return nil, err
	}
	process, err := startConversationProcess(context.WithValue(ctx, workspaceTransportKey{}, access), c, options, provider, key, root)
	if process == nil {
		_ = access.Close()
		return nil, err
	}
	// The managed process owner kills the entire native child tree on exit.
	settled := make(chan struct{})
	process.settled = settled
	go func() {
		defer close(settled)
		<-process.exited
		process.stop()
		_ = access.Close()
		_ = files.RuntimeStopped()
	}()
	return process, err
}

// Called only through the human permission form after chat admission closes.
// Public file operations also compare the policy while holding the service lock.
func (r *ConversationRuntime) SetWorkspacePolicy(ctx context.Context, enabled bool, expected string) (workspace.Status, error) {
	files, err := r.Workspace()
	if err != nil {
		return workspace.Status{}, err
	}
	select {
	case r.gate <- struct{}{}:
		defer func() { <-r.gate }()
	case <-ctx.Done():
		return workspace.Status{}, ctx.Err()
	}
	if r.startup != nil {
		return workspace.Status{}, workspaceUnavailable()
	}
	r.workspaceMu.Lock()
	defer r.workspaceMu.Unlock()
	c := r.config.Get()
	if c.LocalTools.Revision != expected {
		return workspace.Status{}, conversation.Fail("revision_conflict", "err.workspace.conflict")
	}
	if files.Status().Busy {
		return workspace.Status{}, conversation.Fail("busy", "err.workspace.busy")
	}
	if enabled && !files.Status().Supported {
		return workspace.Status{}, workspaceUnavailable()
	}
	if r.process != nil {
		if err = r.process.close(); err != nil {
			return workspace.Status{}, err
		}
		r.process, r.fingerprint = nil, ""
	}
	nextRev := model.ID()
	previous := c.LocalTools
	if err = files.Configure(enabled, nextRev); err != nil {
		return workspace.Status{}, err
	}
	if _, err = r.config.Update(func(latest config.Config) (config.Config, error) {
		if latest.LocalTools.Revision != expected {
			return config.Config{}, conversation.Fail("revision_conflict", "err.workspace.conflict")
		}
		previous = latest.LocalTools
		latest.LocalTools.Enabled = enabled
		latest.LocalTools.Revision = nextRev
		return latest, nil
	}); err != nil {
		_ = files.Configure(previous.Enabled, previous.Revision)
		return workspace.Status{}, err
	}
	return files.Status(), nil
}

func prepareWorkspacePlugin(root string) error {
	destination := filepath.Join(root, "workspace-plugin")
	if err := os.MkdirAll(destination, 0700); err != nil {
		return err
	}
	files, err := workspacePluginFiles.ReadDir("workspaceplugin")
	if err != nil {
		return err
	}
	for _, file := range files {
		data, err := workspacePluginFiles.ReadFile("workspaceplugin/" + file.Name())
		if err != nil {
			return err
		}
		if err = config.AtomicFile(filepath.Join(destination, file.Name()), data, 0600); err != nil {
			return err
		}
	}
	sharedDisplay, err := chatBridgeFiles.ReadFile("chatbridge/tool-display.mjs")
	if err != nil {
		return err
	}
	return config.AtomicFile(filepath.Join(destination, "tool-display.mjs"), sharedDisplay, 0600)
}

func nativeWorkspaceTools() []string {
	return workspace.AssistantToolNames()
}

func configureWorkspace(value map[string]any, c config.Config, root string, access *workspace.Transport) error {
	if !c.LocalTools.Enabled {
		return nil
	}
	if access == nil || c.LocalTools.Revision == "" {
		return workspaceUnavailable()
	}
	tools := nativeWorkspaceTools()
	workspaceRoot := filepath.Join(root, "workspace")
	defaults := value["agents"].(map[string]any)["defaults"].(map[string]any)
	defaults["skipBootstrap"] = true
	defaults["sandbox"] = map[string]any{"mode": "all", "backend": "asterhand-files", "scope": "agent", "workspaceAccess": "rw",
		"workspaceRoot": filepath.Join(root, "sandboxes"), "docker": map[string]any{"workdir": workspaceRoot},
		"browser": map[string]any{"enabled": false}, "prune": map[string]any{"idleHours": 0, "maxAgeDays": 0}}
	value["tools"] = map[string]any{"allow": tools, "deny": []string{"exec", "process", "code_execution", "skills_read", "memory-core"},
		"updatePlan": false, "fs": map[string]any{"workspaceOnly": true}, "exec": map[string]any{"applyPatch": map[string]any{"enabled": true, "workspaceOnly": true}},
		"codeMode": map[string]any{"enabled": false}, "toolSearch": false, "sandbox": map[string]any{"tools": map[string]any{"allow": tools}}}
	plugins := value["plugins"].(map[string]any)
	plugins["slots"].(map[string]any)["memory"] = "memory-core"
	plugins["allow"] = append(plugins["allow"].([]string), workspacePlugin, "memory-core")
	plugins["load"].(map[string]any)["paths"] = append(plugins["load"].(map[string]any)["paths"].([]string), filepath.Join(root, "workspace-plugin"))
	entries := plugins["entries"].(map[string]any)
	if entry, ok := entries[chatPromptPlugin].(map[string]any); ok {
		entry["config"].(map[string]any)["toolsAllow"] = tools
	}
	entries[workspacePlugin] = map[string]any{"enabled": true, "hooks": map[string]any{"allowConversationAccess": true, "allowPromptInjection": true},
		"config": map[string]any{"workspace": workspaceRoot, "endpoint": access.URL, "token": "${ASTERHAND_WORKSPACE_TOKEN}", "policy": c.LocalTools.Revision}}
	entries["memory-core"] = map[string]any{"enabled": true, "config": map[string]any{"dreaming": map[string]any{"enabled": false}}}
	value["memory"] = map[string]any{"search": map[string]any{"provider": "none", "sources": []string{"memory"},
		"rememberAcrossConversations": false, "extraPaths": []string{}, "experimental": map[string]any{"sessionMemory": false},
		"multimodal": map[string]any{"enabled": false}, "store": map[string]any{"vector": map[string]any{"enabled": false}}}}
	return nil
}

// Copy only fixed templates from the verified runtime bundle. Startup never
// widens a model read root to the installation tree or overwrites custom text.
func seedNativeWorkspace(files *workspace.Service, entry, policy string) error {
	for _, name := range []string{"AGENTS.md", "SOUL.md", "TOOLS.md", "IDENTITY.md", "USER.md", "HEARTBEAT.md", "BOOTSTRAP.md"} {
		info, err := files.Stat(name)
		if err != nil {
			return err
		}
		if info != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(filepath.Dir(entry), "docs", "reference", "templates", name))
		if err != nil {
			return err
		}
		text := strings.ReplaceAll(string(data), "\r\n", "\n")
		if strings.HasPrefix(text, "---\n") {
			if end := strings.Index(text[4:], "\n---\n"); end >= 0 {
				text = text[end+9:]
			}
		}
		id := "template-" + model.Digest([]byte(name + policy))[:32]
		_, err = files.Mutate(id, workspace.Scope{Policy: policy}, []workspace.Mutation{{Action: "write", Path: name, Expected: workspace.MissingRevision, Content: text}})
		if err != nil {
			return err
		}
	}
	return nil
}
