package executor

import (
	"context"
	"net/url"
	"regexp"
	"slices"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/i18n"
)

type Capabilities struct {
	Modes               []string `json:"modes"`
	ToolCallbacks       bool     `json:"tool_callbacks"`
	UserQuestions       bool     `json:"user_questions"`
	ProcessCancellation bool     `json:"process_cancellation"`
	NativeResume        bool     `json:"native_resume"`
	NativeApproval      bool     `json:"native_approval"`
	NativeEvents        bool     `json:"native_events"`
}
type Descriptor struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Providers    []string     `json:"providers"`
	Capabilities Capabilities `json:"capabilities"`
}

// Runtime owns agent-specific execution, installation layout and diagnostics.
// Query/recovery and durable questions remain owned by the task service.
type Runtime interface {
	Executor
	Describe() Descriptor
	Discover(config.Config, string) config.Config
	Check(context.Context, config.Config) (map[string]any, error)
	Validate(config.Config) error
	Configuration(config.Config) ConfigurationCapabilities
	ProtectedPaths(config.Config) ([]string, error)
}

func NewRuntime(c *config.Manager) (Runtime, error) {
	return RuntimeFor(c.Get().AgentRuntime, c)
}
func RuntimeFor(id string, c *config.Manager) (Runtime, error) {
	switch id {
	case "openclaw":
		return &OpenClaw{Config: c}, nil
	default:
		return nil, i18n.ErrArgs("err.executor.runtime_missing", map[string]string{"id": id})
	}
}
func Available() []Descriptor { return []Descriptor{(&OpenClaw{}).Describe()} }
func ValidateSelection(c config.Config) error {
	if err := config.ValidateSelection(c); err != nil {
		return err
	}
	adapter, err := RuntimeFor(c.AgentRuntime, nil)
	if err != nil {
		return err
	}
	if !slices.Contains(adapter.Describe().Providers, c.LLM.Provider) {
		return i18n.ErrArgs("err.executor.runtime_provider_unsupported", map[string]string{"provider": c.LLM.Provider})
	}
	return adapter.Validate(c)
}

// Selected resolves each attempt from the current validated node configuration.
type Selected struct{ Config *config.Manager }

func (s *Selected) Execute(ctx context.Context, r Run) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := RequireTaskConfiguration(s.Config, s.Config.Get()); err != nil {
		return Result{}, err
	}
	runtime, err := NewRuntime(s.Config)
	if err != nil {
		return Result{}, err
	}
	return runtime.Execute(ctx, r)
}

var runID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func validateRun(r Run) error {
	if !runID.MatchString(r.ID) || r.Token == "" {
		return i18n.Err("err.executor.invalid_attempt_token")
	}
	if r.Mode != "route" && r.Mode != "execute" && r.Mode != "probe" {
		return i18n.ErrArgs("err.executor.unsupported_exec_mode", map[string]string{"mode": r.Mode})
	}
	u, err := url.Parse(r.Callback)
	if err != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/internal/tool" {
		return i18n.Err("err.executor.callback_must_be_local")
	}
	return nil
}
