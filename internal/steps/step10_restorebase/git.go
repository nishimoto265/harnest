package step10restorebase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// GitClient abstracts the subset of `git` commands step10 needs so tests can
// stub them.
type GitClient interface {
	// WorktreeAdd creates a worktree at `path` checking out `branch` at `sha`.
	// Creates the branch if it does not exist.
	//
	// Idempotency:
	//   - path does not exist  → create + return (created=true, nil)
	//   - path already exists  → verify HEAD sha matches; if it does, return
	//     (created=false, nil); otherwise return a wrapped error so the caller
	//     can decide whether to cleanup.
	WorktreeAdd(ctx context.Context, repoRoot, path, branch, sha string) (created bool, err error)

	// ResolveRef resolves a ref to a 40-hex SHA.
	ResolveRef(ctx context.Context, repoRoot, ref string) (string, error)
}

type gitCLI struct {
	run  cmdRunner
	stat func(path string) (os.FileInfo, error)
}

// NewGitClient returns a GitClient backed by the real `git` binary.
func NewGitClient() GitClient {
	return gitCLI{run: defaultCmdRunner, stat: os.Stat}
}

// NewGitClientWithRunner exposes the subprocess seam for tests.
func NewGitClientWithRunner(runner cmdRunner) GitClient {
	if runner == nil {
		runner = defaultCmdRunner
	}
	return gitCLI{run: runner, stat: os.Stat}
}

// ErrWorktreeDrift indicates an existing worktree at the target path pointing
// to an unexpected commit or branch. Callers should treat this as unrecoverable
// within step10 (orchestrator owns the cleanup path).
var ErrWorktreeDrift = errors.New("step10: worktree drift")

func (g gitCLI) WorktreeAdd(ctx context.Context, repoRoot, path, branch, sha string) (bool, error) {
	if _, err := g.stat(path); err == nil {
		// Path exists. Verify it's a worktree at the expected sha and branch.
		head, herr := g.ResolveRef(ctx, path, "HEAD")
		if herr != nil {
			return false, fmt.Errorf("%w: path=%s: cannot resolve HEAD: %v", ErrWorktreeDrift, path, herr)
		}
		if head != sha {
			return false, fmt.Errorf("%w: path=%s expected=%s actual=%s", ErrWorktreeDrift, path, sha, head)
		}
		currentBranch, berr := g.currentBranch(ctx, path)
		if berr != nil {
			return false, fmt.Errorf("%w: path=%s: cannot resolve branch: %v", ErrWorktreeDrift, path, berr)
		}
		if currentBranch == "" || currentBranch != branch {
			return false, fmt.Errorf("%w: path=%s expected_branch=%s actual_branch=%s", ErrWorktreeDrift, path, branch, currentBranch)
		}
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("step10: stat %s: %w", path, err)
	}

	out, stderr, err := g.run(ctx, "git", "-C", repoRoot, "worktree", "add", "-b", branch, path, sha)
	if err != nil {
		// If branch already exists, retry without -b.
		details := string(out) + string(stderr)
		if strings.Contains(details, "already exists") || strings.Contains(details, "is already checked out") {
			out2, stderr2, err2 := g.run(ctx, "git", "-C", repoRoot, "worktree", "add", path, branch)
			if err2 != nil {
				return false, formatCommandFailure(fmt.Sprintf("step10: git worktree add %s", path), err2, out2, stderr2)
			}
			head, herr := g.ResolveRef(ctx, path, "HEAD")
			if herr != nil {
				return false, fmt.Errorf("%w: path=%s: cannot resolve HEAD after retry: %v", ErrWorktreeDrift, path, herr)
			}
			if head != sha {
				driftErr := fmt.Errorf("%w: path=%s expected=%s actual=%s", ErrWorktreeDrift, path, sha, head)
				if cleanupErr := g.removeWorktreeForce(ctx, repoRoot, path); cleanupErr != nil {
					return false, errors.Join(driftErr, fmt.Errorf("cleanup failed: %w", cleanupErr))
				}
				return false, driftErr
			}
			return true, nil
		}
		return false, formatCommandFailure(fmt.Sprintf("step10: git worktree add %s", path), err, out, stderr)
	}
	return true, nil
}

func (g gitCLI) removeWorktreeForce(ctx context.Context, repoRoot, path string) error {
	out, stderr, err := g.run(ctx, "git", "-C", repoRoot, "worktree", "remove", "--force", path)
	if err != nil {
		return formatCommandFailure(fmt.Sprintf("step10: git worktree remove --force %s", path), err, out, stderr)
	}
	return nil
}

func (g gitCLI) currentBranch(ctx context.Context, repoRoot string) (string, error) {
	out, stderr, err := g.run(ctx, "git", "-C", repoRoot, "branch", "--show-current")
	if err != nil {
		return "", formatCommandFailure(fmt.Sprintf("step10: git branch --show-current (in %s)", repoRoot), err, out, stderr)
	}
	return strings.TrimSpace(string(out)), nil
}

func (g gitCLI) ResolveRef(ctx context.Context, repoRoot, ref string) (string, error) {
	out, stderr, err := g.run(ctx, "git", "-C", repoRoot, "rev-parse", ref)
	if err != nil {
		return "", formatCommandFailure(fmt.Sprintf("step10: git rev-parse %s (in %s)", ref, repoRoot), err, out, stderr)
	}
	return strings.TrimSpace(string(out)), nil
}
