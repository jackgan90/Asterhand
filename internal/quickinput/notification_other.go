//go:build !windows

package quickinput

import "github.com/jackgan90/Asterhand/internal/notifications"

func NewNotificationPresenter(string) notifications.Presenter { return nil }
