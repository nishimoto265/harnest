package step50_implement

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/candidaterules"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/policyrepo"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSynthesizeSuccessCommit_SetsIdentityUnderHardenedGitEnv(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))

	repo := t.TempDir()
	runCommand(t, "", "git", "init", "-b", "main", repo)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644))
	runCommand(t, repo, "git", "add", "README.md")
	runCommand(t, repo, "git", "-c", "user.name=Seed User", "-c", "user.email=seed@example.invalid", "commit", "-m", "base")
	runCommand(t, repo, "git", "checkout", "-b", "auto-improve/test/pass2/a1")
	baseSHA := strings.TrimSpace(runCommand(t, repo, "git", "rev-parse", "HEAD"))
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
		Pass:    2,
		Path:    repo,
		Branch:  "auto-improve/test/pass2/a1",
		BaseSHA: baseSHA,
		HeadSHA: baseSHA,
	}

	commitSHA, parent, err := synthesizeSuccessCommit(context.Background(), allocation, RunContext{
		IO:    runIO,
		Agent: "a1",
	})
	require.NoError(t, err)
	require.Equal(t, baseSHA, parent)

	commit := runCommand(t, repo, "git", "cat-file", "-p", commitSHA)
	require.Contains(t, commit, "author auto-improve <auto-improve@example.invalid>")
	require.Contains(t, commit, "committer auto-improve <auto-improve@example.invalid>")
}

func TestSynthesizeSuccessCommit_UnstagesPreStagedPolicyArtifacts(t *testing.T) {
	repo := t.TempDir()
	runCommand(t, "", "git", "init", "-b", "main", repo)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644))
	runCommand(t, repo, "git", "add", "README.md")
	runCommand(t, repo, "git", "-c", "user.name=Seed User", "-c", "user.email=seed@example.invalid", "commit", "-m", "base")
	runCommand(t, repo, "git", "checkout", "-b", "auto-improve/test/pass2/a1")
	baseSHA := strings.TrimSpace(runCommand(t, repo, "git", "rev-parse", "HEAD"))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".auto-improve", "lessons"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "auto-improve", "rules"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "implemented.txt"), []byte("implementation\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".auto-improve", "lessons", "r.md"), []byte("lesson\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "auto-improve", "rules-registry.jsonl"), []byte("{}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "auto-improve", "rules", "r.md"), []byte("rule\n"), 0o644))
	runCommand(t, repo, "git", "add", "-A")

	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	runIO, err := internalio.NewRunContext(runID, t.TempDir(), t.TempDir())
	require.NoError(t, err)
	allocation := contracts.WorktreeAllocation{
		Agent:   "a1",
		Pass:    2,
		Path:    repo,
		Branch:  "auto-improve/test/pass2/a1",
		BaseSHA: baseSHA,
		HeadSHA: baseSHA,
	}

	commitSHA, _, err := synthesizeSuccessCommit(context.Background(), allocation, RunContext{
		IO:    runIO,
		Agent: "a1",
	})
	require.NoError(t, err)

	files := runCommand(t, repo, "git", "diff-tree", "--no-commit-id", "--name-only", "-r", commitSHA)
	assert.Contains(t, files, "implemented.txt")
	assert.NotContains(t, files, ".auto-improve")
	assert.NotContains(t, files, "auto-improve/rules-registry.jsonl")
	assert.NotContains(t, files, "auto-improve/rules/r.md")
}

func TestRejectCommittedPolicyArtifactChangesFailsClosed(t *testing.T) {
	repo := t.TempDir()
	runCommand(t, "", "git", "init", "-b", "main", repo)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644))
	runCommand(t, repo, "git", "add", "README.md")
	runCommand(t, repo, "git", "-c", "user.name=Seed User", "-c", "user.email=seed@example.invalid", "commit", "-m", "base")
	baseSHA := strings.TrimSpace(runCommand(t, repo, "git", "rev-parse", "HEAD"))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "auto-improve", "rules"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "auto-improve", "rules", "r.md"), []byte("mutated\n"), 0o644))
	runCommand(t, repo, "git", "add", "auto-improve/rules/r.md")
	runCommand(t, repo, "git", "-c", "user.name=Agent", "-c", "user.email=agent@example.invalid", "commit", "-m", "mutate policy")

	err := rejectCommittedPolicyArtifactChanges(context.Background(), contracts.WorktreeAllocation{
		Path:    repo,
		BaseSHA: baseSHA,
	})

	require.Error(t, err)
	assert.ErrorContains(t, err, "committed policy artifact change is not allowed")
}

func TestStepRunIncludesRulePayloadsInPrompt(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	promptCapturePath := filepath.Join(t.TempDir(), "prompt.txt")
	t.Setenv("PROMPT_CAPTURE_FILE", promptCapturePath)

	const proposedBody = "# cand-1\nUse the candidate sidecar, not runsBase/rules.\n"
	rulesDir := filepath.Join(env.run.IO.RunsBase, "rules")
	require.NoError(t, os.MkdirAll(rulesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(rulesDir, "rule-abc.md"), []byte("stale registry body\n"), 0o644))

	candidate := writeCandidateSidecar(t, env.run.IO, contracts.Candidate{
		CandidateID:      "cand-1",
		Kind:             contracts.CandidateKindNew,
		Title:            "Add a new implementation rule",
		ProposedBodyPath: "40/candidates/cand-1.md",
	}, proposedBody)
	writeCandidatesFile(t, env.run.IO, []contracts.Candidate{candidate})

	err := (Step{}).Run(context.Background(), env.run)
	require.NoError(t, err)

	promptBytes, err := os.ReadFile(promptCapturePath)
	require.NoError(t, err)
	assert.Contains(t, string(promptBytes), "cand-1")
	assert.Contains(t, string(promptBytes), "kind: new")
	assert.Contains(t, string(promptBytes), "target_rule_id: (none)")
	assert.Contains(t, string(promptBytes), "Add a new implementation rule")
	assert.Contains(t, string(promptBytes), proposedBody)
	assert.NotContains(t, string(promptBytes), "stale registry body")
}

func TestRenderPrompt_IncludesActiveRulesAndNoPass1FailureOracle(t *testing.T) {
	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	promptText, err := RenderPrompt(PromptData{
		TaskPackage: contracts.TaskPackage{
			SchemaVersion:           "1",
			RunID:                   runID,
			PR:                      42,
			Title:                   "step50 test",
			BaseSHA:                 strings.Repeat("a", 40),
			BestBranch:              "best/main",
			ReconstructedTaskPrompt: "Implement the requested change safely.",
			CreatedAt:               time.Now().UTC(),
		},
		Agent:            "a1",
		CandidateRuleIDs: []string{"cand-1"},
		RulePayloads: []candidaterules.RulePayload{{
			ID:           "cand-1",
			Kind:         string(contracts.CandidateKindNew),
			Title:        "Candidate rule",
			ProposedBody: "## Proposed rule\n- Keep companion files in sync.",
		}},
		ActiveRules: []policyrepo.ActiveRule{{
			RuleID:   "r-existing",
			RulePath: "rules/r-existing.md",
			Body:     "Follow existing policy.",
		}},
		WorktreePath: "/tmp/worktree",
		Pass:         2,
	})
	require.NoError(t, err)

	assert.Contains(t, promptText, "Current Learned Rules")
	assert.Contains(t, promptText, "r-existing")
	assert.Contains(t, promptText, "Follow existing policy.")
	assert.Contains(t, promptText, "Experiment Lessons")
	assert.Contains(t, promptText, "Keep companion files in sync.")
	assert.Contains(t, promptText, "Write `checklist-result.json` at the worktree root.")
	assert.Contains(t, promptText, "`rule_id`: required string")
	assert.Contains(t, promptText, "`verdict`: required string, one of `compliant`, `n_a`, or `exception`")
	assert.Contains(t, promptText, "`rationale`: optional string for `compliant`/`n_a`; required and non-empty when `verdict` is `exception`")
	assert.Contains(t, promptText, "Do not use item keys like `id`, `status`, `result`, `description`, `file`, or `files`.")
	assert.Contains(t, promptText, `"items":[{"rule_id":"task-scope","verdict":"compliant"`)
	assert.NotContains(t, promptText, "make sure those pass1 failure statements are no longer true")
	assert.NotContains(t, promptText, "A pass2 output that repeats the same violated condition from pass1 is incorrect")
}

func TestStepRunSuccessDiffCapturesUntrackedFilesButSkipsChecklistArtifact(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)

	err := (Step{}).Run(context.Background(), env.run)
	require.NoError(t, err)

	manifest := readManifest(t, env.manifestPath)
	success, ok := manifest.Value.(contracts.ManifestSuccess)
	require.True(t, ok)

	diffBytes, readErr := os.ReadFile(filepath.Join(env.run.IO.RunDir(), success.DiffPath))
	require.NoError(t, readErr)
	assert.Contains(t, string(diffBytes), "implemented.txt")
	assert.NotContains(t, string(diffBytes), "checklist-result.json")
}

func TestStepRunRemovesStaleArtifactsOnNonSuccess(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-error.sh", 30)
	for _, rel := range []string{
		filepath.Join("50-pass2", "a1", "diff.patch"),
		filepath.Join("50-pass2", "a1", "session.jsonl"),
		filepath.Join("50-pass2", "a1", "checklist-result.json"),
	} {
		abs := filepath.Join(env.run.IO.RunDir(), rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
		require.NoError(t, os.WriteFile(abs, []byte("stale\n"), 0o644))
	}

	err := (Step{}).Run(context.Background(), env.run)
	require.NoError(t, err)

	manifest := readManifest(t, env.manifestPath)
	assert.Equal(t, contracts.ManifestKindError, manifest.Kind)
	diffPath := filepath.Join(env.run.IO.RunDir(), "50-pass2", "a1", "diff.patch")
	checklistPath := filepath.Join(env.run.IO.RunDir(), "50-pass2", "a1", "checklist-result.json")
	assert.FileExists(t, diffPath)
	assert.FileExists(t, checklistPath)
}

func TestCopyUntrackedFiles_SkipsSymlinksAndKeepsWhitespaceNames(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	worktree := env.run.TaskPackage.Worktrees[3].Path
	secretPath := filepath.Join(t.TempDir(), "id_rsa")
	require.NoError(t, os.WriteFile(secretPath, []byte("secret\n"), 0o600))
	require.NoError(t, os.Symlink(secretPath, filepath.Join(worktree, "loot")))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, "space name.txt"), []byte("hello\n"), 0o644))

	rescueDir := filepath.Join(t.TempDir(), "rescue")
	require.NoError(t, os.MkdirAll(filepath.Join(rescueDir, "untracked"), 0o755))

	budget := agentrunner.NewRescueArtifactBudget()
	artifacts, err := copyUntrackedFilesWithBudget(context.Background(), worktree, rescueDir, &budget)
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(rescueDir, "untracked", "loot"))
	assert.FileExists(t, filepath.Join(rescueDir, "untracked", "space name.txt"))
	assert.FileExists(t, filepath.Join(rescueDir, "untracked-symlinks.txt"))
	symlinkLog, err := os.ReadFile(filepath.Join(rescueDir, "untracked-symlinks.txt"))
	require.NoError(t, err)
	assert.Contains(t, string(symlinkLog), "loot")

	paths := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		paths = append(paths, artifact.Path)
	}
	assert.Contains(t, paths, "untracked/space name.txt")
	assert.Contains(t, paths, "untracked-symlinks.txt")
}

func TestWriteCommitBundle_ZeroCommitProducesEmptyBundle(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	rescueDir := t.TempDir()

	commitCount, bundleMode, err := writeCommitBundle(context.Background(), env.run.TaskPackage.Worktrees[3].Path, rescueDir, env.run.TaskPackage.BaseSHA)
	require.NoError(t, err)
	assert.Equal(t, 0, commitCount)
	assert.Equal(t, agentrunner.RescueBundleModeNone, bundleMode)

	bundlePath := filepath.Join(rescueDir, "commits.bundle")
	info, statErr := os.Stat(bundlePath)
	require.NoError(t, statErr)
	assert.EqualValues(t, 0, info.Size())
}

func TestWriteCommitBundle_FallsBackToFullHeadWhenBaseInvalid(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	worktree := env.run.TaskPackage.Worktrees[3].Path
	rescueDir := t.TempDir()

	commitCount, bundleMode, err := writeCommitBundle(context.Background(), worktree, rescueDir, strings.Repeat("f", 40))
	require.NoError(t, err)
	assert.Greater(t, commitCount, 0)
	assert.Equal(t, agentrunner.RescueBundleModeFullHead, bundleMode)

	bundlePath := filepath.Join(rescueDir, "commits.bundle")
	verifyOutput := runCommand(t, worktree, "git", "bundle", "verify", bundlePath)
	assert.Contains(t, verifyOutput, "is okay")
}

func TestVerifyExistingAllocationWorktreeIgnoresPolicyOverlay(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	worktreePath := filepath.Join(root, "worktree")
	baseSHA := initGitRepoWithWorktree(t, repoDir, worktreePath)

	allocation := contracts.WorktreeAllocation{
		Agent:   "a1",
		Pass:    2,
		Path:    worktreePath,
		Branch:  "test/pass2/a1",
		BaseSHA: baseSHA,
		HeadSHA: baseSHA,
	}
	require.NoError(t, os.MkdirAll(filepath.Join(worktreePath, ".auto-improve"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(worktreePath, ".auto-improve", "checklist.md"), []byte("# Checklist\n"), 0o644))
	require.NoError(t, verifyExistingAllocationWorktree(context.Background(), allocation))

	require.NoError(t, os.WriteFile(filepath.Join(worktreePath, "app.txt"), []byte("dirty\n"), 0o644))
	require.ErrorContains(t, verifyExistingAllocationWorktree(context.Background(), allocation), "existing worktree is dirty")
}
