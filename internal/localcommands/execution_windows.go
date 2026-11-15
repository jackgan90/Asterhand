//go:build windows

package localcommands

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"
	"unsafe"

	"github.com/jackgan90/Asterhand/internal/platform/userprocess"
	"golang.org/x/sys/windows"
)

type windowsBackend struct{}

func NewPlatformBackend() ExecutionBackend { return windowsBackend{} }
func (windowsBackend) Available() bool     { return interruptTime.Find() == nil }

var interruptTime = windows.NewLazySystemDLL("api-ms-win-core-realtime-l1-1-1.dll").NewProc("QueryInterruptTime")

func (platformClock) Now() time.Duration {
	var ticks uint64
	interruptTime.Call(uintptr(unsafe.Pointer(&ticks)))
	return time.Duration(ticks) * 100
}

type windowsExecution struct {
	process, job   windows.Handle
	stdout, stderr *os.File
	mu             sync.Mutex
	closed         bool
}

func (e *windowsExecution) Interaction() InteractionCapabilities {
	return InteractionCapabilities{Input: InputNone, Output: OutputStreams}
}

func (e *windowsExecution) Stdout() io.ReadCloser { return e.stdout }
func (e *windowsExecution) Stderr() io.ReadCloser { return e.stderr }
func (e *windowsExecution) ForceStop() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	return windows.TerminateJobObject(e.job, 1)
}
func (e *windowsExecution) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	e.stdout.Close()
	e.stderr.Close()
	windows.CloseHandle(e.process)
	return windows.CloseHandle(e.job)
}

func jobProcessCount(job windows.Handle) (uint32, error) {
	return userprocess.JobProcessCount(job)
}

func (e *windowsExecution) Wait(ctx context.Context) (ExitEvidence, error) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		count, err := jobProcessCount(e.job)
		if err != nil {
			return ExitEvidence{}, err
		}
		if count == 0 {
			var code uint32
			if err = windows.GetExitCodeProcess(e.process, &code); err != nil {
				return ExitEvidence{}, err
			}
			return ExitEvidence{Kind: "exited", Code: &code}, nil
		}
		select {
		case <-ctx.Done():
			return ExitEvidence{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (windowsBackend) Reconcile(run string) (bool, error) {
	return userprocess.JobStopped("Local\\Asterhand.Command." + run)
}

func (windowsBackend) Start(c ScriptSpec, run string) (Execution, error) {
	token, err := userprocess.OrdinaryToken()
	if err != nil {
		return nil, err
	}
	if token != 0 {
		defer token.Close()
	}
	job, err := userprocess.NewKillOnCloseJob("Local\\Asterhand.Command."+run, 0)
	if err != nil {
		return nil, err
	}
	e := &windowsExecution{job: job}
	success := false
	defer func() {
		if !success {
			windows.CloseHandle(job)
			if e.process != 0 {
				windows.CloseHandle(e.process)
			}
			if e.stdout != nil {
				e.stdout.Close()
			}
			if e.stderr != nil {
				e.stderr.Close()
			}
		}
	}()
	var outWrite, errWrite *os.File
	if e.stdout, outWrite, err = os.Pipe(); err != nil {
		return nil, err
	}
	defer outWrite.Close()
	if e.stderr, errWrite, err = os.Pipe(); err != nil {
		return nil, err
	}
	defer errWrite.Close()
	inRead, inWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer inRead.Close()
	inWrite.Close()
	if err = e.create(c, token, inRead, outWrite, errWrite); err != nil {
		if e.process != 0 {
			success = true
			return e, err
		}
		return nil, err
	}
	success = true
	return e, nil
}

func (e *windowsExecution) create(c ScriptSpec, token windows.Token, in, out, stderr *os.File) error {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	envToken := token
	if envToken == 0 {
		envToken = windows.GetCurrentProcessToken()
	}
	env, err := envToken.Environ(false)
	if err != nil {
		return err
	}
	path, line, env, err := windowsInvocation(system, c, env)
	if err != nil {
		return err
	}
	res, err := userprocess.SpawnAttachedJob(userprocess.SpawnConfig{
		Job:         e.job,
		Token:       token,
		Executable:  path,
		CommandLine: line,
		WorkingDir:  c.WorkingDirectory(),
		Environment: env,
		Stdin:       windows.Handle(in.Fd()),
		Stdout:      windows.Handle(out.Fd()),
		Stderr:      windows.Handle(stderr.Fd()),
		AfterSuspended: func(process windows.Handle) error {
			var actual windows.Token
			if tokErr := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &actual); tokErr != nil {
				return tokErr
			}
			defer actual.Close()
			return userprocess.VerifyToken(actual)
		},
	})
	e.process = res.Process
	if errors.Is(err, userprocess.ErrCmdLineTooLong) {
		return problem("invalid", "err.localcmd.cmdline_too_long")
	}
	return err
}
