package step50_implement

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
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

// TestEnsureAllocationWorktree_ExistingDirWithAdvancedHeadFailsClosed verifies
// that a stale pre-existing worktree dir is rejected before a fresh inactive
// run starts, so prior commits cannot contaminate pass2 output.
func TestEnsureAllocationWorktree_ExistingDirWithAdvancedHeadFailsClosed(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	allocation, err := worktreeFor(env.run.TaskPackage, 2, "a1")
	require.NoError(t, err)
	runCommand(t, allocation.Path, "git", "commit", "--allow-empty", "-m", "legitimate agent progress")

	err = ensureAllocationWorktree(context.Background(), env.run.Config, allocation)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allocation HEAD mismatch")
}

func TestEnsureAllocationWorktreeBeforeResume_AdoptsPolicyOnlyOverlayHeadWithoutState(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	allocation, err := worktreeFor(env.run.TaskPackage, 2, "a1")
	require.NoError(t, err)
	agentDir, err := agentDir(env.run.IO, 2, "a1")
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(filepath.Join(allocation.Path, ".auto-improve", "lessons"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(allocation.Path, ".auto-improve", "lessons", "seed.md"), []byte("lesson\n"), 0o644))
	runCommand(t, allocation.Path, "git", "add", ".auto-improve")
	runCommand(t, allocation.Path, "git", "commit", "-m", "policy overlay")
	overlayHead := strings.TrimSpace(runCommand(t, allocation.Path, "git", "rev-parse", "HEAD"))

	updated, err := ensureAllocationWorktreeBeforeResume(context.Background(), env.run, allocation, agentDir)
	require.NoError(t, err)
	assert.Equal(t, overlayHead, updated.BaseSHA)
	assert.Equal(t, overlayHead, updated.HeadSHA)
}

func TestEnsurePreparedPass2AllocationDoesNotVerifyAgentWorktree(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	allocation, err := worktreeFor(env.run.TaskPackage, 2, "a1")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(allocation.Path, "dirty.txt"), []byte("in-progress\n"), 0o644))
	allocationA2, err := worktreeFor(env.run.TaskPackage, 2, "a2")
	require.NoError(t, err)
	allocationA3, err := worktreeFor(env.run.TaskPackage, 2, "a3")
	require.NoError(t, err)
	require.NoDirExists(t, allocationA2.Path)
	require.NoDirExists(t, allocationA3.Path)

	baseSHA := env.run.TaskPackage.BaseSHA
	runID := string(env.run.TaskPackage.RunID)
	env.run.TaskPackage.PassBases = []contracts.PassBaseAllocation{
		{Pass: 1, Path: filepath.Join(env.run.IO.WorktreeBase, runID+"-pass1-base"), Branch: "test/pass1/base", BaseSHA: baseSHA, HeadSHA: baseSHA},
		{Pass: 2, Path: filepath.Join(env.run.IO.WorktreeBase, runID+"-pass2-base"), Branch: "test/pass2/base", BaseSHA: baseSHA, HeadSHA: baseSHA},
	}

	updated, err := (&Step{}).ensurePreparedPass2Allocation(context.Background(), env.run, allocation, nil, nil)
	require.NoError(t, err)
	assert.NotEqual(t, baseSHA, updated.HeadSHA)
	assert.FileExists(t, filepath.Join(allocation.Path, "dirty.txt"))
	assert.DirExists(t, allocationA2.Path)
	assert.DirExists(t, allocationA3.Path)
	assert.Equal(t, updated.HeadSHA, strings.TrimSpace(runCommand(t, allocationA2.Path, "git", "rev-parse", "HEAD")))
	assert.Equal(t, updated.HeadSHA, strings.TrimSpace(runCommand(t, allocationA3.Path, "git", "rev-parse", "HEAD")))
}

func TestCommitPolicyOverlayBase_RejectsAdvancedImplementationHead(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	allocation, err := worktreeFor(env.run.TaskPackage, 2, "a1")
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(allocation.Path, "implemented.txt"), []byte("implementation\n"), 0o644))
	runCommand(t, allocation.Path, "git", "add", "implemented.txt")
	runCommand(t, allocation.Path, "git", "commit", "-m", "implementation")

	_, err = commitPolicyOverlayBase(context.Background(), allocation, env.run.TaskPackage.RunID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "advanced implementation head")
}

func TestCommitPolicyOverlayBase_UnstagesPreStagedRepoPolicyArtifacts(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	allocation, err := worktreeFor(env.run.TaskPackage, 2, "a1")
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(filepath.Join(allocation.Path, ".auto-improve", "lessons"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(allocation.Path, "auto-improve", "rules"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(allocation.Path, ".auto-improve", "lessons", "overlay.md"), []byte("overlay\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(allocation.Path, "auto-improve", "rules-registry.jsonl"), []byte("{}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(allocation.Path, "auto-improve", "rules", "r.md"), []byte("rule\n"), 0o644))
	runCommand(t, allocation.Path, "git", "add", "-A")

	updated, err := commitPolicyOverlayBase(context.Background(), allocation, env.run.TaskPackage.RunID)
	require.NoError(t, err)

	files := runCommand(t, allocation.Path, "git", "diff-tree", "--no-commit-id", "--name-only", "-r", updated.BaseSHA)
	assert.Contains(t, files, ".auto-improve/lessons/overlay.md")
	assert.NotContains(t, files, "auto-improve/rules-registry.jsonl")
	assert.NotContains(t, files, "auto-improve/rules/r.md")
}
