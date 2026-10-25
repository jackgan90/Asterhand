//go:build !windows

package desktop

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

type Tray struct{}

func Start(Options) (*Tray, error) { return &Tray{}, nil }
func (*Tray) Close()               {}

func Open(target string) error {
	command := "xdg-open"
	if runtime.GOOS == "darwin" {
		command = "open"
	}
	return exec.Command(command, target).Start()
}

func Error(message, logDirectory string) {
	fmt.Fprintln(os.Stderr, message, logDirectory)
}
