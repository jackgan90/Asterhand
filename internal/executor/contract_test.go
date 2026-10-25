package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/packaging"
)

// Re-enter this test binary as an actual child process speaking the CLI envelope.
// This fixture tests the adapter boundary, not the external OpenClaw implementation.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("v" + packaging.Dependencies().Node.Version)
		os.Exit(0)
	}
	if len(os.Args) > 1 && strings.HasPrefix(filepath.Base(os.Args[1]), "fixture-") {
		fixtureCLI(filepath.Base(os.Args[1]))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestRuntimeDiagnosisRejectsProviderVersionDrift(t *testing.T) {
	_, m := fixtureAdapter(t, "fixture-success")
	c := m.Get()
	options, _ := clawOptions(c)
	options.EntryPath = filepath.Join(t.TempDir(), "openclaw.mjs")
	options.PluginPath = t.TempDir()
	options.ProviderPaths[c.LLM.Provider] = t.TempDir()
	setClawOptions(&c, options)
	component, _ := packaging.Dependencies().Component("openclaw")
	os.WriteFile(options.EntryPath, nil, 0600)
	os.WriteFile(filepath.Join(filepath.Dir(options.EntryPath), "package.json"), []byte(`{"version":"`+component.Version+`"}`), 0600)
	os.WriteFile(filepath.Join(options.ProviderPaths[c.LLM.Provider], "package.json"), []byte(`{"version":"0.0.0"}`), 0600)
	checks, err := (&OpenClaw{}).Check(context.Background(), c)
	if err == nil || !strings.Contains(err.Error(), "provider") || checks["provider_version"] != "0.0.0" {
		t.Fatal("provider drift accepted", err)
	}
}
func fixtureCLI(mode string) {
	if mode == "fixture-block-input" {
		for {
			time.Sleep(time.Second)
		}
	}
	goal, _ := io.ReadAll(os.Stdin)
	switch mode {
	case "fixture-success":
		json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": true, "status": "ok", "final": string(goal)})
	case "fixture-error":
		json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": false, "status": "error", "error": map[string]string{"kind": "provider", "message": os.Getenv("DEEPSEEK_API_KEY") + os.Getenv("ASTERHAND_TOOL_TOKEN")}})
		os.Exit(2)
	case "fixture-malformed":
		fmt.Fprint(os.Stderr, os.Getenv("DEEPSEEK_API_KEY"), os.Getenv("ASTERHAND_TOOL_TOKEN"))
		fmt.Fprint(os.Stdout, "not-json")
	case "fixture-inconsistent":
		fmt.Fprint(os.Stdout, `{"ok":true,"status":"error"}`)
	case "fixture-unknown-status":
		fmt.Fprint(os.Stdout, `{"ok":true,"status":"finished"}`)
	case "fixture-hang":
		os.WriteFile("ready", []byte("ready"), 0600)
		for {
			time.Sleep(time.Second)
		}
	}
}
func fixtureAdapter(t *testing.T, mode string) (*Selected, *config.Manager) {
	t.Helper()
	m, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := m.Get()
	setClawOptions(&c, openClawOptions{NodePath: os.Args[0], EntryPath: filepath.Join(m.Home, mode), PluginPath: filepath.Join(m.Home, "plugin"), ProviderPaths: map[string]string{c.LLM.Provider: filepath.Join(m.Home, "provider")}})
	if err = m.Save(c); err != nil {
		t.Fatal(err)
	}
	if err = m.SetKey("synthetic-provider-secret"); err != nil {
		t.Fatal(err)
	}
	return &Selected{Config: m}, m
}
func contractRun() Run {
	return Run{ID: "contract-attempt", Mode: "execute", Goal: "hello world", Callback: "http://127.0.0.1:17432/internal/tool", Token: "synthetic-callback-secret"}
}

func TestRuntimeSelectionAndCapabilitiesContract(t *testing.T) {
	x, m := fixtureAdapter(t, "fixture-success")
	for _, d := range Available() {
		if d.Capabilities.NativeResume || d.Capabilities.NativeApproval || d.Capabilities.NativeEvents {
			t.Fatal("unsupported native capability advertised")
		}
		if !d.Capabilities.ProcessCancellation || !d.Capabilities.UserQuestions || !d.Capabilities.ToolCallbacks {
			t.Fatal("required capability absent")
		}
		if !slices.Equal(d.Capabilities.Modes, []string{"route", "execute", "probe"}) {
			t.Fatal("mode mismatch")
		}
	}
	c := m.Get()
	c.AgentRuntime = "hermes-not-installed"
	m.Save(c)
	if _, err := x.Execute(context.Background(), contractRun()); err == nil {
		t.Fatal("unknown runtime fell back")
	}
	c.AgentRuntime = "openclaw"
	c.LLM.Provider = "foreign"
	m.Save(c)
	if _, err := x.Execute(context.Background(), contractRun()); err == nil {
		t.Fatal("unknown provider fell back")
	}
	if _, err := os.Stat(filepath.Join(m.Home, "runs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid selection started execution")
	}
}

func TestBundleChangeInvalidatesReadiness(t *testing.T) {
	_, m := fixtureAdapter(t, "fixture-success")
	c := m.Get()
	c.ModelVerified = true
	c.VerifiedBundle = "old-bundle"
	m.Save(c)
	if err := InvalidateStaleVerification(m); err != nil {
		t.Fatal(err)
	}
	if m.Get().ModelVerified {
		t.Fatal("old readiness survived bundle change")
	}
	c = m.Get()
	c.ModelVerified = true
	c.VerifiedBundle = BundleIdentity()
	c.VerifiedExecution = ExecutionIdentity(c)
	m.Save(c)
	if err := InvalidateStaleVerification(m); err != nil || !m.Get().ModelVerified {
		t.Fatal("same bundle lost readiness", err)
	}
}
func TestAdapterResultFailureAndRedactionContract(t *testing.T) {
	for _, mode := range []string{"fixture-success", "fixture-error", "fixture-malformed", "fixture-inconsistent", "fixture-unknown-status"} {
		t.Run(mode, func(t *testing.T) {
			x, _ := fixtureAdapter(t, mode)
			r := contractRun()
			r.Goal += " synthetic-provider-secret synthetic-callback-secret"
			result, err := x.Execute(context.Background(), r)
			if mode == "fixture-success" && (err != nil || !result.OK || !strings.HasPrefix(result.Final, "hello world")) {
				t.Fatalf("result mismatch: %+v %v", result, err)
			}
			if mode != "fixture-success" && err == nil {
				t.Fatal("failure reported success")
			}
			output := fmt.Sprintf("%+v %v", result, err)
			if result.Error != nil {
				output += result.Error.Message
			}
			if strings.Contains(output, "synthetic-provider-secret") || strings.Contains(output, r.Token) {
				t.Fatal("secret leaked")
			}
		})
	}
}
func TestAdapterCancellationContract(t *testing.T) {
	for _, mode := range []string{"fixture-hang", "fixture-block-input"} {
		t.Run(mode, func(t *testing.T) {
			x, m := fixtureAdapter(t, mode)
			r := contractRun()
			if mode == "fixture-block-input" {
				r.Goal = strings.Repeat("x", 1024*1024)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := x.Execute(ctx, r); done <- err }()
			deadline := time.Now().Add(10 * time.Second)
			if mode == "fixture-hang" {
				for {
					if _, err := os.Stat(filepath.Join(m.Home, "runs", r.ID, "ready")); err == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("fixture did not become ready")
					}
					time.Sleep(10 * time.Millisecond)
				}
			} else {
				time.Sleep(100 * time.Millisecond)
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("wrong cancellation: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancel did not finish")
			}
		})
	}
}
func TestToolModesAndRunBoundaryContract(t *testing.T) {
	_, m := fixtureAdapter(t, "fixture-success")
	expected := map[string][]string{"execute": {"asterhand_create_text", "asterhand_read_text", "asterhand_ask_user"}, "route": {"asterhand_submit", "asterhand_ask_user"}, "probe": {"asterhand_probe"}}
	for mode, allow := range expected {
		b, err := RuntimeConfig(m.Get(), mode, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		var c struct {
			Tools struct {
				Allow, Deny []string
				Profile     string
			}
		}
		if err = json.Unmarshal(b, &c); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(c.Tools.Allow, allow) || c.Tools.Profile != "full" || !slices.Contains(c.Tools.Deny, "group:runtime") || !slices.Contains(c.Tools.Deny, "group:fs") {
			t.Fatalf("tool boundary drift: %s", b)
		}
	}
	if _, err := RuntimeConfig(m.Get(), "unknown", t.TempDir()); err == nil {
		t.Fatal("unknown mode enabled tools")
	}
	for _, change := range []func(*Run){func(r *Run) { r.ID = "../escape" }, func(r *Run) { r.Mode = "unknown" }, func(r *Run) { r.Callback = "https://remote.invalid/internal/tool" }, func(r *Run) { r.Token = "" }} {
		r := contractRun()
		change(&r)
		if validateRun(r) == nil {
			t.Fatalf("accepted invalid run %+v", r)
		}
	}
}
