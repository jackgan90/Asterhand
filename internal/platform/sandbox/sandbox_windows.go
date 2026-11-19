//go:build windows

package sandbox

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/jackgan90/Asterhand/internal/platform/textcodec"
	"github.com/jackgan90/Asterhand/internal/platform/userprocess"
	"golang.org/x/sys/windows"
)

const (
	jobLimitDieOnUnhandledException = 0x00000400
	maxEncodedPSChars               = 12000
)

type windowsBackend struct {
	mu    sync.Mutex
	tok   windows.Token
	sid   *windows.SID
	ready bool
	ws    string
	tmp   string
}

// NewBackend returns the platform-native sandbox backend implementation.
func NewBackend() Backend {
	return &windowsBackend{}
}

func (b *windowsBackend) Supported() bool { return true }

func (b *windowsBackend) Prepare(workspaceRoot, tempRoot string) error {
	cleanWS := filepath.Clean(workspaceRoot)
	cleanTmp := filepath.Clean(tempRoot)
	_, tmpStatErr := os.Stat(cleanTmp)
	if err := os.MkdirAll(cleanTmp, 0700); err != nil {
		return err
	}
	b.mu.Lock()
	cached := b.ready && b.ws == cleanWS && b.tmp == cleanTmp && b.tok != 0 && b.sid != nil
	cachedSID := b.sid
	b.mu.Unlock()
	if cached {
		if tmpStatErr != nil {
			return EnsureDirGrant(cleanTmp, cachedSID)
		}
		return nil
	}
	sid, err := DeriveSandboxSID(cleanWS)
	if err != nil {
		return err
	}
	if err := EnsureDirGrant(cleanWS, sid); err != nil {
		return err
	}
	if err := EnsureDirGrant(cleanTmp, sid); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ready && b.ws == cleanWS && b.tmp == cleanTmp && b.tok != 0 {
		return nil
	}
	tok, err := CreateWriteRestrictedToken(sid)
	if err != nil {
		return err
	}
	if b.tok != 0 {
		_ = b.tok.Close()
	}
	b.tok, b.sid, b.ws, b.tmp, b.ready = tok, sid, cleanWS, cleanTmp, true
	return nil
}

func (b *windowsBackend) Start(ctx context.Context, spec SpawnSpec) (SessionProcess, error) {
	if ctx.Err() != nil {
		return nil, fail("cancelled")
	}
	if spec.Command == "" {
		return nil, fail("invalid_request")
	}
	if CheckElevationVerbs(spec.Command) {
		return nil, fail("elevation_verb_denied")
	}
	if spec.Escalated {
		return b.startEscalated(ctx, spec)
	}
	if label, bad := CheckSensitiveReferences(spec.Command, spec.WorkspaceRoot, spec.TempRoot, spec.ProtectedDirs); bad {
		_ = label
		return nil, fail("sensitive_path_denied")
	}
	workdir, err := ResolveWorkdir(spec.WorkspaceRoot, spec.Workdir)
	if err != nil {
		return nil, err
	}
	if err := b.Prepare(spec.WorkspaceRoot, spec.TempRoot); err != nil {
		return nil, err
	}
	env, err := BuildEnvironment(spec)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	token := b.tok
	b.mu.Unlock()
	return spawnWindowsSession(ctx, token, "", nil, spec, workdir, env)
}

func isNotFoundWinErr(err error) bool {
	return errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND)
}

func isTransientCleanupErr(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_DIR_NOT_EMPTY)
}

func (b *windowsBackend) CleanupTemp(tempRoot string) error {
	if tempRoot == "" {
		return nil
	}
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		err = clearTempContents(tempRoot, 0)
		if err == nil || os.IsNotExist(err) || !isTransientCleanupErr(err) || attempt == 4 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.MkdirAll(tempRoot, 0700)
}

func clearTempContents(dir string, depth int) error {
	if depth > maxGrantDepth {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, item := range entries {
		full := filepath.Join(dir, item.Name())
		ptr, ptrErr := windows.UTF16PtrFromString(full)
		if ptrErr != nil {
			continue
		}
		attrs, attrErr := windows.GetFileAttributes(ptr)
		if attrErr != nil {
			if isNotFoundWinErr(attrErr) {
				continue
			}
			return attrErr
		}
		isReparse := attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
		isDir := attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0
		if attrs&windows.FILE_ATTRIBUTE_READONLY != 0 {
			_ = windows.SetFileAttributes(ptr, attrs&^windows.FILE_ATTRIBUTE_READONLY)
		}
		if isReparse {
			if isDir {
				if err := windows.RemoveDirectory(ptr); err != nil && !isNotFoundWinErr(err) {
					return err
				}
			} else if err := windows.DeleteFile(ptr); err != nil && !isNotFoundWinErr(err) {
				return err
			}
			continue
		}
		if isDir {
			if err := clearTempContents(full, depth+1); err != nil {
				return err
			}
			if err := windows.RemoveDirectory(ptr); err != nil && !isNotFoundWinErr(err) {
				return err
			}
		} else if err := windows.DeleteFile(ptr); err != nil && !isNotFoundWinErr(err) {
			return err
		}
	}
	return nil
}

type windowsSession struct {
	mu             sync.Mutex
	job            windows.Handle
	process        windows.Handle
	stdout, stderr *os.File
	output         *OutputBuffer
	started        time.Time
	cancelReason   string
	exitInfo       ExitInfo
	done           chan struct{}
	closed         bool
	tempScript     string
}

func spawnWindowsSession(ctx context.Context, token windows.Token, jobName string, afterSuspended func(windows.Handle) error, spec SpawnSpec, workdir string, env []string) (SessionProcess, error) {
	job, err := userprocess.NewKillOnCloseJob(jobName, jobLimitDieOnUnhandledException)
	if err != nil {
		return nil, err
	}
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	errRead, errWrite, err := os.Pipe()
	if err != nil {
		_ = outRead.Close()
		_ = outWrite.Close()
		_ = windows.CloseHandle(job)
		return nil, err
	}
	nulPtr, _ := windows.UTF16PtrFromString("NUL")
	nulHandle, err := windows.CreateFile(nulPtr, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		_ = outRead.Close()
		_ = outWrite.Close()
		_ = errRead.Close()
		_ = errWrite.Close()
		_ = windows.CloseHandle(job)
		return nil, err
	}

	shellPath, cmdLine, tempScript, err := buildShellInvocation(spec)
	if err != nil {
		_ = windows.CloseHandle(nulHandle)
		_ = outRead.Close()
		_ = outWrite.Close()
		_ = errRead.Close()
		_ = errWrite.Close()
		_ = windows.CloseHandle(job)
		return nil, err
	}

	res, err := userprocess.SpawnAttachedJob(userprocess.SpawnConfig{
		Job:            job,
		Token:          token,
		Executable:     shellPath,
		CommandLine:    cmdLine,
		WorkingDir:     workdir,
		Environment:    env,
		Stdin:          nulHandle,
		Stdout:         windows.Handle(outWrite.Fd()),
		Stderr:         windows.Handle(errWrite.Fd()),
		AfterSuspended: afterSuspended,
		AdmitResume: func(resume func() error) error {
			if ctx.Err() != nil {
				return fail("cancelled")
			}
			if spec.AdmitResume != nil {
				return spec.AdmitResume(resume)
			}
			return resume()
		},
	})
	_ = windows.CloseHandle(nulHandle)
	_ = outWrite.Close()
	_ = errWrite.Close()
	if err != nil {
		_ = outRead.Close()
		_ = errRead.Close()
		if res.Process != 0 {
			_ = windows.CloseHandle(res.Process)
		}
		_ = windows.CloseHandle(job)
		if tempScript != "" {
			_ = os.Remove(tempScript)
		}
		var se *Error
		if errors.As(err, &se) && se.Code != "" {
			return nil, se
		}
		return nil, fail("spawn_failed")
	}

	s := &windowsSession{
		job:        job,
		process:    res.Process,
		stdout:     outRead,
		stderr:     errRead,
		output:     NewOutputBuffer(MaxOutputSessionBytes),
		started:    time.Now(),
		done:       make(chan struct{}),
		tempScript: tempScript,
	}
	go s.monitor(ctx, spec.Timeout)
	return s, nil
}

func psUTF16DataExpr(s string) string {
	return "([System.Text.Encoding]::Unicode.GetString([System.Convert]::FromBase64String('" + encodeUTF16Base64(s) + "')))"
}

func formatDirectPowerShellCall(exe, rawArgsTail string, fallbackArgs []string) string {
	var b strings.Builder
	b.WriteString("& ")
	b.WriteString(psUTF16DataExpr(exe))
	if rawArgsTail != "" {
		if rawArgsTail[0] != ' ' && rawArgsTail[0] != '\t' {
			b.WriteByte(' ')
		}
		b.WriteString(rawArgsTail)
		return b.String()
	}
	for _, a := range fallbackArgs {
		b.WriteByte(' ')
		b.WriteString(psUTF16DataExpr(a))
	}
	return b.String()
}

func buildShellInvocation(spec SpawnSpec) (string, string, string, error) {
	shell := resolvePowerShell()
	body := spec.Command
	if spec.DirectExe != "" {
		body = formatDirectPowerShellCall(spec.DirectExe, spec.DirectRawArgs, spec.DirectArgs)
	}
	script := "$ProgressPreference = 'SilentlyContinue'\r\n" +
		"try { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8 } catch {}\r\n" +
		"$OutputEncoding = [System.Text.Encoding]::UTF8\r\n" +
		body + "\r\n" +
		"if (-not $?) { if ($LASTEXITCODE -and $LASTEXITCODE -ne 0) { exit $LASTEXITCODE }; exit 1 }\r\n"
	if len(script) <= maxEncodedPSChars {
		encoded := encodeUTF16Base64(script)
		line := windows.ComposeCommandLine([]string{shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand", encoded})
		if len(utf16.Encode([]rune(line))) < 30000 {
			return shell, line, "", nil
		}
	}
	// Fallback for long scripts: write UTF-8 with BOM to managed sandbox-temp.
	f, err := os.CreateTemp(stripExtendedPrefix(filepath.Clean(spec.TempRoot)), "asterhand-cmd-*.ps1")
	if err != nil {
		return "", "", "", err
	}
	name := f.Name()
	_, wErr := f.Write(append([]byte{0xef, 0xbb, 0xbf}, []byte(script)...))
	cErr := f.Close()
	if wErr != nil || cErr != nil {
		_ = os.Remove(name)
		return "", "", "", fail("io_error")
	}
	line := windows.ComposeCommandLine([]string{shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", name})
	return shell, line, name, nil
}

func resolvePowerShell() string {
	if p, err := exec.LookPath("pwsh.exe"); err == nil && p != "" {
		return p
	}
	sys, err := windows.GetSystemDirectory()
	if err == nil {
		ps := filepath.Join(sys, "WindowsPowerShell", "v1.0", "powershell.exe")
		if _, statErr := os.Stat(ps); statErr == nil {
			return ps
		}
	}
	if p, err := exec.LookPath("powershell.exe"); err == nil && p != "" {
		return p
	}
	return "powershell.exe"
}

func encodeUTF16Base64(s string) string {
	u := utf16.Encode([]rune(s))
	buf := make([]byte, len(u)*2)
	for i, v := range u {
		buf[i*2] = byte(v)
		buf[i*2+1] = byte(v >> 8)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

func (s *windowsSession) releaseMonitorHandles() {
	s.mu.Lock()
	proc := s.process
	s.process = 0
	s.mu.Unlock()
	if proc != 0 {
		_ = windows.CloseHandle(proc)
	}
	_ = s.stdout.Close()
	_ = s.stderr.Close()
	if s.tempScript != "" {
		_ = os.Remove(s.tempScript)
	}
}

func (s *windowsSession) monitor(ctx context.Context, timeout time.Duration) {
	defer close(s.done)
	defer s.releaseMonitorHandles()

	outDone := make(chan struct{})
	errDone := make(chan struct{})
	go s.pumpPipe(s.stdout, outDone)
	go s.pumpPipe(s.stderr, errDone)

	waitDone := make(chan int, 1)
	go func() {
		_, _ = windows.WaitForSingleObject(s.process, windows.INFINITE)
		var code uint32
		_ = windows.GetExitCodeProcess(s.process, &code)
		waitDone <- int(code)
	}()

	var timerCh <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timerCh = t.C
	}

	var exitCode int
	select {
	case exitCode = <-waitDone:
	case <-timerCh:
		s.Terminate("timeout")
		exitCode = <-waitDone
	case <-ctx.Done():
		s.Terminate("cancelled")
		exitCode = <-waitDone
	}

	// Ensure any lingering grandchildren are terminated cleanly on root exit when cancelled,
	// and bound pipe drain to PipeDrainTimeout if a background holder kept stdout/stderr open.
	drainDeadline := time.NewTimer(PipeDrainTimeout)
	defer drainDeadline.Stop()
	for i := 0; i < 2; i++ {
		select {
		case <-outDone:
			outDone = nil
		case <-errDone:
			errDone = nil
		case <-drainDeadline.C:
			i = 2
		}
	}
	if outDone != nil || errDone != nil {
		// Terminate remaining job members without overwriting cancelReason and close pipes
		// so stalled grandchild pipe holders cannot block session teardown.
		s.mu.Lock()
		if !s.closed && s.job != 0 {
			_ = windows.TerminateJobObject(s.job, uint32(exitCode))
		}
		s.mu.Unlock()
		_ = s.stdout.Close()
		_ = s.stderr.Close()
		if outDone != nil {
			<-outDone
		}
		if errDone != nil {
			<-errDone
		}
	}

	fullText, _, _ := s.output.Materialize()
	s.mu.Lock()
	reason := s.cancelReason
	s.exitInfo = ExitInfo{
		Exited:     true,
		ExitCode:   exitCode,
		Reason:     reason,
		DurationMs: time.Since(s.started).Milliseconds(),
		TLSHint:    DetectWindowsTLSHint(exitCode, fullText),
	}
	s.mu.Unlock()
}

func (s *windowsSession) pumpPipe(f *os.File, done chan struct{}) {
	defer close(done)
	dec := textcodec.NewStreamDecoder()
	buf := make([]byte, 8192)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			s.output.Append(dec.Write(buf[:n]))
		}
		if err != nil {
			if tail := dec.Flush(); tail != "" {
				s.output.Append(tail)
			}
			return
		}
	}
}

func (s *windowsSession) OutputSnapshot(offset int64, limit int) OutputSlice {
	return s.output.ReadSlice(offset, limit)
}

func (s *windowsSession) Done() <-chan struct{} { return s.done }

func (s *windowsSession) ExitInfo() ExitInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitInfo
}

func (s *windowsSession) Terminate(reason string) {
	s.mu.Lock()
	if s.cancelReason == "" && reason != "" {
		s.cancelReason = reason
	}
	if !s.closed && s.job != 0 {
		_ = windows.TerminateJobObject(s.job, 1)
	}
	s.mu.Unlock()
}

func (s *windowsSession) ProcessesStopped() (bool, error) {
	select {
	case <-s.done:
	default:
		return false, nil
	}
	s.mu.Lock()
	closed := s.closed
	job := s.job
	s.mu.Unlock()
	if closed || job == 0 {
		return true, nil
	}
	cnt, err := userprocess.JobProcessCount(job)
	if err != nil {
		return false, err
	}
	return cnt == 0, nil
}

func (s *windowsSession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	if s.job != 0 {
		_ = windows.TerminateJobObject(s.job, 1)
	}
	s.mu.Unlock()

	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
		_ = s.stdout.Close()
		_ = s.stderr.Close()
		select {
		case <-s.done:
		case <-time.After(500 * time.Millisecond):
		}
	}

	s.mu.Lock()
	job := s.job
	s.job = 0
	s.mu.Unlock()
	if job != 0 {
		_ = windows.CloseHandle(job)
	}
	if s.tempScript != "" {
		_ = os.Remove(s.tempScript)
	}
	return nil
}

func finalPathByHandle(h windows.Handle) (string, error) {
	var stack [windows.MAX_PATH]uint16
	n, err := windows.GetFinalPathNameByHandle(h, &stack[0], uint32(len(stack)), 0)
	if err == nil && n > 0 && n < uint32(len(stack)) {
		return stripExtendedPrefix(filepath.Clean(windows.UTF16ToString(stack[:n]))), nil
	}
	if err == nil && n >= uint32(len(stack)) {
		buf := make([]uint16, n+1)
		written, bufErr := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
		if bufErr == nil && written > 0 && written < uint32(len(buf)) {
			return stripExtendedPrefix(filepath.Clean(windows.UTF16ToString(buf[:written]))), nil
		}
	}
	return "", err
}

// OpenSharedReadLocked opens an existing regular file (not a directory or reparse point)
// with FILE_SHARE_READ so concurrent writers/deleters are blocked until Close, while child
// processes can still read or execute the verified target.
func OpenSharedReadLocked(path string) (*os.File, string, int64, int64, error) {
	abs, err := filepath.Abs(stripExtendedPrefix(filepath.Clean(path)))
	if err != nil {
		return nil, "", 0, 0, fail("invalid_path")
	}
	openPath := stripExtendedPrefix(abs)
	if len(openPath) >= 248 && len(openPath) >= 3 && openPath[1] == ':' && openPath[2] == '\\' && !strings.HasPrefix(openPath, `\\?\`) {
		openPath = `\\?\` + openPath
	}
	ptr, err := windows.UTF16PtrFromString(openPath)
	if err != nil {
		return nil, "", 0, 0, fail("invalid_path")
	}
	h, err := windows.CreateFile(ptr, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return nil, "", 0, 0, os.ErrNotExist
		}
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil, "", 0, 0, fail("io_error")
		}
		return nil, "", 0, 0, fail("invalid_path")
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		_ = windows.CloseHandle(h)
		return nil, "", 0, 0, fail("io_error")
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		_ = windows.CloseHandle(h)
		return nil, "", 0, 0, fail("unsafe_links_detected")
	}
	finalPath, fpErr := finalPathByHandle(h)
	if fpErr != nil || finalPath == "" {
		_ = windows.CloseHandle(h)
		return nil, "", 0, 0, fail("unsafe_links_detected")
	}
	size := int64(info.FileSizeHigh)<<32 | int64(info.FileSizeLow)
	modNano := info.LastWriteTime.Nanoseconds()
	return os.NewFile(uintptr(h), finalPath), finalPath, size, modNano, nil
}

func evalRealPath(p string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(p))
	if err != nil {
		return "", err
	}
	openPath := stripExtendedPrefix(abs)
	if len(openPath) >= 248 && len(openPath) >= 3 && openPath[1] == ':' && openPath[2] == '\\' && !strings.HasPrefix(openPath, `\\?\`) {
		openPath = `\\?\` + openPath
	}
	ptr, err := windows.UTF16PtrFromString(openPath)
	if err != nil {
		return filepath.EvalSymlinks(abs)
	}
	h, err := windows.CreateFile(ptr, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return "", err
		}
		return filepath.EvalSymlinks(abs)
	}
	defer windows.CloseHandle(h)
	if finalPath, fpErr := finalPathByHandle(h); fpErr == nil && finalPath != "" {
		return finalPath, nil
	}
	return filepath.EvalSymlinks(abs)
}
