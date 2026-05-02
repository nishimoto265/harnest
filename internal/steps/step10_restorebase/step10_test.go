package step10restorebase

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_HappyPath_PassBaseFanout(t *testing.T) {
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
	assert.Equal(t, 4, resp.WorktreesCreated)
	assert.Equal(t, testBaseSHA, resp.BaseSHA)
	assert.Equal(t, rc.RunID, resp.RunID)
	assert.Len(t, resp.TaskPackage.Worktrees, 6)
	assert.Len(t, resp.TaskPackage.PassBases, 2)
	assert.Equal(t, "auto-improve/"+string(rc.RunID)+"/pass1/base", resp.TaskPackage.PassBases[0].Branch)
	assert.Equal(t, "auto-improve/"+string(rc.RunID)+"/pass2/base", resp.TaskPackage.PassBases[1].Branch)

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
	assert.Contains(t, reloaded.ReconstructedTaskPrompt, "# Task")
	assert.Contains(t, reloaded.ReconstructedTaskPrompt, "## Task Content")
	assert.Contains(t, reloaded.ReconstructedTaskPrompt, "### Linked Issues")
	assert.Contains(t, reloaded.ReconstructedTaskPrompt, "### PR Context")
	assert.Contains(t, reloaded.ReconstructedTaskPrompt, "### Changed Tests")
	assert.NotContains(t, reloaded.ReconstructedTaskPrompt, "### Diff Excerpt")
}

func TestRun_UsesCodeBaseForWorktreeBaseAndTaskDiff(t *testing.T) {
	rc := newRunCtx(t)
	repoRoot := t.TempDir()
	git := newStubGit()
	git.resolvedBy[testMergeCommitOID+"^1"] = testBaseSHA
	git.changedFiles = []string{"app/error.tsx"}
	git.diffText = "diff --git a/app/error.tsx b/app/error.tsx\n+error UI\n"
	runner := &Runner{
		GH: stubGH{info: PRInfo{
			Number:         42,
			Title:          "improve error UI",
			BaseRefOid:     testBaseRefOID,
			HeadRefOid:     testBaseSHA,
			MergeCommitOID: testMergeCommitOID,
		}},
		Git: git,
	}

	res, err := runner.Run(context.Background(), Input{
		PR:               42,
		BestBranch:       "auto-improve/best",
		TaskPromptSource: string(TaskPromptSourceAuto),
		RepoRoot:         repoRoot,
		RunCtx:           rc,
	})
	require.NoError(t, err)

	assert.Equal(t, testBaseSHA, res.Response.BaseSHA)
	for _, worktree := range res.Response.TaskPackage.Worktrees {
		assert.Equal(t, testBaseSHA, worktree.BaseSHA)
		assert.Equal(t, testBaseSHA, worktree.HeadSHA)
	}
	assert.Empty(t, git.fetchedBranches)
	assert.Equal(t, 1, git.changedFilesCalls)
	assert.Equal(t, 1, git.diffCalls)
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
	require.Equal(t, 4, first.Response.WorktreesCreated)

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
