package step20_implement

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nishimoto265/harnest/internal/config"
	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
)

func ensureDir(path string) error {
	return internalio.EnsureDirNoFollow(path, 0o700)
}

func ensureAllocationWorktree(ctx context.Context, cfg *config.Config, allocation contracts.WorktreeAllocation) error {
	return ensureAllocationWorktreeAtRef(ctx, cfg, allocation, allocation.HeadSHA, true)
}

func ensureAllocationWorktreeBeforeResume(ctx context.Context, run RunContext, allocation contracts.WorktreeAllocation, agentDir string) (contracts.WorktreeAllocation, error) {
	state, ok, err := loadResumeState(agentDir)
	if err != nil {
		return allocation, err
	}
	if !ok {
		var adopted bool
		allocation, adopted, err = maybeAdoptExistingPolicyOverlayHead(ctx, allocation)
		if err != nil {
			return allocation, err
		}
		if adopted {
			return allocation, ensureAllocationWorktree(ctx, run.Config, allocation)
		}
		return allocation, ensureAllocationWorktree(ctx, run.Config, allocation)
	}
	if state.Pid != 0 {
		if state.ExpectedBaseSHA != "" {
			allocation.BaseSHA = state.ExpectedBaseSHA
			allocation.HeadSHA = state.ExpectedBaseSHA
		}
		return allocation, nil
	}
	if _, statErr := os.Lstat(allocation.Path); statErr != nil {
		if os.IsNotExist(statErr) && state.ExpectedBaseSHA != "" {
			allocation.BaseSHA = state.ExpectedBaseSHA
			allocation.HeadSHA = state.ExpectedBaseSHA
		} else if !os.IsNotExist(statErr) {
			return allocation, statErr
		}
		return allocation, ensureAllocationWorktree(ctx, run.Config, allocation)
	}
	if state.ExpectedBaseSHA != "" {
		allocation.BaseSHA = state.ExpectedBaseSHA
		allocation.HeadSHA = state.ExpectedBaseSHA
	}
	var adopted bool
	allocation, adopted, err = maybeAdoptExistingPolicyOverlayHead(ctx, allocation)
	if err != nil {
		return allocation, err
	}
	if adopted {
		return allocation, ensureAllocationWorktree(ctx, run.Config, allocation)
	}
	return allocation, ensureAllocationWorktree(ctx, run.Config, allocation)
}

func maybeAdoptExistingPolicyOverlayHead(ctx context.Context, allocation contracts.WorktreeAllocation) (contracts.WorktreeAllocation, bool, error) {
	info, err := os.Lstat(allocation.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return allocation, false, nil
		}
		return allocation, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return allocation, false, nil
	}
	before := allocation
	updated, err := adoptExistingPolicyOverlayHead(ctx, allocation)
	if err != nil {
		return allocation, false, err
	}
	adopted := updated.BaseSHA != before.BaseSHA || updated.HeadSHA != before.HeadSHA
	return updated, adopted, nil
}

func ensureAllocationWorktreeForRescue(ctx context.Context, cfg *config.Config, allocation contracts.WorktreeAllocation) error {
	return ensureAllocationWorktreeAtRef(ctx, cfg, allocation, allocation.Branch, false)
}

func ensureAllocationWorktreeAtRef(ctx context.Context, cfg *config.Config, allocation contracts.WorktreeAllocation, ref string, resetBranch bool) error {
	// No-follow Lstat at use time (not just at step10 validation). A symlink
	// could have been swapped in between ValidateWorktreeAllocation and now;
	// os.Stat would follow it and accept an arbitrary target directory.
	if err := internalio.EnsureNoSymlinkPathComponents(allocation.Path); err != nil {
		return fmt.Errorf("step20: worktree path rejected: %w", err)
	}
	info, err := os.Lstat(allocation.Path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("step20: worktree path is a symlink: %s", allocation.Path)
		}
		if !info.IsDir() {
			return fmt.Errorf("step20: worktree path is not a directory: %s", allocation.Path)
		}
		if resetBranch {
			if ref == "" {
				return errors.New("step20: cannot reuse worktree without immutable head_sha")
			}
			return verifyExistingAllocationWorktree(ctx, allocation)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	if cfg == nil {
		return errors.New("step20: config is required to recreate missing worktree")
	}
	if ref == "" {
		if resetBranch {
			return errors.New("step20: cannot recreate worktree without immutable head_sha")
		}
		return errors.New("step20: cannot recreate rescue worktree without allocation branch")
	}
	repoRoot, err := cfg.RepoRoot()
	if err != nil {
		return err
	}
	parent := filepath.Dir(allocation.Path)
	if err := internalio.EnsureNoSymlinkPathComponents(parent); err != nil {
		return fmt.Errorf("step20: worktree parent rejected: %w", err)
	}
	if err := ensureDir(parent); err != nil {
		return err
	}
	if _, err := gitOutputContext(ctx, identity, repoRoot, "worktree", "prune"); err != nil {
		return err
	}
	if resetBranch {
		// Fresh runs pin the recreated worktree to the immutable HeadSHA
		// recorded in the task package rather than trusting the current tip of
		// allocation.Branch.
		if _, err := gitOutputContext(ctx, identity, repoRoot,
			"worktree", "add", "-B", allocation.Branch, allocation.Path, ref); err != nil {
			return err
		}
	} else {
		// Rescue runs must not move allocation.Branch before performRescue has
		// captured commits from the branch's current tip.
		if _, err := gitOutputContext(ctx, identity, repoRoot,
			"worktree", "add", allocation.Path, ref); err != nil {
			return err
		}
	}
	// Re-check symlink components after creation: refuse to continue if the
	// freshly created path or any ancestor was swapped to a symlink mid-setup.
	if err := internalio.EnsureNoSymlinkPathComponents(allocation.Path); err != nil {
		return fmt.Errorf("step20: worktree path swapped after create: %w", err)
	}
	if resetBranch {
		return verifyAllocationHead(ctx, allocation)
	}
	return nil
}

// verifyAllocationHead refuses to continue if the worktree's HEAD does not
// match the immutable allocation.HeadSHA recorded in the task package.
func verifyAllocationHead(ctx context.Context, allocation contracts.WorktreeAllocation) error {
	if allocation.HeadSHA == "" {
		return nil
	}
	head, err := gitOutputContext(ctx, strings.TrimSpace, allocation.Path, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("step20: rev-parse HEAD for allocation %s: %w", allocation.Path, err)
	}
	if head != allocation.HeadSHA {
		return fmt.Errorf("step20: allocation HEAD mismatch: path=%s want=%s got=%s", allocation.Path, allocation.HeadSHA, head)
	}
	return nil
}

func verifyExistingAllocationWorktree(ctx context.Context, allocation contracts.WorktreeAllocation) error {
	currentBranch, err := gitOutputContext(ctx, strings.TrimSpace, allocation.Path, "branch", "--show-current")
	if err != nil {
		return fmt.Errorf("step20: branch --show-current for allocation %s: %w", allocation.Path, err)
	}
	if currentBranch != allocation.Branch {
		return fmt.Errorf("step20: current branch mismatch: path=%s want=%s got=%s", allocation.Path, allocation.Branch, currentBranch)
	}
	if allocation.HeadSHA != "" {
		if _, err := gitOutputContext(ctx, identity, allocation.Path, "merge-base", "--is-ancestor", "HEAD", allocation.HeadSHA); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("step20: allocation HEAD mismatch: path=%s want=%s", allocation.Path, allocation.HeadSHA)
		}
		if _, err := gitOutputContext(ctx, identity, allocation.Path, "merge-base", "--is-ancestor", allocation.HeadSHA, "HEAD"); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("step20: allocation HEAD mismatch: path=%s want=%s", allocation.Path, allocation.HeadSHA)
		}
	}
	statusArgs := append([]string{"status", "--porcelain", "--ignored", "--", "."}, implementationCommitExcludedPathspecs...)
	status, err := gitOutputContext(ctx, strings.TrimSpace, allocation.Path, statusArgs...)
	if err != nil {
		return fmt.Errorf("step20: status --porcelain --ignored for allocation %s: %w", allocation.Path, err)
	}
	if status != "" {
		return fmt.Errorf("step20: existing worktree is dirty: path=%s", allocation.Path)
	}
	return nil
}

func manifestPrefix(_ int, agent contracts.AgentID) string {
	return filepath.Join("20-pass1", string(agent))
}

func worktreeFor(pkg *contracts.TaskPackage, pass int, agent contracts.AgentID) (contracts.WorktreeAllocation, error) {
	if pkg == nil {
		return contracts.WorktreeAllocation{}, errors.New("step20: task package is required")
	}
	for _, worktree := range pkg.Worktrees {
		if worktree.Pass == pass && worktree.Agent == agent {
			return worktree, nil
		}
	}
	return contracts.WorktreeAllocation{}, fmt.Errorf("step20: missing worktree allocation: pass=%d agent=%s", pass, agent)
}

func agentDir(runIO internalio.RunContext, pass int, agent contracts.AgentID) (string, error) {
	return runIO.ResolveRunRelative(manifestPrefix(pass, agent))
}

func stepTimeout(cfg *config.Config, key string) (time.Duration, error) {
	if cfg == nil {
		return 0, errors.New("step20: config is required")
	}
	seconds, ok := cfg.StepTimeouts[key]
	if !ok || seconds <= 0 {
		return 0, fmt.Errorf("step20: missing step timeout: %s", key)
	}
	return time.Duration(seconds) * time.Second, nil
}
