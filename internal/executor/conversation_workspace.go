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
	defer r.workspaceMu.Unlock()
	c := r.config.Get()
	if r.workspace == nil {
		root := filepath.Join(r.config.Home, "conversations", "runtime-"+chatBindingID())
		var err error
		r.workspace, err = workspace.Open(filepath.Join(root, "workspace"), filepath.Join(root, "file-versions"), model.Digest([]byte(c.NodeID + ":" + chatBindingID()))[:32])
		if err != nil {
			return nil, err
		}
	}
	state := r.workspace.Status()
	if state.Authorized != c.LocalTools.Enabled || state.Policy != c.LocalTools.Revision {
		if err := r.workspace.Configure(c.LocalTools.Enabled, c.LocalTools.Revision); err != nil {
			return nil, err
		}
	}
	return r.workspace, nil
}

func (r *ConversationRuntime) startWithWorkspace(ctx context.Context, c config.Config, options openClawOptions, provider providerBridge, key, root string) (*chatProcess, error) {
	if !c.LocalTools.Enabled {
		return startConversationProcess(ctx, c, options, provider, key, root)
	}
	files, err := r.Workspace()
	if err != nil {
		return nil, err
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
	previous := c.LocalTools
	c.LocalTools = config.LocalToolPolicy{Enabled: enabled, Revision: model.ID()}
	if err = files.Configure(enabled, c.LocalTools.Revision); err != nil {
		return workspace.Status{}, err
	}
	if err = r.config.Save(c); err != nil {
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
	return nil
}

func nativeWorkspaceTools() []string {
	return []string{"read", "write", "edit", "apply_patch", "ls", "workspace_search", "memory_search", "memory_get", "progress_card"}
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
	value["tools"] = map[string]any{"allow": tools, "deny": []string{"exec", "process", "code_execution", "skills_read"},
		"fs": map[string]any{"workspaceOnly": true}, "exec": map[string]any{"applyPatch": map[string]any{"enabled": true, "workspaceOnly": true}},
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
		"config": map[string]any{"workspace": workspaceRoot, "endpoint": access.URL, "token": access.Token, "policy": c.LocalTools.Revision}}
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
