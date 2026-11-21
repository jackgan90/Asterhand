package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jackgan90/Asterhand/internal/launch"
)

func main() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "asterhand-mcp:", err)
		os.Exit(1)
	}
	installPath := filepath.Join(filepath.Dir(exe), "installation.json")
	os.Exit(runForwarder(installPath, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func runForwarder(installPath string, userArgs []string, stdin io.Reader, stdout, stderr io.Writer) int {
	inst, err := launch.ReadInstallation(installPath)
	if err != nil {
		fmt.Fprintln(stderr, "asterhand-mcp:", err)
		return 1
	}
	cmd := exec.Command(inst.Executable, buildForwardArgs(inst, userArgs)...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintln(stderr, "asterhand-mcp:", err)
		return 1
	}
	return 0
}

func buildForwardArgs(inst launch.Installation, userArgs []string) []string {
	args := make([]string, 0, len(userArgs)+3)
	args = append(args, "mcp")
	if !hasHomeFlag(userArgs) && inst.Data != "" {
		args = append(args, "--home", inst.Data)
	}
	args = append(args, userArgs...)
	return args
}

func hasHomeFlag(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--home" || a == "-home" || strings.HasPrefix(a, "--home=") || strings.HasPrefix(a, "-home=") {
			return true
		}
	}
	return false
}
