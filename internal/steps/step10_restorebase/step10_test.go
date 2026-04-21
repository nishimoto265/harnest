package step10restorebase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testBaseSHA = "0123456789abcdef0123456789abcdef01234567"

type stubGH struct {
	info PRInfo
	err  error
}

func (s stubGH) PRView(ctx context.Context, pr int, repo string) (PRInfo, error) {
	if s.err != nil {
		return PRInfo{}, s.err
	}
	out := s.info
	if out.Number == 0 {
		out.Number = pr
	}
	return out, nil
}

type stubGit struct {
	mu        sync.Mutex
	known     map[string]string // path → sha (marks "already exists")
	createdBy []string
}

func newStubGit() *stubGit {
	return &stubGit{known: map[string]string{}}
}

func (s *stubGit) WorktreeAdd(ctx context.Context, repoRoot, path, branch, sha string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.known[path]; ok {
		if existing != sha {
			return false, fmt.Errorf("%w: path=%s expected=%s actual=%s", ErrWorktreeDrift, path, sha, existing)
		}
		return false, nil
	}
	s.known[path] = sha
	s.createdBy = append(s.createdBy, path)
	return true, nil
}

func (s *stubGit) ResolveRef(ctx context.Context, repoRoot, ref string) (string, error) {
	return testBaseSHA, nil
}

func newRunCtx(t *testing.T) internalio.RunContext {
	t.Helper()
	base := t.TempDir()
	runsBase := filepath.Join(base, "runs")
	worktreeBase := filepath.Join(base, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	rc, err := internalio.NewRunContext(runID, runsBase, worktreeBase)
	require.NoError(t, err)
	return rc
}

func TestRun_HappyPath_SixWorktrees(t *testing.T) {
	rc := newRunCtx(t)
	runner := &Runner{
		GH: stubGH{info: PRInfo{
			Number:     42,
			Title:      "improve X",
			Body:       "body text\n",
			BaseRefOid: testBaseSHA,
			HeadRefOid: testBaseSHA,
			LinkedIssues: []LinkedIssue{
				{Number: 10, Title: "bug", Body: "repro steps"},
			},
		}},
		Git: newStubGit(),
	}

	in := Input{
		PR:           42,
		BestBranch:   "auto-improve/best",
		HarnessFiles: true,
		RepoRoot:     t.TempDir(),
		RunCtx:       rc,
		Now:          func() time.Time { return time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC) },
	}
	res, err := runner.Run(context.Background(), in)
	require.NoError(t, err)

	resp := res.Response
	require.NoError(t, resp.Validate())
	assert.Equal(t, 6, resp.WorktreesCreated)
	assert.Equal(t, testBaseSHA, resp.BaseSHA)
	assert.Equal(t, rc.RunID, resp.RunID)
	assert.Len(t, resp.TaskPackage.Worktrees, 6)

	// Uniqueness (path + branch) is a TaskPackage invariant; also confirm here.
	paths := map[string]struct{}{}
	branches := map[string]struct{}{}
	for _, w := range resp.TaskPackage.Worktrees {
		paths[w.Path] = struct{}{}
		branches[w.Branch] = struct{}{}
		assert.Equal(t, testBaseSHA, w.BaseSHA)
		assert.Equal(t, testBaseSHA, w.HeadSHA)
	}
	assert.Len(t, paths, 6)
	assert.Len(t, branches, 6)

	// base.sha artifact = 40-hex + trailing newline.
	data, err := os.ReadFile(rc.BaseSHAPath())
	require.NoError(t, err)
	assert.Equal(t, testBaseSHA+"\n", string(data))

	// task-package.json reloads and re-validates.
	reloaded, err := internalio.ReadJSON[contracts.TaskPackage](rc.TaskPackagePath())
	require.NoError(t, err)
	require.NoError(t, reloaded.Validate())
	assert.Equal(t, 42, reloaded.PR)
	assert.Contains(t, reloaded.ReconstructedTaskPrompt, "# PR #42: improve X")
	assert.Contains(t, reloaded.ReconstructedTaskPrompt, "## Linked issues")
}

func TestRun_Resume_NoNewWorktrees(t *testing.T) {
	rc := newRunCtx(t)
	gh := stubGH{info: PRInfo{
		Number:     42,
		Title:      "improve X",
		BaseRefOid: testBaseSHA,
	}}
	git := newStubGit()
	runner := &Runner{GH: gh, Git: git}

	in := Input{
		PR:         42,
		BestBranch: "auto-improve/best",
		RepoRoot:   t.TempDir(),
		RunCtx:     rc,
	}
	first, err := runner.Run(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, 6, first.Response.WorktreesCreated)

	// Second run with the same stub sees existing paths and reports 0 new.
	second, err := runner.Run(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, 0, second.Response.WorktreesCreated)
	assert.Len(t, second.Response.TaskPackage.Worktrees, 6)
}

func TestRun_ExpectedRunIDMismatch(t *testing.T) {
	rc := newRunCtx(t)
	runner := &Runner{GH: stubGH{info: PRInfo{BaseRefOid: testBaseSHA}}, Git: newStubGit()}

	in := Input{
		PR:            42,
		BestBranch:    "auto-improve/best",
		RepoRoot:      t.TempDir(),
		RunCtx:        rc,
		ExpectedRunID: contracts.RunID("2026-04-20-PR99-deadbee"),
	}
	_, err := runner.Run(context.Background(), in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected_run_id mismatch")
}

func TestRun_GHFailurePropagates(t *testing.T) {
	rc := newRunCtx(t)
	ghErr := errors.New("boom")
	runner := &Runner{GH: stubGH{err: ghErr}, Git: newStubGit()}

	in := Input{
		PR:         42,
		BestBranch: "auto-improve/best",
		RepoRoot:   t.TempDir(),
		RunCtx:     rc,
	}
	_, err := runner.Run(context.Background(), in)
	require.Error(t, err)
	require.ErrorIs(t, err, ghErr)
}

func TestRun_InvalidBaseSHA(t *testing.T) {
	rc := newRunCtx(t)
	runner := &Runner{GH: stubGH{info: PRInfo{BaseRefOid: "not-a-sha"}}, Git: newStubGit()}

	in := Input{
		PR:         42,
		BestBranch: "auto-improve/best",
		RepoRoot:   t.TempDir(),
		RunCtx:     rc,
	}
	_, err := runner.Run(context.Background(), in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "base_ref_oid")
}

func TestReconstructTaskPrompt_Variants(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		issues []LinkedIssue
		want   []string // substrings that must appear
		absent []string // substrings that must NOT appear
	}{
		{
			name:   "body_and_issues",
			body:   "PR body.",
			issues: []LinkedIssue{{Number: 7, Title: "t7", Body: "b7"}},
			want:   []string{"# PR #42: hello", "PR body.", "## Linked issues", "### #7: t7", "b7"},
		},
		{
			name:   "body_only",
			body:   "only body",
			issues: nil,
			want:   []string{"# PR #42: hello", "only body"},
			absent: []string{"## Linked issues"},
		},
		{
			name: "issues_only",
			body: "",
			issues: []LinkedIssue{
				{Number: 1, Title: "one", Body: "b1"},
				{Number: 2, Title: "two", Body: ""},
			},
			want: []string{
				"# PR #42: hello",
				"## Linked issues",
				"### #1: one",
				"b1",
				"### #2: two",
			},
		},
		{
			name:   "title_only",
			body:   "",
			issues: nil,
			want:   []string{"# PR #42: hello\n"},
			absent: []string{"## Linked issues"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ReconstructTaskPrompt(42, "hello", tc.body, tc.issues)
			for _, s := range tc.want {
				assert.True(t, strings.Contains(got, s), "want %q in output:\n%s", s, got)
			}
			for _, s := range tc.absent {
				assert.False(t, strings.Contains(got, s), "must NOT contain %q in output:\n%s", s, got)
			}
			assert.True(t, strings.HasSuffix(got, "\n"), "prompt must end with newline: %q", got)
		})
	}
}
