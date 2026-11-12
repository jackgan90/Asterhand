package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/store"
)

type remoteAnswerInput struct {
	InteractionID string `json:"interaction_id"`
	Attempt       string `json:"attempt"`
	AnswerID      string `json:"answer_id"`
	Answer        string `json:"answer"`
}

func (a *App) cancelOutbox(id, actor string) error {
	return a.Store.Modify("outbox", id, func(raw []byte) (any, error) {
		var current model.Outbox
		if err := decode(raw, &current); err != nil {
			return nil, err
		}
		current.Cancel = true
		if actor != "" {
			current.CancelledBy = actor
		}
		return current, nil
	})
}

func (a *App) queueRemoteAnswer(o model.Outbox, input remoteAnswerInput, actor string) (pendingAnswer, error) {
	if len(input.AnswerID) < 8 || input.Answer == "" || len(input.Answer) > 4000 {
		return pendingAnswer{}, i18n.Err("err.app.remote_invalid_answer")
	}
	body, _ := json.Marshal(input)
	key := model.Hash([]string{o.ID, input.InteractionID, input.AnswerID})
	a.mu.Lock()
	defer a.mu.Unlock()
	var existing pendingAnswer
	if err := a.Store.Get("answer-outbox", key, &existing); err == nil {
		if string(existing.Body) != string(body) {
			return existing, i18n.Err("err.app.remote_answer_conflict")
		}
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return existing, err
	}
	answer := pendingAnswer{ID: key, OutboxID: o.ID, Body: body, State: "saved", Actor: actor}
	return answer, a.Store.Put("answer-outbox", key, answer)
}

func (a *App) automationTaskCancel(ctx context.Context, call automation.Call, v automation.TaskRef) (automation.Outcome, error) {
	j, outbox, err := a.loadAutomationTask(call.Principal, v)
	if err != nil {
		return automation.Outcome{}, err
	}
	if err = call.Check(); err != nil {
		return automation.Outcome{}, err
	}
	if outbox != nil {
		err = a.cancelOutbox(outbox.ID, "automation:"+call.Principal.ID)
		summary := map[string]any{"outbox_id": outbox.ID, "cancel_registered": err == nil}
		return automation.Outcome{Data: summary, Summary: summary, Accepted: err == nil, Unknown: err != nil}, err
	}
	j, err = a.Engine.CancelAs(j.ID, "automation:"+call.Principal.ID)
	return automation.Outcome{Data: taskSummary(j), Summary: map[string]string{"task_id": v.ID, "state": j.State}, Accepted: err == nil}, taskError(err)
}

func (a *App) automationTaskAnswer(ctx context.Context, call automation.Call, v automation.TaskAnswer) (automation.Outcome, error) {
	j, outbox, err := a.loadAutomationTask(call.Principal, v.Task)
	if err != nil {
		return automation.Outcome{}, err
	}
	if j.Interaction == nil || j.Interaction.Kind != "question" {
		return automation.Outcome{}, automation.Fail("human_required", "Only ordinary task questions may be answered through automation")
	}
	if j.Attempt != v.Attempt || j.Interaction.Attempt != v.Attempt || j.Interaction.ID != v.Interaction {
		return automation.Outcome{}, automation.Fail("conflict", "Task attempt or interaction changed")
	}
	if err = call.Check(); err != nil {
		return automation.Outcome{}, err
	}
	actor := "automation:" + call.Principal.ID
	if outbox != nil {
		peer, peerErr := a.Peers.Get(outbox.PeerID)
		if peerErr != nil || peer.Revoked {
			return automation.Outcome{}, automation.Fail("forbidden", "The original peer is unavailable or revoked")
		}
		answer, err := a.queueRemoteAnswer(*outbox, remoteAnswerInput{InteractionID: v.Interaction, Attempt: v.Attempt, AnswerID: call.Identity.NativeID(), Answer: v.Answer}, actor)
		summary := map[string]string{"outbox_id": outbox.ID, "answer_id": answer.ID, "state": answer.State}
		return automation.Outcome{Data: summary, Summary: summary, Accepted: err == nil}, taskError(err)
	}
	j, err = a.Engine.Answer(j.ID, v.Interaction, v.Attempt, call.Identity.NativeID(), v.Answer, actor)
	return automation.Outcome{Data: taskSummary(j), Summary: map[string]string{"task_id": v.Task.ID, "answer_id": call.Identity.NativeID(), "interaction_id": v.Interaction}, Accepted: err == nil}, taskError(err)
}

func (a *App) automationTaskReconcile(ctx context.Context, call automation.Call, v automation.TaskRef) (automation.Outcome, error) {
	j, outbox, err := a.loadAutomationTask(call.Principal, v)
	if err != nil {
		return automation.Outcome{}, err
	}
	if outbox != nil {
		a.refreshOutbox(ctx, *outbox)
		var current model.Outbox
		err = a.Store.Get("outbox", outbox.ID, &current)
		return automation.Outcome{Data: outboxSummary(current)}, err
	}
	return automation.Outcome{Data: taskSummary(j)}, nil
}

func (a *App) automationTaskResolve(ctx context.Context, call automation.Call, v automation.TaskResolve) (automation.Outcome, error) {
	j, outbox, err := a.loadAutomationTask(call.Principal, v.Task)
	if err != nil {
		return automation.Outcome{}, err
	}
	if outbox != nil {
		return automation.Outcome{}, automation.Fail("unsupported", "Remote resolution is not an existing human capability on this node")
	}
	if err = call.Check(); err != nil {
		return automation.Outcome{}, err
	}
	j, err = a.Engine.ResolveAs(j.ID, v.Attempt, v.Reason, "automation:"+call.Principal.ID)
	return automation.Outcome{Data: taskSummary(j), Summary: map[string]string{"task_id": v.Task.ID, "state": j.State, "execution_outcome": j.Execution}, Accepted: err == nil}, taskError(err)
}
