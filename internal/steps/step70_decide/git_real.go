package step70_decide

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// RealGitOps executes the production git commands against the source repo.
type RealGitOps struct {
	RepoDir string
	Remote  string
}

func (g RealGitOps) RemoteHead(ctx context.Context, branch string) (string, error) {
	remote := g.remoteName()
	cmd := exec.CommandContext(ctx, "git", "-C", g.RepoDir, "ls-remote", "--heads", remote, branch)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

func (g RealGitOps) PushForceWithLease(ctx context.Context, branch, targetSHA, expected string) error {
	remote := g.remoteName()
	refspec := fmt.Sprintf("%s:%s", targetSHA, branch)
	lease := fmt.Sprintf("--force-with-lease=%s:%s", branch, expected)
	cmd := exec.CommandContext(ctx, "git", "-C", g.RepoDir, "push", remote, refspec, lease)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msg := stderr.String()
		if strings.Contains(msg, "stale info") || strings.Contains(msg, "fetch first") || strings.Contains(msg, "non-fast-forward") {
			return fmt.Errorf("%w: %s", ErrLeaseFailure, strings.TrimSpace(msg))
		}
		return err
	}
	return nil
}

func (g RealGitOps) RemoveWorktree(ctx context.Context, path string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", g.RepoDir, "worktree", "remove", "--force", path)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msgs := make([]string, 0, 2)
		if out := strings.TrimSpace(stdout.String()); out != "" {
			msgs = append(msgs, "stdout="+out)
		}
		if out := strings.TrimSpace(stderr.String()); out != "" {
			msgs = append(msgs, "stderr="+out)
		}
		if len(msgs) == 0 {
			return fmt.Errorf("step70: git worktree remove --force %s: %w", path, err)
		}
		return fmt.Errorf("step70: git worktree remove --force %s: %w: %s", path, err, strings.Join(msgs, "; "))
	}
	return nil
}

func (g RealGitOps) remoteName() string {
	if g.Remote != "" {
		return g.Remote
	}
	return "origin"
}
