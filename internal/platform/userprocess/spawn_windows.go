//go:build windows

package userprocess

import (
	"runtime"
	"slices"
	"strings"
	"unicode/utf16"
	"unsafe"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"golang.org/x/sys/windows"
)

const procThreadAttributeJobList = 0x0002000d

// ErrCmdLineTooLong indicates the UTF-16 Windows command line exceeds CreateProcess limits.
var ErrCmdLineTooLong = i18n.Err("err.localcmd.cmdline_too_long")

// SpawnConfig configures a suspended Windows process bound atomically to a Job Object
// and an explicit inherited handle list.
type SpawnConfig struct {
	Job            windows.Handle
	Token          windows.Token // 0 inherits the current process token
	Executable     string
	CommandLine    string
	WorkingDir     string
	Environment    []string
	Stdin          windows.Handle
	Stdout         windows.Handle
	Stderr         windows.Handle
	AfterSuspended func(process windows.Handle) error
}

// SpawnResult carries the started process handle and OS PID. The thread handle is closed before return.
type SpawnResult struct {
	Process windows.Handle
	PID     uint32
}

// NewKillOnCloseJob creates a Job Object configured with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
// plus optional extra extended limit flags.
func NewKillOnCloseJob(name string, extraFlags uint32) (windows.Handle, error) {
	var ptr *uint16
	if name != "" {
		var err error
		if ptr, err = windows.UTF16PtrFromString(name); err != nil {
			return 0, err
		}
	}
	job, err := windows.CreateJobObject(nil, ptr)
	if err != nil {
		return 0, err
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | extraFlags
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

// EnvironmentBlock builds a sorted UTF-16 NUL-delimited Windows environment block.
func EnvironmentBlock(environment []string) []uint16 {
	sorted := append([]string(nil), environment...)
	slices.SortFunc(sorted, func(a, b string) int {
		return strings.Compare(strings.ToUpper(a), strings.ToUpper(b))
	})
	var block []uint16
	for _, entry := range sorted {
		block = append(block, utf16.Encode([]rune(entry))...)
		block = append(block, 0)
	}
	return append(block, 0)
}

// SpawnAttachedJob creates a child process suspended with explicit handle inheritance
// and job attachment, runs any post-create check, and resumes execution.
func SpawnAttachedJob(cfg SpawnConfig) (SpawnResult, error) {
	handles := []windows.Handle{cfg.Stdin, cfg.Stdout, cfg.Stderr}
	for _, h := range handles {
		if err := windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			return SpawnResult{}, err
		}
	}
	attributes, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return SpawnResult{}, err
	}
	defer attributes.Delete()
	if err = attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil {
		return SpawnResult{}, err
	}
	jobs := []windows.Handle{cfg.Job}
	if err = attributes.Update(procThreadAttributeJobList, unsafe.Pointer(&jobs[0]), unsafe.Sizeof(jobs[0])); err != nil {
		return SpawnResult{}, err
	}
	startup := windows.StartupInfoEx{ProcThreadAttributeList: attributes.List()}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.Flags = windows.STARTF_USESTDHANDLES
	startup.StdInput, startup.StdOutput, startup.StdErr = cfg.Stdin, cfg.Stdout, cfg.Stderr

	var appPtr *uint16
	if cfg.Executable != "" {
		if appPtr, err = windows.UTF16PtrFromString(cfg.Executable); err != nil {
			return SpawnResult{}, err
		}
	}
	args, err := windows.UTF16FromString(cfg.CommandLine)
	if err != nil || len(args) > 32767 {
		return SpawnResult{}, ErrCmdLineTooLong
	}
	var dirPtr *uint16
	if cfg.WorkingDir != "" {
		if dirPtr, err = windows.UTF16PtrFromString(cfg.WorkingDir); err != nil {
			return SpawnResult{}, err
		}
	}
	block := EnvironmentBlock(cfg.Environment)
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW)
	var info windows.ProcessInformation
	if cfg.Token == 0 {
		err = windows.CreateProcess(appPtr, &args[0], nil, nil, true, flags, &block[0], dirPtr, &startup.StartupInfo, &info)
	} else {
		err = windows.CreateProcessAsUser(cfg.Token, appPtr, &args[0], nil, nil, true, flags, &block[0], dirPtr, &startup.StartupInfo, &info)
	}
	runtime.KeepAlive(block)
	runtime.KeepAlive(jobs)
	runtime.KeepAlive(handles)
	if err != nil {
		return SpawnResult{}, err
	}
	defer windows.CloseHandle(info.Thread)
	if cfg.AfterSuspended != nil {
		err = cfg.AfterSuspended(info.Process)
	}
	if err == nil {
		_, err = windows.ResumeThread(info.Thread)
	}
	if err != nil {
		_ = windows.TerminateJobObject(cfg.Job, 1)
		_, _ = windows.WaitForSingleObject(info.Process, 5000)
	}
	return SpawnResult{Process: info.Process, PID: info.ProcessId}, err
}
