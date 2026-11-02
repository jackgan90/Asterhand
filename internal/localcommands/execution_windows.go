//go:build windows

package localcommands

import (
	"context"
	"io"
	"os"
	"runtime"
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
	var info struct {
		User, Kernel, PeriodUser, PeriodKernel int64
		Faults, Total, Active, Terminated      uint32
	}
	err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
	return info.Active, err
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

func jobName(run string) *uint16 {
	name, _ := windows.UTF16PtrFromString("Local\\Asterhand.Command." + run)
	return name
}

func (windowsBackend) Reconcile(run string) (bool, error) {
	// The unpredictable name is durable before creation. Never act on a PID.
	raw, _, callErr := windows.NewLazySystemDLL("kernel32.dll").NewProc("OpenJobObjectW").Call(4, 0, uintptr(unsafe.Pointer(jobName(run))))
	job := windows.Handle(raw)
	var err error
	if job == 0 {
		err = callErr
	}
	if err == windows.ERROR_FILE_NOT_FOUND {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(job)
	count, err := jobProcessCount(job)
	return count == 0, err
}

func (windowsBackend) Start(c Command, run string) (Execution, error) {
	token, err := userprocess.OrdinaryToken()
	if err != nil {
		return nil, err
	}
	if token != 0 {
		defer token.Close()
	}
	job, err := windows.CreateJobObject(nil, jobName(run))
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
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return nil, err
	}
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

func (e *windowsExecution) create(c Command, token windows.Token, in, out, stderr *os.File) error {
	handles := []windows.Handle{windows.Handle(in.Fd()), windows.Handle(out.Fd()), windows.Handle(stderr.Fd())}
	for _, h := range handles {
		if err := windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			return err
		}
	}
	attributes, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return err
	}
	defer attributes.Delete()
	if err = attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil {
		return err
	}
	jobs := []windows.Handle{e.job}
	const jobList = 0x0002000d
	if err = attributes.Update(jobList, unsafe.Pointer(&jobs[0]), unsafe.Sizeof(jobs[0])); err != nil {
		return err
	}
	startup := windows.StartupInfoEx{ProcThreadAttributeList: attributes.List()}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.Flags = windows.STARTF_USESTDHANDLES
	startup.StdInput = handles[0]
	startup.StdOutput = handles[1]
	startup.StdErr = handles[2]
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
	c.Window = "background"
	path, line, env, err := windowsInvocation(system, c, env)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	args, err := windows.UTF16FromString(line)
	if err != nil || len(args) > 32767 {
		return problem("invalid", "err.localcmd.cmdline_too_long")
	}
	directory, err := windows.UTF16PtrFromString(c.WorkingDirectory())
	if err != nil {
		return err
	}
	block := environmentBlock(env)
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW)
	var info windows.ProcessInformation
	if token == 0 {
		err = windows.CreateProcess(name, &args[0], nil, nil, true, flags, &block[0], directory, &startup.StartupInfo, &info)
	} else {
		err = windows.CreateProcessAsUser(token, name, &args[0], nil, nil, true, flags, &block[0], directory, &startup.StartupInfo, &info)
	}
	runtime.KeepAlive(block)
	runtime.KeepAlive(jobs)
	runtime.KeepAlive(handles)
	if err != nil {
		return err
	}
	e.process = info.Process
	defer windows.CloseHandle(info.Thread)
	var actual windows.Token
	err = windows.OpenProcessToken(info.Process, windows.TOKEN_QUERY, &actual)
	if err == nil {
		err = userprocess.VerifyToken(actual)
		actual.Close()
	}
	if err == nil {
		_, err = windows.ResumeThread(info.Thread)
	}
	if err != nil {
		_ = windows.TerminateJobObject(e.job, 1)
		_, _ = windows.WaitForSingleObject(info.Process, 5000)
	}
	return err
}
