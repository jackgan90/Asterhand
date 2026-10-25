package executor

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jackgan90/Asterhand/internal/config"
)

type directoryFixture struct {
	Runtime
	paths []string
	err   error
}

func (f directoryFixture) ProtectedPaths(config.Config) ([]string, error) { return f.paths, f.err }

func TestWorkspaceUsesAdapterDeclarationAndResolvesAliases(t *testing.T) {
	base := t.TempDir()
	protected := filepath.Join(base, "vendor-not-in-production")
	home := filepath.Join(base, "node-data")
	if err := os.Mkdir(protected, 0700); err != nil {
		t.Fatal(err)
	}
	f := directoryFixture{paths: []string{protected}}
	c := config.Config{AgentRuntime: "different-agent"}
	for _, path := range []string{protected, filepath.Join(protected, "new", "child"), base, home, filepath.Join(home, "work")} {
		if validateWorkspace(f, c, home, path) == nil {
			t.Fatalf("accepted overlapping directory: %s", path)
		}
	}
	if err := validateWorkspace(f, c, home, filepath.Join(base, "workspace")); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if runtime.GOOS == "windows" {
		if output, err := exec.Command("cmd", "/c", "mklink", "/J", alias, protected).CombinedOutput(); err != nil {
			t.Fatalf("junction setup: %v %s", err, output)
		}
	} else if err := os.Symlink(protected, alias); err != nil {
		t.Fatal(err)
	}
	if validateWorkspace(f, c, home, filepath.Join(alias, "new-child")) == nil {
		a, _ := canonicalPath(filepath.Join(alias, "new-child"))
		b, _ := canonicalPath(protected)
		t.Fatalf("directory alias bypassed protection: alias=%q protected=%q", a, b)
	}
	for _, bad := range []directoryFixture{{err: errors.New("declaration failed")}, {paths: []string{"relative"}}, {paths: []string{""}}} {
		if validateWorkspace(bad, c, home, filepath.Join(base, "work")) == nil {
			t.Fatal("invalid declaration failed open")
		}
	}
}

func TestOpenClawOptionsValidateAndProtectAllDirectories(t *testing.T) {
	base := t.TempDir()
	c := config.Config{AgentRuntime: "openclaw"}
	options := openClawOptions{NodePath: filepath.Join(base, "host", "node.exe"), EntryPath: filepath.Join(base, "agent", "entry.mjs"), PluginPath: filepath.Join(base, "tools"), ProviderPaths: map[string]string{"first": filepath.Join(base, "model-one"), "second": filepath.Join(base, "model-two")}}
	setClawOptions(&c, options)
	paths, err := (&OpenClaw{}).ProtectedPaths(c)
	if err != nil || len(paths) != 5 {
		t.Fatal("not all adapter directories protected", paths, err)
	}
	for _, path := range paths {
		if validateWorkspace(&OpenClaw{}, c, filepath.Join(base, "data"), filepath.Join(path, "work")) == nil {
			t.Fatal("adapter directory not protected", path)
		}
	}
	for _, raw := range []string{`null`, `[]`, `{"unknown":1}`, `{"entry_path":"relative"}`, `{} {}`} {
		c.SetRuntimeOptions("openclaw", json.RawMessage(raw))
		if _, err := clawOptions(c); err == nil {
			t.Fatal("invalid options accepted", raw)
		}
	}
}
