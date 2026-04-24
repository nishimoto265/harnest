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

type errorRemover struct {
	err error
}

func (r errorRemover) RemoveWorktree(context.Context, string) error {
	return r.err
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
