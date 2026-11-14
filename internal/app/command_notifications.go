package app

import (
	"context"
	"net/url"

	"github.com/jackgan90/Asterhand/internal/desktop"
	"github.com/jackgan90/Asterhand/internal/localcommands"
	"github.com/jackgan90/Asterhand/internal/notifications"
	"github.com/jackgan90/Asterhand/internal/quickinput"
)

func (a *App) prepareCommandNotifications(runs *localcommands.RunManager) {
	a.Notifications = notifications.New(quickinput.NewNotificationPresenter(a.Config.Home), a.openNotification)
	runs.SetCompletionObserver(func(c localcommands.Completion) {
		a.Notifications.Enqueue(notifications.Completion{CommandID: c.CommandID, RunID: c.RunID, Name: c.Name,
			Result: c.Result, Reason: c.Reason, Started: c.Started, Ended: c.Ended, Elapsed: c.Elapsed})
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
