package app

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/config"

	"github.com/jackgan90/Asterhand/internal/editors"
	"github.com/jackgan90/Asterhand/internal/launch"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/operations"
	"github.com/jackgan90/Asterhand/internal/workers"
)

func (a *App) prepareAutomation() error {
	access := automation.NewAccess(a.Config.Home)
	var err error
	r := automation.NewRegistry(a.Config.Get().NodeID, model.ID(), a.Store)
	profile := automation.Profile{Node: r.Node, Pipe: automation.PipeName(a.Config.Home, r.Node), Certificate: a.Peers.Certificate}
	if executable, e := os.Executable(); e == nil {
		profile.Installation = launch.FindInstallation(executable, a.Config.Home)
	}
	if err = access.ConfigureLocal(profile, a.loginToken); err != nil {
		return err
	}
	if err = r.LoadCursorKey(a.Config.Home); err != nil {
		return err
	}
	if a.Harnesses != nil {
		access.ResolveCaller = a.Harnesses.AuthenticateCaller
	}
	r.Authorize = access.Authenticate
	r.Reconcile = a.reconcileAutomationOperation
	a.registerAutomation(r)
	a.Automation = &automation.Server{Registry: r, Access: access}

	a.Editors = editors.New()
	a.Input.SetEditorHandler(a.serveNativeEditors)
	a.Automation.Handler = http.HandlerFunc(a.requests.wrap(a.Automation.ServeHTTP))
	if err = a.Config.SetJournal(a.Store); err != nil {
		return err
	}
	if err = a.Input.SetJournal(a.Store); err != nil {
		return err
	}
	return a.Commands.SetJournal(a.Store)
}

func (a *App) startAutomation(ctx context.Context) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	return a.Automation.Start(ctx, automation.PipeName(a.Config.Home, a.Config.Get().NodeID), a.Peers.TLS)
}

func (a *App) registerAutomation(r *automation.Registry) {
	a.registerAutomationCore(r)
	a.registerAutomationCommands(r)
	a.registerAutomationRuns(r)
	a.registerAutomationLogs(r)
	a.registerAutomationConversations(r)
	a.registerAutomationWorkspace(r)
	a.registerAutomationSettings(r)
	a.registerAutomationTasks(r)
	a.registerAutomationNodes(r)
	a.registerLiveEditors(r)
	a.registerAutomationDiagnostics(r)
	a.registerAutomationBatches(r)
	a.registerAutomationWorkers(r)
	a.registerAutomationHarnesses(r)
	describeAutomationResults(r)
}

// Definitions can be generated or inspected offline. Registration captures
// service methods without opening a writable node or invoking those methods.
func AutomationDefinitions() []automation.Definition {
	r := automation.NewRegistry("", "", nil)
	new(App).registerAutomation(r)
	return r.Definitions(automation.Principal{Roles: []string{"read", "edit", "execute"}})
}

func (a *App) registerAutomationCore(r *automation.Registry) {
	automation.Register(r, automation.Definition{Name: "operations_new_id", Description: "Create an immutable write identity using the node clock before submitting a side effect", Role: "read", Toolset: automation.ToolsetCore}, nil, nil,
		func(ctx context.Context, call automation.Call, v struct {
			Operation string `json:"operation,omitempty" description:"Optional target write operation name (e.g. \"workers_create\" or \"workers_send\")"`
		}) (automation.Outcome, error) {
			if v.Operation != "" {
				found := false
				for _, d := range r.Definitions(call.Principal) {
					if d.Name == v.Operation && d.Write {
						found = true
						break
					}
				}
				if !found {
					return automation.Outcome{}, automation.Fail("invalid_parameters", "Unknown or unavailable write operation")
				}
			}
			return automation.Outcome{Data: map[string]string{"operation_id": operations.NewIDFor(v.Operation, r.Now())}}, nil
		})
	automation.Register(r, automation.Definition{Name: "capabilities", Description: "Discover granted operations, API version, node instance, server time and limits", Role: "read", Toolset: automation.ToolsetCore}, nil, nil,
		func(ctx context.Context, call automation.Call, v automation.Empty) (automation.Outcome, error) {
			names := []map[string]any{}
			for _, d := range r.Definitions(call.Principal) {
				names = append(names, map[string]any{"name": d.Name, "write": d.Write, "batch": d.Batch, "role": d.Role, "toolset": d.EffectiveToolset()})
			}
			toolsets := call.Principal.Toolsets
			if toolsets == nil {
				toolsets = map[string]bool{automation.ToolsetCore: true, automation.ToolsetGeneral: true, automation.ToolsetCollab: true}
			}
			wb := workers.DefaultBudgets()
			if a.Workers != nil {
				wb = a.Workers.Budgets()
			}
			limits := map[string]any{
				"request_bytes":             automation.MaxRequest,
				"response_bytes":            automation.MaxResponse,
				"page":                      200,
				"batch":                     32,
				"wait_seconds":              30,
				"waits_per_actor":           4,
				"waits_per_node":            16,
				"writes_per_actor":          4,
				"writes_per_node":           16,
				"worker_wall_clock_seconds":     int(wb.WallClockTimeout / time.Second),
				"worker_max_wall_clock_seconds": workers.MaxWallClockSeconds,
				"worker_stall_seconds":          int(wb.StallTimeout / time.Second),
				"worker_max_active_turns":       wb.MaxActiveTurns,
			}
			return automation.Outcome{Data: map[string]any{"api_version": automation.APIVersion, "node_id": r.Node, "instance_id": r.Instance, "caller_id": call.Principal.CallerID, "policy_revision": call.Principal.PolicyRevision, "cli_path": a.automationCLIPath(), "roles": call.Principal.Roles, "toolsets": toolsets, "server_time_ms": r.Now().UnixMilli(), "operations": names, "limits": limits}}, nil
		})
	type receiptInput struct {
		ID string `json:"operation_id" description:"Operation identity previously returned by operations_new_id"`
	}
	automation.Register(r, automation.Definition{Name: "operations_get", Description: "Read the original durable write receipt without executing it again", Role: "read", Toolset: automation.ToolsetCore, Control: true}, nil, nil,
		func(ctx context.Context, call automation.Call, v receiptInput) (automation.Outcome, error) {
			if _, err := operations.IDTime(v.ID); err != nil {
				return automation.Outcome{}, err
			}
			receipt, err := a.Store.Lookup(call.Principal.ID, r.Node, v.ID)
			if err != nil {
				return automation.Outcome{}, err
			}
			if err = authorizeReceipt(r, call.Principal, receipt); err != nil {
				return automation.Outcome{}, err
			}
			receipt, err = a.reconcileAutomationOperation(receipt)
			if err != nil {
				return automation.Outcome{}, err
			}
			// Re-verify receipt authorization with the latest Principal immediately
			// before returning so mid-flight toolset revocation cannot leak old receipts.
			if call.Authorize != nil {
				latest, authErr := call.Authorize()
				if authErr != nil {
					return automation.Outcome{}, authErr
				}
				if err = authorizeReceipt(r, latest, receipt); err != nil {
					return automation.Outcome{}, err
				}
			}
			return automation.Outcome{Data: receipt}, nil
		})
	automation.Register(r, automation.Definition{Name: "node_version", Description: "Read product and public API versions", Role: "read"}, nil, nil,
		func(ctx context.Context, call automation.Call, v automation.Empty) (automation.Outcome, error) {
			return automation.Outcome{Data: map[string]any{"version": config.Version, "api_version": automation.APIVersion, "platform": runtime.GOOS, "source_digest": config.SourceDigest}}, nil
		})
}

func authorizeReceipt(r *automation.Registry, p automation.Principal, rec operations.Receipt) error {
	if r != nil {
		if def, ok := r.Definition(rec.Name); ok {
			if !p.AllowsToolset(def.EffectiveToolset()) {
				return automation.Fail("toolset_disabled", "This toolset is disabled for the current caller")
			}
		}
	}
	family := "node"
	switch {
	case strings.HasPrefix(rec.Name, "commands_"), strings.HasPrefix(rec.Name, "runs_"), strings.HasPrefix(rec.Name, "logs_"):
		family = "commands"
	case strings.HasPrefix(rec.Name, "conversations_"), strings.HasPrefix(rec.Name, "turns_"), strings.HasPrefix(rec.Name, "messages_"):
		family = "conversations"
	case strings.HasPrefix(rec.Name, "tasks_"):
		family = "projects"
	case strings.HasPrefix(rec.Name, "workspace_"):
		family = "workspaces"
	case strings.HasPrefix(rec.Name, "workers_"):
		family = "workers"
	case strings.HasPrefix(rec.Name, "drafts_"), strings.HasPrefix(rec.Name, "batches_"):
		return nil // Draft receipts contain only identities owned by this actor.
	}
	return p.Require(family, rec.Target)
}

func (a *App) automationHumanAPI(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/automation/") {
		return false
	}
	if a.Automation == nil {
		a.fail(w, r, 503, automation.Fail("unavailable", "Automation service is unavailable"))
		return true
	}
	if r.URL.Path == "/api/automation/editor" && r.Method == "POST" {
		var input editors.Poll
		r.Body = http.MaxBytesReader(w, r.Body, automation.MaxRequest+64*1024)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if err := d.Decode(&input); err != nil {
			a.fail(w, r, 400, err)
			return true
		}
		if err := a.Editors.Respond("web", input); err != nil {
			a.fail(w, r, 409, err)
		} else {
			send(w, 200, map[string]bool{"ok": true})
		}
		return true
	}
	if r.URL.Path == "/api/automation/human-call" && r.Method == "POST" {
		a.automationHumanCall(w, r)
		return true
	}
	http.NotFound(w, r)
	return true
}

func (a *App) automationCLIPath() string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return ""
	}
	home := ""
	if a != nil && a.Config != nil {
		home = a.Config.Home
	}
	if installPath := launch.FindInstallation(exe, home); installPath != "" {
		cliExe := filepath.Join(filepath.Dir(installPath), "asterhand-cli.exe")
		if info, sErr := os.Stat(cliExe); sErr == nil && !info.IsDir() {
			return cliExe
		}
	}
	return exe
}

func compactResult(value any) json.RawMessage { b, _ := json.Marshal(value); return b }

func boundedWait(seconds int) (time.Duration, error) {
	if seconds < 0 || seconds > 30 {
		return 0, automation.Fail("invalid_parameters", "Wait must be between 1 and 30 seconds")
	}
	if seconds == 0 {
		seconds = 30
	}
	return time.Duration(seconds) * time.Second, nil
}
