//go:build windows

package quickinput

import "github.com/jackgan90/Asterhand/internal/localcommands"

// StartLocalCommand reuses the audited ordinary-user token policy, but neither
// the input host's restricted environment nor its kill-on-close process Job.
func StartLocalCommand(command localcommands.Command) error {
	token, err := restrictedHostToken()
	if err != nil {
		return err
	}
	if token != 0 {
		defer token.Close()
	}
	return localcommands.StartWindows(command, token)
}
