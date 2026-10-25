package environment

import (
	"context"
	"errors"
	"testing"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/executor"
	"github.com/jackgan90/Asterhand/packaging"
)

type bundleFixture struct {
	executor.Runtime
	seen *[]config.Config
	fail bool
}

func (f bundleFixture) Discover(c config.Config, base string) config.Config {
	c.SetRuntimeOptions(c.AgentRuntime, []byte(`{"discovered":true}`))
	return c
}
func (f bundleFixture) Validate(c config.Config) error {
	if c.RuntimeOptions[c.AgentRuntime] == nil {
		return errors.New("discovery skipped")
	}
	return nil
}
func (f bundleFixture) Check(ctx context.Context, c config.Config) (map[string]any, error) {
	*f.seen = append(*f.seen, c)
	if f.fail {
		return nil, errors.New("fixture failure")
	}
	return nil, nil
}

func TestBundleDispatchesEveryDescribedTarget(t *testing.T) {
	catalog := packaging.Dependencies()
	catalog.Bundle = packaging.Bundle{DefaultTarget: "one", Targets: []packaging.Target{{ID: "one", Runtime: "foreign-agent-a", Provider: "foreign-model-a", Model: "model-a", CredentialRef: "key-a"}, {ID: "two", Runtime: "foreign-agent-b", Provider: "foreign-model-b", Model: "model-b", CredentialRef: "key-b", Endpoint: "https://example.invalid"}}}
	var seen []config.Config
	resolve := func(id string) (executor.Runtime, error) {
		if id != "foreign-agent-a" && id != "foreign-agent-b" {
			t.Fatalf("hardcoded runtime dispatched: %s", id)
		}
		return bundleFixture{seen: &seen}, nil
	}
	if err := checkBundleTargets(context.Background(), t.TempDir(), catalog, resolve); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0].LLM.Provider != "foreign-model-a" || seen[1].LLM.Model != "model-b" || seen[1].LLM.Endpoint != "https://example.invalid" || seen[1].LLM.CredentialRef != "key-b" {
		t.Fatal("target selection lost")
	}
	resolve = func(id string) (executor.Runtime, error) {
		return bundleFixture{seen: &seen, fail: id == "foreign-agent-b"}, nil
	}
	if checkBundleTargets(context.Background(), t.TempDir(), catalog, resolve) == nil {
		t.Fatal("second target failure ignored")
	}
	resolve = func(string) (executor.Runtime, error) { return nil, errors.New("not registered") }
	if checkBundleTargets(context.Background(), t.TempDir(), catalog, resolve) == nil {
		t.Fatal("unknown target accepted")
	}
	catalog.Bundle.Targets = nil
	if checkBundleTargets(context.Background(), t.TempDir(), catalog, resolve) == nil {
		t.Fatal("empty bundle accepted")
	}
}
