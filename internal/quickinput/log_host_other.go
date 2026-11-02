//go:build !windows

package quickinput

import "github.com/jackgan90/Asterhand/internal/i18n"

func NewLogWindowHost(home string, call CommandHandler) LogWindowHost {
	return newLogWindowHost(home, call, func() (hostProcess, error) { return nil, i18n.Err("err.localcmd.log_window_failed") })
}
