package step70_decide

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/processenv"
)

// RealGitOps executes the production git commands against the source repo.
type RealGitOps struct {
	RepoDir string
	Remote  string
}

func (g RealGitOps) RemoteHead(ctx context.Context, branch string) (string, error) {
	remote := g.remoteName()
	cmd, err := processenv.TrustedCommandContext(ctx, "git", "-C", g.RepoDir, "ls-remote", "--heads", remote, branch)
	if err != nil {
		return "", err
	}
	// ls-remote hits the network; preserve ssh-agent/token auth while disabling git extension points.
	cmd.Env = processenv.GitNetworkEnv()
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
	cmd, err := processenv.TrustedCommandContext(ctx, "git", "-C", g.RepoDir, "push", remote, refspec, lease)
	if err != nil {
		return err
	}
	// push hits the network; preserve ssh-agent/token auth while disabling git extension points.
	cmd.Env = processenv.GitNetworkEnv()
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
	ok, err := g.worktreeBelongsToRepo(ctx, path)
	if err != nil {
		return err
	}
	if !ok {
		if _, err := os.Lstat(path); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		return fmt.Errorf("%w: %s", ErrWorktreeUnregistered, path)
	}
	cmd, err := processenv.TrustedCommandContext(ctx, "git", "-C", g.RepoDir, "worktree", "remove", "--force", path)
	if err != nil {
		return err
	}
	cmd.Env = processenv.GitLocalEnv()
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

func (g RealGitOps) worktreeBelongsToRepo(ctx context.Context, path string) (bool, error) {
	cmd, err := processenv.TrustedCommandContext(ctx, "git", "-C", g.RepoDir, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	cmd.Env = processenv.GitLocalEnv()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		msgs := make([]string, 0, 2)
		if out := strings.TrimSpace(stdout.String()); out != "" {
			msgs = append(msgs, "stdout="+out)
		}
		if out := strings.TrimSpace(stderr.String()); out != "" {
			msgs = append(msgs, "stderr="+out)
		}
		if len(msgs) == 0 {
			return false, fmt.Errorf("step70: git worktree list --porcelain: %w", err)
		}
		return false, fmt.Errorf("step70: git worktree list --porcelain: %w: %s", err, strings.Join(msgs, "; "))
	}
	want, err := contracts.CanonicalizePathForUniqueness(filepath.Clean(path))
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		have, err := contracts.CanonicalizePathForUniqueness(filepath.Clean(strings.TrimSpace(strings.TrimPrefix(line, "worktree "))))
		if err != nil {
			return false, err
		}
		if have == want {
			return true, nil
		}
	}
	return false, nil
}
