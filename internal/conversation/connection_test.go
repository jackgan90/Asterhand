package conversation

import (
	"context"
	"testing"
)

type connectionFixture struct {
	*fixtureRuntime
	connected bool
}

func (f *connectionFixture) Connection(_ context.Context, connect bool) string {
	f.connected = f.connected || connect
	if f.connected {
		return "connected"
	}
	return "unavailable"
}

func TestConnectionOperationsDoNotCreateOrSend(t *testing.T) {
	runtime := &connectionFixture{fixtureRuntime: &fixtureRuntime{}}
	service, _ := newFixtureService(t, runtime.fixtureRuntime)
	service.runtime = runtime
	ctx := context.Background()
	for _, step := range []struct{ action, expected string }{
		{"connection", "unavailable"}, {"connect", "connected"}, {"connection", "connected"},
	} {
		response, err := service.Call(ctx, Request{Action: step.action, Target: service.Target()})
		if err != nil || response.Connection != step.expected || response.Conversation != nil || response.Turn != nil {
			t.Fatal(step.action, response, err)
		}
	}
	if runtime.sends != 0 || runtime.createCalls != 0 {
		t.Fatal("connection touched conversation or model")
	}
	response, err := service.Call(ctx, Request{Action: "connect", Target: Target{Node: "wrong-node-000000", Role: DefaultRole}})
	if err == nil || response.Connection != "unavailable" {
		t.Fatal("wrong target was connected", response, err)
	}
}
