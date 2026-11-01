package executor

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/conversation"
)

const conversationStartupBudget = 180 * time.Second

type chatStartup struct {
	done chan struct{}
	err  error
}

type chatStarter func(context.Context, config.Config, openClawOptions, providerBridge, string, string) (*chatProcess, error)

func startConversationProcess(ctx context.Context, c config.Config, options openClawOptions, provider providerBridge, key, root string) (*chatProcess, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	diagnostic := newChatDiagnostics(root)
	if err := checkConversationBundle(ctx, options, provider, c.LLM.Provider); err != nil {
		diagnostic.finish("bundle_invalid")
		return nil, err
	}
	process, err := startChatProcess(ctx, c, options, provider, key, root, diagnostic)
	if err != nil {
		diagnostic.failedIfStarting()
	}
	return process, err
}

// Startup belongs to the runtime, not the history request that happened to
// arrive first. Waiting callers can leave without killing recovery in progress.
func (r *ConversationRuntime) ensure(ctx context.Context, binding conversation.Binding) (*chatProcess, error) {
	for {
		process, startup, err := r.beginStartup(ctx, binding)
		if err != nil || process != nil {
			return process, err
		}
		select {
		case <-startup.done:
			if startup.err != nil {
				return nil, startup.err
			}
			// Recheck configuration and ownership after waiting; settings may have
			// changed while a read-only request was starting the previous runtime.
		case <-ctx.Done():
			return nil, conversation.Fail("unavailable", "聊天运行时仍在启动，草稿已保留，请稍后重试")
		case <-r.ctx.Done():
			return nil, conversation.Fail("unavailable", "会话运行时已关闭")
		}
	}
}

func (r *ConversationRuntime) beginStartup(ctx context.Context, binding conversation.Binding) (*chatProcess, *chatStartup, error) {
	select {
	case r.gate <- struct{}{}:
		defer func() { <-r.gate }()
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-r.ctx.Done():
		return nil, nil, conversation.Fail("unavailable", "会话运行时已关闭")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if r.ctx.Err() != nil {
		return nil, nil, conversation.Fail("unavailable", "会话运行时已关闭")
	}
	c := r.config.Get()
	options, err := clawOptions(c)
	if err != nil || binding.Runtime != "openclaw" || c.AgentRuntime != "openclaw" || binding.Generation != chatBindingID() {
		return nil, nil, conversation.Fail("binding_changed", "原会话的运行时绑定不可用，请恢复原配置")
	}
	provider, err := openClawProvider(c)
	if err != nil {
		return nil, nil, err
	}
	key, _ := r.config.Credential(c.LLM.CredentialRef)
	fingerprint := chatFingerprint(c, options, key)
	if r.startup != nil {
		return nil, r.startup, nil
	}
	if r.process != nil && r.fingerprint == fingerprint && r.process.alive() {
		return r.process, nil, nil
	}
	if r.process != nil {
		if err := r.process.close(); err != nil {
			return nil, nil, err
		}
		r.process = nil
	}
	startup := &chatStartup{done: make(chan struct{})}
	r.startup = startup
	root := filepath.Join(r.config.Home, "conversations", "runtime-"+binding.Generation)
	go r.runStartup(startup, c, options, provider, key, root, fingerprint)
	return nil, startup, nil
}

func (r *ConversationRuntime) runStartup(attempt *chatStartup, c config.Config, options openClawOptions, provider providerBridge, key, root, fingerprint string) {
	ctx, cancel := context.WithTimeout(r.ctx, conversationStartupBudget)
	defer cancel()
	process, err := r.start(ctx, c, options, provider, key, root)
	r.gate <- struct{}{}
	defer func() { <-r.gate }()
	if r.ctx.Err() != nil && process != nil {
		err = process.close()
		if err == nil {
			process = nil
			err = conversation.Fail("unavailable", "会话运行时已关闭")
		}
	}
	if process != nil {
		r.process, r.fingerprint = process, ""
		if err == nil {
			r.fingerprint = fingerprint
		}
	}
	attempt.err = err
	r.startup = nil
	close(attempt.done)
}
