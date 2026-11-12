package app

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackgan90/Asterhand/internal/operations"
	"github.com/jackgan90/Asterhand/internal/store"
)

// Persist only the identity of the domain operation, before dispatch to the
// window. The domain owns the text and its receipt even if the window closes.
type editorCommitRef struct {
	Draft    string `json:"draft_id"`
	Revision string `json:"revision"`
	Domain   string `json:"domain_operation"`
	ID       string `json:"domain_id"`
}

func (a *App) prepareEditorCommit(identity operations.Identity, s liveSnapshot) (editorCommitRef, error) {
	domain := ""
	switch s.Kind {
	case "settings":
		domain = "settings.patch"
	case "command":
		domain = "commands.create"
		if s.Target.Command != "" {
			domain = "commands.patch"
		}
	case "chat":
		domain = "conversations.send"
	case "task_answer":
		domain = "tasks.answer"
	case "task_goal":
		domain = "tasks.submit"
	}
	stamp, _, _ := strings.Cut(identity.ID, "_")
	ref := editorCommitRef{Draft: s.ID, Revision: s.Revision, Domain: domain, ID: stamp + "_" + identity.NativeID()}
	receipt, err := a.Store.Lookup(identity.Actor, identity.Node, identity.ID)
	if err != nil {
		return ref, err
	}
	receipt.State, receipt.Result = "unknown", compactResult(ref)
	return ref, a.Store.Finish(receipt, time.Now())
}

func (a *App) reconcileEditorCommit(receipt operations.Receipt) (operations.Receipt, error) {
	var ref editorCommitRef
	if json.Unmarshal(receipt.Result, &ref) != nil || ref.Domain == "" || ref.ID == "" {
		return receipt, nil
	}
	state := "saved"
	switch ref.Domain {
	case "tasks.submit":
		job, err := a.Store.RequestJob(receipt.Node, ref.ID)
		if errors.Is(err, store.ErrNotFound) {
			return receipt, nil
		}
		if err != nil {
			return receipt, err
		}
		if job.ID == "" {
			return receipt, nil
		}
		state = "accepted"
	case "conversations.send":
		_, turn, err := a.Chat.OperationStatus(ref.ID)
		if errors.Is(err, store.ErrNotFound) {
			return receipt, nil
		}
		if err != nil {
			return receipt, err
		}
		if turn == nil {
			return receipt, nil
		}
		switch turn.Delivery {
		case "accepted":
			state = "accepted"
		case "queued":
			state = "queued"
		case "rejected":
			receipt.State, receipt.Code, receipt.Accepted = "rejected", "rejected", false
			return a.finishEditorReceipt(receipt)
		default:
			return receipt, nil
		}
	case "settings.patch", "commands.create", "commands.patch", "tasks.answer":
		inner, err := a.Store.Lookup("human:web", receipt.Node, ref.ID)
		if errors.Is(err, operations.ErrMissing) {
			return receipt, nil
		}
		if err != nil {
			return receipt, err
		}
		if inner.Name != ref.Domain || !inner.Terminal() {
			return receipt, nil
		}
		if inner.State == "rejected" {
			receipt.State, receipt.Code, receipt.Accepted = "rejected", inner.Code, false
			return a.finishEditorReceipt(receipt)
		}
		if ref.Domain == "tasks.answer" {
			state = "accepted"
		}
	default:
		return receipt, nil
	}
	receipt.State, receipt.Accepted, receipt.Code = "completed", true, ""
	receipt.Result = compactResult(map[string]string{"draft_id": ref.Draft, "revision": ref.Revision, "state": state})
	return a.finishEditorReceipt(receipt)
}

func (a *App) finishEditorReceipt(receipt operations.Receipt) (operations.Receipt, error) {
	err := a.Store.Finish(receipt, time.Now())
	if errors.Is(err, operations.ErrConflict) {
		return a.Store.Lookup(receipt.Actor, receipt.Node, receipt.ID)
	}
	return receipt, err
}
