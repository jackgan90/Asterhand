package conversation

import (
	"context"
	"testing"

	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/prompt"
)

func TestMissingPromptOverrideDoesNotBlockConversation(t *testing.T) {
	runtime := &fixtureRuntime{}
	service, _ := newFixtureService(t, runtime)
	id := createFixture(t, service)
	response, err := service.Call(context.Background(), Request{Action: "submit", Target: service.Target(), Conversation: id, Operation: model.ID(), Text: "ordinary chat"})
	if err != nil || response.Turn.Delivery != "accepted" || response.Turn.Prompt.Mode != prompt.Unsupported || response.Turn.Prompt.State != prompt.Degraded {
		t.Fatalf("optional override blocked or misreported chat: %+v %v", response.Turn, err)
	}
	runtime.observation = Observation{Accepted: true, Stopped: true, Run: "completed", Saved: true}
	result, _, err := service.reconcile(context.Background(), response.Turn.Operation)
	if err != nil || result.Run != "completed" || runtime.sends != 1 {
		t.Fatal("unsupported prompt prevented ordinary completion")
	}
}

func TestPromptObservationPersistsPerTurnAndMissingProofDegrades(t *testing.T) {
	application := prompt.Application{Revision: "fixture-revision", Mode: prompt.Replace, State: prompt.Pending}
	record := turnRecord{Unsettled: true, Turn: Turn{Prompt: application, Run: "running"}}
	confirmed := application
	confirmed.State = prompt.Applied
	applyObservation(&record, Observation{Prompt: &confirmed, Accepted: true, Stopped: true, Run: "completed", Saved: true})
	if record.Turn.Prompt.State != prompt.Applied {
		t.Fatal("confirmed policy lost")
	}
	// A later runtime restart without an observation must retain established proof.
	applyObservation(&record, Observation{Stopped: true, Run: "completed", Saved: true})
	if record.Turn.Prompt.State != prompt.Applied {
		t.Fatal("missing new observation erased prior proof")
	}
	record.Turn.Prompt = application
	applyObservation(&record, Observation{Stopped: true, Run: "failed"})
	if record.Turn.Prompt.State != prompt.Degraded {
		t.Fatal("terminal turn falsely implied policy was applied")
	}
}
