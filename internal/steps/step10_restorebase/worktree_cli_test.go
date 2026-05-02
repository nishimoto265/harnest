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

func TestRun_WorktreeRetryDriftPropagates(t *testing.T) {
	rc := newRunCtx(t)
	repoRoot := t.TempDir()
	basePath := filepath.Join(rc.WorktreeBase, fmt.Sprintf("%s-pass1-base", rc.RunID))
	baseBranch := fmt.Sprintf("auto-improve/%s/pass1/base", rc.RunID)
	firstPath := filepath.Join(rc.WorktreeBase, fmt.Sprintf("%s-pass1-a1", rc.RunID))
	firstBranch := fmt.Sprintf("auto-improve/%s/pass1/%s", rc.RunID, DefaultAgents[0])

	git := gitCLI{
		stat: os.Stat,
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			switch {
			case slices.Equal(args, []string{"-C", repoRoot, "config", "--get", "remote.origin.url"}):
				return []byte("git@github.com:owner/repo.git\n"), nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "fetch", "--no-tags", "origin", testMergeCommitOID}):
				return nil, nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "rev-parse", testMergeCommitOID + "^1"}):
				return []byte(testBaseSHA + "\n"), nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "fetch", "--no-tags", "origin", "+refs/heads/auto-improve/best:refs/remotes/origin/auto-improve/best"}):
				return nil, nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "rev-parse", "origin/auto-improve/best"}):
				return []byte(testBaseSHA + "\n"), nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "add", "-b", baseBranch, basePath, testBaseSHA}):
				return []byte("Preparing base worktree\n"), nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "add", "-b", firstBranch, firstPath, testBaseSHA}):
				return nil, []byte("fatal: a branch named '" + firstBranch + "' already exists\n"), errors.New("exit status 128")
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "add", firstPath, firstBranch}):
				return []byte("Preparing worktree\n"), nil, nil
			case slices.Equal(args, []string{"-C", firstPath, "rev-parse", "HEAD"}):
				return []byte(testBaseRefOID + "\n"), nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "remove", "--force", firstPath}):
				return []byte("Removed\n"), nil, nil
			default:
				return nil, nil, fmt.Errorf("unexpected git args: %v", args)
			}
		},
	}

	runner := &Runner{
		GH: stubGH{info: PRInfo{
			Number:         42,
			Title:          "improve X",
			MergeCommitOID: testMergeCommitOID,
			LinkedIssues:   []LinkedIssue{{Number: 7, Title: "issue title", Body: "issue body"}},
		}},
		Git: git,
	}
	in := Input{
		PR:               42,
		BestBranch:       "auto-improve/best",
		TaskPromptSource: "issue",
		RepoRoot:         repoRoot,
		RunCtx:           rc,
	}

	_, err := runner.Run(context.Background(), in)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrWorktreeDrift)
}

func TestRun_ExistingWorktreeBranchDriftPropagates(t *testing.T) {
	rc := newRunCtx(t)
	repoRoot := t.TempDir()
	basePath := filepath.Join(rc.WorktreeBase, fmt.Sprintf("%s-pass1-base", rc.RunID))
	baseBranch := fmt.Sprintf("auto-improve/%s/pass1/base", rc.RunID)
	firstPath := filepath.Join(rc.WorktreeBase, fmt.Sprintf("%s-pass1-a1", rc.RunID))
	require.NoError(t, os.MkdirAll(firstPath, 0o755))

	git := gitCLI{
		stat: os.Stat,
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			switch {
			case slices.Equal(args, []string{"-C", repoRoot, "config", "--get", "remote.origin.url"}):
				return []byte("git@github.com:owner/repo.git\n"), nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "fetch", "--no-tags", "origin", testMergeCommitOID}):
				return nil, nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "rev-parse", testMergeCommitOID + "^1"}):
				return []byte(testBaseSHA + "\n"), nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "fetch", "--no-tags", "origin", "+refs/heads/auto-improve/best:refs/remotes/origin/auto-improve/best"}):
				return nil, nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "rev-parse", "origin/auto-improve/best"}):
				return []byte(testBaseSHA + "\n"), nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "add", "-b", baseBranch, basePath, testBaseSHA}):
				return []byte("Preparing base worktree\n"), nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "list", "--porcelain"}):
				return []byte("worktree " + firstPath + "\n\n"), nil, nil
			case slices.Equal(args, []string{"-C", firstPath, "rev-parse", "HEAD"}):
				return []byte(testBaseSHA + "\n"), nil, nil
			case slices.Equal(args, []string{"-C", firstPath, "branch", "--show-current"}):
				return []byte("wrong-branch\n"), nil, nil
			default:
				return nil, nil, fmt.Errorf("unexpected git args: %v", args)
			}
		},
	}

	runner := &Runner{
		GH: stubGH{info: PRInfo{
			Number:         42,
			Title:          "improve X",
			MergeCommitOID: testMergeCommitOID,
			LinkedIssues:   []LinkedIssue{{Number: 7, Title: "issue title", Body: "issue body"}},
		}},
		Git: git,
	}
	in := Input{
		PR:               42,
		BestBranch:       "auto-improve/best",
		TaskPromptSource: "issue",
		RepoRoot:         repoRoot,
		RunCtx:           rc,
	}

	_, err := runner.Run(context.Background(), in)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrWorktreeDrift)
}

func TestGitCLIWorktreeAdd_RetryBranchExisting_VerifiesHEAD(t *testing.T) {
	repoRoot := t.TempDir()
	path := filepath.Join(t.TempDir(), "worktree")
	branch := "auto-improve/run/pass1/a1"

	git := gitCLI{
		stat: os.Stat,
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			switch {
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "add", "-b", branch, path, testBaseSHA}):
				return nil, []byte("fatal: a branch named '" + branch + "' already exists\n"), errors.New("exit status 128")
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "add", path, branch}):
				return []byte("Preparing worktree\n"), nil, nil
			case slices.Equal(args, []string{"-C", path, "rev-parse", "HEAD"}):
				return []byte(testBaseRefOID + "\n"), nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "remove", "--force", path}):
				return []byte("Removed\n"), nil, nil
			default:
				return nil, nil, fmt.Errorf("unexpected git args: %v", args)
			}
		},
	}

	created, err := git.WorktreeAdd(context.Background(), repoRoot, path, branch, testBaseSHA)
	require.Error(t, err)
	assert.False(t, created)
	assert.ErrorIs(t, err, ErrWorktreeDrift)
}

func TestGitCLIWorktreeAdd_RetryBranchExisting_DriftRemovesWorktree(t *testing.T) {
	repoRoot := t.TempDir()
	path := filepath.Join(t.TempDir(), "worktree")
	branch := "auto-improve/run/pass1/a1"
	var calls [][]string

	git := gitCLI{
		stat: os.Stat,
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			calls = append(calls, append([]string(nil), args...))
			switch {
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "add", "-b", branch, path, testBaseSHA}):
				return nil, []byte("fatal: a branch named '" + branch + "' already exists\n"), errors.New("exit status 128")
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "add", path, branch}):
				return []byte("Preparing worktree\n"), nil, nil
			case slices.Equal(args, []string{"-C", path, "rev-parse", "HEAD"}):
				return []byte(testBaseRefOID + "\n"), nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "remove", "--force", path}):
				return []byte("Removed\n"), nil, nil
			default:
				return nil, nil, fmt.Errorf("unexpected git args: %v", args)
			}
		},
	}

	created, err := git.WorktreeAdd(context.Background(), repoRoot, path, branch, testBaseSHA)
	require.Error(t, err)
	assert.False(t, created)
	assert.ErrorIs(t, err, ErrWorktreeDrift)
	assert.Equal(t,
		[][]string{
			{"-C", repoRoot, "worktree", "add", "-b", branch, path, testBaseSHA},
			{"-C", repoRoot, "worktree", "add", path, branch},
			{"-C", path, "rev-parse", "HEAD"},
			{"-C", repoRoot, "worktree", "remove", "--force", path},
		},
		calls,
	)
}

func TestGitCLIWorktreeAdd_RetryBranchExisting_CleanupFailurePreservesDrift(t *testing.T) {
	repoRoot := t.TempDir()
	path := filepath.Join(t.TempDir(), "worktree")
	branch := "auto-improve/run/pass1/a1"

	git := gitCLI{
		stat: os.Stat,
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			switch {
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "add", "-b", branch, path, testBaseSHA}):
				return nil, []byte("fatal: a branch named '" + branch + "' already exists\n"), errors.New("exit status 128")
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "add", path, branch}):
				return []byte("Preparing worktree\n"), nil, nil
			case slices.Equal(args, []string{"-C", path, "rev-parse", "HEAD"}):
				return []byte(testBaseRefOID + "\n"), nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "remove", "--force", path}):
				return nil, []byte("permission denied\n"), errors.New("exit status 1")
			default:
				return nil, nil, fmt.Errorf("unexpected git args: %v", args)
			}
		},
	}

	created, err := git.WorktreeAdd(context.Background(), repoRoot, path, branch, testBaseSHA)
	require.Error(t, err)
	assert.False(t, created)
	assert.ErrorIs(t, err, ErrWorktreeDrift)
	assert.Contains(t, err.Error(), "cleanup failed")
	assert.Contains(t, err.Error(), "permission denied")
}

func TestGitCLIResolveRef_IgnoresStderrFromRunner(t *testing.T) {
	repoRoot := t.TempDir()
	git := gitCLI{
		stat: os.Stat,
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			require.Equal(t, "git", name)
			require.Equal(t, []string{"-C", repoRoot, "rev-parse", "HEAD"}, args)
			return []byte(testBaseSHA + "\n"), []byte("xcrun: warning: cache directory unavailable\n"), nil
		},
	}

	sha, err := git.ResolveRef(context.Background(), repoRoot, "HEAD")
	require.NoError(t, err)
	assert.Equal(t, testBaseSHA, sha)
}

func TestGitCLIWorktreeAdd_ExistingPath_VerifiesBranch(t *testing.T) {
	path := t.TempDir()
	branch := "auto-improve/run/pass1/a1"
	repoRoot := t.TempDir()

	git := gitCLI{
		stat: os.Stat,
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			switch {
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "list", "--porcelain"}):
				return []byte("worktree " + path + "\n\n"), nil, nil
			case slices.Equal(args, []string{"-C", path, "rev-parse", "HEAD"}):
				return []byte(testBaseSHA + "\n"), nil, nil
			case slices.Equal(args, []string{"-C", path, "branch", "--show-current"}):
				return []byte("wrong-branch\n"), nil, nil
			default:
				return nil, nil, fmt.Errorf("unexpected git args: %v", args)
			}
		},
	}

	created, err := git.WorktreeAdd(context.Background(), repoRoot, path, branch, testBaseSHA)
	require.Error(t, err)
	assert.False(t, created)
	assert.ErrorIs(t, err, ErrWorktreeDrift)
}

func TestGitCLIWorktreeAdd_RecreatesDirtyExistingWorktree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worktree")
	repoRoot := t.TempDir()
	branch := "auto-improve/run/pass1/a1"
	require.NoError(t, os.MkdirAll(path, 0o755))

	var calls [][]string
	git := gitCLI{
		stat: os.Stat,
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			calls = append(calls, append([]string(nil), args...))
			switch {
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "list", "--porcelain"}):
				return []byte("worktree " + path + "\n\n"), nil, nil
			case slices.Equal(args, []string{"-C", path, "rev-parse", "HEAD"}):
				return []byte(testBaseSHA + "\n"), nil, nil
			case slices.Equal(args, []string{"-C", path, "branch", "--show-current"}):
				return []byte(branch + "\n"), nil, nil
			case slices.Equal(args, []string{"-C", path, "status", "--porcelain", "--ignored"}):
				return []byte(" M README.md\n"), nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "remove", "--force", path}):
				require.NoError(t, os.RemoveAll(path))
				return []byte("Removed\n"), nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "add", "-b", branch, path, testBaseSHA}):
				return []byte("Preparing worktree\n"), nil, nil
			default:
				return nil, nil, fmt.Errorf("unexpected git args: %v", args)
			}
		},
	}

	created, err := git.WorktreeAdd(context.Background(), repoRoot, path, branch, testBaseSHA)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t,
		[][]string{
			{"-C", repoRoot, "worktree", "list", "--porcelain"},
			{"-C", path, "rev-parse", "HEAD"},
			{"-C", path, "branch", "--show-current"},
			{"-C", path, "status", "--porcelain", "--ignored"},
			{"-C", repoRoot, "worktree", "remove", "--force", path},
			{"-C", repoRoot, "worktree", "add", "-b", branch, path, testBaseSHA},
		},
		calls,
	)
}

func TestGitCLIWorktreeAdd_RecreatesIgnoredExistingWorktree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worktree")
	repoRoot := t.TempDir()
	branch := "auto-improve/run/pass1/a1"
	require.NoError(t, os.MkdirAll(path, 0o755))

	var calls [][]string
	git := gitCLI{
		stat: os.Stat,
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			calls = append(calls, append([]string(nil), args...))
			switch {
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "list", "--porcelain"}):
				return []byte("worktree " + path + "\n\n"), nil, nil
			case slices.Equal(args, []string{"-C", path, "rev-parse", "HEAD"}):
				return []byte(testBaseSHA + "\n"), nil, nil
			case slices.Equal(args, []string{"-C", path, "branch", "--show-current"}):
				return []byte(branch + "\n"), nil, nil
			case slices.Equal(args, []string{"-C", path, "status", "--porcelain", "--ignored"}):
				return []byte("!! .env.local\n"), nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "remove", "--force", path}):
				require.NoError(t, os.RemoveAll(path))
				return []byte("Removed\n"), nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "add", "-b", branch, path, testBaseSHA}):
				return []byte("Preparing worktree\n"), nil, nil
			default:
				return nil, nil, fmt.Errorf("unexpected git args: %v", args)
			}
		},
	}

	created, err := git.WorktreeAdd(context.Background(), repoRoot, path, branch, testBaseSHA)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Contains(t, calls, []string{"-C", path, "status", "--porcelain", "--ignored"})
}

func TestGitCLIWorktreeAdd_ExistingPathRequiresRegisteredWorktree(t *testing.T) {
	path := t.TempDir()
	repoRoot := t.TempDir()

	git := gitCLI{
		stat: os.Stat,
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			require.Equal(t, "git", name)
			switch {
			case slices.Equal(args, []string{"-C", repoRoot, "worktree", "list", "--porcelain"}):
				return []byte(""), nil, nil
			default:
				t.Fatalf("unexpected git args: %v", args)
				return nil, nil, nil
			}
		},
	}

	created, err := git.WorktreeAdd(context.Background(), repoRoot, path, "auto-improve/run/pass1/a1", testBaseSHA)
	require.Error(t, err)
	assert.False(t, created)
	assert.ErrorIs(t, err, ErrWorktreeDrift)
}
