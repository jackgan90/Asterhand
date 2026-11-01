package executor

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/packaging"
)

//go:embed chatbridge/*.mjs
var chatBridgeFiles embed.FS

// ConversationRuntime is independent of task Execute. It owns a private Gateway
// and never adopts an existing listener or the user's personal OpenClaw home.
type ConversationRuntime struct {
	config      *config.Manager
	gate        chan struct{}
	process     *chatProcess
	fingerprint string
	startup     *chatStartup
	start       chatStarter
	ctx         context.Context
	cancel      context.CancelFunc
}

func NewConversationRuntime(manager *config.Manager) *ConversationRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	return &ConversationRuntime{config: manager, gate: make(chan struct{}, 1), ctx: ctx, cancel: cancel, start: startConversationProcess}
}

func (r *ConversationRuntime) Describe() (conversation.Descriptor, error) {
	c := r.config.Get()
	if c.AgentRuntime != "openclaw" {
		return conversation.Descriptor{}, conversation.Fail("unsupported", "当前运行时尚未提供持续会话能力")
	}
	_, err := clawOptions(c)
	if err != nil {
		return conversation.Descriptor{}, err
	}
	if _, err = openClawProvider(c); err != nil {
		return conversation.Descriptor{}, err
	}
	status := ConfiguredCapabilities(r.config, c).Chat
	return conversation.Descriptor{ID: "openclaw", Binding: chatBindingID(), Provider: c.LLM.Provider, Model: c.LLM.Model,
		Prompt:       conversationPrompt(c).Application,
		CanSend:      status.Available,
		SendError:    status.Detail,
		Capabilities: conversation.Capabilities{History: true, Streaming: true, Cancellation: true, DurableReceipts: true}}, nil
}

func chatBindingID() string {
	// The node data directory owns the logical runtime generation. Installer
	// version paths and provider/model/key changes must not orphan its history.
	return model.Digest([]byte("openclaw-chat-v1"))[:32]
}

func checkConversationBundle(ctx context.Context, options openClawOptions, provider providerBridge, name string) error {
	if options.NodePath == "" || options.EntryPath == "" || options.ProviderPaths[name] == "" {
		return conversation.Fail("unavailable", "聊天运行环境未安装，请运行 repair")
	}
	catalog := packaging.Dependencies()
	component, err := catalog.Component("openclaw")
	if err != nil {
		return err
	}
	for path, version := range map[string]string{
		filepath.Join(filepath.Dir(options.EntryPath), "package.json"): component.Version,
		filepath.Join(options.ProviderPaths[name], "package.json"):     provider.Version,
	} {
		data, readErr := os.ReadFile(path)
		var pkg struct {
			Version string `json:"version"`
		}
		if readErr != nil || json.Unmarshal(data, &pkg) != nil || pkg.Version != version {
			return conversation.Fail("incompatible_runtime", "聊天运行时版本与依赖清单不匹配，请修复安装")
		}
	}
	cmd := exec.CommandContext(ctx, options.NodePath, "--version")
	hideDiagnostic(cmd)
	output, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(output)) != "v"+catalog.Node.Version {
		return conversation.Fail("incompatible_runtime", "Node.js 版本与聊天依赖清单不匹配")
	}
	return nil
}

func startChatProcess(ctx context.Context, c config.Config, options openClawOptions, provider providerBridge, key, root string, diagnostic *chatDiagnostics) (*chatProcess, error) {
	for _, directory := range []string{"state", "workspace", "bridge", "userprofile", "appdata", "localappdata", "temp"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0700); err != nil {
			return nil, err
		}
	}
	entries, err := chatBridgeFiles.ReadDir("chatbridge")
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		data, err := chatBridgeFiles.ReadFile("chatbridge/" + entry.Name())
		if err != nil {
			return nil, err
		}
		if err := config.AtomicFile(filepath.Join(root, "bridge", entry.Name()), data, 0600); err != nil {
			return nil, err
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	token := rand.Text()
	plan, promptEnabled := prepareConversationPrompt(c, root)
	if !promptEnabled {
		plan.Application = plan.Application.Fallback("unconfirmed")
	}
	data, err := conversationConfig(c, options, provider, root, port, promptEnabled)
	if err != nil {
		return nil, err
	}
	if err := config.AtomicFile(filepath.Join(root, "openclaw.json"), data, 0600); err != nil {
		return nil, err
	}
	cmd := exec.Command(options.NodePath, filepath.Join(root, "bridge", "main.mjs"))
	cmd.Dir, cmd.Env = root, conversationEnvironment(root, options.NodePath, provider.KeyEnv, key, token)
	process, err := launchChatProcess(cmd, diagnostic)
	if err != nil {
		diagnostic.finish("launch_failed")
		return nil, err
	}
	bootstrap := chatCommand{Action: "bootstrap", Entry: options.EntryPath, Port: port, Token: token,
		Prompt: &plan.Application, PromptInstance: plan.Instance}
	if deadline, ok := ctx.Deadline(); ok {
		bootstrap.StartupTimeoutMS = max(1, min(conversationStartupBudget.Milliseconds(), time.Until(deadline).Milliseconds()-1000))
	}
	if err := process.call(ctx, bootstrap, nil); err != nil {
		outcome := "bootstrap_failed"
		if errors.Is(err, context.DeadlineExceeded) {
			outcome = "startup_timeout"
		}
		if errors.Is(err, context.Canceled) {
			outcome = "startup_cancelled"
		}
		diagnostic.finish(outcome)
		if cleanupErr := process.close(); cleanupErr != nil {
			return process, cleanupErr
		}
		return nil, conversation.Fail("unavailable", "聊天运行时启动失败，请查看启动诊断后重试")
	}
	diagnostic.finish("ready")
	return process, nil
}

func (r *ConversationRuntime) Close() error {
	r.cancel()
	r.gate <- struct{}{}
	startup := r.startup
	<-r.gate
	if startup != nil {
		<-startup.done
	}
	r.gate <- struct{}{}
	defer func() { <-r.gate }()
	if r.process != nil {
		return r.process.close()
	}
	return nil
}

func (r *ConversationRuntime) invoke(ctx context.Context, binding conversation.Binding, command chatCommand, result any) error {
	process, err := r.ensure(ctx, binding)
	if err != nil {
		return err
	}
	command.Binding = binding
	return process.call(ctx, command, result)
}

func (r *ConversationRuntime) Create(ctx context.Context, binding conversation.Binding) (conversation.Binding, error) {
	result := binding
	err := r.invoke(ctx, binding, chatCommand{Action: "create"}, &result)
	return result, err
}

// Queue preparation waits for startup before starting the submission deadline.
// It never sends a model message.
func (r *ConversationRuntime) Prepare(ctx context.Context, binding conversation.Binding) error {
	_, err := r.ensure(ctx, binding)
	return err
}

func (r *ConversationRuntime) Submit(ctx context.Context, binding conversation.Binding, submission conversation.Submission) (conversation.Receipt, error) {
	process, err := r.ensure(ctx, binding)
	if err != nil {
		return conversation.Receipt{}, conversation.Fail("rejected", conversation.PublicError(err).Message)
	}
	var receipt conversation.Receipt
	err = process.call(ctx, chatCommand{Action: "submit", Binding: binding, Run: submission.ID, Text: submission.Text}, &receipt)
	return receipt, err
}

func (r *ConversationRuntime) Observe(ctx context.Context, binding conversation.Binding, request string, started time.Time) (conversation.Observation, error) {
	var observation conversation.Observation
	err := r.invoke(ctx, binding, chatCommand{Action: "observe", Run: request, Started: started.UnixMilli()}, &observation)
	return observation, err
}

func (r *ConversationRuntime) History(ctx context.Context, binding conversation.Binding, offset int, cursor string) (conversation.Page, error) {
	var page conversation.Page
	err := r.invoke(ctx, binding, chatCommand{Action: "history", Offset: offset, Cursor: cursor}, &page)
	return page, err
}

func (r *ConversationRuntime) Message(ctx context.Context, binding conversation.Binding, message string, offset int) (conversation.Fragment, error) {
	var fragment conversation.Fragment
	err := r.invoke(ctx, binding, chatCommand{Action: "message", Message: message, Offset: offset}, &fragment)
	return fragment, err
}

func (r *ConversationRuntime) Cancel(ctx context.Context, binding conversation.Binding, request string) error {
	return r.invoke(ctx, binding, chatCommand{Action: "cancel", Run: request}, nil)
}

var _ conversation.Runtime = (*ConversationRuntime)(nil)
