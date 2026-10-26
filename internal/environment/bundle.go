package environment

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/executor"
	"github.com/jackgan90/Asterhand/internal/llm"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostbundle"
	"github.com/jackgan90/Asterhand/packaging"
)

// CheckBundle is read-only and needs neither a node home nor credentials/network.
func CheckBundle(ctx context.Context, base string) error {
	b, err := os.ReadFile(filepath.Join(base, "packaging", "dependencies.json"))
	if err != nil {
		return err
	}
	if sha256.Sum256(b) != packaging.Digest() {
		return fmt.Errorf("安装包依赖清单与程序内嵌清单不符")
	}
	if runtime.GOOS == "windows" {
		host, err := hostbundle.Verify(base, config.Version, config.SourceDigest)
		if err != nil {
			return err
		}
		host.Close()
	}
	return checkBundleTargets(ctx, base, packaging.Dependencies(), func(id string) (executor.Runtime, error) {
		return executor.RuntimeFor(id, nil)
	})
}

func checkBundleTargets(ctx context.Context, base string, catalog packaging.Catalog, resolve func(string) (executor.Runtime, error)) error {
	if err := catalog.Validate(); err != nil {
		return err
	}
	for _, target := range catalog.Bundle.Targets {
		c := config.Config{AgentRuntime: target.Runtime, LLM: llm.Selection{Provider: target.Provider, Model: target.Model, CredentialRef: target.CredentialRef, Endpoint: target.Endpoint}}
		adapter, err := resolve(target.Runtime)
		if err != nil {
			return fmt.Errorf("安装包目标 %s: %w", target.ID, err)
		}
		c = adapter.Discover(c, base)
		if err = adapter.Validate(c); err == nil {
			_, err = adapter.Check(ctx, c)
		}
		if err != nil {
			return fmt.Errorf("安装包目标 %s: %w", target.ID, err)
		}
	}
	return nil
}
