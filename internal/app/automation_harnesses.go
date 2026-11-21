package app

import (
	"context"
	"strings"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/harnesses"
)

type harnessesListInput struct {
	DetectedOnly bool `json:"detected_only,omitempty" description:"When true, only return harnesses currently detected on this machine"`
}

type harnessesGetInput struct {
	HarnessID string `json:"harness_id" description:"Target harness identifier (antigravity, codex, claude-code, or deepseek-harness)"`
}

type harnessesListView struct {
	Revision string                  `json:"revision"`
	Items    []harnesses.HarnessView `json:"items"`
}

func (a *App) registerAutomationHarnesses(r *automation.Registry) {
	scopeHarnesses := func(p automation.Principal, target string) error {
		return p.Require("workers", target)
	}

	automation.Register(r, automation.Definition{
		Name:        "harnesses_list",
		Description: "List known local coding harnesses, detection status, versions, capabilities, and collaboration readiness (non-secret view)",
		Role:        "read",
		Toolset:     automation.ToolsetCollab,
	}, func(v harnessesListInput) string { return "*" },
		func(p automation.Principal, v harnessesListInput) error { return scopeHarnesses(p, "*") },
		func(ctx context.Context, call automation.Call, v harnessesListInput) (automation.Outcome, error) {
			if a.Harnesses == nil {
				return automation.Outcome{}, automation.Fail("unavailable", "Harnesses service is unavailable")
			}
			st := a.Harnesses.Status()
			items := make([]harnesses.HarnessView, 0, len(st.Harnesses))
			for _, h := range st.Harnesses {
				if v.DetectedOnly && !h.Detected {
					continue
				}
				items = append(items, h)
			}
			return automation.Outcome{Data: harnessesListView{
				Revision: st.Revision,
				Items:    items,
			}}, nil
		})

	automation.Register(r, automation.Definition{
		Name:        "harnesses_get",
		Description: "Inspect non-secret discovery status, version, capabilities, and policy state for a single local coding harness",
		Role:        "read",
		Toolset:     automation.ToolsetCollab,
	}, func(v harnessesGetInput) string { return strings.TrimSpace(v.HarnessID) },
		func(p automation.Principal, v harnessesGetInput) error { return scopeHarnesses(p, strings.TrimSpace(v.HarnessID)) },
		func(ctx context.Context, call automation.Call, v harnessesGetInput) (automation.Outcome, error) {
			if a.Harnesses == nil {
				return automation.Outcome{}, automation.Fail("unavailable", "Harnesses service is unavailable")
			}
			id := strings.TrimSpace(v.HarnessID)
			if id == "" {
				return automation.Outcome{}, automation.Fail("invalid_parameters", "harness_id is required")
			}
			h, ok := a.Harnesses.Get(id)
			if !ok {
				return automation.Outcome{}, automation.Fail("not_found", "Unknown harness_id")
			}
			return automation.Outcome{Data: h}, nil
		})
}

func describeHarnessAutomationResults(r *automation.Registry) {
	r.DescribeData("harnesses_list", resultObject("revision", "", "", map[string]any{
		"items": arrayOf(automation.Schema[harnesses.HarnessView]()),
	}))
	r.DescribeData("harnesses_get", automation.Schema[harnesses.HarnessView]())
}
