package app

import (
	"context"
	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/model"
	"testing"
)

func grantTestAutomation(t *testing.T, a *App, roles []string, scopes map[string][]string) (automation.Principal, string) {
	t.Helper()
	p := automation.Principal{ID: model.ID(), Name: "fixture", Roles: roles, Scopes: scopes}
	token := model.ID()
	previous := a.Automation.Registry.Authorize
	a.Automation.Registry.Authorize = func(candidate string) (automation.Principal, error) {
		if candidate == token {
			return p, nil
		}
		return previous(candidate)
	}
	t.Cleanup(func() { a.Automation.Registry.Authorize = previous })
	return p, token
}
func testAutomationCall(a *App, token, name, id string, v any) automation.Response {
	return a.Automation.Registry.Dispatch(context.Background(), token, automation.Request{Version: automation.APIVersion, Node: a.Config.Get().NodeID, Operation: name, ID: id, Arguments: compactResult(v)})
}
