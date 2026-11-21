package app

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/operations"
)

type batchItemState struct {
	Operation string `json:"operation"`
	ID        string `json:"operation_id"`
	Digest    string `json:"digest"`
	Target    string `json:"target"`
	State     string `json:"state"`
}
type batchState struct {
	OnError    string           `json:"on_error"`
	Items      []batchItemState `json:"items"`
	Dispatched bool             `json:"dispatched"`
}

func (a *App) registerAutomationBatches(r *automation.Registry) {
	automation.Register(r, automation.Definition{Name: "batches.submit", Description: "Admit up to 32 fixed write items; up to four dispatch concurrently, with per-item receipts and no restart replay", Write: true}, nil, nil, a.automationBatchSubmit)
	automation.Register(r, automation.Definition{Name: "batches.get", Description: "Read original per-item outcomes; undispatched work is never resumed after a restart", Role: "read", Toolset: automation.ToolsetCore, Control: true}, nil, nil, a.automationBatchGet)
	automation.Register(r, automation.Definition{Name: "read.many", Description: "Read up to 100 explicitly identified objects with bounded response pages and per-item errors", Role: "read"}, nil, nil, a.automationMultiRead)
}

func (a *App) automationBatchSubmit(ctx context.Context, call automation.Call, v automation.BatchSubmit) (automation.Outcome, error) {
	if len(v.Items) < 1 || len(v.Items) > 32 || v.OnError != "" && v.OnError != "stop" && v.OnError != "continue" {
		return automation.Outcome{}, automation.Fail("invalid_parameters", "A batch needs 1-32 items and on_error stop or continue")
	}
	if v.OnError == "" {
		v.OnError = "stop"
	}
	r := a.Automation.Registry
	batch := batchState{OnError: v.OnError, Items: make([]batchItemState, len(v.Items))}
	for i, item := range v.Items {
		identity, err := r.PrepareItem(call.Principal, item, true)
		if err != nil {
			return automation.Outcome{}, err
		}
		id := strings.Split(call.Identity.ID, "_")[0] + "_" + operations.Hash([]any{call.Identity.ID, "batch-item", i})[:32]
		batch.Items[i] = batchItemState{Operation: identity.Name, ID: id, Digest: identity.Digest, Target: identity.Target, State: "pending"}
	}
	release, err := r.ReserveBatch(call.Principal.ID)
	if err != nil {
		return automation.Outcome{}, err
	}
	a.mu.Lock()
	if a.stopping {
		a.mu.Unlock()
		release()
		return automation.Outcome{}, automation.Fail("node_unavailable", "Node is stopping")
	}
	if err = call.Check(); err != nil {
		a.mu.Unlock()
		release()
		return automation.Outcome{}, err
	}
	state := operations.State{Identity: call.Identity, Instance: r.Instance, Updated: time.Now().UnixMilli(), Data: compactResult(batch)}
	if err = a.Store.SaveAutomationState(state); err != nil {
		a.mu.Unlock()
		release()
		return automation.Outcome{}, err
	}
	a.wg.Add(1)
	a.runningBatches++
	a.mu.Unlock()
	go func() {
		defer a.wg.Done()
		defer release()
		defer func() { a.mu.Lock(); a.runningBatches--; a.mu.Unlock() }()
		a.dispatchBatch(ctx, call, v, &state, &batch)
	}()
	ref := map[string]any{"batch_id": call.Identity.ID, "items": len(batch.Items)}
	return automation.Outcome{Data: ref, Summary: ref, Accepted: true}, nil
}

func (a *App) dispatchBatch(ctx context.Context, call automation.Call, v automation.BatchSubmit, state *operations.State, batch *batchState) {
	var mu sync.Mutex
	next, failed := 0, false
	persist := func() error {
		state.Data = compactResult(batch)
		state.Updated = time.Now().UnixMilli()
		return a.Store.SaveAutomationState(*state)
	}
	var workers sync.WaitGroup
	for range min(4, len(v.Items)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				mu.Lock()
				if next == len(v.Items) || failed && batch.OnError == "stop" || ctx.Err() != nil {
					mu.Unlock()
					return
				}
				i := next
				next++
				batch.Items[i].State = "dispatching"
				if err := persist(); err != nil {
					failed = true
					batch.Items[i].State = "not_dispatched"
					next = len(v.Items)
					mu.Unlock()
					return
				}
				mu.Unlock()
				response := call.Run(ctx, automation.Request{Version: automation.APIVersion, Node: call.Identity.Node, Operation: v.Items[i].Operation, ID: batch.Items[i].ID, Arguments: v.Items[i].Arguments}, nil)
				mu.Lock()
				batch.Items[i].State = "done"
				if response.Error != nil {
					failed = true
					if response.Receipt == nil {
						batch.Items[i].State = "not_dispatched"
					}
				}
				if err := persist(); err != nil {
					failed = true
					next = len(v.Items)
				}
				mu.Unlock()
			}
		}()
	}
	workers.Wait()
	for i := range batch.Items {
		if batch.Items[i].State == "pending" {
			batch.Items[i].State = "not_dispatched"
		}
	}
	batch.Dispatched = true
	state.Complete = true
	for _, item := range batch.Items {
		receipt, err := a.Store.Lookup(call.Principal.ID, call.Identity.Node, item.ID)
		if err == nil && !receipt.Terminal() || err != nil && !errors.Is(err, operations.ErrMissing) {
			state.Complete = false
			break
		}
	}
	_ = persist()
}

func (a *App) automationBatchGet(ctx context.Context, call automation.Call, v automation.BatchGet) (automation.Outcome, error) {
	r := a.Automation.Registry
	receipt, err := a.Store.Lookup(call.Principal.ID, r.Node, v.ID)
	if err != nil {
		return automation.Outcome{}, err
	}
	if receipt.Name != "batches.submit" {
		return automation.Outcome{}, automation.Fail("invalid_parameters", "Identity is not a batch")
	}
	state, err := a.Store.AutomationState(receipt.Identity)
	if err != nil {
		return automation.Outcome{}, err
	}
	var batch batchState
	if err = json.Unmarshal(state.Data, &batch); err != nil {
		return automation.Outcome{}, err
	}
	binding := []any{"batch", v.ID, call.Principal.ID}
	cursor, err := automation.ParseCursor(v.Cursor, binding)
	if err != nil {
		return automation.Outcome{}, err
	}
	start, err := pageOffset(cursor.After, len(batch.Items))
	if err != nil {
		return automation.Outcome{}, err
	}
	items := []any{}
	end := min(start+4, len(batch.Items))
	for _, item := range batch.Items[start:end] {
		current, e := a.Store.Lookup(call.Principal.ID, r.Node, item.ID)
		if e == nil {
			latest := call.Principal
			if call.Authorize != nil {
				if fresh, authErr := call.Authorize(); authErr != nil {
					return automation.Outcome{}, authErr
				} else {
					latest = fresh
				}
			}
			if err = authorizeReceipt(r, latest, current); err != nil {
				items = append(items, map[string]string{"operation_id": item.ID, "state": "forbidden"})
				continue
			}
			current, e = a.reconcileAutomationOperation(current)
			if e == nil && call.Authorize != nil {
				latest, authErr := call.Authorize()
				if authErr != nil {
					return automation.Outcome{}, authErr
				}
				if err = authorizeReceipt(r, latest, current); err != nil {
					items = append(items, map[string]string{"operation_id": item.ID, "state": "forbidden"})
					continue
				}
			}
		}
		if e == nil {
			items = append(items, map[string]any{"operation_id": item.ID, "state": current.State, "receipt": current})
			continue
		}
		if !errors.Is(e, operations.ErrMissing) {
			return automation.Outcome{}, e
		}
		status := item.State
		if state.Instance != r.Instance {
			status = "not_dispatched"
		} else if status == "done" {
			status = "unknown"
		}
		items = append(items, map[string]string{"operation_id": item.ID, "state": status})
	}
	nextCursor := ""
	if end < len(batch.Items) {
		nextCursor = automation.EncodeCursor(binding, "", strconv.Itoa(end))
	}
	return automation.Outcome{Data: map[string]any{"batch_id": v.ID, "items": items, "complete": batch.Dispatched || state.Instance != r.Instance, "next": nextCursor}}, nil
}

func pageOffset(after string, total int) (int, error) {
	if after == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(after)
	if err != nil || value < 0 || value > total {
		return 0, automation.Fail("invalid_cursor", "Invalid page offset")
	}
	return value, nil
}

func (a *App) automationMultiRead(ctx context.Context, call automation.Call, v automation.MultiRead) (automation.Outcome, error) {
	if len(v.Items) < 1 || len(v.Items) > 100 {
		return automation.Outcome{}, automation.Fail("invalid_parameters", "read.many accepts 1-100 items")
	}
	binding := []any{"read-many", call.Principal.ID, v.Items}
	cursor, err := automation.ParseCursor(v.Cursor, binding)
	if err != nil {
		return automation.Outcome{}, err
	}
	start, err := pageOffset(cursor.After, len(v.Items))
	if err != nil {
		return automation.Outcome{}, err
	}
	items := []any{}
	used, end := 2048, start
	for end < len(v.Items) {
		item := v.Items[end]
		_, e := a.Automation.Registry.PrepareItem(call.Principal, item, false)
		response := automation.Response{Version: automation.APIVersion, Node: call.Identity.Node, Instance: a.Automation.Registry.Instance}
		if e != nil {
			response.Error = automation.PublicError(e)
		} else {
			response = call.Run(ctx, automation.Request{Version: automation.APIVersion, Node: call.Identity.Node, Operation: item.Operation, Arguments: item.Arguments}, nil)
		}
		b, _ := json.Marshal(response)
		if len(b) > automation.MaxResponse-4096 {
			response.Data = nil
			response.Error = automation.Fail("response_too_large", "Read this item individually with its bounded content interface")
			b, _ = json.Marshal(response)
		}
		if used+len(b) > automation.MaxResponse-2048 {
			break
		}
		items = append(items, map[string]any{"index": end, "response": response})
		used += len(b) + 64
		end++
	}
	next := ""
	if end < len(v.Items) {
		next = automation.EncodeCursor(binding, "", strconv.Itoa(end))
	}
	return automation.Outcome{Data: map[string]any{"items": items, "next": next}}, nil
}
