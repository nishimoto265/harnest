package step10restorebase

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/validation"
)

type GitClient interface {
	WorktreeAdd(ctx context.Context, repoRoot, path, branch, sha string) (created bool, err error)
	ResolveRef(ctx context.Context, repoRoot, ref string) (string, error)
}

type realGitClient struct {
	run subprocessRunner
}

func NewRealGitClient() GitClient {
	return &realGitClient{run: runSubprocess}
}

func (c *realGitClient) WorktreeAdd(ctx context.Context, repoRoot, path, branch, sha string) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}

	if _, err := os.Stat(path); err == nil {
		if err := c.validateExistingWorktree(ctx, repoRoot, path, branch, sha); err != nil {
			return false, err
		}
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}

	branchSHA, err := c.ResolveRef(ctx, repoRoot, branch)
	if err == nil {
		if branchSHA != sha {
			return false, fmt.Errorf("branch already exists at wrong sha: branch=%q branch_sha=%s want=%s", branch, branchSHA, sha)
		}
		if _, err := c.run(ctx, repoRoot, "git", "-C", repoRoot, "worktree", "add", path, branch); err != nil {
			return false, err
		}
	} else {
		if _, err := c.run(ctx, repoRoot, "git", "-C", repoRoot, "worktree", "add", "-b", branch, path, sha); err != nil {
			return false, err
		}
	}

	if err := c.validateExistingWorktree(ctx, repoRoot, path, branch, sha); err != nil {
		return false, err
	}
	return true, nil
}

func (c *realGitClient) ResolveRef(ctx context.Context, repoRoot, ref string) (string, error) {
	output, err := c.run(ctx, repoRoot, "git", "-C", repoRoot, "rev-parse", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(output))
	if err := validation.Instance().Var(sha, "required,sha1_hex"); err != nil {
		return "", err
	}
	return sha, nil
}

func (c *realGitClient) validateExistingWorktree(ctx context.Context, repoRoot, path, branch, sha string) error {
	headSHA, err := c.ResolveRef(ctx, path, "HEAD")
	if err != nil {
		return fmt.Errorf("existing worktree head: %w", err)
	}
	if headSHA != sha {
		return fmt.Errorf("existing worktree is at wrong sha: path=%q head_sha=%s want=%s", path, headSHA, sha)
	}

	output, err := c.run(ctx, repoRoot, "git", "-C", path, "branch", "--show-current")
	if err != nil {
		return fmt.Errorf("existing worktree branch: %w", err)
	}
	currentBranch := strings.TrimSpace(string(output))
	if currentBranch != branch {
		return fmt.Errorf("existing worktree is on wrong branch: path=%q branch=%q want=%q", path, currentBranch, branch)
	}
	return nil
}
