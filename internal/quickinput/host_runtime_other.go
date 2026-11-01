//go:build !windows

package quickinput

import "github.com/jackgan90/Asterhand/internal/i18n"

func RunInputHost([]string) error { return i18n.Err("err.quickinput.host_platform_unsupported") }
