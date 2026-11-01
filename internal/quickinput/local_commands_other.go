//go:build !windows

package quickinput

import (
	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/localcommands"
)

func StartLocalCommand(localcommands.Command) error {
	return i18n.Err("err.quickinput.localcmd_windows_only")
}
