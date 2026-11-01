//go:build !windows

package quickinput

import (
	"context"
	"fmt"
)

func PickLocalCommandScript(context.Context) (string, error) {
	return "", fmt.Errorf("脚本浏览仅支持 Windows")
}

func RunScriptPicker() (string, error) { return "", fmt.Errorf("脚本浏览仅支持 Windows") }
