package worktreecleanup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCleanupReturnsRepoUnverifiedWithoutRemovingWorktree(t *testing.T) {
	runCtx := testRunContext(t)
	wtPath := filepath.Join(runCtx.WorktreeBase, string(runCtx.RunID)+"-pass1-a1")
	require.NoError(t, os.MkdirAll(wtPath, 0o755))
	pkg := contracts.TaskPackage{
		Worktrees: []contracts.WorktreeAllocation{testAllocation(wtPath)},
	}

	err := Cleanup(context.Background(), runCtx, &pkg, RepoGit{RepoDir: filepath.Join(t.TempDir(), "missing-repo")})

	require.ErrorIs(t, err, ErrRepoUnverified)
	assert.DirExists(t, wtPath)
}

func TestCleanupAllowsUnregisteredWorktreeFallbackRemoval(t *testing.T) {
	runCtx := testRunContext(t)
	wtPath := filepath.Join(runCtx.WorktreeBase, string(runCtx.RunID)+"-pass1-a1")
	require.NoError(t, os.MkdirAll(wtPath, 0o755))
	pkg := contracts.TaskPackage{
		Worktrees: []contracts.WorktreeAllocation{testAllocation(wtPath)},
	}

	err := Cleanup(context.Background(), runCtx, &pkg, errorRemover{err: fmt.Errorf("%w: %s", ErrUnregistered, wtPath)})

	require.NoError(t, err)
	assert.NoDirExists(t, wtPath)
}

func TestCleanupDeletesOwnedLocalBranchAfterGitWorktreeRemoval(t *testing.T) {
	runCtx := testRunContext(t)
	wtPath := filepath.Join(runCtx.WorktreeBase, string(runCtx.RunID)+"-pass1-a1")
	require.NoError(t, os.MkdirAll(wtPath, 0o755))
	wt := testAllocation(wtPath)
	wt.Branch = fmt.Sprintf("auto-improve/%s/pass1/a1", runCtx.RunID)
	pkg := contracts.TaskPackage{
		Worktrees: []contracts.WorktreeAllocation{wt},
	}
	remover := &recordingRemover{}

	err := Cleanup(context.Background(), runCtx, &pkg, remover)

	require.NoError(t, err)
	assert.Equal(t, []string{wtPath}, remover.removedWorktrees)
	assert.Equal(t, []string{wt.Branch}, remover.deletedBranches)
}

func TestCleanupKeepsForeignBranch(t *testing.T) {
	runCtx := testRunContext(t)
	wtPath := filepath.Join(runCtx.WorktreeBase, string(runCtx.RunID)+"-pass1-a1")
	require.NoError(t, os.MkdirAll(wtPath, 0o755))
	wt := testAllocation(wtPath)
	wt.Branch = "feature/user-owned"
	pkg := contracts.TaskPackage{
		Worktrees: []contracts.WorktreeAllocation{wt},
	}
	remover := &recordingRemover{}

	err := Cleanup(context.Background(), runCtx, &pkg, remover)

	require.NoError(t, err)
	assert.Equal(t, []string{wtPath}, remover.removedWorktrees)
	assert.Empty(t, remover.deletedBranches)
}

type errorRemover struct {
	err error
}

func (r errorRemover) RemoveWorktree(context.Context, string) error {
	return r.err
}

type recordingRemover struct {
	removedWorktrees []string
	deletedBranches  []string
}

func (r *recordingRemover) RemoveWorktree(_ context.Context, path string) error {
	r.removedWorktrees = append(r.removedWorktrees, path)
	return nil
}

func (r *recordingRemover) DeleteBranch(_ context.Context, branch string) error {
	r.deletedBranches = append(r.deletedBranches, branch)
	return nil
}

func testRunContext(t *testing.T) internalio.RunContext {
	t.Helper()
	runCtx, err := internalio.NewRunContext(
		contracts.RunID("2026-04-21-PR1-abcdef0"),
		filepath.Join(t.TempDir(), "runs"),
		filepath.Join(t.TempDir(), "worktrees"),
	)
	require.NoError(t, err)
	return runCtx
}

func testAllocation(path string) contracts.WorktreeAllocation {
	return contracts.WorktreeAllocation{
		Agent:   "a1",
		Pass:    1,
		Path:    path,
		Branch:  "test/pass1/a1",
		BaseSHA: strings.Repeat("a", 40),
		HeadSHA: strings.Repeat("a", 40),
	}
}
