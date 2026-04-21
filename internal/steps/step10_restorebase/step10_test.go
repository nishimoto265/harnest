package step10restorebase

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubGHClient struct {
	info PRInfo
	err  error
}

func (s stubGHClient) PRView(ctx context.Context, pr int, repoRoot string) (PRInfo, error) {
	_ = ctx
	_ = pr
	_ = repoRoot
	if s.err != nil {
		return PRInfo{}, s.err
	}
	return s.info, nil
}

type stubGitClient struct {
	allocations map[string]stubWorktree
}

type stubWorktree struct {
	branch string
	sha    string
}

func (s *stubGitClient) WorktreeAdd(ctx context.Context, repoRoot, path, branch, sha string) (bool, error) {
	_ = ctx
	_ = repoRoot
	if s.allocations == nil {
		s.allocations = make(map[string]stubWorktree)
	}
	if existing, ok := s.allocations[path]; ok {
		if existing.branch != branch || existing.sha != sha {
			return false, errors.New("stub git: existing allocation mismatch")
		}
		return false, nil
	}
	s.allocations[path] = stubWorktree{
		branch: branch,
		sha:    sha,
	}
	return true, nil
}

func (s *stubGitClient) ResolveRef(ctx context.Context, repoRoot, ref string) (string, error) {
	_ = ctx
	_ = repoRoot
	_ = ref
	return strings.Repeat("a", 40), nil
}

func TestRunnerRun_HappyPathAndResume(t *testing.T) {
	repoRoot := t.TempDir()
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	runCtx, err := internalio.NewRunContext("2026-04-21-PR42-abcdef0", runsBase, worktreeBase)
	require.NoError(t, err)

	gitStub := &stubGitClient{}
	runner := &Runner{
		GH: stubGHClient{
			info: PRInfo{
				Number:     42,
				Title:      "Tighten restore-base",
				Body:       "Restore all worktrees from the merged base.",
				BaseRefOid: strings.Repeat("a", 40),
				HeadRefOid: strings.Repeat("b", 40),
				LinkedIssues: []LinkedIssue{
					{
						Number: 7,
						Title:  "Document worktree layout",
						Body:   "Include pass1/pass2 paths.",
					},
				},
			},
		},
		Git: gitStub,
	}

	now := time.Date(2026, 4, 21, 10, 30, 0, 0, time.UTC)
	input := Input{
		PR:            42,
		BestBranch:    "auto-improve/best",
		HarnessFiles:  true,
		ExpectedRunID: runCtx.RunID,
		RepoRoot:      repoRoot,
		RunCtx:        runCtx,
		Now:           func() time.Time { return now },
	}

	result, err := runner.Run(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, 6, result.Response.WorktreesCreated)
	require.NoError(t, result.Response.TaskPackage.Validate())

	pkg := result.Response.TaskPackage
	require.Len(t, pkg.Worktrees, 6)
	assert.Equal(t, "1", pkg.SchemaVersion)
	assert.Equal(t, runCtx.RunID, pkg.RunID)
	assert.Equal(t, "Tighten restore-base", pkg.Title)
	assert.Equal(t, strings.Repeat("a", 40), pkg.BaseSHA)
	assert.Equal(t, "auto-improve/best", pkg.BestBranch)
	assert.Equal(t, now, pkg.CreatedAt)
	assert.Equal(t, "# PR #42: Tighten restore-base\n\nRestore all worktrees from the merged base.\n\n## Linked issues\n\n### #7: Document worktree layout\n\nInclude pass1/pass2 paths.\n", pkg.ReconstructedTaskPrompt)

	pathSet := make(map[string]struct{}, len(pkg.Worktrees))
	branchSet := make(map[string]struct{}, len(pkg.Worktrees))
	for _, worktree := range pkg.Worktrees {
		pathSet[worktree.Path] = struct{}{}
		branchSet[worktree.Branch] = struct{}{}
	}
	assert.Len(t, pathSet, 6)
	assert.Len(t, branchSet, 6)

	baseData, err := os.ReadFile(runCtx.BaseSHAPath())
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("a", 40)+"\n", string(baseData))

	persistedPkg, err := internalio.ReadJSON[contracts.TaskPackage](runCtx.TaskPackagePath())
	require.NoError(t, err)
	assert.Equal(t, pkg, persistedPkg)

	resumed, err := runner.Run(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, 0, resumed.Response.WorktreesCreated)
}

func TestRunnerRun_ExpectedRunIDMismatch(t *testing.T) {
	runCtx := newRunContextForTest(t)
	runner := &Runner{
		GH:  stubGHClient{},
		Git: &stubGitClient{},
	}

	_, err := runner.Run(context.Background(), Input{
		PR:            42,
		BestBranch:    "auto-improve/best",
		HarnessFiles:  true,
		ExpectedRunID: "2026-04-21-PR42-deadbee",
		RepoRoot:      t.TempDir(),
		RunCtx:        runCtx,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected_run_id")
}

func TestRunnerRun_GHFailurePropagates(t *testing.T) {
	runCtx := newRunContextForTest(t)
	runner := &Runner{
		GH:  stubGHClient{err: errors.New("gh exploded")},
		Git: &stubGitClient{},
	}

	_, err := runner.Run(context.Background(), Input{
		PR:           42,
		BestBranch:   "auto-improve/best",
		HarnessFiles: true,
		RepoRoot:     t.TempDir(),
		RunCtx:       runCtx,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "step10")
	assert.Contains(t, err.Error(), "gh exploded")
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
				{Number: 10, Title: "Issue", Body: "Issue body."},
			},
			want: "# Title\n\nBody text.\n\n## Linked issues\n\n### #10: Issue\n\nIssue body.\n",
		},
		{
			name: "body only",
			body: "Body text.",
			want: "# Title\n\nBody text.\n",
		},
		{
			name: "issues only",
			issues: []LinkedIssue{
				{Number: 10, Title: "Issue", Body: "Issue body."},
			},
			want: "# Title\n\n## Linked issues\n\n### #10: Issue\n\nIssue body.\n",
		},
		{
			name: "heading only",
			want: "# Title\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ReconstructTaskPrompt("Title", tt.body, tt.issues))
		})
	}
}

func newRunContextForTest(t *testing.T) internalio.RunContext {
	t.Helper()
	runsBase := filepath.Join(t.TempDir(), "runs")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")
	runCtx, err := internalio.NewRunContext("2026-04-21-PR42-abcdef0", runsBase, worktreeBase)
	require.NoError(t, err)
	return runCtx
}
