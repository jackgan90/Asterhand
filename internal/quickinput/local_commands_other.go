//go:build !windows

package quickinput

import (
	"fmt"
	"github.com/jackgan90/Asterhand/internal/localcommands"
)

func StartLocalCommand(localcommands.Command) error {
	return fmt.Errorf("本地快捷命令仅支持 Windows")
}
