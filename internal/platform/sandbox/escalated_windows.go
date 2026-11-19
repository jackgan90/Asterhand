//go:build windows

package sandbox

import (
	"context"
	"os"
	"path/filepath"

	"github.com/jackgan90/Asterhand/internal/platform/userprocess"
	"golang.org/x/sys/windows"
)

func (b *windowsBackend) startEscalated(ctx context.Context, spec SpawnSpec) (SessionProcess, error) {
	if ctx.Err() != nil {
		return nil, fail("cancelled")
	}
	if _, bad := CheckSensitiveReferences(spec.Command, spec.WorkspaceRoot, spec.TempRoot, spec.ProtectedDirs); bad {
		return nil, fail("sensitive_path_denied")
	}
	if spec.TempRoot != "" {
		if err := os.MkdirAll(stripExtendedPrefix(filepath.Clean(spec.TempRoot)), 0700); err != nil {
			return nil, err
		}
	}
	allowAnyNonProtected := spec.ExternalDirs == nil
	workdir, err := ResolveEscalatedWorkdir(spec.WorkspaceRoot, spec.Workdir, spec.ExternalDirs, allowAnyNonProtected, spec.ProtectedDirs)
	if err != nil {
		return nil, err
	}
	env, err := BuildEscalatedEnvironment(spec)
	if err != nil {
		return nil, err
	}
	token, err := userprocess.OrdinaryToken()
	if err != nil {
		return nil, fail("spawn_failed")
	}
	if token != 0 {
		defer token.Close()
	}
	return spawnWindowsSession(ctx, token, spec.JobName, verifyOrdinaryChildProcessToken, spec, workdir, env)
}

func verifyOrdinaryChildProcessToken(proc windows.Handle) error {
	var actual windows.Token
	if err := windows.OpenProcessToken(proc, windows.TOKEN_QUERY, &actual); err != nil {
		return err
	}
	defer actual.Close()
	return userprocess.VerifyToken(actual)
}
