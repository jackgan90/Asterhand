//go:build !windows

package workers

import (
	"errors"
	"io"
)

type managedProc struct {
	done chan struct{}
}

func spawnManagedProcess(executable string, args []string, workdir string, env []string) (*managedProc, error) {
	return nil, errors.New("managed coding harness processes are only supported on Windows in this milestone")
}

func (p *managedProc) Stdin() io.WriteCloser { return nil }
func (p *managedProc) Stdout() io.ReadCloser { return nil }
func (p *managedProc) Stderr() io.ReadCloser { return nil }
func (p *managedProc) Done() <-chan struct{} { return p.done }
func (p *managedProc) Kill() error           { return nil }

func sanitizedWorkerEnv() []string { return nil }
