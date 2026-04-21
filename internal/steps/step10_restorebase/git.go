package step10restorebase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/validation"
)

type GitClient interface {
	WorktreeAdd(ctx context.Context, repoRoot, path, branch, sha string) (created bool, err error)
	ResolveRef(ctx context.Context, repoRoot, ref string) (string, error)
}

type gitCommandRunner func(context.Context, ...string) ([]byte, error)

type RealGitClient struct {
	run gitCommandRunner
}

func NewRealGitClient() GitClient {
	return &RealGitClient{
		run: func(ctx context.Context, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, "git", args...)
			return cmd.CombinedOutput()
		},
	}
}

func (c *RealGitClient) WorktreeAdd(ctx context.Context, repoRoot, path, branch, sha string) (bool, error) {
	if err := contracts.EnsureCleanAbsolutePath(repoRoot); err != nil {
		return false, fmt.Errorf("git worktree add: %w", err)
	}
	if err := contracts.EnsureCleanAbsolutePath(path); err != nil {
		return false, fmt.Errorf("git worktree add: %w", err)
	}
	if branch == "" {
		return false, errors.New("git worktree add: branch is required")
	}
	if err := validation.Instance().Var(sha, "required,sha1_hex"); err != nil {
		return false, fmt.Errorf("git worktree add: %w", err)
	}

	if _, err := os.Stat(path); err == nil {
		if err := c.validateExistingWorktree(ctx, repoRoot, path, branch, sha); err != nil {
			return false, err
		}
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("git worktree add: stat %q: %w", path, err)
	}

	branchExists, err := c.branchExists(ctx, repoRoot, branch)
	if err != nil {
		return false, err
	}

	var args []string
	if branchExists {
		resolved, err := c.ResolveRef(ctx, repoRoot, branch)
		if err != nil {
			return false, err
		}
		if resolved != sha {
			return false, fmt.Errorf("git worktree add: existing branch %q resolves to %s, want %s", branch, resolved, sha)
		}
		args = []string{"-C", repoRoot, "worktree", "add", path, branch}
	} else {
		args = []string{"-C", repoRoot, "worktree", "add", "-b", branch, path, sha}
	}

	output, err := c.run(ctx, args...)
	if err != nil {
		return false, wrapCommandError("git worktree add", err, output)
	}
	if err := c.validateExistingWorktree(ctx, repoRoot, path, branch, sha); err != nil {
		return false, err
	}
	return true, nil
}

func (c *RealGitClient) ResolveRef(ctx context.Context, repoRoot, ref string) (string, error) {
	return c.resolveRefAt(ctx, repoRoot, ref, "git resolve ref")
}

func (c *RealGitClient) resolveRefAt(ctx context.Context, repoDir, ref, op string) (string, error) {
	if err := contracts.EnsureCleanAbsolutePath(repoDir); err != nil {
		return "", fmt.Errorf("%s: %w", op, err)
	}
	if ref == "" {
		return "", fmt.Errorf("%s: ref is required", op)
	}
	output, err := c.run(ctx, "-C", repoDir, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", wrapCommandError(op, err, output)
	}
	sha := strings.TrimSpace(string(output))
	if err := validation.Instance().Var(sha, "required,sha1_hex"); err != nil {
		return "", fmt.Errorf("%s: %w", op, err)
	}
	return sha, nil
}

func (c *RealGitClient) validateExistingWorktree(ctx context.Context, repoRoot, path, branch, sha string) error {
	headSHA, err := c.resolveRefAt(ctx, path, "HEAD", "git worktree add")
	if err != nil {
		return fmt.Errorf("git worktree add: existing path %q is not a valid worktree: %w", path, err)
	}
	if headSHA != sha {
		return fmt.Errorf("git worktree add: existing worktree at %q resolves to %s, want %s", path, headSHA, sha)
	}

	output, err := c.run(ctx, "-C", path, "branch", "--show-current")
	if err != nil {
		return wrapCommandError("git worktree add", err, output)
	}
	currentBranch := strings.TrimSpace(string(output))
	if currentBranch != branch {
		return fmt.Errorf("git worktree add: existing worktree at %q uses branch %q, want %q", path, currentBranch, branch)
	}

	resolvedBranch, err := c.ResolveRef(ctx, repoRoot, branch)
	if err != nil {
		return err
	}
	if resolvedBranch != sha {
		return fmt.Errorf("git worktree add: existing branch %q resolves to %s, want %s", branch, resolvedBranch, sha)
	}
	return nil
}

func (c *RealGitClient) branchExists(ctx context.Context, repoRoot, branch string) (bool, error) {
	output, err := c.run(ctx, "-C", repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, wrapCommandError("git worktree add", err, output)
}
