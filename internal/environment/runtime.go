package environment

import (
	"context"
	"runtime"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/executor"
	"github.com/jackgan90/Asterhand/internal/network"
)

func Discover(c config.Config, base string) config.Config {
	adapter, err := executor.RuntimeFor(c.AgentRuntime, nil)
	if err != nil {
		return c
	}
	return adapter.Discover(c, base)
}
func Check(c *config.Manager) (map[string]any, error) {
	cfg := c.Get()
	checks := map[string]any{"version": config.Version, "os": runtime.GOOS, "arch": runtime.GOARCH, "node_id": cfg.NodeID, "home": c.Home, "model_verified": cfg.ModelVerified}
	_, err := c.Key()
	checks["key_configured"] = err == nil
	checks["network"] = network.Inspect(context.Background(), "17433")
	if cfg.Profile == "basic" {
		checks["profile"] = "basic"
		return checks, nil
	}
	adapter, err := executor.NewRuntime(c)
	if err != nil {
		return checks, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	runtimeChecks, err := adapter.Check(ctx, cfg)
	for k, v := range runtimeChecks {
		checks[k] = v
	}
	return checks, err
}
