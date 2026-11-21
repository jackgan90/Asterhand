package workers

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/jackgan90/Asterhand/internal/harnesses"
)

// CaptureVCSSnapshot inspects the current worktree state non-destructively.
// Non-Git directories cleanly return VCS="none". Results always mark
// ExternalChangesPossible=true because the user or native IDE shares the tree.
func CaptureVCSSnapshot(ctx context.Context, dir string) *VCSSnapshot {
	_, isGit := harnesses.WorktreeKey(dir)
	if !isGit {
		return &VCSSnapshot{VCS: "none", ExternalChangesPossible: true}
	}
	qCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	snap := &VCSSnapshot{VCS: "git", ExternalChangesPossible: true}
	if head, err := runGit(qCtx, dir, "rev-parse", "--short", "HEAD"); err == nil {
		snap.HeadCommit = strings.TrimSpace(head)
	}
	if branch, err := runGit(qCtx, dir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		snap.Branch = strings.TrimSpace(branch)
	}
	porcelain, err := runGit(qCtx, dir, "status", "--porcelain=v1")
	if err != nil {
		return snap
	}
	for _, line := range strings.Split(porcelain, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < 3 {
			continue
		}
		x, y := line[0], line[1]
		file := strings.TrimSpace(line[3:])
		if x == '?' && y == '?' {
			snap.UntrackedCount++
		} else {
			if x != ' ' && x != '?' {
				snap.StagedCount++
			}
			if y != ' ' && y != '?' {
				snap.UnstagedCount++
			}
		}
		if len(snap.ChangedFiles) < 32 {
			snap.ChangedFiles = append(snap.ChangedFiles, file)
		}
	}
	snap.DirtyBaseline = snap.StagedCount > 0 || snap.UnstagedCount > 0 || snap.UntrackedCount > 0
	return snap
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.String(), err
}
