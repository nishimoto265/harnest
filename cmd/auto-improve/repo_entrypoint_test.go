package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepoEntrypointProcessesCommaSeparatedPRs(t *testing.T) {
	runtime := testRepoEntrypointRuntime(t)
	cloneCalled := false
	stub := &stubPipelineRunner{}
	originalBootstrap := repoEntrypointBootstrap
	originalEnsureClone := repoEntrypointEnsureClone
	originalPRFiles := repoEntrypointPRFiles
	originalNewPipelineRunner := newPipelineRunner
	repoEntrypointBootstrap = func(context.Context, string) (repoEntrypointRuntime, error) {
		return runtime, nil
	}
	repoEntrypointEnsureClone = func(context.Context, repoEntrypointRuntime) error {
		cloneCalled = true
		return nil
	}
	repoEntrypointPRFiles = func(context.Context, string, int) ([]string, error) {
		return []string{"internal/app.go"}, nil
	}
	newPipelineRunner = func(*config.Config) (pipelineRunner, error) {
		return stub, nil
	}
	t.Cleanup(func() {
		repoEntrypointBootstrap = originalBootstrap
		repoEntrypointEnsureClone = originalEnsureClone
		repoEntrypointPRFiles = originalPRFiles
		newPipelineRunner = originalNewPipelineRunner
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"https://github.com/owner/repo", "--pr", "85,90,93"})

	require.NoError(t, cmd.Execute())
	assert.True(t, cloneCalled)
	assert.Equal(t, []int{85, 90, 93}, stub.prs)
}

func TestRepoEntrypointExplicitPRSkipsDocsOnly(t *testing.T) {
	runtime := testRepoEntrypointRuntime(t)
	stub := &stubPipelineRunner{}
	originalBootstrap := repoEntrypointBootstrap
	originalEnsureClone := repoEntrypointEnsureClone
	originalPRFiles := repoEntrypointPRFiles
	originalNewPipelineRunner := newPipelineRunner
	repoEntrypointBootstrap = func(context.Context, string) (repoEntrypointRuntime, error) {
		return runtime, nil
	}
	repoEntrypointEnsureClone = func(context.Context, repoEntrypointRuntime) error {
		return nil
	}
	repoEntrypointPRFiles = func(_ context.Context, _ string, pr int) ([]string, error) {
		if pr == 90 {
			return []string{"README.md", "docs/setup.md"}, nil
		}
		return []string{"internal/app.go"}, nil
	}
	newPipelineRunner = func(*config.Config) (pipelineRunner, error) {
		return stub, nil
	}
	t.Cleanup(func() {
		repoEntrypointBootstrap = originalBootstrap
		repoEntrypointEnsureClone = originalEnsureClone
		repoEntrypointPRFiles = originalPRFiles
		newPipelineRunner = originalNewPipelineRunner
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"https://github.com/owner/repo", "--pr", "85,90"})

	require.NoError(t, cmd.Execute())
	assert.Equal(t, []int{85}, stub.prs)
}

func TestRepoEntrypointRejectsPRAndLimit(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"https://github.com/owner/repo", "--pr", "85,90", "--limit", "2"})

	err := cmd.Execute()

	require.Error(t, err)
	assertCommandExitCode(t, err, 2)
	assert.Contains(t, err.Error(), "--pr and --limit are mutually exclusive")
}

func TestRepoEntrypointLimitRunsSelectedCandidates(t *testing.T) {
	runtime := testRepoEntrypointRuntime(t)
	stub := &stubPipelineRunner{}
	originalBootstrap := repoEntrypointBootstrap
	originalEnsureClone := repoEntrypointEnsureClone
	originalCandidates := repoEntrypointCandidates
	originalNewPipelineRunner := newPipelineRunner
	repoEntrypointBootstrap = func(context.Context, string) (repoEntrypointRuntime, error) {
		return runtime, nil
	}
	repoEntrypointEnsureClone = func(context.Context, repoEntrypointRuntime) error {
		return nil
	}
	repoEntrypointCandidates = func(context.Context, config.Config, string) ([]repoCandidate, []repoSkippedPR, error) {
		return []repoCandidate{{Number: 101}, {Number: 102}, {Number: 103}}, nil, nil
	}
	newPipelineRunner = func(*config.Config) (pipelineRunner, error) {
		return stub, nil
	}
	t.Cleanup(func() {
		repoEntrypointBootstrap = originalBootstrap
		repoEntrypointEnsureClone = originalEnsureClone
		repoEntrypointCandidates = originalCandidates
		newPipelineRunner = originalNewPipelineRunner
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"https://github.com/owner/repo", "--limit", "2"})

	require.NoError(t, cmd.Execute())
	assert.Equal(t, []int{101, 102}, stub.prs)
}

func TestRepoEntrypointDefaultWatchRunsOneTick(t *testing.T) {
	runtime := testRepoEntrypointRuntime(t)
	stub := &stubPipelineRunner{}
	sleepCalled := false
	originalBootstrap := repoEntrypointBootstrap
	originalEnsureClone := repoEntrypointEnsureClone
	originalCandidates := repoEntrypointCandidates
	originalSleep := repoEntrypointSleep
	originalNewPipelineRunner := newPipelineRunner
	repoEntrypointBootstrap = func(context.Context, string) (repoEntrypointRuntime, error) {
		return runtime, nil
	}
	repoEntrypointEnsureClone = func(context.Context, repoEntrypointRuntime) error {
		return nil
	}
	repoEntrypointCandidates = func(context.Context, config.Config, string) ([]repoCandidate, []repoSkippedPR, error) {
		return []repoCandidate{{Number: 301}}, nil, nil
	}
	repoEntrypointSleep = func(context.Context, time.Duration) error {
		sleepCalled = true
		return context.Canceled
	}
	newPipelineRunner = func(*config.Config) (pipelineRunner, error) {
		return stub, nil
	}
	t.Cleanup(func() {
		repoEntrypointBootstrap = originalBootstrap
		repoEntrypointEnsureClone = originalEnsureClone
		repoEntrypointCandidates = originalCandidates
		repoEntrypointSleep = originalSleep
		newPipelineRunner = originalNewPipelineRunner
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"https://github.com/owner/repo"})

	require.NoError(t, cmd.Execute())
	assert.True(t, sleepCalled)
	assert.Equal(t, []int{301}, stub.prs)
}

func TestRepoEntrypointDryRunPrintsPlanWithoutCloneOrRun(t *testing.T) {
	runtime := testRepoEntrypointRuntime(t)
	cloneCalled := false
	runnerCreated := false
	originalBootstrap := repoEntrypointBootstrap
	originalEnsureClone := repoEntrypointEnsureClone
	originalCandidates := repoEntrypointCandidates
	originalNewPipelineRunner := newPipelineRunner
	repoEntrypointBootstrap = func(context.Context, string) (repoEntrypointRuntime, error) {
		return runtime, nil
	}
	repoEntrypointEnsureClone = func(context.Context, repoEntrypointRuntime) error {
		cloneCalled = true
		return nil
	}
	repoEntrypointCandidates = func(context.Context, config.Config, string) ([]repoCandidate, []repoSkippedPR, error) {
		return []repoCandidate{{Number: 201, Title: "code change"}, {Number: 203, Title: "config change"}},
			[]repoSkippedPR{{Number: 202, Title: "docs", Reason: "docs_only"}}, nil
	}
	newPipelineRunner = func(*config.Config) (pipelineRunner, error) {
		runnerCreated = true
		return &stubPipelineRunner{}, nil
	}
	t.Cleanup(func() {
		repoEntrypointBootstrap = originalBootstrap
		repoEntrypointEnsureClone = originalEnsureClone
		repoEntrypointCandidates = originalCandidates
		newPipelineRunner = originalNewPipelineRunner
	})

	var stdout bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"https://github.com/owner/repo", "--limit", "1", "--dry-run"})

	require.NoError(t, cmd.Execute())
	assert.False(t, cloneCalled)
	assert.False(t, runnerCreated)
	var plan repoEntrypointPlan
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &plan))
	assert.Equal(t, "repo_entrypoint_dry_run", plan.Event)
	assert.Equal(t, "limit", plan.Mode)
	require.Len(t, plan.Selected, 1)
	assert.Equal(t, 201, plan.Selected[0].Number)
	require.Len(t, plan.Skipped, 1)
	assert.Equal(t, "docs_only", plan.Skipped[0].Reason)
}

func TestRepoEntrypointDryRunPRPrintsSelectedAndSkipped(t *testing.T) {
	runtime := testRepoEntrypointRuntime(t)
	originalBootstrap := repoEntrypointBootstrap
	originalEnsureClone := repoEntrypointEnsureClone
	originalPRFiles := repoEntrypointPRFiles
	originalNewPipelineRunner := newPipelineRunner
	repoEntrypointBootstrap = func(context.Context, string) (repoEntrypointRuntime, error) {
		return runtime, nil
	}
	repoEntrypointEnsureClone = func(context.Context, repoEntrypointRuntime) error {
		t.Fatal("dry-run must not clone")
		return nil
	}
	repoEntrypointPRFiles = func(_ context.Context, _ string, pr int) ([]string, error) {
		if pr == 90 {
			return []string{"README.md"}, nil
		}
		return []string{"internal/app.go"}, nil
	}
	newPipelineRunner = func(*config.Config) (pipelineRunner, error) {
		t.Fatal("dry-run must not create a runner")
		return &stubPipelineRunner{}, nil
	}
	t.Cleanup(func() {
		repoEntrypointBootstrap = originalBootstrap
		repoEntrypointEnsureClone = originalEnsureClone
		repoEntrypointPRFiles = originalPRFiles
		newPipelineRunner = originalNewPipelineRunner
	})

	var stdout bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"https://github.com/owner/repo", "--pr", "85,90", "--dry-run"})

	require.NoError(t, cmd.Execute())
	var plan repoEntrypointPlan
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &plan))
	assert.Equal(t, "pr", plan.Mode)
	assert.Equal(t, []int{85, 90}, plan.PRs)
	require.Len(t, plan.Selected, 1)
	assert.Equal(t, 85, plan.Selected[0].Number)
	require.Len(t, plan.Skipped, 1)
	assert.Equal(t, 90, plan.Skipped[0].Number)
	assert.Equal(t, "docs_only", plan.Skipped[0].Reason)
}

func TestRepoEntrypointCandidateSelectionSkipsDocsOnly(t *testing.T) {
	originalMergedPRs := repoEntrypointMergedPRs
	originalPRFiles := repoEntrypointPRFiles
	repoEntrypointMergedPRs = func(context.Context, string, string, string) ([]repoCandidate, error) {
		return []repoCandidate{{Number: 401, Title: "docs"}, {Number: 402, Title: "code"}}, nil
	}
	repoEntrypointPRFiles = func(_ context.Context, _ string, pr int) ([]string, error) {
		if pr == 401 {
			return []string{"README.md", "docs/setup.md"}, nil
		}
		return []string{"README.md", "internal/app.go"}, nil
	}
	t.Cleanup(func() {
		repoEntrypointMergedPRs = originalMergedPRs
		repoEntrypointPRFiles = originalPRFiles
	})

	candidates, skipped, err := selectRepoEntrypointCandidates(context.Background(), config.Config{Repo: config.RepoConfig{GitHub: "owner/repo", DefaultBranch: "main"}}, "/tmp/processed.jsonl")

	require.NoError(t, err)
	require.Len(t, candidates, 1)
	assert.Equal(t, 402, candidates[0].Number)
	require.Len(t, skipped, 1)
	assert.Equal(t, 401, skipped[0].Number)
	assert.Equal(t, "docs_only", skipped[0].Reason)
}

func testRepoEntrypointRuntime(t *testing.T) repoEntrypointRuntime {
	t.Helper()
	root := realTempDir(t)
	repoRoot := filepath.Join(root, "repos", "owner", "repo")
	runsPath := filepath.Join(root, "runs", "owner__repo", "runs")
	worktreePath := filepath.Join(root, "worktrees", "owner__repo", "worktrees")
	require.NoError(t, os.MkdirAll(runsPath, 0o755))
	require.NoError(t, os.MkdirAll(worktreePath, 0o755))
	cfg := config.Default().ForRepository(config.RepoConfig{
		GitHub:        "owner/repo",
		Root:          repoRoot,
		DefaultBranch: "main",
		BestBranch:    "auto-improve/best",
		PolicyBranch:  "auto-improve/policy",
	}, runsPath, worktreePath)
	require.NoError(t, cfg.Validate())
	runsBase, err := cfg.RunsBase()
	require.NoError(t, err)
	worktreeBase, err := cfg.WorktreeBase()
	require.NoError(t, err)
	return repoEntrypointRuntime{
		Config:        cfg,
		RepoURL:       "https://github.com/owner/repo",
		Repo:          "owner/repo",
		DefaultBranch: "main",
		RepoRoot:      repoRoot,
		RunsBase:      runsBase,
		WorktreeBase:  worktreeBase,
		Home:          root,
	}
}
