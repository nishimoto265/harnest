package step20_implement

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ensureAllocationWorktreeEnv sets up a real repo + worktree pair suitable for
// exercising ensureAllocationWorktree's recreation path. Unlike the default
// step20 testFixture (standalone repo-as-worktree), this one uses a proper
// `git worktree add` so we can exercise the git plumbing behaviour.
type ensureEnv struct {
	cfg          *config.Config
	runCtx       RunContext
	repoDir      string
	worktreeBase string
	taskPackage  contracts.TaskPackage
}

func newEnsureEnv(t *testing.T) *ensureEnv {
	t.Helper()

	root := t.TempDir()
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	repoDir := filepath.Join(root, "repo")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	require.NoError(t, os.MkdirAll(repoDir, 0o755))

	runGit(t, repoDir, "init", "-b", "main")
	runGit(t, repoDir, "config", "user.name", "Test User")
	runGit(t, repoDir, "config", "user.email", "test@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644))
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "base")
	baseSHA := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))

	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	worktreePath := filepath.Join(worktreeBase, string(runID)+"-pass1-a1")
	branch := "auto-improve/" + string(runID) + "/pass1/a1"
	runGit(t, repoDir, "worktree", "add", "-b", branch, worktreePath, baseSHA)

	runIO, err := internalio.NewRunContext(runID, runsBase, worktreeBase)
	require.NoError(t, err)
	pkg := buildTaskPackage(t, runID, worktreeBase, worktreePath, baseSHA)

	cfg := &config.Config{
		Repo: config.RepoConfig{
			Root:          repoDir,
			DefaultBranch: "main",
			BestBranch:    "best",
		},
		Worktree: config.WorktreeConfig{Base: worktreeBase},
		Paths:    config.PathsConfig{Runs: runsBase},
		StepTimeouts: map[string]int{
			"step20": 30,
		},
	}
	run := RunContext{
		Config:      cfg,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		PR:          42,
		Pass:        1,
		Agent:       "a1",
		IO:          runIO,
		TaskPackage: &pkg,
	}
	return &ensureEnv{
		cfg:          cfg,
		runCtx:       run,
		repoDir:      repoDir,
		worktreeBase: worktreeBase,
		taskPackage:  pkg,
	}
}

// TestEnsureAllocationWorktree_Step20_RecreatesToImmutableHeadSHA verifies F7:
// when a missing worktree is recreated, ensureAllocationWorktree pins HEAD to
// the immutable allocation.HeadSHA even if allocation.Branch has advanced in
// the meantime (e.g. by a prior attempt or manual edit). Without this, step20
// would run against the wrong tree and the BaseSHA-anchored rescue/diff
// invariant would silently break.
func TestEnsureAllocationWorktree_Step20_RecreatesToImmutableHeadSHA(t *testing.T) {
	env := newEnsureEnv(t)
	allocation, err := worktreeFor(&env.taskPackage, 1, "a1")
	require.NoError(t, err)
	immutableHead := allocation.HeadSHA

	// Advance the branch past the allocation's immutable HeadSHA, then delete
	// the worktree so ensureAllocationWorktree must recreate.
	runGit(t, allocation.Path, "commit", "--allow-empty", "-m", "post-step10 drift")
	driftHead := strings.TrimSpace(runGit(t, allocation.Path, "rev-parse", "HEAD"))
	require.NotEqual(t, immutableHead, driftHead)
	require.NoError(t, os.RemoveAll(allocation.Path))
	runGit(t, env.repoDir, "worktree", "prune")

	require.NoError(t, ensureAllocationWorktree(context.Background(), env.cfg, allocation))

	restored := strings.TrimSpace(runGit(t, allocation.Path, "rev-parse", "HEAD"))
	assert.Equal(t, immutableHead, restored, "recreated worktree must sit at immutable HeadSHA")
	restoredBranch := strings.TrimSpace(runGit(t, allocation.Path, "branch", "--show-current"))
	assert.Equal(t, allocation.Branch, restoredBranch, "branch must be reset to allocation.Branch at HeadSHA")
}

// TestEnsureAllocationWorktree_Step20_RejectsSymlinkedPath verifies F13: if
// the allocation path was swapped for a symlink between the upfront
// ValidateWorktreeAllocation and ensureAllocationWorktree, os.Lstat catches
// it and refuses to continue instead of following the link.
func TestEnsureAllocationWorktree_Step20_RejectsSymlinkedPath(t *testing.T) {
	env := newEnsureEnv(t)
	allocation, err := worktreeFor(&env.taskPackage, 1, "a2")
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(allocation.Path))

	victim := filepath.Join(env.worktreeBase, "symlink-victim")
	require.NoError(t, os.MkdirAll(victim, 0o755))
	require.NoError(t, os.Symlink(victim, allocation.Path))

	err = ensureAllocationWorktree(context.Background(), env.cfg, allocation)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")
}

// TestEnsureAllocationWorktree_Step20_MissingHeadSHAFailsClosed verifies that
// the recreation path refuses to run without the immutable HeadSHA metadata.
func TestEnsureAllocationWorktree_Step20_MissingHeadSHAFailsClosed(t *testing.T) {
	env := newEnsureEnv(t)
	allocation, err := worktreeFor(&env.taskPackage, 1, "a3")
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(allocation.Path))
	allocation.HeadSHA = ""

	err = ensureAllocationWorktree(context.Background(), env.cfg, allocation)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "head_sha")
}

// TestEnsureAllocationWorktree_Step20_ExistingDirWithAdvancedHeadFailsClosed
// verifies that a stale pre-existing worktree dir is rejected before a fresh
// inactive run starts, so prior commits cannot contaminate pass1 output.
func TestEnsureAllocationWorktree_Step20_ExistingDirWithAdvancedHeadFailsClosed(t *testing.T) {
	env := newEnsureEnv(t)
	allocation, err := worktreeFor(&env.taskPackage, 1, "a1")
	require.NoError(t, err)
	runGit(t, allocation.Path, "commit", "--allow-empty", "-m", "legitimate agent progress")

	err = ensureAllocationWorktree(context.Background(), env.cfg, allocation)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allocation HEAD mismatch")
}
