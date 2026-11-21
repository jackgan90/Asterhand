//go:build windows

package workers

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/platform/userprocess"
	"golang.org/x/sys/windows"
)

type managedProc struct {
	mu      sync.Mutex
	job     windows.Handle
	process windows.Handle
	stdin   *os.File
	stdout  *os.File
	stderr  *os.File
	done    chan struct{}
	closed  bool
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
		close(mp.done)
	}()
	return mp, nil
}

func (p *managedProc) Stdin() io.WriteCloser  { return p.stdin }
func (p *managedProc) Stdout() io.ReadCloser  { return p.stdout }
func (p *managedProc) Stderr() io.ReadCloser  { return p.stderr }
func (p *managedProc) Done() <-chan struct{}  { return p.done }

func (p *managedProc) Kill() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	job := p.job
	proc := p.process
	p.job = 0
	p.process = 0
	p.mu.Unlock()

	_ = p.stdin.Close()
	if job != 0 {
		_ = windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(job)
	}
	if proc != 0 {
		_ = windows.CloseHandle(proc)
	}
	_ = p.stdout.Close()
	_ = p.stderr.Close()
	return nil
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
	out := []string{}
	for _, kv := range os.Environ() {
		upper := strings.ToUpper(kv)
		if strings.HasPrefix(upper, "ASTERHAND_") {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "ASTERHAND_MANAGED_WORKER=1")
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
