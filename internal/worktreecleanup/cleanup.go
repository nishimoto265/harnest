package worktreecleanup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/processenv"
)

var (
	ErrUnregistered   = errors.New("worktreecleanup: git worktree path is not registered")
	ErrRepoUnverified = errors.New("worktreecleanup: repo root could not be verified")
)

type Remover interface {
	RemoveWorktree(ctx context.Context, path string) error
}

type BranchRemover interface {
	DeleteBranch(ctx context.Context, branch string) error
}

type UnregisteredWorktreeVerifier interface {
	VerifyUnregisteredWorktreeRemoval(ctx context.Context, allocation contracts.WorktreeAllocation) error
}

type RepoGit struct {
	RepoDir string
}

func Cleanup(ctx context.Context, runCtx internalio.RunContext, pkg *contracts.TaskPackage, remover Remover) error {
	if pkg == nil {
		return nil
	}
	for _, wt := range pkg.Worktrees {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := runCtx.ValidateWorktreeAllocation(wt); err != nil {
			return err
		}
		path := filepath.Clean(wt.Path)
		cleanedWorktree := false
		branchCleanupAllowed := true
		var unregisteredErr error
		if remover != nil {
			if err := remover.RemoveWorktree(ctx, path); err != nil {
				if errors.Is(err, ErrUnregistered) {
					unregisteredErr = err
					branchCleanupAllowed = false
				} else if !os.IsNotExist(err) {
					return err
				}
			} else {
				cleanedWorktree = true
			}
		}
		if _, err := os.Lstat(path); err == nil {
			if unregisteredErr != nil {
				verifier, ok := remover.(UnregisteredWorktreeVerifier)
				if !ok {
					return unregisteredErr
				}
				if err := verifier.VerifyUnregisteredWorktreeRemoval(ctx, wt); err != nil {
					return err
				}
				branchCleanupAllowed = true
			}
			if err := os.RemoveAll(path); err != nil {
				return err
			}
			cleanedWorktree = true
		} else if !os.IsNotExist(err) {
			return err
		} else {
			cleanedWorktree = true
		}
		if branchRemover, ok := remover.(BranchRemover); ok && cleanedWorktree && branchCleanupAllowed && cleanupOwnsBranch(runCtx.RunID, wt) {
			if err := branchRemover.DeleteBranch(ctx, wt.Branch); err != nil {
				return err
			}
		}
	}
	for _, base := range pkg.PassBases {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := runCtx.ValidatePassBaseAllocation(base); err != nil {
			return err
		}
		wt := contracts.WorktreeAllocation{
			Agent:   "a1",
			Pass:    base.Pass,
			Path:    base.Path,
			Branch:  base.Branch,
			BaseSHA: base.BaseSHA,
			HeadSHA: base.HeadSHA,
		}
		if err := cleanupOne(ctx, runCtx, wt, remover); err != nil {
			return err
		}
	}
	return nil
}

func cleanupOne(ctx context.Context, runCtx internalio.RunContext, wt contracts.WorktreeAllocation, remover Remover) error {
	path := filepath.Clean(wt.Path)
	cleanedWorktree := false
	branchCleanupAllowed := true
	var unregisteredErr error
	if remover != nil {
		if err := remover.RemoveWorktree(ctx, path); err != nil {
			if errors.Is(err, ErrUnregistered) {
				unregisteredErr = err
				branchCleanupAllowed = false
			} else if !os.IsNotExist(err) {
				return err
			}
		} else {
			cleanedWorktree = true
		}
	}
	if _, err := os.Lstat(path); err == nil {
		if unregisteredErr != nil {
			verifier, ok := remover.(UnregisteredWorktreeVerifier)
			if !ok {
				return unregisteredErr
			}
			if err := verifier.VerifyUnregisteredWorktreeRemoval(ctx, wt); err != nil {
				return err
			}
			branchCleanupAllowed = true
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		cleanedWorktree = true
	} else if !os.IsNotExist(err) {
		return err
	} else {
		cleanedWorktree = true
	}
	if branchRemover, ok := remover.(BranchRemover); ok && cleanedWorktree && branchCleanupAllowed && cleanupOwnsBranch(runCtx.RunID, wt) {
		if err := branchRemover.DeleteBranch(ctx, wt.Branch); err != nil {
			return err
		}
	}
	return nil
}

func (g RepoGit) RemoveWorktree(ctx context.Context, path string) error {
	if strings.TrimSpace(g.RepoDir) == "" {
		return fmt.Errorf("%w: empty repo root", ErrRepoUnverified)
	}
	if _, err := os.Stat(filepath.Join(g.RepoDir, ".git")); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrRepoUnverified, g.RepoDir)
		}
		return err
	}
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
		return fmt.Errorf("%w: %s", ErrUnregistered, path)
	}
	if err := g.run(ctx, "worktree", "remove", "--force", path); err != nil {
		if pruneErr := g.pruneMissing(ctx, path); pruneErr == nil {
			return nil
		}
		return err
	}
	return nil
}

func (g RepoGit) DeleteBranch(ctx context.Context, branch string) error {
	if strings.TrimSpace(g.RepoDir) == "" {
		return fmt.Errorf("%w: empty repo root", ErrRepoUnverified)
	}
	if _, err := os.Stat(filepath.Join(g.RepoDir, ".git")); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrRepoUnverified, g.RepoDir)
		}
		return err
	}
	if err := g.run(ctx, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		return nil
	}
	return g.run(ctx, "branch", "-D", branch)
}

func (g RepoGit) VerifyUnregisteredWorktreeRemoval(ctx context.Context, allocation contracts.WorktreeAllocation) error {
	if strings.TrimSpace(g.RepoDir) == "" {
		return fmt.Errorf("%w: empty repo root", ErrRepoUnverified)
	}
	if err := allocation.Validate(); err != nil {
		return err
	}
	path := filepath.Clean(allocation.Path)
	commonDir, err := g.commonGitDir(ctx, path)
	if err != nil {
		return fmt.Errorf("%w: unregistered path is not a valid git worktree for cleanup: %s: %v", ErrUnregistered, path, err)
	}
	repoCommonDir, err := g.commonGitDir(ctx, g.RepoDir)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrRepoUnverified, g.RepoDir, err)
	}
	if !sameCanonicalPath(commonDir, repoCommonDir) {
		return fmt.Errorf("%w: unregistered path belongs to different repo: %s", ErrUnregistered, path)
	}
	branch, err := g.outputInDir(ctx, path, "branch", "--show-current")
	if err != nil {
		return fmt.Errorf("%w: unregistered path branch could not be verified: %s: %v", ErrUnregistered, path, err)
	}
	if strings.TrimSpace(string(branch)) != allocation.Branch {
		return fmt.Errorf("%w: unregistered path branch mismatch: path=%s want=%s", ErrUnregistered, path, allocation.Branch)
	}
	return nil
}

func cleanupOwnsBranch(runID contracts.RunID, wt contracts.WorktreeAllocation) bool {
	want := fmt.Sprintf("auto-improve/%s/pass%d/%s", runID, wt.Pass, wt.Agent)
	baseWant := fmt.Sprintf("auto-improve/%s/pass%d/base", runID, wt.Pass)
	return wt.Branch == want || wt.Branch == baseWant
}

func (g RepoGit) pruneMissing(ctx context.Context, path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("worktreecleanup: registered git worktree still exists after failed remove: %s", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := g.run(ctx, "worktree", "prune"); err != nil {
		return err
	}
	registered, err := g.worktreeBelongsToRepo(ctx, path)
	if err != nil {
		return err
	}
	if registered {
		return fmt.Errorf("worktreecleanup: git worktree prune left stale registration: %s", path)
	}
	return nil
}

func (g RepoGit) worktreeBelongsToRepo(ctx context.Context, path string) (bool, error) {
	out, err := g.output(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	want, err := contracts.CanonicalizePathForUniqueness(filepath.Clean(path))
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(out), "\n") {
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

func (g RepoGit) run(ctx context.Context, args ...string) error {
	_, err := g.output(ctx, args...)
	return err
}

func (g RepoGit) output(ctx context.Context, args ...string) ([]byte, error) {
	return g.outputInDir(ctx, g.RepoDir, args...)
}

func (g RepoGit) outputInDir(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmdArgs := append([]string{"-C", dir}, args...)
	cmd, err := processenv.TrustedCommandContext(ctx, "git", cmdArgs...)
	if err != nil {
		return nil, err
	}
	cmd.Env = processenv.GitLocalEnv()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		msgs := make([]string, 0, 2)
		if out := strings.TrimSpace(stdout.String()); out != "" {
			msgs = append(msgs, "stdout="+out)
		}
		if out := strings.TrimSpace(stderr.String()); out != "" {
			msgs = append(msgs, "stderr="+out)
		}
		if len(msgs) == 0 {
			return nil, fmt.Errorf("worktreecleanup: git %s: %w", strings.Join(cmdArgs, " "), err)
		}
		return nil, fmt.Errorf("worktreecleanup: git %s: %w: %s", strings.Join(cmdArgs, " "), err, strings.Join(msgs, "; "))
	}
	return stdout.Bytes(), nil
}

func (g RepoGit) commonGitDir(ctx context.Context, dir string) (string, error) {
	out, err := g.outputInDir(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	gitDir := strings.TrimSpace(string(out))
	if gitDir == "" {
		return "", errors.New("empty git common dir")
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	return filepath.Clean(gitDir), nil
}

func sameCanonicalPath(a, b string) bool {
	aKey, aErr := contracts.CanonicalizePathForUniqueness(filepath.Clean(a))
	bKey, bErr := contracts.CanonicalizePathForUniqueness(filepath.Clean(b))
	if aErr != nil || bErr != nil {
		return false
	}
	return aKey == bKey
}
