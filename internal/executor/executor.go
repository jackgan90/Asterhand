package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jackgan90/Asterhand/internal/config"
)

type Run struct {
	ID       string
	Goal     string
	Mode     string
	Callback string
	Token    string
}
type Result struct {
	OK     bool   `json:"ok"`
	Status string `json:"status"`
	Final  string `json:"final"`
	Error  *struct {
		Message string `json:"message"`
		Kind    string `json:"kind"`
	} `json:"error,omitempty"`
}
type Executor interface {
	Execute(context.Context, Run) (Result, error)
}
type OpenClaw struct{ Config *config.Manager }
type boundedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if b.b.Len() < 1024*1024 {
		room := 1024*1024 - b.b.Len()
		if len(p) > room {
			p = p[:room]
		}
		b.b.Write(p)
	}
	return n, nil
}
func (b *boundedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

func (o *OpenClaw) Execute(ctx context.Context, r Run) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := validateRun(r); err != nil {
		return Result{}, err
	}
	c := o.Config.Get()
	options, err := clawOptions(c)
	if err != nil {
		return Result{}, err
	}
	bridge, err := openClawProvider(c)
	if err != nil {
		return Result{}, err
	}
	key, err := o.Config.Credential(c.LLM.CredentialRef)
	if err != nil {
		return Result{}, errors.New("请在本机设置中配置所选模型的凭据")
	}
	if options.NodePath == "" || options.EntryPath == "" {
		return Result{}, errors.New("OpenClaw 运行环境未安装，请运行 repair")
	}
	base := filepath.Join(o.Config.Home, "runs", r.ID)
	for _, name := range []string{"state", "workspace"} {
		if err = os.MkdirAll(filepath.Join(base, name), 0700); err != nil {
			return Result{}, err
		}
	}
	cfg, err := RuntimeConfig(c, r.Mode, filepath.Join(base, "workspace"))
	if err != nil {
		return Result{}, err
	}
	configPath := filepath.Join(base, "openclaw.json")
	if err = config.AtomicFile(configPath, cfg, 0600); err != nil {
		return Result{}, err
	}
	args := []string{options.EntryPath, "agent", "exec", "--config", configPath, "--state-dir", filepath.Join(base, "state"), "--cwd", filepath.Join(base, "workspace"), "--message-file", "-", "--json", "--code-mode", "direct", "--timeout", "7200"}
	cmd := exec.Command(options.NodePath, args...)
	cmd.Dir = base
	cmd.Env = runtimeEnvironment(o.Config.Home, key, bridge.KeyEnv, r, options.NodePath)
	stdout, stderr, err := runCommand(ctx, cmd, r.Goal)
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	var result Result
	if json.Unmarshal([]byte(stdout), &result) != nil {
		detail := strings.ReplaceAll(stderr, key, "[redacted]")
		detail = strings.ReplaceAll(detail, r.Token, "[redacted]")
		if len(detail) > 3000 {
			detail = detail[len(detail)-3000:]
		}
		return Result{}, fmt.Errorf("OpenClaw 未返回有效结果: %v; %s", err, detail)
	}
	result.Final = redact(result.Final, key, r.Token)
	if result.Error != nil {
		result.Error.Message = redact(result.Error.Message, key, r.Token)
		result.Error.Kind = redact(result.Error.Kind, key, r.Token)
	}
	if result.Status != "ok" && result.Status != "error" && result.Status != "timeout" {
		return Result{}, errors.New("OpenClaw 返回未知结果状态，请重新验证运行时兼容性")
	}
	if result.OK != (result.Status == "ok") || (result.OK && result.Error != nil) {
		return Result{}, errors.New("OpenClaw 返回不一致的执行结果")
	}
	if err != nil || !result.OK {
		if result.Error != nil {
			return result, fmt.Errorf("OpenClaw: %s", result.Error.Message)
		}
		return result, fmt.Errorf("OpenClaw 执行未成功: %v", err)
	}
	return result, nil
}

func runtimeEnvironment(home, key, keyEnv string, r Run, nodePath string) []string {
	env := []string{}
	for _, name := range []string{"SYSTEMROOT", "WINDIR", "COMSPEC", "TEMP", "TMP", "PATHEXT", "USERPROFILE", "LOCALAPPDATA", "APPDATA"} {
		if value := os.Getenv(name); value != "" {
			env = append(env, name+"="+value)
		}
	}
	systemPath := filepath.Join(os.Getenv("SYSTEMROOT"), "System32")
	env = append(env, "PATH="+filepath.Dir(nodePath)+string(os.PathListSeparator)+systemPath)
	return append(env, "OPENCLAW_STATE_DIR="+filepath.Join(home, "runtime-state"), "OPENCLAW_CONFIG_PATH="+filepath.Join(home, "runs", r.ID, "openclaw.json"), "OPENCLAW_NO_RESPAWN=1", keyEnv+"="+key, "ASTERHAND_CALLBACK="+r.Callback, "ASTERHAND_TOOL_TOKEN="+r.Token, "ASTERHAND_MODE="+r.Mode, "NO_COLOR=1")
}

func RuntimeConfig(c config.Config, mode, workspace string) ([]byte, error) {
	options, err := clawOptions(c)
	if err != nil {
		return nil, err
	}
	bridge, err := openClawProvider(c)
	if err != nil {
		return nil, err
	}
	if mode != "route" && mode != "execute" && mode != "probe" {
		return nil, fmt.Errorf("unsupported mode: %s", mode)
	}
	allowed := []string{"asterhand_create_text", "asterhand_read_text", "asterhand_ask_user"}
	if mode == "route" {
		allowed = []string{"asterhand_submit", "asterhand_ask_user"}
	}
	if mode == "probe" {
		allowed = []string{"asterhand_probe"}
	}
	value := map[string]any{
		"agents":  map[string]any{"defaults": map[string]any{"model": c.LLM.Model, "workspace": workspace, "skipBootstrap": true, "models": map[string]any{c.LLM.Model: map[string]any{"agentRuntime": map[string]any{"id": "openclaw"}}}}},
		"tools":   map[string]any{"profile": "full", "allow": allowed, "deny": []string{"group:openclaw", "group:fs", "group:runtime", "canvas", "gateway", "plugins", "openclaw", "code_execution", "tool_search"}, "codeMode": map[string]any{"enabled": false}},
		"plugins": map[string]any{"allow": []string{"asterhand-tools", bridge.PluginID}, "load": map[string]any{"paths": []string{options.PluginPath, options.ProviderPaths[c.LLM.Provider]}}, "entries": map[string]any{"asterhand-tools": map[string]any{"enabled": true}, bridge.PluginID: map[string]any{"enabled": true}}},
	}
	return json.MarshalIndent(value, "", "  ")
}

func redact(text string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	return text
}

func runCommand(ctx context.Context, cmd *exec.Cmd, goal string) (string, string, error) {
	var stdout, stderr boundedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", "", err
	}
	cleanup, err := startManaged(cmd)
	if err != nil {
		return "", "", err
	}
	defer cleanup()
	written := make(chan error, 1)
	go func() {
		_, err := io.WriteString(stdin, goal)
		stdin.Close()
		written <- err
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-ctx.Done():
		stdin.Close()
		cleanup()
		<-done
		return "", "", ctx.Err()
	}
	if writeErr := <-written; writeErr != nil {
		return "", "", writeErr
	}

	return stdout.String(), stderr.String(), err
}
