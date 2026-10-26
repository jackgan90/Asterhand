//go:build windows

package quickinput

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostbundle"
	"golang.org/x/sys/windows"
)

type windowsHostProcess struct {
	input, output *os.File
	process, job  windows.Handle
	pid           uint32
	exited        chan struct{}
	stop          sync.Once
	stopErr       error
}

func (p *windowsHostProcess) Input() io.WriteCloser   { return p.input }
func (p *windowsHostProcess) Output() io.ReadCloser   { return p.output }
func (p *windowsHostProcess) Exited() <-chan struct{} { return p.exited }

func launchWindowsHost() (hostProcess, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return launchWindowsHostFrom(filepath.Dir(executable))
}

func launchWindowsHostFrom(directory string) (hostProcess, error) {
	if err := checkInteractiveSession(); err != nil {
		return nil, err
	}
	guard, err := hostbundle.Verify(directory, config.Version, config.SourceDigest)
	if err != nil {
		return nil, &RegistrationError{"host_bundle_invalid", err.Error()}
	}
	defer guard.Close()
	token, err := restrictedHostToken()
	if err != nil {
		return nil, &RegistrationError{"host_token_failed", fmt.Sprintf("无法创建非提权输入宿主: %v", err)}
	}
	if token != 0 {
		defer token.Close()
	}
	commandRead, commandWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer commandRead.Close()
	statusRead, statusWrite, err := os.Pipe()
	if err != nil {
		commandWrite.Close()
		return nil, err
	}
	defer statusWrite.Close()
	p := &windowsHostProcess{input: commandWrite, output: statusRead, exited: make(chan struct{})}
	p.job, err = newHostJob()
	if err == nil {
		err = p.create(guard.Name(), token, commandRead, statusWrite)
	}
	if err != nil {
		commandWrite.Close()
		statusRead.Close()
		if p.job != 0 {
			windows.CloseHandle(p.job)
		}
		return nil, fmt.Errorf("启动非提权输入宿主失败: %w", err)
	}
	go func() { _, _ = windows.WaitForSingleObject(p.process, windows.INFINITE); close(p.exited) }()
	return p, nil
}

func newHostJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func (p *windowsHostProcess) create(path string, token windows.Token, commands, status *os.File) error {
	handles := []windows.Handle{windows.Handle(commands.Fd()), windows.Handle(status.Fd())}
	for _, handle := range handles {
		if err := windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
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
	// PROC_THREAD_ATTRIBUTE_JOB_LIST binds ownership during process creation;
	// assigning after CreateProcess would leave a suspended orphan if we died.
	const procThreadAttributeJobList = 0x0002000d
	jobs := []windows.Handle{p.job}
	if err = attributes.Update(procThreadAttributeJobList, unsafe.Pointer(&jobs[0]), unsafe.Sizeof(jobs[0])); err != nil {
		return err
	}
	startup := windows.StartupInfoEx{ProcThreadAttributeList: attributes.List()}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	args := windows.ComposeCommandLine([]string{path, "--command-handle", strconv.FormatUint(uint64(handles[0]), 10), "--status-handle", strconv.FormatUint(uint64(handles[1]), 10)})
	command, _ := windows.UTF16PtrFromString(args)
	directory, _ := windows.UTF16PtrFromString(filepath.Dir(path))
	environment := hostEnvironment()
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW)
	var info windows.ProcessInformation
	if token == 0 {
		err = windows.CreateProcess(name, command, nil, nil, true, flags, &environment[0], directory, &startup.StartupInfo, &info)
	} else {
		err = windows.CreateProcessAsUser(token, name, command, nil, nil, true, flags, &environment[0], directory, &startup.StartupInfo, &info)
	}
	runtime.KeepAlive(environment)
	runtime.KeepAlive(jobs)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(info.Thread)
	var actual windows.Token
	err = windows.OpenProcessToken(info.Process, windows.TOKEN_QUERY, &actual)
	if err == nil {
		err = verifyHostToken(actual)
		actual.Close()
	}
	if err == nil {
		_, err = windows.ResumeThread(info.Thread)
	}
	if err != nil {
		_ = windows.TerminateProcess(info.Process, 1)
		_, _ = windows.WaitForSingleObject(info.Process, 5000)
		windows.CloseHandle(info.Process)
		return err
	}
	p.process, p.pid = info.Process, info.ProcessId
	return nil
}

func hostEnvironment() []uint16 {
	// Deliberately allow only platform profile and runtime locations. No node
	// tokens, provider secrets, WEBVIEW2_* debug overrides, or inherited PATH.
	var values []string
	for _, key := range []string{"SystemRoot", "WINDIR", "USERPROFILE", "LOCALAPPDATA", "APPDATA", "TEMP", "TMP", "ProgramFiles", "ProgramFiles(x86)", "ProgramData", "COMSPEC"} {
		if value := os.Getenv(key); value != "" {
			values = append(values, key+"="+value)
		}
	}
	values = append(values, "PATH="+filepath.Join(os.Getenv("SystemRoot"), "System32"))
	// UTF16FromString rejects internal NULs, so encode individual entries.
	var block []uint16
	for _, value := range values {
		encoded, _ := windows.UTF16FromString(value)
		block = append(block, encoded...)
	}
	return append(block, 0)
}

func (p *windowsHostProcess) GrantForeground() error {
	select {
	case <-p.exited:
		return fmt.Errorf("输入宿主已退出")
	default:
	}
	ok, _, _ := uiDLL.NewProc("AllowSetForegroundWindow").Call(uintptr(p.pid))
	if ok == 0 {
		return &RegistrationError{"focus_denied", "系统拒绝激活输入框，请从托盘重试"}
	}
	return nil
}

func (p *windowsHostProcess) Stop() error {
	p.stop.Do(func() {
		_ = p.input.Close()
		_ = p.output.Close()
		_ = windows.TerminateJobObject(p.job, 1)
		select {
		case <-p.exited:
		case <-time.After(5 * time.Second):
			p.stopErr = fmt.Errorf("输入宿主退出未确认")
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			count, err := hostJobProcessCount(p.job)
			if err != nil {
				p.stopErr = err
				break
			}
			if count == 0 {
				break
			}
			if time.Now().After(deadline) {
				p.stopErr = fmt.Errorf("输入宿主进程组退出未确认")
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		windows.CloseHandle(p.job)
		// Do not invalidate a process handle still used by the wait goroutine.
		go func() { <-p.exited; windows.CloseHandle(p.process) }()
	})
	return p.stopErr
}

func hostJobProcessCount(job windows.Handle) (uint32, error) {
	var info struct {
		User, Kernel, PeriodUser, PeriodKernel int64
		Faults, Total, Active, Terminated      uint32
	}
	err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
	return info.Active, err
}

func inheritedPipe(value string, name string) (*os.File, error) {
	raw, err := strconv.ParseUint(value, 10, 64)
	if err != nil || raw == 0 {
		return nil, fmt.Errorf("缺少父进程通信句柄")
	}
	h := windows.Handle(raw)
	kind, err := windows.GetFileType(h)
	if err != nil || kind != windows.FILE_TYPE_PIPE {
		return nil, fmt.Errorf("输入宿主通道无效")
	}
	if err = windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), strings.TrimSpace(name)), nil
}
