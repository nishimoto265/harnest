package step10restorebase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitCLIResolveRef_IgnoresStderrWhenStdoutValid(t *testing.T) {
	git := gitCLI{
		run: func(ctx context.Context, name string, args ...string) (cmdResult, error) {
			return cmdResult{
				stdout: []byte(testBaseSHA + "\n"),
				stderr: []byte("xcrun: warning: using legacy toolchain\n"),
			}, nil
		},
	}

	sha, err := git.ResolveRef(context.Background(), t.TempDir(), "HEAD")
	require.NoError(t, err)
	assert.Equal(t, testBaseSHA, sha)
}

func TestGitCLIWorktreeAdd_RetryBranchExisting_CleanupFailurePreservesDrift(t *testing.T) {
	repoRoot := t.TempDir()
	path := filepath.Join(t.TempDir(), "worktree")
	branch := "auto-improve/run/pass1/a1"

	git := gitCLI{
		stat: os.Stat,
		run: func(ctx context.Context, name string, args ...string) (cmdResult, error) {
			switch {
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "add", "-b", branch, path, testBaseSHA}):
				return cmdResult{stderr: []byte("fatal: a branch named '" + branch + "' already exists\n")}, errors.New("exit status 128")
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "add", path, branch}):
				return cmdResult{stdout: []byte("Preparing worktree\n")}, nil
			case slices.Equal(args, []string{"-C", path, "rev-parse", "HEAD"}):
				return cmdResult{stdout: []byte(testBaseRefOID + "\n")}, nil
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "remove", "--force", path}):
				return cmdResult{stderr: []byte("fatal: remove failed\n")}, errors.New("exit status 128")
			default:
				return cmdResult{}, fmt.Errorf("unexpected git args: %v", args)
			}
		},
	}

	created, err := git.WorktreeAdd(context.Background(), repoRoot, path, branch, testBaseSHA)
	require.Error(t, err)
	assert.False(t, created)
	assert.ErrorIs(t, err, ErrWorktreeDrift)
	assert.Contains(t, err.Error(), "cleanup failed")
	assert.Contains(t, err.Error(), "remove failed")
}
