//go:build !windows

package quickinput

import "fmt"

func RunInputHost([]string) error { return fmt.Errorf("当前平台不支持原生快捷输入宿主") }
