package hostipc

import (
	"math"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/notifications"
)

const NotificationFrame Kind = "notification_frame"
const NotificationEvent Kind = "notification_event"

func validNotifications(s *notifications.Snapshot) bool {
	if s == nil || s.Revision == 0 || len(s.Cards) > notifications.MaxVisible || len(s.Interactions) > 32 || !validTheme(s.Theme) || !i18n.ValidLocale(s.Locale) {
		return false
	}
	ids := map[uint64]bool{}
	for _, c := range s.Cards {
		if c.ID == 0 || ids[c.ID] || (c.Phase != "entering" && c.Phase != "visible" && c.Phase != "exiting") || len(c.ErrorKey) > 128 {
			return false
		}
		ids[c.ID] = true
		if c.Count > 0 {
			if c.Count > (1<<53)-1 || c.Failed > c.Count || c.CommandID != "" || c.RunID != "" || c.Name != "" {
				return false
			}
		} else if !wire.ValidID(c.CommandID) || !wire.ValidID(c.RunID) || len(c.Name) > 64 || len(c.Reason) > 64 ||
			(c.Result != "succeeded" && c.Result != "failed") || c.Elapsed < 0 || math.IsNaN(c.Elapsed) || math.IsInf(c.Elapsed, 0) {
			return false
		}
	}
	for _, it := range s.Interactions {
		if it.RequestID == "" || len(it.RequestID) > 64 || (it.Kind != "approval" && it.Kind != "input") || len(it.Summary) > 1024 || len(it.Command) > 16384 {
			return false
		}
	}
	return true
}

func validNotificationEvent(e *notifications.Event) bool {
	if e == nil {
		return false
	}
	switch e.Kind {
	case "environment":
		return e.ID == 0 && e.Capacity >= 0 && e.Capacity <= notifications.MaxVisible && e.DurationMS >= 0 && e.DurationMS <= 86400000
	case "hover":
		return e.ID == 0 && e.Capacity == 0 && e.DurationMS == 0
	case "entered", "exited", "dismiss", "activate":
		return e.ID != 0 && !e.Paused && e.Capacity == 0 && e.DurationMS == 0
	case "worker_action":
		return e.WorkerAction != nil && e.WorkerAction.RequestID != "" && len(e.WorkerAction.RequestID) <= 64 && len(e.WorkerAction.Decision) <= 32 && len(e.WorkerAction.Text) <= MaxTextBytes
	}
	return false
}
