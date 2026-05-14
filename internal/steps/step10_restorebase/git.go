package step10restorebase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/nishimoto265/harnest/internal/gitremote"
	"github.com/nishimoto265/harnest/internal/policyartifact"
	"github.com/nishimoto265/harnest/internal/policyrepo"
	"github.com/nishimoto265/harnest/internal/processenv"
	"github.com/nishimoto265/harnest/internal/steps/policyoverlay"
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

	// PreparePassBase applies the harness policy overlay to a pass base
	// worktree and advances the pass base branch to the prepared head.
	PreparePassBase(ctx context.Context, allocation contracts.PassBaseAllocation, runID contracts.RunID, policySnapshotDir string, activeRules []policyrepo.ActiveRule, experimentLessons []policyoverlay.ExperimentLesson) (contracts.PassBaseAllocation, error)

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
	// ChangedFiles returns the changed file list between two commits.
	ChangedFiles(ctx context.Context, repoRoot, from, to string) ([]string, error)
	// Diff returns the unified diff between two commits.
	Diff(ctx context.Context, repoRoot, from, to string) (string, error)
}

type gitCLI struct {
	run  cmdRunner
	stat func(path string) (os.FileInfo, error)
}

// NewGitClient returns a GitClient backed by the real `git` binary.
func NewGitClient() GitClient {
	return gitCLI{stat: os.Stat}
}

// NewGitClientWithRunner exposes the subprocess seam for tests. The same
// runner is used for every git operation.
func NewGitClientWithRunner(runner cmdRunner) GitClient {
	if runner == nil {
		return NewGitClient()
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

	out, stderr, err := g.runLocal(ctx, "-C", repoRoot, "worktree", "add", "-b", branch, path, sha)
	if err != nil {
		// If branch already exists, retry without -b.
		details := string(out) + string(stderr)
		if strings.Contains(details, "already exists") || strings.Contains(details, "is already checked out") {
			out2, stderr2, err2 := g.runLocal(ctx, "-C", repoRoot, "worktree", "add", path, branch)
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

func (g gitCLI) PreparePassBase(ctx context.Context, allocation contracts.PassBaseAllocation, runID contracts.RunID, policySnapshotDir string, activeRules []policyrepo.ActiveRule, experimentLessons []policyoverlay.ExperimentLesson) (contracts.PassBaseAllocation, error) {
	if err := policyoverlay.ApplyWithSnapshot(allocation.Path, policySnapshotDir, activeRules, experimentLessons); err != nil {
		return allocation, err
	}
	resetArgs := append([]string{"-C", allocation.Path, "reset", "--quiet", "--"}, policyartifact.GitResetPathspecs()...)
	if _, stderr, err := g.runLocal(ctx, resetArgs...); err != nil {
		return allocation, formatCommandFailure("step10: git reset policy checklist result", err, nil, stderr)
	}
	policyPathspecs := policyartifact.ExistingPolicyBasePathspecs(allocation.Path)
	if len(policyPathspecs) == 0 {
		head, err := g.ResolveRef(ctx, allocation.Path, "HEAD")
		if err != nil {
			return allocation, err
		}
		allocation.BaseSHA = head
		allocation.HeadSHA = head
		return allocation, nil
	}
	addArgs := append([]string{"-C", allocation.Path, "add", "-A", "-f", "--"}, policyPathspecs...)
	if _, stderr, err := g.runLocal(ctx, addArgs...); err != nil {
		return allocation, formatCommandFailure("step10: git add policy overlay", err, nil, stderr)
	}
	diffArgs := append([]string{"-C", allocation.Path, "diff", "--cached", "--name-only", "--"}, policyPathspecs...)
	out, stderr, err := g.runLocal(ctx, diffArgs...)
	if err != nil {
		return allocation, formatCommandFailure("step10: git diff policy overlay", err, out, stderr)
	}
	if strings.TrimSpace(string(out)) == "" {
		head, err := g.ResolveRef(ctx, allocation.Path, "HEAD")
		if err != nil {
			return allocation, err
		}
		allocation.BaseSHA = head
		allocation.HeadSHA = head
		return allocation, nil
	}
	tree, stderr, err := g.runLocal(ctx, "-C", allocation.Path, "write-tree")
	if err != nil {
		return allocation, formatCommandFailure("step10: git write-tree policy overlay", err, tree, stderr)
	}
	commit, stderr, err := g.runLocalWithEnv(ctx, syntheticCommitEnv(), "-C", allocation.Path,
		"commit-tree", strings.TrimSpace(string(tree)),
		"-p", allocation.BaseSHA,
		"-m", fmt.Sprintf("auto-improve: prepare pass%d policy base for %s", allocation.Pass, runID),
	)
	if err != nil {
		return allocation, formatCommandFailure("step10: git commit-tree policy overlay", err, commit, stderr)
	}
	commitSHA := strings.TrimSpace(string(commit))
	if _, stderr, err := g.runLocal(ctx, "-C", allocation.Path, "update-ref", "refs/heads/"+allocation.Branch, commitSHA); err != nil {
		return allocation, formatCommandFailure("step10: git update-ref policy base", err, nil, stderr)
	}
	if _, stderr, err := g.runLocal(ctx, "-C", allocation.Path, "reset", "--hard", commitSHA); err != nil {
		return allocation, formatCommandFailure("step10: git reset policy base", err, nil, stderr)
	}
	allocation.HeadSHA = commitSHA
	return allocation, nil
}

func (g gitCLI) removeWorktreeForce(ctx context.Context, repoRoot, path string) error {
	out, stderr, err := g.runLocal(ctx, "-C", repoRoot, "worktree", "remove", "--force", path)
	if err != nil {
		return formatCommandFailure(fmt.Sprintf("step10: git worktree remove --force %s", path), err, out, stderr)
	}
	return nil
}

func (g gitCLI) currentBranch(ctx context.Context, repoRoot string) (string, error) {
	out, stderr, err := g.runLocal(ctx, "-C", repoRoot, "branch", "--show-current")
	if err != nil {
		return "", formatCommandFailure(fmt.Sprintf("step10: git branch --show-current (in %s)", repoRoot), err, out, stderr)
	}
	return strings.TrimSpace(string(out)), nil
}

func (g gitCLI) worktreeClean(ctx context.Context, repoRoot string) (bool, error) {
	out, stderr, err := g.runLocal(ctx, "-C", repoRoot, "status", "--porcelain", "--ignored")
	if err != nil {
		return false, formatCommandFailure(fmt.Sprintf("step10: git status --porcelain --ignored (in %s)", repoRoot), err, out, stderr)
	}
	return strings.TrimSpace(string(out)) == "", nil
}

func (g gitCLI) ResolveRef(ctx context.Context, repoRoot, ref string) (string, error) {
	out, stderr, err := g.runLocal(ctx, "-C", repoRoot, "rev-parse", ref)
	if err != nil {
		return "", formatCommandFailure(fmt.Sprintf("step10: git rev-parse %s (in %s)", ref, repoRoot), err, out, stderr)
	}
	return strings.TrimSpace(string(out)), nil
}

func (g gitCLI) MergeBase(ctx context.Context, repoRoot, left, right string) (string, error) {
	out, stderr, err := g.runLocal(ctx, "-C", repoRoot, "merge-base", left, right)
	if err != nil {
		return "", formatCommandFailure(fmt.Sprintf("step10: git merge-base %s %s (in %s)", left, right, repoRoot), err, out, stderr)
	}
	return strings.TrimSpace(string(out)), nil
}

func (g gitCLI) FetchCommit(ctx context.Context, repoRoot, sha string) error {
	out, stderr, err := g.runNetwork(ctx, repoRoot, "-C", repoRoot, "fetch", "--no-tags", "origin", sha)
	if err != nil {
		return formatCommandFailure(fmt.Sprintf("step10: git fetch origin %s (in %s)", sha, repoRoot), err, out, stderr)
	}
	return nil
}

func (g gitCLI) FetchBranch(ctx context.Context, repoRoot, branch string) error {
	branch = strings.TrimPrefix(strings.TrimSpace(branch), "refs/heads/")
	refspec := fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", branch, branch)
	out, stderr, err := g.runNetwork(ctx, repoRoot, "-C", repoRoot, "fetch", "--no-tags", "origin", refspec)
	if err != nil {
		return formatCommandFailure(fmt.Sprintf("step10: git fetch origin %s (in %s)", branch, repoRoot), err, out, stderr)
	}
	return nil
}

func (g gitCLI) RepoSlug(ctx context.Context, repoRoot string) (string, error) {
	out, stderr, err := g.runLocal(ctx, "-C", repoRoot, "config", "--get", "remote.origin.url")
	if err != nil {
		return "", formatCommandFailure(fmt.Sprintf("step10: git config --get remote.origin.url (in %s)", repoRoot), err, out, stderr)
	}
	slug, err := repoSlugFromRemoteURL(strings.TrimSpace(string(out)))
	if err != nil {
		return "", fmt.Errorf("step10: resolve repo slug from origin remote (in %s): %w", repoRoot, err)
	}
	return slug, nil
}

func (g gitCLI) ChangedFiles(ctx context.Context, repoRoot, from, to string) ([]string, error) {
	out, stderr, err := g.runLocal(ctx, "-C", repoRoot, "diff", "--no-ext-diff", "--name-only", "--find-renames", from, to, "--")
	if err != nil {
		return nil, formatCommandFailure(fmt.Sprintf("step10: git diff --name-only %s %s (in %s)", from, to, repoRoot), err, out, stderr)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	files := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		files = append(files, line)
	}
	return files, nil
}

func (g gitCLI) Diff(ctx context.Context, repoRoot, from, to string) (string, error) {
	out, stderr, err := g.runLocal(ctx, "-C", repoRoot, "diff", "--no-ext-diff", "--find-renames", "--unified=3", from, to, "--")
	if err != nil {
		return "", formatCommandFailure(fmt.Sprintf("step10: git diff %s %s (in %s)", from, to, repoRoot), err, out, stderr)
	}
	return string(out), nil
}

func (g gitCLI) worktreeBelongsToRepo(ctx context.Context, repoRoot, path string) (bool, error) {
	out, stderr, err := g.runLocal(ctx, "-C", repoRoot, "worktree", "list", "--porcelain")
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
	info, err := gitremote.ParseGitHubRemote(remoteURL, gitremote.AllowedGitHubHostsFromEnv(processenv.SanitizeForNetworkExec()))
	if err != nil {
		return "", err
	}
	return info.Slug, nil
}

func (g gitCLI) runLocal(ctx context.Context, args ...string) ([]byte, []byte, error) {
	if g.run != nil {
		return g.run(ctx, "git", args...)
	}
	return runGitWithEnv(ctx, processenv.GitLocalEnv(), args...)
}

func (g gitCLI) runLocalWithEnv(ctx context.Context, env []string, args ...string) ([]byte, []byte, error) {
	if g.run != nil {
		return g.run(ctx, "git", args...)
	}
	return runGitWithEnv(ctx, env, args...)
}

func (g gitCLI) runNetwork(ctx context.Context, repoRoot string, args ...string) ([]byte, []byte, error) {
	if g.run != nil {
		return g.run(ctx, "git", args...)
	}
	remoteURL, err := g.originRemoteURL(ctx, repoRoot)
	if err != nil {
		return nil, nil, err
	}
	return runGitWithEnv(ctx, processenv.GitNetworkEnvForRemoteURL(remoteURL), args...)
}

func (g gitCLI) originRemoteURL(ctx context.Context, repoRoot string) (string, error) {
	out, stderr, err := g.runLocal(ctx, "-C", repoRoot, "remote", "get-url", "origin")
	if err != nil {
		return "", formatCommandFailure(fmt.Sprintf("step10: git remote get-url origin (in %s)", repoRoot), err, out, stderr)
	}
	return strings.TrimSpace(string(out)), nil
}

func runGitWithEnv(ctx context.Context, env []string, args ...string) ([]byte, []byte, error) {
	cmd, err := processenv.TrustedCommandContext(ctx, "git", args...)
	if err != nil {
		return nil, nil, err
	}
	cmd.Env = env
	stdout, err := cmd.Output()
	if err == nil {
		return stdout, nil, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout, exitErr.Stderr, err
	}
	return stdout, nil, err
}

func syntheticCommitEnv() []string {
	env := processenv.GitLocalEnv()
	env = append(env,
		"GIT_AUTHOR_NAME=auto-improve",
		"GIT_AUTHOR_EMAIL=auto-improve@example.invalid",
		"GIT_COMMITTER_NAME=auto-improve",
		"GIT_COMMITTER_EMAIL=auto-improve@example.invalid",
	)
	return env
}
