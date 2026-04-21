package step10restorebase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
const testMergeCommitOID = "89abcdef0123456789abcdef0123456789abcdef"
const testBaseRefOID = "76543210fedcba9876543210fedcba9876543210"

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
	mu         sync.Mutex
	known      map[string]string // path → sha (marks "already exists")
	createdBy  []string
	resolvedBy map[string]string
}

func newStubGit() *stubGit {
	return &stubGit{
		known:      map[string]string{},
		resolvedBy: map[string]string{},
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
	if sha, ok := s.resolvedBy[repoRoot+"::"+ref]; ok {
		return sha, nil
	}
	if sha, ok := s.resolvedBy[ref]; ok {
		return sha, nil
	}
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
	git := newStubGit()
	git.resolvedBy[testMergeCommitOID+"^1"] = testBaseSHA
	runner := &Runner{
		GH: stubGH{info: PRInfo{
			Number:         42,
			Title:          "improve X",
			Body:           "body text\n",
			BaseRefOid:     testBaseRefOID,
			HeadRefOid:     testBaseSHA,
			MergeCommitOID: testMergeCommitOID,
			LinkedIssues: []LinkedIssue{
				{Number: 10, Title: "bug", Body: "repro steps"},
			},
		}},
		Git: git,
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
		Number:         42,
		Title:          "improve X",
		MergeCommitOID: testMergeCommitOID,
	}}
	git := newStubGit()
	git.resolvedBy[testMergeCommitOID+"^1"] = testBaseSHA
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
	git := newStubGit()
	git.resolvedBy[testMergeCommitOID+"^1"] = testBaseSHA
	runner := &Runner{GH: stubGH{info: PRInfo{MergeCommitOID: testMergeCommitOID}}, Git: git}

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
	git := newStubGit()
	git.resolvedBy[testMergeCommitOID+"^1"] = "not-a-sha"
	runner := &Runner{GH: stubGH{info: PRInfo{MergeCommitOID: testMergeCommitOID}}, Git: git}

	in := Input{
		PR:         42,
		BestBranch: "auto-improve/best",
		RepoRoot:   t.TempDir(),
		RunCtx:     rc,
	}
	_, err := runner.Run(context.Background(), in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "merge-base")
}

func TestRun_NotMergedPR(t *testing.T) {
	rc := newRunCtx(t)
	runner := &Runner{GH: stubGH{info: PRInfo{}}, Git: newStubGit()}

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

func TestRun_RebaseMergedPR_FallsBackToBaseRefOID(t *testing.T) {
	rc := newRunCtx(t)
	runner := &Runner{
		GH: stubGH{info: PRInfo{
			Number:     42,
			Title:      "improve X",
			State:      "MERGED",
			BaseRefOid: testBaseRefOID,
		}},
		Git: newStubGit(),
	}

	in := Input{
		PR:         42,
		BestBranch: "auto-improve/best",
		RepoRoot:   t.TempDir(),
		RunCtx:     rc,
	}
	res, err := runner.Run(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, testBaseRefOID, res.Response.BaseSHA)
	assert.Equal(t, 6, res.Response.WorktreesCreated)
}

func TestRun_RebaseUnmergedPR_RejectsOpenState(t *testing.T) {
	rc := newRunCtx(t)
	runner := &Runner{
		GH: stubGH{info: PRInfo{
			Number:     42,
			Title:      "improve X",
			State:      "OPEN",
			BaseRefOid: testBaseRefOID,
		}},
		Git: newStubGit(),
	}

	in := Input{
		PR:         42,
		BestBranch: "auto-improve/best",
		RepoRoot:   t.TempDir(),
		RunCtx:     rc,
	}
	_, err := runner.Run(context.Background(), in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "step10 requires a merged PR: state=OPEN")
}

func TestRun_PersistedBaseSHADrift(t *testing.T) {
	rc := newRunCtx(t)
	require.NoError(t, internalio.WriteAtomic(rc.BaseSHAPath(), []byte(testBaseRefOID+"\n")))

	git := newStubGit()
	git.resolvedBy[testMergeCommitOID+"^1"] = testBaseSHA
	runner := &Runner{
		GH:  stubGH{info: PRInfo{Number: 42, Title: "improve X", MergeCommitOID: testMergeCommitOID}},
		Git: git,
	}

	in := Input{
		PR:         42,
		BestBranch: "auto-improve/best",
		RepoRoot:   t.TempDir(),
		RunCtx:     rc,
	}
	_, err := runner.Run(context.Background(), in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "persisted base.sha="+testBaseRefOID)
	assert.Contains(t, err.Error(), "merge-base="+testBaseSHA)
}

func TestRun_PersistedBaseSHAMatchesMergeBase(t *testing.T) {
	rc := newRunCtx(t)
	require.NoError(t, internalio.WriteAtomic(rc.BaseSHAPath(), []byte(testBaseSHA+"\n")))

	git := newStubGit()
	git.resolvedBy[testMergeCommitOID+"^1"] = testBaseSHA
	runner := &Runner{
		GH:  stubGH{info: PRInfo{Number: 42, Title: "improve X", MergeCommitOID: testMergeCommitOID}},
		Git: git,
	}

	in := Input{
		PR:         42,
		BestBranch: "auto-improve/best",
		RepoRoot:   t.TempDir(),
		RunCtx:     rc,
	}
	res, err := runner.Run(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, testBaseSHA, res.Response.BaseSHA)
}

func TestRun_WorktreeRetryDriftPropagates(t *testing.T) {
	rc := newRunCtx(t)
	repoRoot := t.TempDir()
	firstPath := filepath.Join(rc.WorktreeBase, fmt.Sprintf("%s-pass1-a1", rc.RunID))
	firstBranch := fmt.Sprintf("auto-improve/%s/pass1/%s", rc.RunID, DefaultAgents[0])

	git := gitCLI{
		stat: os.Stat,
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			switch {
			case slices.Equal(args, []string{"-C", repoRoot, "rev-parse", testMergeCommitOID + "^1"}):
				return []byte(testBaseSHA + "\n"), nil, nil
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
		GH:  stubGH{info: PRInfo{Number: 42, Title: "improve X", MergeCommitOID: testMergeCommitOID}},
		Git: git,
	}
	in := Input{
		PR:         42,
		BestBranch: "auto-improve/best",
		RepoRoot:   repoRoot,
		RunCtx:     rc,
	}

	_, err := runner.Run(context.Background(), in)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrWorktreeDrift)
}

func TestRun_ExistingWorktreeBranchDriftPropagates(t *testing.T) {
	rc := newRunCtx(t)
	repoRoot := t.TempDir()
	firstPath := filepath.Join(rc.WorktreeBase, fmt.Sprintf("%s-pass1-a1", rc.RunID))
	require.NoError(t, os.MkdirAll(firstPath, 0o755))

	git := gitCLI{
		stat: os.Stat,
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			switch {
			case slices.Equal(args, []string{"-C", repoRoot, "rev-parse", testMergeCommitOID + "^1"}):
				return []byte(testBaseSHA + "\n"), nil, nil
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
		GH:  stubGH{info: PRInfo{Number: 42, Title: "improve X", MergeCommitOID: testMergeCommitOID}},
		Git: git,
	}
	in := Input{
		PR:         42,
		BestBranch: "auto-improve/best",
		RepoRoot:   repoRoot,
		RunCtx:     rc,
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

	git := gitCLI{
		stat: os.Stat,
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			switch {
			case slices.Equal(args, []string{"-C", path, "rev-parse", "HEAD"}):
				return []byte(testBaseSHA + "\n"), nil, nil
			case slices.Equal(args, []string{"-C", path, "branch", "--show-current"}):
				return []byte("wrong-branch\n"), nil, nil
			default:
				return nil, nil, fmt.Errorf("unexpected git args: %v", args)
			}
		},
	}

	created, err := git.WorktreeAdd(context.Background(), t.TempDir(), path, branch, testBaseSHA)
	require.Error(t, err)
	assert.False(t, created)
	assert.ErrorIs(t, err, ErrWorktreeDrift)
}

func TestGHCLIPRView_EmptyObjectRejected(t *testing.T) {
	gh := ghCLI{
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			require.Equal(t, "gh", name)
			return []byte(`{}`), nil, nil
		},
	}

	_, err := gh.PRView(context.Background(), 42, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required fields")
	assert.Contains(t, err.Error(), "number")
	assert.Contains(t, err.Error(), "title")
	assert.Contains(t, err.Error(), "state")
}

func TestGHCLIPRView_EmptyTitleRejected(t *testing.T) {
	gh := ghCLI{
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			require.Equal(t, "gh", name)
			return []byte(`{"number":42,"title":"","state":"MERGED"}`), nil, nil
		},
	}

	_, err := gh.PRView(context.Background(), 42, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required fields")
	assert.Contains(t, err.Error(), "title")
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
