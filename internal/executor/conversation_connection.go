package executor

import (
	"context"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/model"
)

func chatFingerprint(c config.Config, options openClawOptions, key string) string {
	return model.Hash(struct {
		Options              openClawOptions
		Provider, Model, Key string
	}{options, c.LLM.Provider, c.LLM.Model, model.Digest([]byte(key))})
}

// Connection never submits a model message. Only an explicit connect may start
// the shared runtime; subsequent snapshots cannot retry a failed startup.
func (r *ConversationRuntime) Connection(ctx context.Context, connect bool) string {
	descriptor, err := r.Describe()
	if err != nil || !descriptor.CanSend || r.ctx.Err() != nil {
		return "unavailable"
	}
	if connect {
		_, attempt, err := r.beginStartup(ctx, conversation.Binding{Runtime: "openclaw", Generation: chatBindingID()})
		if err != nil {
			return "unavailable"
		}
		if attempt != nil {
			return "connecting"
		}
	}
	select {
	case r.gate <- struct{}{}:
	case <-ctx.Done():
		return "unavailable"
	case <-r.ctx.Done():
		return "unavailable"
	}
	process, starting, fingerprint := r.process, r.startup != nil, r.fingerprint
	<-r.gate
	if starting {
		return "connecting"
	}
	c := r.config.Get()
	options, err := clawOptions(c)
	key, _ := r.config.Credential(c.LLM.CredentialRef)
	if err != nil || process == nil || !process.alive() || fingerprint != chatFingerprint(c, options, key) {
		return "unavailable"
	}
	snapshot := struct {
		State string `json:"state"`
	}{}
	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	action := "connection"
	if connect {
		action = "connect"
	}
	if process.call(readCtx, chatCommand{Action: action}, &snapshot) != nil {
		return "unavailable"
	}
	switch snapshot.State {
	case "connecting", "connected":
		return snapshot.State
	default:
		return "unavailable"
	}
}
