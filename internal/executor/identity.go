package executor

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/llm"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/packaging"
)

var errVerificationCurrent = errors.New("verification current")

func BundleIdentity() string { return fmt.Sprintf("%s:%x", config.SourceDigest, packaging.Digest()) }

func ExecutionIdentity(c config.Config) string {
	return model.Hash(struct {
		Runtime string
		LLM     llm.Selection
		Options json.RawMessage
	}{c.AgentRuntime, c.LLM, c.RuntimeOptions[c.AgentRuntime]})
}

// Diagnostic evidence belongs to one build/configuration, not to task admission.
func InvalidateStaleVerification(c *config.Manager) error {
	cfg := c.Get()
	if !cfg.ModelVerified || (cfg.VerifiedBundle == BundleIdentity() && cfg.VerifiedExecution == ExecutionIdentity(cfg)) {
		return nil
	}
	_, err := c.Update(func(latest config.Config) (config.Config, error) {
		if latest.ModelVerified && (latest.VerifiedBundle != BundleIdentity() || latest.VerifiedExecution != ExecutionIdentity(latest)) {
			latest.InvalidateModelDiagnostic()
			return latest, nil
		}
		return latest, errVerificationCurrent
	})
	if errors.Is(err, errVerificationCurrent) {
		return nil
	}
	return err
}
