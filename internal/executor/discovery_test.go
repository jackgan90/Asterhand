package executor

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jackgan90/Asterhand/internal/config"
)

func discoveryBundle(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	node := "node"
	if runtime.GOOS == "windows" {
		node = "node.exe"
	}
	path := filepath.Join(base, "runtime", "node", node)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	return base
}

func verifiedConfig(t *testing.T, m *config.Manager, c config.Config) {
	t.Helper()
	c.ModelVerified = true
	c.VerifiedBundle = BundleIdentity()
	c.VerifiedExecution = ExecutionIdentity(c)
	if err := m.Save(c); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveryPreservesExplicitOptionsAcrossRestart(t *testing.T) {
	for _, kind := range []string{"custom", "explicit-other-bundle", "legacy-custom"} {
		t.Run(kind, func(t *testing.T) {
			_, m := fixtureAdapter(t, "fixture-success")
			c := m.Get()
			if kind == "explicit-other-bundle" {
				c.RuntimeOptions = nil
				c = (&OpenClaw{}).Discover(c, discoveryBundle(t))
			}
			if kind != "legacy-custom" {
				c.MarkRuntimeOptionsExplicit(c.AgentRuntime, true)
			}
			before, _ := clawOptions(c)
			verifiedConfig(t, m, c)
			reopened, err := config.Open(m.Home)
			if err != nil {
				t.Fatal(err)
			}
			after := (&OpenClaw{}).Discover(reopened.Get(), discoveryBundle(t))
			if err = reopened.Save(after); err != nil {
				t.Fatal(err)
			}
			if err = InvalidateStaleVerification(reopened); err != nil {
				t.Fatal(err)
			}
			after = reopened.Get()
			options, _ := clawOptions(after)
			if before.NodePath != options.NodePath || before.EntryPath != options.EntryPath || before.PluginPath != options.PluginPath || before.ProviderPaths[c.LLM.Provider] != options.ProviderPaths[c.LLM.Provider] {
				t.Fatal("explicit paths changed")
			}
			if !after.ModelVerified || !after.RuntimeOptionsExplicit[c.AgentRuntime] {
				t.Fatal("stable explicit configuration lost readiness or provenance")
			}
		})
	}
}

func TestAutomaticAndLegacyBundlePathsMigrateAndInvalidateReadiness(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "marked", true: "legacy"}[legacy], func(t *testing.T) {
			m, err := config.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			c := (&OpenClaw{}).Discover(m.Get(), discoveryBundle(t))
			before, _ := clawOptions(c)
			if legacy {
				c.RuntimeOptionsExplicit = nil
			}
			verifiedConfig(t, m, c)
			base := discoveryBundle(t)
			after := (&OpenClaw{}).Discover(m.Get(), base)
			if err = m.Save(after); err != nil {
				t.Fatal(err)
			}
			if err = InvalidateStaleVerification(m); err != nil {
				t.Fatal(err)
			}
			options, _ := clawOptions(m.Get())
			if before.NodePath == options.NodePath || options.PluginPath != filepath.Join(base, "plugins", "asterhand-tools") {
				t.Fatal("automatic bundle did not migrate")
			}
			if m.Get().ModelVerified || m.Get().RuntimeOptionsExplicit[c.AgentRuntime] {
				t.Fatal("new paths retained readiness or became explicit")
			}
		})
	}
}

func TestExecutionIdentityInvalidatesChangedOptionsAndLegacyReadiness(t *testing.T) {
	for _, change := range []string{"options", "selection", "missing-identity", "unrelated-options"} {
		t.Run(change, func(t *testing.T) {
			_, m := fixtureAdapter(t, "fixture-success")
			c := m.Get()
			verifiedConfig(t, m, c)
			c = m.Get()
			switch change {
			case "options":
				options, _ := clawOptions(c)
				options.EntryPath = filepath.Join(t.TempDir(), "changed.mjs")
				setClawOptions(&c, options)
			case "selection":
				c.LLM.Model = "deepseek/deepseek-v4-flash"
			case "missing-identity":
				c.VerifiedExecution = ""
			case "unrelated-options":
				c.SetRuntimeOptions("inactive-runtime", []byte(`{"anything":true}`))
			}
			if err := m.Save(c); err != nil {
				t.Fatal(err)
			}
			if err := InvalidateStaleVerification(m); err != nil {
				t.Fatal(err)
			}
			if m.Get().ModelVerified != (change == "unrelated-options") {
				t.Fatal("wrong readiness decision", change)
			}
		})
	}
}
