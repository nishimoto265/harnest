package step10restorebase

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
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

	// MergeBase resolves the immutable merge base between two commits.
	MergeBase(ctx context.Context, repoRoot, left, right string) (string, error)

	// FetchCommit ensures the given object ID is available in the local clone.
	FetchCommit(ctx context.Context, repoRoot, sha string) error

	// RepoSlug resolves the authoritative owner/name slug from the local clone.
	// step10 uses this for gh requests so it never inherits the caller cwd or a
	// stale config-provided repo string.
	RepoSlug(ctx context.Context, repoRoot string) (string, error)
}

type gitCLI struct {
	run  cmdRunner
	stat func(path string) (os.FileInfo, error)
}

// NewGitClient returns a GitClient backed by the real `git` binary.
func NewGitClient() GitClient {
	return gitCLI{run: defaultCmdRunner, stat: os.Stat}
}

// NewGitClientWithRunner exposes the subprocess seam for tests. The same
// runner is used for every git operation.
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
		ok, werr := g.worktreeBelongsToRepo(ctx, repoRoot, path)
		if werr != nil {
			return false, fmt.Errorf("%w: path=%s: cannot verify worktree membership: %v", ErrWorktreeDrift, path, werr)
		}
		if !ok {
			return false, fmt.Errorf("%w: path=%s is not registered under repo_root=%s", ErrWorktreeDrift, path, repoRoot)
		}
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
		clean, cerr := g.worktreeClean(ctx, path)
		if cerr != nil {
			return false, fmt.Errorf("%w: path=%s: cannot inspect worktree cleanliness: %v", ErrWorktreeDrift, path, cerr)
		}
		if !clean {
			if err := g.removeWorktreeForce(ctx, repoRoot, path); err != nil {
				return false, err
			}
			return g.WorktreeAdd(ctx, repoRoot, path, branch, sha)
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

func (g gitCLI) worktreeClean(ctx context.Context, repoRoot string) (bool, error) {
	out, stderr, err := g.run(ctx, "git", "-C", repoRoot, "status", "--porcelain", "--ignored")
	if err != nil {
		return false, formatCommandFailure(fmt.Sprintf("step10: git status --porcelain --ignored (in %s)", repoRoot), err, out, stderr)
	}
	return strings.TrimSpace(string(out)) == "", nil
}

func (g gitCLI) ResolveRef(ctx context.Context, repoRoot, ref string) (string, error) {
	out, stderr, err := g.run(ctx, "git", "-C", repoRoot, "rev-parse", ref)
	if err != nil {
		return "", formatCommandFailure(fmt.Sprintf("step10: git rev-parse %s (in %s)", ref, repoRoot), err, out, stderr)
	}
	return strings.TrimSpace(string(out)), nil
}

func (g gitCLI) MergeBase(ctx context.Context, repoRoot, left, right string) (string, error) {
	out, stderr, err := g.run(ctx, "git", "-C", repoRoot, "merge-base", left, right)
	if err != nil {
		return "", formatCommandFailure(fmt.Sprintf("step10: git merge-base %s %s (in %s)", left, right, repoRoot), err, out, stderr)
	}
	return strings.TrimSpace(string(out)), nil
}

func (g gitCLI) FetchCommit(ctx context.Context, repoRoot, sha string) error {
	out, stderr, err := g.run(ctx, "git", "-C", repoRoot, "fetch", "--no-tags", "origin", sha)
	if err != nil {
		return formatCommandFailure(fmt.Sprintf("step10: git fetch origin %s (in %s)", sha, repoRoot), err, out, stderr)
	}
	return nil
}

func (g gitCLI) RepoSlug(ctx context.Context, repoRoot string) (string, error) {
	out, stderr, err := g.run(ctx, "git", "-C", repoRoot, "remote", "get-url", "origin")
	if err != nil {
		return "", formatCommandFailure(fmt.Sprintf("step10: git remote get-url origin (in %s)", repoRoot), err, out, stderr)
	}
	slug, err := repoSlugFromRemoteURL(strings.TrimSpace(string(out)))
	if err != nil {
		return "", fmt.Errorf("step10: resolve repo slug from origin remote (in %s): %w", repoRoot, err)
	}
	return slug, nil
}

func (g gitCLI) worktreeBelongsToRepo(ctx context.Context, repoRoot, path string) (bool, error) {
	out, stderr, err := g.run(ctx, "git", "-C", repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return false, formatCommandFailure(fmt.Sprintf("step10: git worktree list --porcelain (in %s)", repoRoot), err, out, stderr)
	}
	want, err := contracts.CanonicalizePathForUniqueness(filepath.Clean(path))
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		candidate := strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		have, err := contracts.CanonicalizePathForUniqueness(filepath.Clean(candidate))
		if err != nil {
			return false, err
		}
		if have == want {
			return true, nil
		}
	}
	return false, nil
}

func repoSlugFromRemoteURL(remoteURL string) (string, error) {
	if remoteURL == "" {
		return "", errors.New("origin remote url is empty")
	}
	if strings.HasPrefix(remoteURL, "/") || strings.HasPrefix(remoteURL, "./") || strings.HasPrefix(remoteURL, "../") {
		return "", fmt.Errorf("expected owner/name repo slug, got %q", remoteURL)
	}
	remoteURL = strings.TrimSuffix(remoteURL, ".git")

	if strings.Contains(remoteURL, "://") {
		parsed, err := url.Parse(remoteURL)
		if err != nil {
			return "", err
		}
		return repoSlugFromPath(parsed.Path)
	}

	if at := strings.LastIndex(remoteURL, "@"); at >= 0 {
		if colon := strings.Index(remoteURL[at+1:], ":"); colon >= 0 {
			return repoSlugFromPath(remoteURL[at+1+colon+1:])
		}
	}

	return repoSlugFromPath(remoteURL)
}

func repoSlugFromPath(path string) (string, error) {
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	switch len(parts) {
	case 2:
		if parts[0] == "" || parts[1] == "" {
			break
		}
		if !looksLikeOwner(parts[0]) || !looksLikeRepoName(parts[1]) {
			break
		}
		return parts[0] + "/" + parts[1], nil
	case 3:
		if looksLikeHost(parts[0]) {
			if parts[1] == "" || parts[2] == "" {
				break
			}
			if !looksLikeOwner(parts[1]) || !looksLikeRepoName(parts[2]) {
				break
			}
			return parts[1] + "/" + parts[2], nil
		}
	}
	return "", fmt.Errorf("expected owner/name repo slug, got %q", path)
}

func looksLikeHost(segment string) bool {
	return strings.Contains(segment, ".") || strings.Contains(segment, ":") || segment == "github.com"
}

func looksLikeOwner(segment string) bool {
	return segment != "." && segment != ".." && !strings.Contains(segment, ".")
}

func looksLikeRepoName(segment string) bool {
	return segment != "." && segment != ".."
}
