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
		fmt.Fprintln(os.Stderr, "asterhand-cli:", err)
		os.Exit(1)
	}
	installPath := filepath.Join(filepath.Dir(exe), "installation.json")
	os.Exit(runCLIForwarder(installPath, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func runCLIForwarder(installPath string, userArgs []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(userArgs) == 0 {
		fmt.Fprintln(stderr, "asterhand-cli: usage: asterhand-cli <wait|automation|status|version> [args...]")
		return 2
	}
	// Pre-validate allowed command shape before reading installation.json so invalid
	// commands always report exit 2 cleanly without touching stdout.
	probeArgs, ok := buildCLIForwardArgs(launch.Installation{}, userArgs)
	if !ok {
		fmt.Fprintf(stderr, "asterhand-cli: unsupported subcommand %q (allowed: wait, automation, status, version)\n", userArgs[0])
		return 2
	}
	_ = probeArgs

	inst, err := launch.ReadInstallation(installPath)
	if err != nil {
		fmt.Fprintln(stderr, "asterhand-cli:", err)
		return 1
	}
	forwardArgs, ok := buildCLIForwardArgs(inst, userArgs)
	if !ok {
		fmt.Fprintf(stderr, "asterhand-cli: unsupported subcommand %q (allowed: wait, automation, status, version)\n", userArgs[0])
		return 2
	}
	cmd := exec.Command(inst.Executable, forwardArgs...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintln(stderr, "asterhand-cli:", err)
		return 1
	}
	return 0
}

func buildCLIForwardArgs(inst launch.Installation, userArgs []string) ([]string, bool) {
	if len(userArgs) == 0 {
		return nil, false
	}
	cmd := userArgs[0]
	rest := userArgs[1:]
	switch cmd {
	case "version":
		args := append([]string{"version"}, rest...)
		return args, true
	case "wait", "status":
		args := make([]string, 0, len(rest)+3)
		args = append(args, cmd)
		if !hasHomeFlag(rest) && inst.Data != "" {
			args = append(args, "--home", inst.Data)
		}
		args = append(args, rest...)
		return args, true
	case "automation":
		if len(rest) == 0 {
			return []string{"automation"}, true
		}
		op := rest[0]
		opRest := rest[1:]
		if op == "help" || op == "schema" || op == "new-id" || strings.HasPrefix(op, "-") {
			args := make([]string, 0, len(rest)+1)
			args = append(args, "automation")
			args = append(args, rest...)
			return args, true
		}
		args := make([]string, 0, len(rest)+3)
		args = append(args, "automation", op)
		if !hasHomeFlag(opRest) && inst.Data != "" {
			args = append(args, "--home", inst.Data)
		}
		args = append(args, opRest...)
		return args, true
	default:
		return nil, false
	}
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
