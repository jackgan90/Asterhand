//go:build !windows

package quickinput

import (
	"context"

	"github.com/jackgan90/Asterhand/internal/i18n"
)

func PickLocalCommandScript(context.Context) (string, error) {
	return "", i18n.Err("err.quickinput.picker_windows_only")
}

func RunScriptPicker(string) (string, error) {
	return "", i18n.Err("err.quickinput.picker_windows_only")
}
