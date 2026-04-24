package step10restorebase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

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
	mu                sync.Mutex
	known             map[string]string // path → sha (marks "already exists")
	createdBy         []string
	resolvedBy        map[string]string
	mergeBase         map[string]string
	fetched           []string
	repoSlug          string
	repoSlugByRoot    map[string]string
	changedFiles      []string
	diffText          string
	changedFilesErr   error
	diffErr           error
	changedFilesCalls int
	diffCalls         int
}

func newStubGit() *stubGit {
	return &stubGit{
		known:          map[string]string{},
		resolvedBy:     map[string]string{},
		mergeBase:      map[string]string{},
		repoSlug:       "owner/repo",
		repoSlugByRoot: map[string]string{},
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

func (s *stubGit) MergeBase(ctx context.Context, repoRoot, left, right string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sha, ok := s.mergeBase[repoRoot+"::"+left+"::"+right]; ok {
		return sha, nil
	}
	if sha, ok := s.mergeBase[left+"::"+right]; ok {
		return sha, nil
	}
	return "", errors.New("merge-base unavailable")
}

func (s *stubGit) FetchCommit(ctx context.Context, repoRoot, sha string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetched = append(s.fetched, repoRoot+"::"+sha)
	return nil
}

func (s *stubGit) RepoSlug(ctx context.Context, repoRoot string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slug, ok := s.repoSlugByRoot[repoRoot]; ok {
		return slug, nil
	}
	return s.repoSlug, nil
}

func (s *stubGit) ChangedFiles(ctx context.Context, repoRoot, from, to string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.changedFilesCalls++
	if s.changedFilesErr != nil {
		return nil, s.changedFilesErr
	}
	return append([]string(nil), s.changedFiles...), nil
}

func (s *stubGit) Diff(ctx context.Context, repoRoot, from, to string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.diffCalls++
	if s.diffErr != nil {
		return "", s.diffErr
	}
	return s.diffText, nil
}

type recordingGH struct {
	repo string
	info PRInfo
	err  error
}

func (g *recordingGH) PRView(ctx context.Context, pr int, repo string) (PRInfo, error) {
	g.repo = repo
	if g.err != nil {
		return PRInfo{}, g.err
	}
	out := g.info
	if out.Number == 0 {
		out.Number = pr
	}
	return out, nil
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
	git.changedFiles = []string{"internal/foo.go", "internal/foo_test.go"}
	git.diffText = "diff --git a/internal/foo.go b/internal/foo.go\n+new behavior\n"
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
	assert.Contains(t, reloaded.ReconstructedTaskPrompt, "# Task Brief")
	assert.Contains(t, reloaded.ReconstructedTaskPrompt, "## Goal")
	assert.Contains(t, reloaded.ReconstructedTaskPrompt, "### Linked Issues")
	assert.Contains(t, reloaded.ReconstructedTaskPrompt, "### PR Title")
}

func TestRun_NoPolicyBranchSnapshotsLocalPolicy(t *testing.T) {
	rc := newRunCtx(t)
	const localRule = "# Local policy\n\nbody\n"
	require.NoError(t, os.MkdirAll(filepath.Join(rc.RunsBase, "rules"), 0o755))
	registry := "{\"kind\":\"added\",\"schema_version\":\"1\",\"rule_id\":\"r-local\",\"rule_path\":\"rules/r-local.md\",\"sha256\":\"" + sha256HexForStep10Test(localRule) + "\",\"idempotency_key\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"version_seq\":1,\"prev_hash\":\"\",\"by_run_id\":\"2026-04-21-PR1-abcdef0\",\"at\":\"2026-04-21T08:00:00Z\"}\n"
	require.NoError(t, os.WriteFile(filepath.Join(rc.RunsBase, "rules-registry.jsonl"), []byte(registry), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(rc.RunsBase, "rules", "r-local.md"), []byte(localRule), 0o644))
	git := newStubGit()
	git.resolvedBy[testMergeCommitOID+"^1"] = testBaseSHA
	runner := &Runner{
		GH:  stubGH{info: PRInfo{Number: 42, Title: "improve X", MergeCommitOID: testMergeCommitOID}},
		Git: git,
	}

	_, err := runner.Run(context.Background(), Input{
		PR:           42,
		BestBranch:   "auto-improve/best",
		HarnessFiles: true,
		RepoRoot:     t.TempDir(),
		RunCtx:       rc,
		Now:          func() time.Time { return time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, err)

	registryBytes, err := os.ReadFile(filepath.Join(rc.RunDir(), "policy", "rules-registry.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, registry, string(registryBytes))
	ruleBytes, err := os.ReadFile(filepath.Join(rc.RunDir(), "policy", "rules", "r-local.md"))
	require.NoError(t, err)
	assert.Equal(t, localRule, string(ruleBytes))
	assert.FileExists(t, filepath.Join(rc.RunDir(), "policy", "snapshot.json"))
}

func sha256HexForStep10Test(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func TestRun_FetchesMergeCommitBeforeResolveRef(t *testing.T) {
	rc := newRunCtx(t)
	repoRoot := t.TempDir()
	git := newStubGit()
	git.resolvedBy[testMergeCommitOID+"^1"] = testBaseSHA
	runner := &Runner{
		GH:  stubGH{info: PRInfo{Number: 42, Title: "improve X", MergeCommitOID: testMergeCommitOID}},
		Git: git,
	}

	_, err := runner.Run(context.Background(), Input{
		PR:         42,
		BestBranch: "auto-improve/best",
		RepoRoot:   repoRoot,
		RunCtx:     rc,
	})
	require.NoError(t, err)
	require.Contains(t, git.fetched, repoRoot+"::"+testMergeCommitOID)
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

func TestRun_UsesRepoRootSlugWhenInputRepoEmpty(t *testing.T) {
	rc := newRunCtx(t)
	git := newStubGit()
	git.resolvedBy[testMergeCommitOID+"^1"] = testBaseSHA
	gh := &recordingGH{info: PRInfo{Number: 42, Title: "improve X", MergeCommitOID: testMergeCommitOID}}
	runner := &Runner{GH: gh, Git: git}

	in := Input{
		PR:         42,
		BestBranch: "auto-improve/best",
		RepoRoot:   t.TempDir(),
		RunCtx:     rc,
	}
	_, err := runner.Run(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, "owner/repo", gh.repo)
}

func TestRun_UsesConfiguredRepoWhenProvided(t *testing.T) {
	rc := newRunCtx(t)
	repoRoot := t.TempDir()
	git := newStubGit()
	git.repoSlugByRoot[repoRoot] = "owner/repo"
	gh := &recordingGH{info: PRInfo{Number: 42, Title: "improve X", MergeCommitOID: testMergeCommitOID}}
	runner := &Runner{
		GH:  gh,
		Git: git,
	}

	in := Input{
		PR:         42,
		BestBranch: "auto-improve/best",
		RepoRoot:   repoRoot,
		Repo:       "owner/repo",
		RunCtx:     rc,
	}
	_, err := runner.Run(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, "owner/repo", gh.repo)
}

func TestRepoSlugFromRemoteURL_RejectsLocalPathRemote(t *testing.T) {
	_, err := repoSlugFromRemoteURL("/tmp/repo")
	require.Error(t, err)
}

func TestRun_RejectsConfiguredRepoMismatchWhenLocalSlugIsKnown(t *testing.T) {
	rc := newRunCtx(t)
	repoRoot := t.TempDir()
	git := newStubGit()
	git.repoSlugByRoot[repoRoot] = "owner/repo"
	runner := &Runner{
		GH:  &recordingGH{info: PRInfo{Number: 42, Title: "improve X", MergeCommitOID: testMergeCommitOID}},
		Git: git,
	}

	in := Input{
		PR:         42,
		BestBranch: "auto-improve/best",
		RepoRoot:   repoRoot,
		Repo:       "stale/slug",
		RunCtx:     rc,
	}
	_, err := runner.Run(context.Background(), in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repo mismatch")
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

func TestRun_RebaseMergedPR_UsesGitMergeBase(t *testing.T) {
	rc := newRunCtx(t)
	git := newStubGit()
	git.mergeBase[testBaseSHA+"::"+testBaseRefOID] = testBaseSHA
	git.changedFiles = []string{"pkg/change.go"}
	git.diffText = "diff --git a/pkg/change.go b/pkg/change.go\n"
	runner := &Runner{
		GH: stubGH{info: PRInfo{
			Number:     42,
			Title:      "improve X",
			State:      "MERGED",
			BaseRefOid: testBaseRefOID,
			HeadRefOid: testBaseSHA,
		}},
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
	assert.Equal(t, 6, res.Response.WorktreesCreated)
	assert.Contains(t, git.fetched, in.RepoRoot+"::"+testBaseSHA)
	assert.Contains(t, git.fetched, in.RepoRoot+"::"+testBaseRefOID)
}

func TestRun_TaskPromptSourcePRSkipsDiffContext(t *testing.T) {
	rc := newRunCtx(t)
	git := newStubGit()
	git.resolvedBy[testMergeCommitOID+"^1"] = testBaseSHA
	git.changedFiles = []string{"internal/foo.go", "internal/foo_test.go"}
	git.diffText = "diff --git a/internal/foo.go b/internal/foo.go\n+new behavior\n"
	runner := &Runner{
		GH:  stubGH{info: PRInfo{Number: 42, Title: "improve X", Body: "body text", MergeCommitOID: testMergeCommitOID}},
		Git: git,
	}

	res, err := runner.Run(context.Background(), Input{
		PR:               42,
		BestBranch:       "auto-improve/best",
		TaskPromptSource: "pr",
		RepoRoot:         t.TempDir(),
		RunCtx:           rc,
	})
	require.NoError(t, err)
	assert.NotContains(t, res.Response.TaskPackage.ReconstructedTaskPrompt, "### Changed Tests")
	assert.NotContains(t, res.Response.TaskPackage.ReconstructedTaskPrompt, "### Diff Excerpt")
	assert.Zero(t, git.changedFilesCalls)
	assert.Zero(t, git.diffCalls)
}

func TestRun_TaskPromptSourceIssueSkipsDiffFetch(t *testing.T) {
	rc := newRunCtx(t)
	git := newStubGit()
	git.resolvedBy[testMergeCommitOID+"^1"] = testBaseSHA
	git.changedFilesErr = errors.New("should not be called")
	git.diffErr = errors.New("should not be called")
	runner := &Runner{
		GH: stubGH{info: PRInfo{
			Number:         42,
			Title:          "improve X",
			Body:           "see linked issue",
			MergeCommitOID: testMergeCommitOID,
			LinkedIssues:   []LinkedIssue{{Number: 7, Title: "issue title", Body: "issue goal"}},
		}},
		Git: git,
	}

	res, err := runner.Run(context.Background(), Input{
		PR:               42,
		BestBranch:       "auto-improve/best",
		TaskPromptSource: "issue",
		RepoRoot:         t.TempDir(),
		RunCtx:           rc,
	})
	require.NoError(t, err)
	assert.Contains(t, res.Response.TaskPackage.ReconstructedTaskPrompt, "### Linked Issues")
	assert.Zero(t, git.changedFilesCalls)
	assert.Zero(t, git.diffCalls)
}

func TestRun_TaskPromptSourceIssueRequiresUsableIssue(t *testing.T) {
	rc := newRunCtx(t)
	git := newStubGit()
	git.resolvedBy[testMergeCommitOID+"^1"] = testBaseSHA
	runner := &Runner{
		GH: stubGH{info: PRInfo{
			Number:         42,
			Title:          "improve X",
			Body:           "see linked issue",
			MergeCommitOID: testMergeCommitOID,
			LinkedIssues:   []LinkedIssue{{Number: 7, Title: "issue title", Body: "[issue #7: fetch failed]"}},
		}},
		Git: git,
	}

	_, err := runner.Run(context.Background(), Input{
		PR:               42,
		BestBranch:       "auto-improve/best",
		TaskPromptSource: "issue",
		RepoRoot:         t.TempDir(),
		RunCtx:           rc,
	})
	require.ErrorContains(t, err, "task_prompt.source=issue requires at least one usable linked issue")
	assert.Zero(t, git.changedFilesCalls)
	assert.Zero(t, git.diffCalls)
}

func TestRun_TaskPromptSourceDiffSynthRequiresImmutableMergedDiff(t *testing.T) {
	rc := newRunCtx(t)
	git := newStubGit()
	git.mergeBase[testBaseSHA+"::"+testBaseRefOID] = testBaseSHA
	runner := &Runner{
		GH: stubGH{info: PRInfo{
			Number:     42,
			Title:      "improve X",
			Body:       "body text",
			State:      "MERGED",
			BaseRefOid: testBaseRefOID,
			HeadRefOid: testBaseSHA,
		}},
		Git: git,
	}

	_, err := runner.Run(context.Background(), Input{
		PR:               42,
		BestBranch:       "auto-improve/best",
		TaskPromptSource: "diff_synth",
		RepoRoot:         t.TempDir(),
		RunCtx:           rc,
	})
	require.ErrorContains(t, err, "diff_synth requires an immutable merged diff source")
}

func TestRun_RebaseMergedPR_WithoutImmutableBaseFailsClosed(t *testing.T) {
	rc := newRunCtx(t)
	runner := &Runner{
		GH: stubGH{info: PRInfo{
			Number:     42,
			Title:      "improve X",
			State:      "MERGED",
			BaseRefOid: testBaseRefOID,
			HeadRefOid: testBaseSHA,
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
	require.ErrorContains(t, err, "recover immutable base")
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
			case slices.Equal(args, []string{"-C", repoRoot, "remote", "get-url", "origin"}):
				return []byte("git@github.com:owner/repo.git\n"), nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "fetch", "--no-tags", "origin", testMergeCommitOID}):
				return nil, nil, nil
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
		PR:               42,
		BestBranch:       "auto-improve/best",
		TaskPromptSource: "pr",
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
	firstPath := filepath.Join(rc.WorktreeBase, fmt.Sprintf("%s-pass1-a1", rc.RunID))
	require.NoError(t, os.MkdirAll(firstPath, 0o755))

	git := gitCLI{
		stat: os.Stat,
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			switch {
			case slices.Equal(args, []string{"-C", repoRoot, "remote", "get-url", "origin"}):
				return []byte("git@github.com:owner/repo.git\n"), nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "fetch", "--no-tags", "origin", testMergeCommitOID}):
				return nil, nil, nil
			case slices.Equal(args, []string{"-C", repoRoot, "rev-parse", testMergeCommitOID + "^1"}):
				return []byte(testBaseSHA + "\n"), nil, nil
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
		GH:  stubGH{info: PRInfo{Number: 42, Title: "improve X", MergeCommitOID: testMergeCommitOID}},
		Git: git,
	}
	in := Input{
		PR:               42,
		BestBranch:       "auto-improve/best",
		TaskPromptSource: "pr",
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

func TestGHCLIPRView_IssueViewFailureIsBestEffort(t *testing.T) {
	gh := ghCLI{
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			require.Equal(t, "gh", name)
			switch {
			case slices.Equal(args[:2], []string{"pr", "view"}):
				return []byte(`{"number":42,"title":"PR","body":"body","state":"MERGED","closingIssuesReferences":[{"number":7,"title":"Issue title"}]}`), nil, nil
			case slices.Equal(args[:2], []string{"issue", "view"}):
				return nil, []byte("boom"), errors.New("exit status 1")
			default:
				t.Fatalf("unexpected gh args: %v", args)
				return nil, nil, nil
			}
		},
	}

	info, err := gh.PRView(context.Background(), 42, "")
	require.NoError(t, err)
	require.Len(t, info.LinkedIssues, 1)
	assert.Equal(t, 7, info.LinkedIssues[0].Number)
	assert.Equal(t, "Issue title", info.LinkedIssues[0].Title)
	assert.Equal(t, "[issue #7: fetch failed]", info.LinkedIssues[0].Body)
}

func TestGHCLIPRView_CapsIssueBodySize(t *testing.T) {
	largeBody := strings.Repeat("x", issueBodyMaxBytes+1024)
	gh := ghCLI{
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			require.Equal(t, "gh", name)
			switch {
			case slices.Equal(args[:2], []string{"pr", "view"}):
				return []byte(`{"number":42,"title":"PR","body":"body","state":"MERGED","closingIssuesReferences":[{"number":7,"title":"Issue title"}]}`), nil, nil
			case slices.Equal(args[:2], []string{"issue", "view"}):
				return []byte(fmt.Sprintf(`{"number":7,"title":"Issue title","body":"%s"}`, largeBody)), nil, nil
			default:
				t.Fatalf("unexpected gh args: %v", args)
				return nil, nil, nil
			}
		},
	}

	info, err := gh.PRView(context.Background(), 42, "")
	require.NoError(t, err)
	require.Len(t, info.LinkedIssues, 1)
	assert.LessOrEqual(t, len(info.LinkedIssues[0].Body), issueBodyMaxBytes)
}

func TestGHCLIPRView_CapsLinkedIssueFetchesAtTen(t *testing.T) {
	var issueCalls int
	refs := make([]string, 0, 12)
	for i := 1; i <= 12; i++ {
		refs = append(refs, fmt.Sprintf(`{"number":%d,"title":"Issue %d"}`, i, i))
	}
	gh := ghCLI{
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			require.Equal(t, "gh", name)
			switch {
			case slices.Equal(args[:2], []string{"pr", "view"}):
				return []byte(fmt.Sprintf(`{"number":42,"title":"PR","body":"body","state":"MERGED","closingIssuesReferences":[%s]}`, strings.Join(refs, ","))), nil, nil
			case slices.Equal(args[:2], []string{"issue", "view"}):
				issueCalls++
				return []byte(`{"number":7,"title":"Issue","body":"body"}`), nil, nil
			default:
				t.Fatalf("unexpected gh args: %v", args)
				return nil, nil, nil
			}
		},
	}

	info, err := gh.PRView(context.Background(), 42, "")
	require.NoError(t, err)
	assert.Len(t, info.LinkedIssues, 10)
	assert.Equal(t, 10, issueCalls)
}

func TestGHCLIPRView_StopsFetchingWhenPromptBudgetIsExhausted(t *testing.T) {
	var issueCalls int
	gh := ghCLI{
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			require.Equal(t, "gh", name)
			switch {
			case slices.Equal(args[:2], []string{"pr", "view"}):
				return []byte(fmt.Sprintf(`{"number":42,"title":"PR","body":"%s","state":"MERGED","closingIssuesReferences":[{"number":7,"title":"Issue title"}]}`, strings.Repeat("x", reconstructedPromptMaxBytes))), nil, nil
			case slices.Equal(args[:2], []string{"issue", "view"}):
				issueCalls++
				return []byte(`{"number":7,"title":"Issue","body":"body"}`), nil, nil
			default:
				t.Fatalf("unexpected gh args: %v", args)
				return nil, nil, nil
			}
		},
	}

	info, err := gh.PRView(context.Background(), 42, "")
	require.NoError(t, err)
	assert.Empty(t, info.LinkedIssues)
	assert.Zero(t, issueCalls)
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

func TestReconstructTaskPrompt_CapsTotalSize(t *testing.T) {
	body := strings.Repeat("b", reconstructedPromptMaxBytes)
	issues := []LinkedIssue{{Number: 7, Title: "issue", Body: strings.Repeat("i", reconstructedPromptMaxBytes)}}

	got := ReconstructTaskPrompt(42, "hello", body, issues)
	assert.LessOrEqual(t, len(got), reconstructedPromptMaxBytes)
	assert.True(t, strings.HasSuffix(got, "\n"))
}

func TestSynthesizeTaskBrief_AutoWithoutIssuesIncludesDiffContext(t *testing.T) {
	got := SynthesizeTaskBrief("auto", TaskBriefInput{
		PR:           42,
		Title:        "hello",
		Body:         "Implement the new behavior.",
		ChangedFiles: []string{"internal/foo.go", "internal/foo_test.go"},
		Diff:         "diff --git a/internal/foo.go b/internal/foo.go\n+new behavior\n",
	})
	assert.Contains(t, got, "## Goal")
	assert.Contains(t, got, "### Changed Tests")
	assert.Contains(t, got, "internal/foo_test.go")
	assert.Contains(t, got, "### Diff Excerpt")
	assert.NotContains(t, got, "### Linked Issues")
}

func TestSynthesizeTaskBrief_IssueSourceIncludesIssuesAndSkipsDiffContext(t *testing.T) {
	got := SynthesizeTaskBrief("issue", TaskBriefInput{
		PR:    42,
		Title: "hello",
		Body:  "Implement the new behavior.",
		Issues: []LinkedIssue{
			{Number: 7, Title: "issue title", Body: "issue body"},
		},
		ChangedFiles: []string{"internal/foo.go", "internal/foo_test.go"},
		Diff:         "diff --git a/internal/foo.go b/internal/foo.go\n+new behavior\n",
	})
	assert.Contains(t, got, "### Linked Issues")
	assert.Contains(t, got, "#7: issue title")
	assert.Contains(t, got, "### Weak Supporting PR Context")
	assert.NotContains(t, got, "### PR Body")
	assert.NotContains(t, got, "### Diff Excerpt")
	assert.Contains(t, got, "- issue body")
}

func TestSynthesizeTaskBrief_AutoWithUsableIssuesAlsoKeepsDiffContext(t *testing.T) {
	got := SynthesizeTaskBrief("auto", TaskBriefInput{
		PR:    42,
		Title: "hello",
		Body:  "see linked issue",
		Issues: []LinkedIssue{
			{Number: 7, Title: "issue title", Body: "Issue body goal."},
		},
		ChangedFiles: []string{"tests/test_api.py", "app/service.py"},
		Diff:         "diff --git a/tests/test_api.py b/tests/test_api.py\n+assert True\n",
	})
	assert.Contains(t, got, "### Linked Issues")
	assert.Contains(t, got, "### Changed Tests")
	assert.Contains(t, got, "tests/test_api.py")
	assert.Contains(t, got, "### Diff Excerpt")
	assert.Contains(t, got, "- Issue body goal.")
}

func TestSynthesizeTaskBrief_AutoIgnoresPlaceholderIssuesAndFallsBackToDiff(t *testing.T) {
	got := SynthesizeTaskBrief("auto", TaskBriefInput{
		PR:    42,
		Title: "hello",
		Body:  "Implement the new behavior.",
		Issues: []LinkedIssue{
			{Number: 7, Title: "issue title", Body: "[issue #7: fetch failed]"},
		},
		ChangedFiles: []string{"spec/bar_spec.rb", "app/service.rb"},
		Diff:         "diff --git a/spec/bar_spec.rb b/spec/bar_spec.rb\n+expect(true)\n",
	})
	assert.NotContains(t, got, "### Linked Issues")
	assert.Contains(t, got, "### Changed Tests")
	assert.Contains(t, got, "spec/bar_spec.rb")
}

func TestSynthesizeTaskBrief_DiffSynthUsesDiffEvidenceAsGoal(t *testing.T) {
	got := SynthesizeTaskBrief("diff_synth", TaskBriefInput{
		PR:           42,
		Title:        "misleading title",
		Body:         "Misleading PR body.",
		ChangedFiles: []string{"tests/test_api.py", "app/service.py"},
		Diff:         "diff --git a/tests/test_api.py b/tests/test_api.py\n+assert True\n",
	})
	assert.Contains(t, got, "- Recreate the intended behavior covered by the changed tests and merged diff evidence.")
	assert.Contains(t, got, "### Weak Supporting PR Context")
	assert.NotContains(t, got, "### PR Body")
}

func TestTruncateUTF8Bytes_PreservesRuneBoundaries(t *testing.T) {
	value := "abcあ"
	truncated := truncateUTF8Bytes(value, len("abc")+1)
	assert.Equal(t, "abc", truncated)
	assert.True(t, utf8.ValidString(truncated))
}
