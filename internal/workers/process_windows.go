//go:build windows

package workers

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/platform/userprocess"
	"golang.org/x/sys/windows"
)

const jobObjectBasicAccountingInformation int32 = 1

type jobObjectBasicAccountingInfo struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

func queryJobActiveProcesses(job windows.Handle) (uint32, error) {
	var info jobObjectBasicAccountingInfo
	err := windows.QueryInformationJobObject(
		job,
		jobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
		nil,
	)
	if err != nil {
		return 0, err
	}
	return info.ActiveProcesses, nil
}

type managedProc struct {
	mu          sync.Mutex
	job         windows.Handle
	process     windows.Handle
	stdin       *os.File
	stdout      *os.File
	stderr      *os.File
	done        chan struct{}
	stdinClosed bool
	ioClosed    bool
	killOnce    sync.Once
	killErr     error
}

func spawnManagedProcess(executable string, args []string, workdir string, env []string) (*managedProc, error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return nil, err
	}

	token, err := userprocess.OrdinaryToken()
	if err != nil {
		closeAll(inR, inW, outR, outW, errR, errW)
		return nil, err
	}
	if token != 0 {
		defer token.Close()
	}

	jobName := `Local\Asterhand.Worker.` + model.ID()
	job, err := userprocess.NewKillOnCloseJob(jobName, 0)
	if err != nil {
		closeAll(inR, inW, outR, outW, errR, errW)
		return nil, err
	}

	spawnExe, cmdLine := buildWindowsCommandLine(executable, args)
	res, err := userprocess.SpawnAttachedJob(userprocess.SpawnConfig{
		Job:         job,
		Token:       token,
		Executable:  spawnExe,
		CommandLine: cmdLine,
		WorkingDir:  workdir,
		Environment: env,
		Stdin:       windows.Handle(inR.Fd()),
		Stdout:      windows.Handle(outW.Fd()),
		Stderr:      windows.Handle(errW.Fd()),
	})
	inR.Close()
	outW.Close()
	errW.Close()
	if err != nil {
		_ = windows.CloseHandle(job)
		closeAll(inW, outR, errR)
		return nil, err
	}

	mp := &managedProc{
		job:     job,
		process: res.Process,
		stdin:   inW,
		stdout:  outR,
		stderr:  errR,
		done:    make(chan struct{}),
	}
	go func() {
		_, _ = windows.WaitForSingleObject(res.Process, windows.INFINITE)
		_ = windows.CloseHandle(res.Process)
		close(mp.done)
	}()
	return mp, nil
}

func (p *managedProc) Stdin() io.WriteCloser { return p.stdin }
func (p *managedProc) Stdout() io.ReadCloser { return p.stdout }
func (p *managedProc) Stderr() io.ReadCloser { return p.stderr }
func (p *managedProc) Done() <-chan struct{} { return p.done }

func (p *managedProc) Kill() error {
	p.killOnce.Do(func() {
		p.killErr = p.killOnceImpl()
	})
	return p.killErr
}

func (p *managedProc) killOnceImpl() error {
	p.mu.Lock()
	job := p.job
	p.job = 0
	closeStdin := !p.stdinClosed && p.stdin != nil
	p.stdinClosed = true
	p.mu.Unlock()

	if closeStdin {
		_ = p.stdin.Close()
	}

	deadline := time.Now().Add(2 * time.Second)
	var termErr error
	jobConfirmed := true
	if job != 0 {
		if err := windows.TerminateJobObject(job, 1); err != nil {
			termErr = err
			// Independently verify whether the job is actually empty. If we
			// cannot confirm ActiveProcesses == 0, do NOT trust root-process
			// exit alone because descendant processes may still be alive.
			if active, qErr := queryJobActiveProcesses(job); qErr == nil && active == 0 {
				jobConfirmed = true
			} else {
				jobConfirmed = false
			}
		} else {
			jobConfirmed = waitJobEmpty(job, deadline)
		}
		_ = windows.CloseHandle(job)
	}

	rootExited := true
	if p.done != nil {
		rem := time.Until(deadline)
		if rem <= 0 {
			select {
			case <-p.done:
				rootExited = true
			default:
				rootExited = false
			}
		} else {
			select {
			case <-p.done:
				rootExited = true
			case <-time.After(rem):
				rootExited = false
			}
		}
	}

	if rootExited && jobConfirmed {
		p.mu.Lock()
		closeIO := !p.ioClosed
		p.ioClosed = true
		p.mu.Unlock()
		if closeIO {
			if p.stdout != nil {
				_ = p.stdout.Close()
			}
			if p.stderr != nil {
				_ = p.stderr.Close()
			}
		}
		return nil
	}
	if termErr != nil {
		return fmt.Errorf("%w: terminate worker process tree failed: %v", ErrProcessStopUnconfirmed, termErr)
	}
	return ErrProcessStopUnconfirmed
}

func waitJobEmpty(job windows.Handle, deadline time.Time) bool {
	for {
		active, err := queryJobActiveProcesses(job)
		if err != nil {
			return false
		}
		if active == 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(15 * time.Millisecond)
	}
}

func buildWindowsCommandLine(executable string, args []string) (string, string) {
	ext := strings.ToLower(filepath.Ext(executable))
	if ext == ".cmd" || ext == ".bat" {
		comspec := os.Getenv("ComSpec")
		if comspec == "" {
			comspec = `C:\Windows\System32\cmd.exe`
		}
		var b strings.Builder
		b.WriteString(quoteWinArg(comspec))
		b.WriteString(` /d /s /c "`)
		b.WriteString(quoteWinArg(executable))
		for _, a := range args {
			b.WriteByte(' ')
			b.WriteString(quoteWinArg(a))
		}
		b.WriteByte('"')
		return comspec, b.String()
	}
	var b strings.Builder
	b.WriteString(quoteWinArg(executable))
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(quoteWinArg(a))
	}
	return executable, b.String()
}

func quoteWinArg(s string) string {
	return windows.EscapeArg(s)
}

func sanitizedWorkerEnv() []string {
	return sanitizedWorkerEnvWith(nil)
}

func sanitizedWorkerEnvWith(overrides map[string]string) []string {
	upperOverrides := map[string]string{}
	for k, v := range overrides {
		upperOverrides[strings.ToUpper(k)] = k + "=" + v
	}
	out := []string{}
	for _, kv := range os.Environ() {
		upper := strings.ToUpper(kv)
		if strings.HasPrefix(upper, "ASTERHAND_") {
			continue
		}
		if eq := strings.IndexByte(upper, '='); eq > 0 {
			if _, skip := upperOverrides[upper[:eq]]; skip {
				continue
			}
		}
		out = append(out, kv)
	}
	out = append(out, "ASTERHAND_MANAGED_WORKER=1")
	for _, kv := range upperOverrides {
		out = append(out, kv)
	}
	return out
}

func closeAll(files ...*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}

func fmtProcessErr(msg string, err error) error {
	return fmt.Errorf("%s: %w", msg, err)
}
