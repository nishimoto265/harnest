package step20_implement

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/policyrepo"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteCommitBundleFallsBackToFullHeadWhenBaseIsUnreachable(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	baseSHA := initGitRepo(t, repo, "auto-improve/test/pass1/a1")
	require.NotEmpty(t, baseSHA)
	runGit(t, repo, "commit", "--allow-empty", "-m", "head")

	rescueDir := filepath.Join(root, "rescue")
	require.NoError(t, os.MkdirAll(rescueDir, 0o755))

	commitCount, bundleMode, err := writeCommitBundle(context.Background(), repo, rescueDir, strings.Repeat("f", 40))
	require.NoError(t, err)
	require.Equal(t, "full_head", bundleMode)
	require.Greater(t, commitCount, 0)
	require.FileExists(t, filepath.Join(rescueDir, "commits.bundle"))

	verifyOut := runGit(t, repo, "bundle", "verify", filepath.Join(rescueDir, "commits.bundle"))
	require.Contains(t, verifyOut, "is okay")
}

func TestWriteSuccessArtifacts_SynthesizesCommitWhenHeadHasNotAdvanced(t *testing.T) {
	fx := newTestFixture(t, 5)
	require.NoError(t, os.WriteFile(filepath.Join(fx.worktree, "README.md"), []byte("dirty worktree\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(fx.worktree, checklistFileName), []byte(`{"schema_version":"1","run_id":"2026-04-21-PR42-abcdef0","pass":1,"agent":"a1","items":[]}`), 0o644))

	err := fx.step.writeSuccessArtifacts(context.Background(), fx.run, fx.run.TaskPackage.Worktrees[0], runnerResult{
		StartedAt:  time.Now().Add(-time.Second).UTC(),
		FinishedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	manifest := fx.readManifest(t)
	success := manifest.Value.(contracts.ManifestSuccess)
	require.NotEqual(t, fx.baseSHA, success.HeadSHA)
	diffBytes, readErr := os.ReadFile(fx.diffPath())
	require.NoError(t, readErr)
	require.Contains(t, string(diffBytes), "README.md")
	require.Equal(t, success.HeadSHA, strings.TrimSpace(runGit(t, fx.worktree, "rev-parse", "HEAD")))
}

func TestSynthesizeSuccessCommit_SetsIdentityUnderHardenedGitEnv(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))

	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644))
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "-c", "user.name=Seed User", "-c", "user.email=seed@example.invalid", "commit", "-m", "base")
	runGit(t, repo, "checkout", "-b", "auto-improve/test/pass1/a1")
	baseSHA := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	localIdentity := exec.Command("git", "config", "--local", "--get", "user.email")
	localIdentity.Dir = repo
	localIdentityOut, localIdentityErr := localIdentity.CombinedOutput()
	require.Error(t, localIdentityErr, string(localIdentityOut))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("changed\n"), 0o644))

	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	runIO, err := internalio.NewRunContext(runID, t.TempDir(), t.TempDir())
	require.NoError(t, err)
	allocation := contracts.WorktreeAllocation{
		Agent:   "a1",
		Pass:    1,
		Path:    repo,
		Branch:  "auto-improve/test/pass1/a1",
		BaseSHA: baseSHA,
		HeadSHA: baseSHA,
	}

	commitSHA, parent, err := synthesizeSuccessCommit(context.Background(), allocation, RunContext{
		IO:    runIO,
		Agent: "a1",
	})
	require.NoError(t, err)
	require.Equal(t, baseSHA, parent)

	commit := runGit(t, repo, "cat-file", "-p", commitSHA)
	require.Contains(t, commit, "author auto-improve <auto-improve@example.invalid>")
	require.Contains(t, commit, "committer auto-improve <auto-improve@example.invalid>")
}

func TestSynthesizeSuccessCommit_UnstagesPreStagedPolicyArtifacts(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644))
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "-c", "user.name=Seed User", "-c", "user.email=seed@example.invalid", "commit", "-m", "base")
	runGit(t, repo, "checkout", "-b", "auto-improve/test/pass1/a1")
	baseSHA := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".auto-improve", "lessons"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "auto-improve", "rules"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "implemented.txt"), []byte("implementation\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".auto-improve", "lessons", "r.md"), []byte("lesson\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "auto-improve", "rules-registry.jsonl"), []byte("{}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "auto-improve", "rules", "r.md"), []byte("rule\n"), 0o644))
	runGit(t, repo, "add", "-A")

	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	runIO, err := internalio.NewRunContext(runID, t.TempDir(), t.TempDir())
	require.NoError(t, err)
	allocation := contracts.WorktreeAllocation{
		Agent:   "a1",
		Pass:    1,
		Path:    repo,
		Branch:  "auto-improve/test/pass1/a1",
		BaseSHA: baseSHA,
		HeadSHA: baseSHA,
	}

	commitSHA, _, err := synthesizeSuccessCommit(context.Background(), allocation, RunContext{
		IO:    runIO,
		Agent: "a1",
	})
	require.NoError(t, err)

	files := runGit(t, repo, "diff-tree", "--no-commit-id", "--name-only", "-r", commitSHA)
	assert.Contains(t, files, "implemented.txt")
	assert.NotContains(t, files, ".auto-improve")
	assert.NotContains(t, files, "auto-improve/rules-registry.jsonl")
	assert.NotContains(t, files, "auto-improve/rules/r.md")
}

func TestRejectCommittedPolicyArtifactChangesFailsClosed(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644))
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "-c", "user.name=Seed User", "-c", "user.email=seed@example.invalid", "commit", "-m", "base")
	baseSHA := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".auto-improve", "lessons"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".auto-improve", "lessons", "r.md"), []byte("mutated\n"), 0o644))
	runGit(t, repo, "add", ".auto-improve/lessons/r.md")
	runGit(t, repo, "-c", "user.name=Agent", "-c", "user.email=agent@example.invalid", "commit", "-m", "mutate policy")

	err := rejectCommittedPolicyArtifactChanges(context.Background(), contracts.WorktreeAllocation{
		Path:    repo,
		BaseSHA: baseSHA,
	})

	require.Error(t, err)
	assert.ErrorContains(t, err, "committed policy artifact change is not allowed")
}

func TestStepRun_PersistsTerminalSuccessAfterParentCancellationAndClearsLease(t *testing.T) {
	fx := newTestFixture(t, 5)
	agentDir := fx.agentDir

	ctx, cancel := context.WithCancel(context.Background())
	step := newStep(fx.cfg, stepOptions{
		now:               time.Now,
		heartbeatInterval: 10 * time.Millisecond,
		staleAfter:        time.Second,
		runner: cancelAfterSuccessRunner{
			cancel: cancel,
			runID:  fx.run.IO.RunID,
			agent:  fx.run.Agent,
		},
	})
	err := step.Run(ctx, fx.run)
	require.NoError(t, err)

	manifest := fx.readManifest(t)
	assert.Equal(t, contracts.ManifestKindSuccess, manifest.Kind)

	state, ok, err := loadResumeState(agentDir)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Zero(t, state.Pid)
	assert.Zero(t, state.Pgid)
	assert.Empty(t, state.LeaderStartTime)

	_, statErr := os.Stat(fx.heartbeatLeasePath())
	require.Error(t, statErr)
	assert.True(t, os.IsNotExist(statErr))
}

func TestCopyUntrackedFiles_SkipsFIFOWithinBoundedTime(t *testing.T) {
	fx := newTestFixture(t, 5)
	poisonPath := filepath.Join(fx.worktree, "poison")
	require.NoError(t, syscall.Mkfifo(poisonPath, 0o644))

	rescueDir := filepath.Join(t.TempDir(), "rescue")
	require.NoError(t, os.MkdirAll(filepath.Join(rescueDir, "untracked"), 0o755))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	budget := agentrunner.NewRescueArtifactBudget()
	artifacts, err := copyUntrackedFilesWithBudget(ctx, fx.worktree, rescueDir, &budget)
	require.NoError(t, err)
	assert.Less(t, time.Since(start), time.Second)
	assert.NoFileExists(t, filepath.Join(rescueDir, "untracked", "poison"))

	paths := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		paths = append(paths, artifact.Path)
	}
	assert.NotContains(t, paths, "untracked/poison")
}

func TestStepRun_FailsWhenSuccessDiffOverflows(t *testing.T) {
	fx := newTestFixture(t, 30)
	originalCollectSuccessDiffBytes := collectSuccessDiffBytes
	collectSuccessDiffBytes = func(context.Context, string, string, string) ([]byte, error) {
		return nil, agentrunner.ErrSuccessDiffOverflow
	}
	t.Cleanup(func() {
		collectSuccessDiffBytes = originalCollectSuccessDiffBytes
	})

	err := fx.step.Run(context.Background(), fx.run)
	require.Error(t, err)
	assert.ErrorIs(t, err, agentrunner.ErrSuccessDiffOverflow)
	assert.NoFileExists(t, fx.manifestPath())
}

func TestStepRun_FailsClosedOnFIFOChecklist(t *testing.T) {
	fx := newTestFixture(t, 5)
	t.Setenv("FAKE_SKIP_CHECKLIST", "1")
	t.Setenv("FAKE_CLAUDE_MKFIFO_CHECKLIST", "1")

	err := fx.step.Run(context.Background(), fx.run)
	require.Error(t, err)
	assert.ErrorIs(t, err, agentrunner.ErrArtifactNotRegular)
	assert.NoFileExists(t, fx.manifestPath())
}

func TestStepRun_RejectsForeignDetachedHead(t *testing.T) {
	fx := newTestFixture(t, 5)
	runGit(t, fx.worktree, "checkout", "main")
	require.NoError(t, os.WriteFile(filepath.Join(fx.worktree, "foreign.txt"), []byte("foreign\n"), 0o644))
	runGit(t, fx.worktree, "add", "foreign.txt")
	runGit(t, fx.worktree, "commit", "-m", "foreign commit")
	foreignSHA := strings.TrimSpace(runGit(t, fx.worktree, "rev-parse", "HEAD"))
	runGit(t, fx.worktree, "checkout", "auto-improve/"+string(fx.run.IO.RunID)+"/pass1/a1")

	t.Setenv("FAKE_CLAUDE_CHECKOUT_REF_BEFORE_EXIT", foreignSHA)

	err := fx.step.Run(context.Background(), fx.run)
	require.ErrorContains(t, err, "current branch mismatch")
}

func TestStepRunSuccessArtifactsHonorContextCancellation(t *testing.T) {
	fx := newTestFixture(t, 5)

	realGit, err := exec.LookPath("git")
	require.NoError(t, err)

	wrapperDir := t.TempDir()
	logPath := filepath.Join(wrapperDir, "git.log")
	writeFakeGitWrapper(t, wrapperDir)
	useFakeGitWrapper(t, filepath.Join(wrapperDir, "git"))
	t.Setenv("REAL_GIT", realGit)
	t.Setenv("FAKE_GIT_LOG", logPath)
	t.Setenv("FAKE_GIT_SLEEP_ON_SUBSTRING", " rev-parse HEAD")
	t.Setenv("FAKE_GIT_SLEEP_SECONDS", "5")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- fx.step.Run(ctx, fx.run)
	}()

	require.Eventually(t, func() bool {
		logBytes, readErr := os.ReadFile(logPath)
		if readErr != nil {
			return false
		}
		return strings.Contains(string(logBytes), "rev-parse HEAD")
	}, processTestEventuallyTimeout, 10*time.Millisecond)

	cancel()

	err = <-errCh
	require.NoError(t, err)

	manifest := fx.readManifest(t)
	assert.Equal(t, contracts.ManifestKindError, manifest.Kind)
	assert.NoFileExists(t, fx.diffPath())
}

func TestStepRun_GitCommandsIgnoreInheritedGitDir(t *testing.T) {
	fx := newTestFixture(t, 5)

	otherRepo := filepath.Join(t.TempDir(), "other-repo")
	otherBase := initGitRepo(t, otherRepo, "other/pass1/a1")
	runGit(t, otherRepo, "commit", "--allow-empty", "-m", "other-head")
	otherHead := strings.TrimSpace(runGit(t, otherRepo, "rev-parse", "HEAD"))
	require.NotEqual(t, otherBase, otherHead)

	t.Setenv("GIT_DIR", filepath.Join(otherRepo, ".git"))
	t.Setenv("GIT_WORK_TREE", otherRepo)
	t.Setenv("FAKE_CLAUDE_WRITE_FILE", filepath.Join(fx.worktree, "local-change.txt"))

	require.NoError(t, fx.step.Run(context.Background(), fx.run))

	manifest := fx.readManifest(t)
	success := manifest.Value.(contracts.ManifestSuccess)
	assert.NotEqual(t, fx.baseSHA, success.HeadSHA)
	assert.NotEqual(t, otherHead, success.HeadSHA)
}

func TestStepRun_RejectsTaskPackageRunIDMismatch(t *testing.T) {
	fx := newTestFixture(t, 5)
	fx.run.TaskPackage.RunID = contracts.RunID("2026-04-21-PR42-deadbee")

	err := fx.step.Run(context.Background(), fx.run)
	require.ErrorContains(t, err, "task package run_id mismatch")
	assert.NoFileExists(t, fx.manifestPath())
}

func TestVerifyExistingAllocationWorktreeIgnoresPolicyOverlay(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	worktreePath := filepath.Join(root, "worktree")
	require.NoError(t, os.MkdirAll(repoDir, 0o755))

	runGit(t, repoDir, "init", "-b", "main")
	runGit(t, repoDir, "config", "user.name", "Test User")
	runGit(t, repoDir, "config", "user.email", "test@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644))
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "base")
	baseSHA := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
	branch := "auto-improve/2026-04-21-PR42-abcdef0/pass1/a1"
	runGit(t, repoDir, "worktree", "add", "-b", branch, worktreePath, baseSHA)

	allocation := contracts.WorktreeAllocation{
		Agent:   "a1",
		Pass:    1,
		Path:    worktreePath,
		Branch:  branch,
		BaseSHA: baseSHA,
		HeadSHA: baseSHA,
	}
	require.NoError(t, os.MkdirAll(filepath.Join(worktreePath, ".auto-improve"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(worktreePath, ".auto-improve", "checklist.md"), []byte("# Checklist\n"), 0o644))
	require.NoError(t, verifyExistingAllocationWorktree(context.Background(), allocation))

	require.NoError(t, os.WriteFile(filepath.Join(worktreePath, "app.txt"), []byte("dirty\n"), 0o644))
	require.ErrorContains(t, verifyExistingAllocationWorktree(context.Background(), allocation), "existing worktree is dirty")
}

func TestRenderPrompt_UsesChecklistAtWorktreeRoot(t *testing.T) {
	fx := newTestFixture(t, 5)
	promptText, err := renderPrompt(fx.cfg, promptData{
		TaskPackage: fx.run.TaskPackage,
		Agent:       fx.run.Agent,
		OutputDir:   manifestPrefix(fx.run.Pass, fx.run.Agent),
		TaskPrompt:  "Implement the requested change.",
		ActiveRules: []policyrepo.ActiveRule{{
			RuleID:   "r-existing",
			RulePath: "rules/r-existing.md",
			Body:     "Follow existing policy.",
		}},
	})
	require.NoError(t, err)
	assert.Contains(t, promptText, "checklist_output_path: checklist-result.json")
	assert.Contains(t, promptText, "Write `checklist-result.json` at the worktree root.")
	assert.Contains(t, promptText, "`rule_id`: required string")
	assert.Contains(t, promptText, "`verdict`: required string, one of `compliant`, `n_a`, or `exception`")
	assert.Contains(t, promptText, "`rationale`: optional string for `compliant`/`n_a`; required and non-empty when `verdict` is `exception`")
	assert.Contains(t, promptText, "Do not use item keys like `id`, `status`, `result`, `description`, `file`, or `files`.")
	assert.Contains(t, promptText, `"items":[{"rule_id":"task-scope","verdict":"compliant"`)
	assert.Contains(t, promptText, "Do not create or overwrite `manifest.json`, `session.jsonl`, or `diff.patch` yourself.")
	assert.Contains(t, promptText, "Current Learned Rules")
	assert.Contains(t, promptText, "r-existing")
	assert.Contains(t, promptText, "Follow existing policy.")
}
