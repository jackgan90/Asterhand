package launch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jackgan90/Asterhand/internal/desktop"
	"github.com/jackgan90/Asterhand/internal/i18n"
)

type processRecord struct {
	PID        int    `json:"pid"`
	Created    uint64 `json:"created"`
	Executable string `json:"executable"`
}

type runner struct {
	timeout time.Duration
	poll    time.Duration
	open    func(string) error
	start   func(Installation) (processRecord, <-chan error, error)
}

// Run serializes launching by canonical data directory, including the first
// launch before a configuration exists. A timeout preserves a live child.
func Run(installationPath string, noBrowser bool) error {
	r := runner{timeout: 20 * time.Second, poll: 200 * time.Millisecond, open: desktop.Open, start: startNode}
	return r.run(installationPath, noBrowser)
}

func (r runner) run(path string, noBrowser bool) error {
	installation, err := ReadInstallation(path)
	if err != nil {
		return err
	}
	applyDataLocale(installation.Data)
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	unlock, err := acquireLaunchLock(ctx, installation.Data)
	if err != nil {
		return err
	}
	address, err := r.launchLocked(path, installation)
	unlock()
	if err != nil {
		return i18n.Wrap("err.launch.with_log_dir", map[string]string{"err": err.Error(), "dir": installation.Data}, err)
	}
	if !noBrowser {
		if err := r.open(address); err != nil {
			return i18n.Wrap("err.launch.browser_open_failed", nil, err)
		}
	}
	return nil
}

func (r runner) launchLocked(path string, initial Installation) (string, error) {
	installation, err := ReadInstallation(path)
	if err != nil {
		return "", err
	}
	if !samePath(installation.Data, initial.Data) {
		return "", i18n.Err("err.launch.data_dir_changed")
	}
	if err = prepareHome(installation.Data); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	probeCtx, stopProbe := context.WithTimeout(ctx, 500*time.Millisecond)
	address, probeErr := existingURL(probeCtx, installation.Data)
	stopProbe()
	if probeErr == nil {
		return address, nil
	}
	busy, err := serviceBusy(installation.Data)
	if err != nil {
		return "", err
	}
	pending, err := pendingProcess(installation.Data)
	if err != nil {
		return "", err
	}
	if busy || pending {
		return r.waitReady(ctx, installation.Data, nil)
	}
	if ctx.Err() != nil {
		return "", i18n.Err("err.launch.start_check_timeout")
	}
	process, exited, err := r.start(installation)
	if err != nil {
		return "", err
	}
	if err := saveProcess(installation.Data, process); err != nil {
		return "", i18n.Wrap("err.launch.save_state_failed", nil, err)
	}
	address, err = r.waitReady(ctx, installation.Data, exited)
	if err == nil {
		if removeErr := os.Remove(filepath.Join(installation.Data, "launch-state.json")); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return "", i18n.Wrap("err.launch.remove_state_failed", nil, removeErr)
		}
	}
	return address, err
}

func (r runner) waitReady(ctx context.Context, home string, exited <-chan error) (string, error) {
	for {
		probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		address, err := existingURL(probeCtx, home)
		cancel()
		if err == nil {
			return address, nil
		}
		select {
		case <-exited:
			// A direct CLI start may have won the service lock. Give that
			// instance one last health check instead of reporting a duplicate.
			if address, err := existingURL(ctx, home); err == nil {
				return address, nil
			}
			return "", i18n.Err("err.launch.exited_during_start")
		case <-ctx.Done():
			return "", i18n.Err("err.launch.health_timeout")
		case <-time.After(r.poll):
		}
	}
}

func pendingProcess(home string) (bool, error) {
	path := filepath.Join(home, "launch-state.json")
	var saved processRecord
	if err := readJSON(path, &saved); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, i18n.Err("err.launch.state_corrupt")
	}
	if saved.PID <= 0 || saved.Executable == "" {
		return false, i18n.Err("err.launch.state_missing_pid")
	}
	current, err := inspectProcess(saved.PID)
	if errors.Is(err, os.ErrProcessDone) || (err == nil && saved.Created != 0 && current.Created != saved.Created) {
		return false, os.Remove(path)
	}
	if err != nil || saved.Created == 0 || !samePath(saved.Executable, current.Executable) {
		return false, i18n.Err("err.launch.process_unverified")
	}
	return true, nil
}

func saveProcess(home string, process processRecord) error {
	b, err := json.Marshal(process)
	if err != nil {
		return err
	}
	// The node may be validating a fresh home concurrently. Keep the temporary
	// file outside that home so it never looks like unrelated user content.
	f, err := os.CreateTemp(filepath.Dir(home), ".asterhand-launch-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), filepath.Join(home, "launch-state.json"))
}

func startNode(installation Installation) (processRecord, <-chan error, error) {
	for _, name := range []string{"startup.log", "error.log"} {
		info, err := os.Lstat(filepath.Join(installation.Data, name))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return processRecord{}, nil, err
		}
		if err == nil && !info.Mode().IsRegular() {
			return processRecord{}, nil, i18n.ErrArgs("err.launch.named_log_regular", map[string]string{"name": name})
		}
	}
	stdout, err := os.OpenFile(filepath.Join(installation.Data, "startup.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return processRecord{}, nil, err
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(filepath.Join(installation.Data, "error.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return processRecord{}, nil, err
	}
	defer stderr.Close()
	cmd := exec.Command(installation.Executable, "start", "--home", installation.Data)
	cmd.Dir = filepath.Dir(installation.Executable)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	configureChild(cmd)
	if err = cmd.Start(); err != nil {
		return processRecord{}, nil, i18n.Wrap("err.launch.spawn_failed", nil, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	process, err := inspectProcess(cmd.Process.Pid)
	if err != nil {
		// Zero creation time explicitly preserves uncertainty; a later launcher
		// refuses to replace a live process whose identity could not be read.
		process = processRecord{PID: cmd.Process.Pid, Executable: installation.Executable}
	}
	return process, exited, nil
}
