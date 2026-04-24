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
	"github.com/nishimoto265/auto-improve/internal/processenv"
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

func TestCleanupRefusesUnregisteredWorktreeFallbackRemovalWithoutVerification(t *testing.T) {
	runCtx := testRunContext(t)
	wtPath := filepath.Join(runCtx.WorktreeBase, string(runCtx.RunID)+"-pass1-a1")
	require.NoError(t, os.MkdirAll(wtPath, 0o755))
	pkg := contracts.TaskPackage{
		Worktrees: []contracts.WorktreeAllocation{testAllocation(wtPath)},
	}

	err := Cleanup(context.Background(), runCtx, &pkg, errorRemover{err: fmt.Errorf("%w: %s", ErrUnregistered, wtPath)})

	require.ErrorIs(t, err, ErrUnregistered)
	assert.DirExists(t, wtPath)
}

func TestCleanupAllowsVerifiedUnregisteredWorktreeFallbackRemoval(t *testing.T) {
	runCtx := testRunContext(t)
	wtPath := filepath.Join(runCtx.WorktreeBase, string(runCtx.RunID)+"-pass1-a1")
	require.NoError(t, os.MkdirAll(wtPath, 0o755))
	wt := testAllocation(wtPath)
	pkg := contracts.TaskPackage{
		Worktrees: []contracts.WorktreeAllocation{wt},
	}
	remover := &recordingErrorRemover{err: fmt.Errorf("%w: %s", ErrUnregistered, wtPath), verify: true}

	err := Cleanup(context.Background(), runCtx, &pkg, remover)

	require.NoError(t, err)
	assert.NoDirExists(t, wtPath)
	assert.Equal(t, []contracts.WorktreeAllocation{wt}, remover.verifiedAllocations)
}

func TestCleanupDeletesOwnedLocalBranchAfterUnregisteredFallbackRemoval(t *testing.T) {
	runCtx := testRunContext(t)
	wtPath := filepath.Join(runCtx.WorktreeBase, string(runCtx.RunID)+"-pass1-a1")
	require.NoError(t, os.MkdirAll(wtPath, 0o755))
	wt := testAllocation(wtPath)
	wt.Branch = fmt.Sprintf("auto-improve/%s/pass1/a1", runCtx.RunID)
	pkg := contracts.TaskPackage{
		Worktrees: []contracts.WorktreeAllocation{wt},
	}
	remover := &recordingErrorRemover{err: fmt.Errorf("%w: %s", ErrUnregistered, wtPath), verify: true}

	err := Cleanup(context.Background(), runCtx, &pkg, remover)

	require.NoError(t, err)
	assert.NoDirExists(t, wtPath)
	assert.Equal(t, []string{wt.Branch}, remover.deletedBranches)
}

func TestCleanupDoesNotDeleteOwnedBranchAfterUnverifiedMissingUnregisteredWorktree(t *testing.T) {
	runCtx := testRunContext(t)
	wtPath := filepath.Join(runCtx.WorktreeBase, string(runCtx.RunID)+"-pass1-a1")
	wt := testAllocation(wtPath)
	wt.Branch = fmt.Sprintf("auto-improve/%s/pass1/a1", runCtx.RunID)
	pkg := contracts.TaskPackage{
		Worktrees: []contracts.WorktreeAllocation{wt},
	}
	remover := &recordingErrorRemover{err: fmt.Errorf("%w: %s", ErrUnregistered, wtPath)}

	err := Cleanup(context.Background(), runCtx, &pkg, remover)

	require.NoError(t, err)
	assert.Empty(t, remover.deletedBranches)
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

func TestCleanupWithRepoGitDeletesOwnedRegisteredBranch(t *testing.T) {
	repoRoot := newCleanupTestRepo(t)
	runCtx := testRunContext(t)
	require.NoError(t, os.MkdirAll(runCtx.WorktreeBase, 0o755))
	wtPath := filepath.Join(runCtx.WorktreeBase, string(runCtx.RunID)+"-pass1-a1")
	wt := testAllocation(wtPath)
	wt.Branch = fmt.Sprintf("auto-improve/%s/pass1/a1", runCtx.RunID)
	pkg := contracts.TaskPackage{
		Worktrees: []contracts.WorktreeAllocation{wt},
	}
	cleanupRunGit(t, repoRoot, "worktree", "add", "-b", wt.Branch, wt.Path, "HEAD")

	err := Cleanup(context.Background(), runCtx, &pkg, RepoGit{RepoDir: repoRoot})

	require.NoError(t, err)
	assert.NoDirExists(t, wt.Path)
	assertBranchMissing(t, repoRoot, wt.Branch)
}

func TestCleanupWithRepoGitKeepsForeignRegisteredBranch(t *testing.T) {
	repoRoot := newCleanupTestRepo(t)
	runCtx := testRunContext(t)
	require.NoError(t, os.MkdirAll(runCtx.WorktreeBase, 0o755))
	wtPath := filepath.Join(runCtx.WorktreeBase, string(runCtx.RunID)+"-pass1-a1")
	wt := testAllocation(wtPath)
	wt.Branch = "feature/user-owned"
	pkg := contracts.TaskPackage{
		Worktrees: []contracts.WorktreeAllocation{wt},
	}
	cleanupRunGit(t, repoRoot, "worktree", "add", "-b", wt.Branch, wt.Path, "HEAD")

	err := Cleanup(context.Background(), runCtx, &pkg, RepoGit{RepoDir: repoRoot})

	require.NoError(t, err)
	assert.NoDirExists(t, wt.Path)
	assertBranchExists(t, repoRoot, wt.Branch)
}

type errorRemover struct {
	err error
}

func (r errorRemover) RemoveWorktree(context.Context, string) error {
	return r.err
}

type recordingErrorRemover struct {
	err                 error
	verify              bool
	deletedBranches     []string
	verifiedAllocations []contracts.WorktreeAllocation
}

func (r *recordingErrorRemover) RemoveWorktree(context.Context, string) error {
	return r.err
}

func (r *recordingErrorRemover) DeleteBranch(_ context.Context, branch string) error {
	r.deletedBranches = append(r.deletedBranches, branch)
	return nil
}

func (r *recordingErrorRemover) VerifyUnregisteredWorktreeRemoval(_ context.Context, allocation contracts.WorktreeAllocation) error {
	if !r.verify {
		return fmt.Errorf("%w: verification disabled", ErrUnregistered)
	}
	r.verifiedAllocations = append(r.verifiedAllocations, allocation)
	return nil
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

func newCleanupTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := processenv.TrustedLookPath("git"); err != nil {
		t.Skipf("git not available in trusted PATH: %v", err)
	}
	repoRoot := filepath.Join(t.TempDir(), "repo")
	cleanupRunGit(t, "", "init", "-b", "main", repoRoot)
	cleanupRunGit(t, repoRoot, "config", "user.email", "auto-improve@example.test")
	cleanupRunGit(t, repoRoot, "config", "user.name", "auto-improve test")
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte("fixture\n"), 0o644))
	cleanupRunGit(t, repoRoot, "add", "README.md")
	cleanupRunGit(t, repoRoot, "commit", "-m", "initial")
	return repoRoot
}

func assertBranchExists(t *testing.T, repoRoot, branch string) {
	t.Helper()
	cleanupRunGit(t, repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
}

func assertBranchMissing(t *testing.T, repoRoot, branch string) {
	t.Helper()
	cmdArgs := []string{"-C", repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/" + branch}
	cmd, err := processenv.TrustedCommand("git", cmdArgs...)
	require.NoError(t, err)
	cmd.Env = processenv.SanitizeForLocalExec()
	err = cmd.Run()
	require.Error(t, err)
}

func cleanupRunGit(t *testing.T, repoRoot string, args ...string) string {
	t.Helper()
	cmdArgs := args
	if repoRoot != "" {
		cmdArgs = append([]string{"-C", repoRoot}, args...)
	}
	cmd, err := processenv.TrustedCommand("git", cmdArgs...)
	require.NoError(t, err)
	cmd.Env = processenv.SanitizeForLocalExec()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return string(out)
}
