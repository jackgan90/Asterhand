package executor

import (
	"fmt"
	"path/filepath"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/packaging"
)

// This translation belongs to OpenClaw; another agent uses its own provider bridge.
type providerBridge struct{ PluginID, Package, KeyEnv, Version string }

func openClawProvider(c config.Config) (providerBridge, error) {
	if err := config.ValidateSelection(c); err != nil {
		return providerBridge{}, err
	}
	switch c.LLM.Provider {
	case "deepseek":
		d, err := packaging.Dependencies().Component("deepseek-provider")
		if err != nil {
			return providerBridge{}, err
		}
		return providerBridge{"deepseek", d.Package, "DEEPSEEK_API_KEY", d.Version}, nil
	default:
		return providerBridge{}, fmt.Errorf("OpenClaw 未适配模型 provider: %s", c.LLM.Provider)
	}
}
func (p providerBridge) Path(base string) string {
	return filepath.Join(base, "runtime", "node_modules", filepath.FromSlash(p.Package))
}
