package app

import (
	"context"
	"net/url"

	"github.com/jackgan90/Asterhand/internal/desktop"
	"github.com/jackgan90/Asterhand/internal/localcommands"
	"github.com/jackgan90/Asterhand/internal/notifications"
	"github.com/jackgan90/Asterhand/internal/quickinput"
	"github.com/jackgan90/Asterhand/internal/workers"
)

func (a *App) prepareCommandNotifications(runs *localcommands.RunManager) {
	a.Notifications = notifications.New(quickinput.NewNotificationPresenter(a.Config.Home), a.openNotification)
	runs.SetCompletionObserver(func(c localcommands.Completion) {
		a.Notifications.Enqueue(notifications.Completion{CommandID: c.CommandID, RunID: c.RunID, Name: c.Name,
			Result: c.Result, Reason: c.Reason, Started: c.Started, Ended: c.Ended, Elapsed: c.Elapsed})
	})
}

func (a *App) wireWorkerNotifications() {
	if a.Workers == nil || a.Notifications == nil {
		return
	}
	a.Workers.SetInteractionsObserver(func(views []workers.InteractionView) {
		items := make([]notifications.WorkerInteraction, 0, len(views))
		for _, v := range views {
			qs := make([]notifications.WorkerQuestion, 0, len(v.Questions))
			for _, q := range v.Questions {
				opts := make([]notifications.WorkerOption, 0, len(q.Options))
				for _, o := range q.Options {
					opts = append(opts, notifications.WorkerOption{Label: o.Label, Description: o.Description})
				}
				qs = append(qs, notifications.WorkerQuestion{
					ID: q.ID, Header: q.Header, Question: q.Question, Multi: q.Multi, Options: opts,
				})
			}
			cmd := v.Command
			if len(cmd) > 12000 {
				cmd = cmd[:12000] + "..."
			}
			items = append(items, notifications.WorkerInteraction{
				RequestID:        v.RequestID,
				Kind:             v.Kind,
				WorkerID:         v.WorkerID,
				WorkerAlias:      v.WorkerAlias,
				WorkspaceRoot:    v.WorkspaceRoot,
				HarnessID:        v.HarnessID,
				TurnID:           v.TurnID,
				ToolName:         v.ToolName,
				Summary:          v.Summary,
				Command:          cmd,
				Paths:            append([]string(nil), v.Paths...),
				Reason:           v.Reason,
				RiskLevel:        v.RiskLevel,
				HardRedline:      v.HardRedline,
				OutsideWorkspace: v.OutsideWorkspace,
				AllowSessionOK:   v.AllowSessionOK,
				EscalationNote:   v.EscalationNote,
				Questions:        qs,
			})
		}
		a.Notifications.SetWorkerInteractions(items)
		a.notifyWorkers()
	})
	a.Notifications.SetWorkerActionHandler(func(act notifications.WorkerActionEvent) {
		_, _ = a.Workers.DecideInteractionHuman(act.RequestID, workers.InteractionDecision{
			Decision: act.Decision,
			Text:     act.Text,
			Answers:  act.Answers,
		})
	})
}

func (a *App) openNotification(ctx context.Context, card notifications.Card) string {
	if a.Stopping() {
		return "err.app.node_stopping"
	}
	var err error
	if card.Count > 0 {
		a.mu.Lock()
		address := a.webAddress
		a.mu.Unlock()
		if address == "" {
			return "err.app.web_not_ready"
		}
		fragment := url.Values{"login": {a.loginToken}, "commands": {""}}
		err = desktop.Open("http://" + address + "/#" + fragment.Encode())
	} else {
		if _, e := a.Commands.Get(card.CommandID); e != nil {
			return "err.localcmd.deleted_refresh"
		}
		if a.Commands.Run(card.CommandID, card.RunID) == nil {
			return "err.localcmd.run_changed"
		}
		if a.CommandLogs == nil {
			return "err.localcmd.log_window_failed"
		}
		err = a.CommandLogs.Open(quickinput.WithLogRun(ctx, card.RunID), card.CommandID)
	}
	if err == nil {
		return ""
	}
	// Notifications never display raw native errors, paths or credential URLs.
	return "err.localcmd.log_window_failed"
}

func (a *App) updateDesktopAppearance(st quickinput.Status) {
	if a.CommandLogs != nil {
		a.CommandLogs.Update(st.Desired.Theme, st.EffectiveLocale)
	}
	if a.Notifications != nil {
		a.Notifications.Update(st.Desired.Theme, st.EffectiveLocale)
	}
}

func (a *App) notificationStatus() notifications.Status {
	if a.Notifications == nil {
		return notifications.Status{State: "unsupported"}
	}
	return a.Notifications.Status()
}
