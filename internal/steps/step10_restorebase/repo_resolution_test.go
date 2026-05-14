package step10restorebase

import (
	"context"
	"errors"
	"testing"

	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestRun_RejectsConfiguredRepoWhenLocalSlugCannotBeResolved(t *testing.T) {
	rc := newRunCtx(t)
	repoRoot := t.TempDir()
	git := newStubGit()
	git.repoSlugErr = errors.New("origin unavailable")
	runner := &Runner{
		GH:  &recordingGH{info: PRInfo{Number: 42, Title: "improve X", MergeCommitOID: testMergeCommitOID}},
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
	require.Error(t, err)
	assert.Contains(t, err.Error(), "origin unavailable")
}

func TestRun_RejectsConfiguredRepoWhenLocalSlugIsEmpty(t *testing.T) {
	rc := newRunCtx(t)
	repoRoot := t.TempDir()
	git := newStubGit()
	git.repoSlugByRoot[repoRoot] = ""
	runner := &Runner{
		GH:  &recordingGH{info: PRInfo{Number: 42, Title: "improve X", MergeCommitOID: testMergeCommitOID}},
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
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolved repo slug is empty")
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
	assert.Equal(t, 4, res.Response.WorktreesCreated)
	assert.Contains(t, git.fetched, in.RepoRoot+"::"+testBaseSHA)
	assert.Contains(t, git.fetched, in.RepoRoot+"::"+testBaseRefOID)
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
	assert.Contains(t, err.Error(), "code_base_sha="+testBaseSHA)
}

func TestRun_PersistedBaseSHAMatchesCodeBase(t *testing.T) {
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
