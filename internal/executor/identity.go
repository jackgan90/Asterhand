package executor

import (
	"encoding/json"
	"fmt"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/llm"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/packaging"
)

func BundleIdentity() string { return fmt.Sprintf("%s:%x", config.SourceDigest, packaging.Digest()) }

func ExecutionIdentity(c config.Config) string {
	return model.Hash(struct {
		Runtime string
		LLM     llm.Selection
		Options json.RawMessage
	}{c.AgentRuntime, c.LLM, c.RuntimeOptions[c.AgentRuntime]})
}

// A new binary or dependency set must earn readiness with a fresh tool probe.
func InvalidateStaleVerification(c *config.Manager) error {
	cfg := c.Get()
	if cfg.ModelVerified && (cfg.VerifiedBundle != BundleIdentity() || cfg.VerifiedExecution != ExecutionIdentity(cfg)) {
		cfg.ModelVerified = false
		return c.Save(cfg)
	}
	return nil
}
