package app

import (
	"context"
	"encoding/json"
	"runtime"

	"github.com/jackgan90/Asterhand/internal/automation"
	commandswire "github.com/jackgan90/Asterhand/internal/localcommands/wire"
)

func (a *App) registerAutomationDiagnostics(r *automation.Registry) {
	type inspect struct {
		ID string `json:"operation_id,omitempty"`
	}
	automation.Register(r, automation.Definition{Name: "diagnostics.inspect", Description: "Read current model readiness or the original diagnostic result without a model call", Role: "read"}, nil, func(p automation.Principal, v inspect) error { return p.Require("node", r.Node) },
		func(ctx context.Context, call automation.Call, v inspect) (automation.Outcome, error) {
			if v.ID == "" {
				data := a.probeSnapshot("en-US")
				delete(data, "error")
				delete(data, "message_args")
				return automation.Outcome{Data: data}, nil
			}
			receipt, err := a.Store.Lookup(call.Principal.ID, r.Node, v.ID)
			if err != nil {
				return automation.Outcome{}, err
			}
			if receipt.Name != "diagnostics.run" {
				return automation.Outcome{}, automation.Fail("invalid_parameters", "This identity is not a diagnostic")
			}
			state, err := a.Store.AutomationState(receipt.Identity)
			if err != nil {
				return automation.Outcome{}, err
			}
			var data map[string]any
			if err = json.Unmarshal(state.Data, &data); err != nil {
				return automation.Outcome{}, err
			}
			if state.Instance != r.Instance && !state.Complete {
				data = map[string]any{"state": "interrupted", "outcome": "unknown"}
			}
			data["operation_id"] = v.ID
			return automation.Outcome{Data: data}, nil
		})
	automation.Register(r, automation.Definition{Name: "diagnostics.run", Description: "Explicitly run the existing model/tool diagnostic; consumes API quota", Role: "execute", Write: true}, func(v automation.Empty) string { return r.Node }, func(p automation.Principal, v automation.Empty) error { return p.Require("node", r.Node) },
		func(ctx context.Context, call automation.Call, v automation.Empty) (automation.Outcome, error) {
			err := a.startProbe(&call.Identity, call.Check)
			ref := map[string]string{"operation_id": call.Identity.ID, "state": "running"}
			return automation.Outcome{Data: ref, Summary: ref, Accepted: err == nil}, err
		})
	type open struct {
		View    string `json:"view"`
		Command string `json:"command_id"`
	}
	automation.Register(r, automation.Definition{Name: "desktop.open", Description: "Explicitly open the existing command settings or logs window on the local desktop", Role: "execute", Write: true}, func(v open) string { return v.Command }, func(p automation.Principal, v open) error { return p.Require("commands", v.Command) },
		func(ctx context.Context, call automation.Call, v open) (automation.Outcome, error) {
			if runtime.GOOS != "windows" || !a.Input.Status().Supported {
				return automation.Outcome{}, automation.Fail("desktop_unavailable", "An interactive Windows desktop is required")
			}
			if v.View != "settings" && v.View != "logs" {
				return automation.Outcome{}, automation.Fail("invalid_parameters", "view must be settings or logs")
			}
			if _, err := a.Commands.Get(v.Command); err != nil {
				return automation.Outcome{}, commandError(err)
			}
			if err := call.Check(); err != nil {
				return automation.Outcome{}, err
			}
			reply := a.commandCall(ctx, commandswire.Request{Action: v.View, ID: v.Command})
			if reply.Error != nil {
				return automation.Outcome{Unknown: true}, commandError(reply.Error)
			}
			ref := map[string]string{"view": v.View, "command_id": v.Command}
			return automation.Outcome{Data: ref, Summary: ref, Accepted: true}, nil
		})
}
