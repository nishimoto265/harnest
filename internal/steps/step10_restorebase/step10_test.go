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

const (
	testBaseSHA       = "0123456789abcdef0123456789abcdef01234567"
	testBaseTipSHA    = "89abcdef0123456789abcdef0123456789abcdef"
	testHeadSHA       = "fedcba9876543210fedcba9876543210fedcba98"
	testMergeCommitID = "1111111111111111111111111111111111111111"
	testOtherSHA      = "2222222222222222222222222222222222222222"
)

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
	refs      map[string]string // ref → sha
	createdBy []string
}

func newStubGit() *stubGit {
	return &stubGit{
		known: map[string]string{},
		refs: map[string]string{
			testMergeCommitID + "^1": testBaseSHA,
		},
	}
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if sha, ok := s.refs[ref]; ok {
		return sha, nil
	}
	return "", fmt.Errorf("unexpected ref: %s", ref)
}

func testPRInfo() PRInfo {
	return PRInfo{
		Number:         42,
		Title:          "improve X",
		Body:           "body text\n",
		BaseRefOid:     testBaseTipSHA,
		HeadRefOid:     testHeadSHA,
		MergeCommitOID: testMergeCommitID,
		LinkedIssues: []LinkedIssue{
			{Number: 10, Title: "bug", Body: "repro steps"},
		},
	}
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
		GH:  stubGH{info: testPRInfo()},
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
	gh := stubGH{info: testPRInfo()}
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
	runner := &Runner{GH: stubGH{info: testPRInfo()}, Git: newStubGit()}

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

func TestRun_InvalidMergeCommitOID(t *testing.T) {
	rc := newRunCtx(t)
	runner := &Runner{GH: stubGH{info: PRInfo{MergeCommitOID: "not-a-sha"}}, Git: newStubGit()}

	in := Input{
		PR:         42,
		BestBranch: "auto-improve/best",
		RepoRoot:   t.TempDir(),
		RunCtx:     rc,
	}
	_, err := runner.Run(context.Background(), in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "merge_commit_oid")
}

func TestRun_NotMergedPR(t *testing.T) {
	rc := newRunCtx(t)
	info := testPRInfo()
	info.MergeCommitOID = ""
	runner := &Runner{GH: stubGH{info: info}, Git: newStubGit()}

	in := Input{
		PR:         42,
		BestBranch: "auto-improve/best",
		RepoRoot:   t.TempDir(),
		RunCtx:     rc,
	}
	_, err := runner.Run(context.Background(), in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "step10 requires a merged PR")
}

func TestRun_WorktreeRetryHeadDrift(t *testing.T) {
	rc := newRunCtx(t)
	repoRoot := t.TempDir()
	git := gitCLI{
		stat: func(path string) (os.FileInfo, error) {
			return nil, os.ErrNotExist
		},
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			require.Equal(t, "git", name)
			switch {
			case len(args) == 4 && args[2] == "rev-parse" && args[3] == testMergeCommitID+"^1":
				return []byte(testBaseSHA + "\n"), nil
			case len(args) == 4 && args[2] == "rev-parse" && args[3] == "HEAD":
				return []byte(testOtherSHA + "\n"), nil
			case len(args) == 8 && args[2] == "worktree" && args[3] == "add" && args[4] == "-b":
				return []byte("fatal: a branch named 'auto-improve/x' already exists\n"), errors.New("exit status 128")
			case len(args) == 6 && args[2] == "worktree" && args[3] == "add":
				return nil, nil
			default:
				return nil, fmt.Errorf("unexpected git args: %v", args)
			}
		},
	}

	runner := &Runner{GH: stubGH{info: testPRInfo()}, Git: git}
	in := Input{
		PR:         42,
		BestBranch: "auto-improve/best",
		RepoRoot:   repoRoot,
		RunCtx:     rc,
	}
	_, err := runner.Run(context.Background(), in)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrWorktreeDrift)
	assert.Contains(t, err.Error(), "expected="+testBaseSHA)
	assert.Contains(t, err.Error(), "actual="+testOtherSHA)
}

func TestRun_ExistingWorktreeWrongBranch(t *testing.T) {
	rc := newRunCtx(t)
	repoRoot := t.TempDir()
	existingInfo, statErr := os.Stat(t.TempDir())
	require.NoError(t, statErr)
	git := gitCLI{
		stat: func(path string) (os.FileInfo, error) {
			return existingInfo, nil
		},
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			require.Equal(t, "git", name)
			switch {
			case len(args) == 4 && args[2] == "rev-parse" && args[3] == testMergeCommitID+"^1":
				return []byte(testBaseSHA + "\n"), nil
			case len(args) == 4 && args[2] == "rev-parse" && args[3] == "HEAD":
				return []byte(testBaseSHA + "\n"), nil
			case len(args) == 4 && args[2] == "branch" && args[3] == "--show-current":
				return []byte("auto-improve/wrong/pass1/a1\n"), nil
			default:
				return nil, fmt.Errorf("unexpected git args: %v", args)
			}
		},
	}

	runner := &Runner{GH: stubGH{info: testPRInfo()}, Git: git}
	in := Input{
		PR:         42,
		BestBranch: "auto-improve/best",
		RepoRoot:   repoRoot,
		RunCtx:     rc,
	}
	_, err := runner.Run(context.Background(), in)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrWorktreeDrift)
	assert.Contains(t, err.Error(), "expected_branch=auto-improve/"+string(rc.RunID)+"/pass1/a1")
}

func TestRun_PersistedBaseSHADrift(t *testing.T) {
	rc := newRunCtx(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(rc.BaseSHAPath()), 0o755))
	require.NoError(t, os.WriteFile(rc.BaseSHAPath(), []byte(testOtherSHA+"\n"), 0o644))
	runner := &Runner{GH: stubGH{info: testPRInfo()}, Git: newStubGit()}

	in := Input{
		PR:         42,
		BestBranch: "auto-improve/best",
		RepoRoot:   t.TempDir(),
		RunCtx:     rc,
	}
	_, err := runner.Run(context.Background(), in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "persisted base.sha="+testOtherSHA)
	assert.Contains(t, err.Error(), "merge-base="+testBaseSHA)
}

func TestRun_PersistedBaseSHAMatch(t *testing.T) {
	rc := newRunCtx(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(rc.BaseSHAPath()), 0o755))
	require.NoError(t, os.WriteFile(rc.BaseSHAPath(), []byte(testBaseSHA+"\n"), 0o644))
	runner := &Runner{GH: stubGH{info: testPRInfo()}, Git: newStubGit()}

	in := Input{
		PR:         42,
		BestBranch: "auto-improve/best",
		RepoRoot:   t.TempDir(),
		RunCtx:     rc,
	}
	res, err := runner.Run(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, testBaseSHA, res.Response.BaseSHA)

	data, err := os.ReadFile(rc.BaseSHAPath())
	require.NoError(t, err)
	assert.Equal(t, testBaseSHA+"\n", string(data))
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
