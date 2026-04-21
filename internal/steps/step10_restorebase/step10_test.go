package step10restorebase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunnerRunHappyPathAndResume(t *testing.T) {
	t.Parallel()

	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	repoRoot := t.TempDir()
	runCtx, err := internalio.NewRunContext("2026-04-21-PR42-abcdef0", runsBase, worktreeBase)
	require.NoError(t, err)

	gh := stubGHClient{
		info: PRInfo{
			Number:     42,
			Title:      "Improve restore-base",
			Body:       "Carry the merged PR context into pass runners.",
			BaseRefOid: "1111111111111111111111111111111111111111",
			HeadRefOid: "2222222222222222222222222222222222222222",
			LinkedIssues: []LinkedIssue{
				{Number: 7, Title: "Edge case", Body: "Need to preserve resumes."},
			},
		},
	}
	git := newStubGitClient()
	runner := &Runner{GH: gh, Git: git}

	result, err := runner.Run(context.Background(), Input{
		PR:           42,
		BestBranch:   "auto-improve/best",
		HarnessFiles: true,
		RepoRoot:     repoRoot,
		RunCtx:       runCtx,
		Now: func() time.Time {
			return time.Unix(1713657600, 0).UTC()
		},
	})
	require.NoError(t, err)
	require.Equal(t, 6, result.Response.WorktreesCreated)
	require.NoError(t, result.Response.Validate())
	require.NoError(t, result.Response.TaskPackage.Validate())

	baseData, err := os.ReadFile(runCtx.BaseSHAPath())
	require.NoError(t, err)
	assert.Equal(t, "1111111111111111111111111111111111111111\n", string(baseData))

	loaded, err := internalio.ReadJSON[contracts.TaskPackage](runCtx.TaskPackagePath())
	require.NoError(t, err)
	require.NoError(t, loaded.Validate())
	assert.Equal(t, result.Response.TaskPackage, loaded)

	paths := make(map[string]struct{}, len(loaded.Worktrees))
	branches := make(map[string]struct{}, len(loaded.Worktrees))
	for _, worktree := range loaded.Worktrees {
		_, pathSeen := paths[worktree.Path]
		assert.False(t, pathSeen)
		paths[worktree.Path] = struct{}{}

		_, branchSeen := branches[worktree.Branch]
		assert.False(t, branchSeen)
		branches[worktree.Branch] = struct{}{}
	}

	second, err := runner.Run(context.Background(), Input{
		PR:           42,
		BestBranch:   "auto-improve/best",
		HarnessFiles: true,
		RepoRoot:     repoRoot,
		RunCtx:       runCtx,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, second.Response.WorktreesCreated)
}

func TestRunnerRunExpectedRunIDMismatch(t *testing.T) {
	t.Parallel()

	runCtx := testRunContext(t)
	runner := &Runner{
		GH:  stubGHClient{},
		Git: newStubGitClient(),
	}

	_, err := runner.Run(context.Background(), Input{
		PR:            42,
		BestBranch:    "auto-improve/best",
		HarnessFiles:  true,
		ExpectedRunID: "2026-04-21-PR99-abcdef0",
		RepoRoot:      t.TempDir(),
		RunCtx:        runCtx,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected_run_id")
}

func TestRunnerRunGHFailurePropagates(t *testing.T) {
	t.Parallel()

	runCtx := testRunContext(t)
	runner := &Runner{
		GH: stubGHClient{
			err: errors.New("boom"),
		},
		Git: newStubGitClient(),
	}

	_, err := runner.Run(context.Background(), Input{
		PR:           42,
		BestBranch:   "auto-improve/best",
		HarnessFiles: true,
		RepoRoot:     t.TempDir(),
		RunCtx:       runCtx,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "step10: gh pr view")
	assert.ErrorIs(t, err, runner.GH.(stubGHClient).err)
}

func TestReconstructTaskPrompt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		body   string
		issues []LinkedIssue
		want   string
	}{
		{
			name: "body and issues",
			body: "Body text.",
			issues: []LinkedIssue{
				{Number: 1, Title: "Issue one", Body: "Issue body."},
			},
			want: "# PR #42: Title\n\nBody text.\n\n## Linked issues\n\n### #1: Issue one\nIssue body.\n",
		},
		{
			name: "body without issues",
			body: "Body text.",
			want: "# PR #42: Title\n\nBody text.\n",
		},
		{
			name: "issues without body",
			issues: []LinkedIssue{
				{Number: 1, Title: "Issue one", Body: "Issue body."},
			},
			want: "# PR #42: Title\n\n## Linked issues\n\n### #1: Issue one\nIssue body.\n",
		},
		{
			name: "empty body and no issues",
			want: "# PR #42: Title\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ReconstructTaskPrompt(42, "Title", tt.body, tt.issues)
			assert.Equal(t, tt.want, got)
		})
	}
}

type stubGHClient struct {
	info PRInfo
	err  error
}

func (s stubGHClient) PRView(_ context.Context, _ int, _ string) (PRInfo, error) {
	if s.err != nil {
		return PRInfo{}, s.err
	}
	return s.info, nil
}

type stubGitClient struct {
	worktrees map[string]stubWorktreeState
}

type stubWorktreeState struct {
	branch string
	sha    string
}

func newStubGitClient() *stubGitClient {
	return &stubGitClient{
		worktrees: make(map[string]stubWorktreeState),
	}
}

func (s *stubGitClient) WorktreeAdd(_ context.Context, _ string, path, branch, sha string) (bool, error) {
	if existing, ok := s.worktrees[path]; ok {
		if existing.branch != branch || existing.sha != sha {
			return false, fmt.Errorf("mismatched existing worktree: path=%q", path)
		}
		return false, nil
	}
	s.worktrees[path] = stubWorktreeState{branch: branch, sha: sha}
	return true, nil
}

func (s *stubGitClient) ResolveRef(_ context.Context, _ string, ref string) (string, error) {
	if ref == "HEAD" {
		return "", errors.New("not implemented")
	}
	return "", errors.New("not implemented")
}

func testRunContext(t *testing.T) internalio.RunContext {
	t.Helper()

	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	runCtx, err := internalio.NewRunContext("2026-04-21-PR42-abcdef0", runsBase, worktreeBase)
	require.NoError(t, err)
	return runCtx
}

func TestRunnerRunWritesPathsUnderWorktreeBase(t *testing.T) {
	t.Parallel()

	runCtx := testRunContext(t)
	repoRoot := t.TempDir()
	git := newStubGitClient()
	runner := &Runner{
		GH: stubGHClient{
			info: PRInfo{
				Number:     42,
				Title:      "Title",
				BaseRefOid: "1111111111111111111111111111111111111111",
			},
		},
		Git: git,
	}

	_, err := runner.Run(context.Background(), Input{
		PR:           42,
		BestBranch:   "auto-improve/best",
		HarnessFiles: true,
		RepoRoot:     repoRoot,
		RunCtx:       runCtx,
	})
	require.NoError(t, err)

	for path := range git.worktrees {
		assert.DirExists(t, filepath.Dir(path))
		assert.Contains(t, path, string(runCtx.RunID))
	}
}
