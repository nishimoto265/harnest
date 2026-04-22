package step50_implement

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnsureAllocationWorktree_RecreatesToImmutableHeadSHA verifies that when
// a missing worktree is recreated, ensureAllocationWorktree checks out the
// immutable allocation.HeadSHA recorded in the task package — even if the
// branch has been advanced behind its back.
func TestEnsureAllocationWorktree_RecreatesToImmutableHeadSHA(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)

	allocation, err := worktreeFor(env.run.TaskPackage, 2, "a1")
	require.NoError(t, err)
	branch := allocation.Branch
	immutableHead := allocation.HeadSHA

	// Advance the branch behind the allocation's back, then remove the
	// worktree so ensureAllocationWorktree must recreate.
	runCommand(t, allocation.Path, "git", "commit", "--allow-empty", "-m", "post-step10 drift")
	driftHead := strings.TrimSpace(runCommand(t, allocation.Path, "git", "rev-parse", "HEAD"))
	require.NotEqual(t, immutableHead, driftHead)
	require.NoError(t, os.RemoveAll(allocation.Path))
	runCommand(t, env.repoDir, "git", "worktree", "prune")

	require.NoError(t, ensureAllocationWorktree(context.Background(), env.run.Config, allocation))

	restoredHead := strings.TrimSpace(runCommand(t, allocation.Path, "git", "rev-parse", "HEAD"))
	assert.Equal(t, immutableHead, restoredHead, "recreated worktree must sit at immutable HeadSHA")
	restoredBranch := strings.TrimSpace(runCommand(t, allocation.Path, "git", "branch", "--show-current"))
	assert.Equal(t, branch, restoredBranch, "branch must be reset to allocation.Branch at HeadSHA")
}

// TestEnsureAllocationWorktree_RejectsSymlinkedPath verifies that when the
// allocation path was swapped for a symlink between ValidateWorktreeAllocation
// and ensureAllocationWorktree, the latter refuses to continue rather than
// following the link to an attacker-controlled directory.
func TestEnsureAllocationWorktree_RejectsSymlinkedPath(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	allocation, err := worktreeFor(env.run.TaskPackage, 2, "a2")
	require.NoError(t, err)
	// Ensure the allocation path does not exist (its agent was never carved
	// by newStepTestEnv; only a1 has a real worktree).
	require.NoError(t, os.RemoveAll(allocation.Path))

	victim := filepath.Join(filepath.Dir(allocation.Path), "symlink-target")
	require.NoError(t, os.MkdirAll(victim, 0o755))
	require.NoError(t, os.Symlink(victim, allocation.Path))

	err = ensureAllocationWorktree(context.Background(), env.run.Config, allocation)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink", "error must mention symlink rejection: %v", err)
}

// TestEnsureAllocationWorktree_MissingHeadSHAFailsClosed verifies that the
// recreation path refuses to run without the immutable HeadSHA metadata.
func TestEnsureAllocationWorktree_MissingHeadSHAFailsClosed(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	allocation, err := worktreeFor(env.run.TaskPackage, 2, "a3")
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(allocation.Path))
	allocation.HeadSHA = "" // strip immutable head metadata

	cfg := &config.Config{
		Repo:             env.run.Config.Repo,
		RunsBasePath:     env.run.Config.RunsBasePath,
		WorktreeBasePath: env.run.Config.WorktreeBasePath,
	}
	err = ensureAllocationWorktree(context.Background(), cfg, allocation)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "head_sha")
}

// TestEnsureAllocationWorktree_ExistingDirAcceptedEvenIfHeadAdvanced verifies
// that an existing worktree dir is trusted without forcing HEAD back to
// allocation.HeadSHA — the agent may have legitimately advanced HEAD via an
// earlier successful attempt.
func TestEnsureAllocationWorktree_ExistingDirAcceptedEvenIfHeadAdvanced(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	allocation, err := worktreeFor(env.run.TaskPackage, 2, "a1")
	require.NoError(t, err)
	runCommand(t, allocation.Path, "git", "commit", "--allow-empty", "-m", "legitimate agent progress")

	require.NoError(t, ensureAllocationWorktree(context.Background(), env.run.Config, allocation))

	// Current HEAD should remain the advanced one — we must not rewind.
	advancedHead := strings.TrimSpace(runCommand(t, allocation.Path, "git", "rev-parse", "HEAD"))
	assert.NotEqual(t, allocation.HeadSHA, advancedHead)
}

