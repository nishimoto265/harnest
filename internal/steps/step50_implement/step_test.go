package step50_implement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/candidaterules"
	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/policyrepo"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStepRunTerminalVariants(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	tests := []struct {
		name           string
		script         string
		timeoutSeconds int
		wantKind       contracts.ManifestKind
		wantReason     string
		wantExitCode   int
		wantArtifacts  bool
	}{
		{
			name:           "success",
			script:         "fake-claude-success.sh",
			timeoutSeconds: 30,
			wantKind:       contracts.ManifestKindSuccess,
			wantArtifacts:  true,
		},
		{
			name:           "error",
			script:         "fake-claude-error.sh",
			timeoutSeconds: 30,
			wantKind:       contracts.ManifestKindError,
			wantReason:     "rate_limit",
			wantArtifacts:  false,
		},
		{
			name:           "signal",
			script:         "fake-claude-signal.sh",
			timeoutSeconds: 30,
			wantKind:       contracts.ManifestKindError,
			wantReason:     "signal",
			wantExitCode:   143,
			wantArtifacts:  false,
		},
		{
			name:           "timeout",
			script:         "fake-claude-timeout.sh",
			timeoutSeconds: 1,
			wantKind:       contracts.ManifestKindTimeout,
			wantArtifacts:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newStepTestEnv(t, tt.script, tt.timeoutSeconds)

			start := time.Now()
			err := (Step{}).Run(context.Background(), env.run)
			require.NoError(t, err)
			assert.Less(t, time.Since(start), 8*time.Second)

			manifest := readManifest(t, env.manifestPath)
			assert.Equal(t, tt.wantKind, manifest.Kind)

			switch tt.wantKind {
			case contracts.ManifestKindSuccess:
				success, ok := manifest.Value.(contracts.ManifestSuccess)
				require.True(t, ok)
				assert.Equal(t, filepath.Join("50-pass2", "a1", "diff.patch"), success.DiffPath)
				assert.Equal(t, filepath.Join("50-pass2", "a1", "session.jsonl"), success.SessionPath)
				assert.Equal(t, filepath.Join("50-pass2", "a1", "checklist-result.json"), success.ChecklistPath)

				diffPath := filepath.Join(env.run.IO.RunDir(), success.DiffPath)
				sessionPath := filepath.Join(env.run.IO.RunDir(), success.SessionPath)
				checklistPath := filepath.Join(env.run.IO.RunDir(), success.ChecklistPath)

				diffBytes, err := os.ReadFile(diffPath)
				require.NoError(t, err)
				assert.Contains(t, string(diffBytes), "implemented.txt")

				sessionBytes, err := os.ReadFile(sessionPath)
				require.NoError(t, err)
				assert.Contains(t, string(sessionBytes), `"event":"start"`)

				checklistBytes, err := os.ReadFile(checklistPath)
				require.NoError(t, err)
				assert.Contains(t, string(checklistBytes), `"run_id":"2026-04-21-PR42-abcdef0"`)
			case contracts.ManifestKindError:
				errorVariant, ok := manifest.Value.(contracts.ManifestError)
				require.True(t, ok)
				assert.Equal(t, tt.wantReason, errorVariant.Reason)
				if tt.wantExitCode != 0 {
					assert.Equal(t, tt.wantExitCode, errorVariant.ExitCode)
				}
				if tt.wantReason == "rate_limit" {
					assert.Contains(t, errorVariant.Detail, "rate_limit")
				}
			case contracts.ManifestKindTimeout:
				timeoutVariant, ok := manifest.Value.(contracts.ManifestTimeout)
				require.True(t, ok)
				assert.Equal(t, tt.timeoutSeconds, timeoutVariant.TimeoutSeconds)
			}

			assertArtifactPresence(t, env.run.IO.RunDir(), tt.wantArtifacts)
		})
	}
}

func TestStepRun_UsesConfiguredCodexImplementerProfile(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.yaml")
	agentsPath := filepath.Join(configDir, "agents.yaml")
	scriptPath := testScriptPath(t, "fake-claude-success.sh")

	require.NoError(t, os.WriteFile(configPath, []byte(fmt.Sprintf(`
repo:
  root: %q
  github: "owner/repo"
  default_branch: "main"
  best_branch: "best/main"
paths:
  runs: %q
worktree:
  base: %q
agent_config_path: "./agents.yaml"
claude_cli_path: "/does/not/exist"
`, env.repoDir, env.run.IO.RunsBase, env.run.IO.WorktreeBase)), 0o644))
	require.NoError(t, os.WriteFile(agentsPath, []byte(fmt.Sprintf(`
profiles:
  codex_impl:
    provider: codex
    binary: %q
  stub:
    provider: stub
roles:
  implementer: codex_impl
  judge_primary: stub
`, scriptPath)), 0o644))

	cfg, err := config.LoadConfig(configPath)
	require.NoError(t, err)
	env.run.Config = cfg

	err = (Step{}).Run(context.Background(), env.run)
	require.NoError(t, err)

	manifest := readManifest(t, env.manifestPath)
	assert.Equal(t, contracts.ManifestKindSuccess, manifest.Kind)
}

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

func TestStepRun_PersistsChildPIDAndPGIDInResumeState(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-timeout.sh", 30)
	t.Setenv("FAKE_SLEEP_SECONDS", "1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- (Step{}).Run(ctx, env.run)
	}()

	agentDir, err := agentDir(env.run.IO, 2, "a1")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		state, ok, err := loadResumeState(agentDir)
		if err != nil || !ok {
			return false
		}
		return state.Pid > 0 && state.Pgid > 0 && state.LeaderStartTime != ""
	}, time.Second, 10*time.Millisecond)

	state, ok, err := loadResumeState(agentDir)
	require.NoError(t, err)
	require.True(t, ok)
	assert.NotEqual(t, os.Getpid(), state.Pid)
	assert.NotZero(t, state.Pgid)
	assert.NotEmpty(t, state.LeaderStartTime)

	cancel()
	require.ErrorIs(t, <-errCh, context.Canceled)
}

func TestResumeIfNeeded_RequiresDeadPIDAsWellAsStaleHeartbeat(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	agentDir, err := agentDir(env.run.IO, 2, "a1")
	require.NoError(t, err)

	oldTime := time.Now().Add(-2 * time.Hour).UTC()
	currentPGID, err := syscall.Getpgid(os.Getpid())
	require.NoError(t, err)
	startTime, err := lookupLeaseStartTime(os.Getpid())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(agentDir, 0o755))
	require.NoError(t, saveResumeState(agentDir, resumeState{
		ExpectedBaseSHA: env.run.TaskPackage.BaseSHA,
		StartedAt:       oldTime,
		Pid:             os.Getpid(),
		Pgid:            currentPGID,
		LeaderStartTime: startTime,
		RetryCount:      1,
		LastHeartbeat:   oldTime,
	}))
	require.NoError(t, touchHeartbeat(agentDir, oldTime))

	allocation, err := worktreeFor(env.run.TaskPackage, 2, "a1")
	require.NoError(t, err)
	step := newStep(env.run.Config, stepOptions{
		now:        func() time.Time { return oldTime.Add(3 * time.Hour) },
		staleAfter: time.Second,
	})

	_, err = step.resumeIfNeeded(context.Background(), env.run, allocation, agentDir)
	require.ErrorIs(t, err, ErrRescueAbortedLeaseActive)
}

func TestResumeIfNeeded_RescuesRecycledPIDWhenLeaderStartTimeDiffers(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	agentDir, err := agentDir(env.run.IO, 2, "a1")
	require.NoError(t, err)
	allocation, err := worktreeFor(env.run.TaskPackage, 2, "a1")
	require.NoError(t, err)

	oldTime := time.Now().Add(-2 * time.Hour).UTC()
	currentPGID, err := syscall.Getpgid(os.Getpid())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(agentDir, 0o755))
	require.NoError(t, saveResumeState(agentDir, resumeState{
		ExpectedBaseSHA: env.run.TaskPackage.BaseSHA,
		StartedAt:       oldTime,
		Pid:             os.Getpid(),
		Pgid:            currentPGID,
		LeaderStartTime: "stale-start-time",
		RetryCount:      0,
		LastHeartbeat:   oldTime,
	}))
	require.NoError(t, touchHeartbeat(agentDir, oldTime))

	originalKill := killProcess
	originalGetpgid := getProcessGroupID
	originalLookup := lookupLeaseStartTime
	originalWorktreePIDs := rescueWorktreeProcessIDs
	killProcess = func(int, syscall.Signal) error { return nil }
	getProcessGroupID = func(int) (int, error) { return currentPGID, nil }
	lookupLeaseStartTime = func(int) (string, error) { return "current-start-time", nil }
	rescueWorktreeProcessIDs = func(context.Context, string) ([]int, error) { return nil, nil }
	t.Cleanup(func() {
		killProcess = originalKill
		getProcessGroupID = originalGetpgid
		lookupLeaseStartTime = originalLookup
		rescueWorktreeProcessIDs = originalWorktreePIDs
	})

	step := newStep(env.run.Config, stepOptions{
		now:               time.Now,
		heartbeatInterval: 10 * time.Millisecond,
		staleAfter:        time.Second,
	})
	retryCount, err := step.resumeIfNeeded(context.Background(), env.run, allocation, agentDir)
	require.NoError(t, err)
	assert.Equal(t, 1, retryCount)

	state, ok, err := loadResumeState(agentDir)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Zero(t, state.Pid)
	assert.Zero(t, state.Pgid)
	assert.Empty(t, state.LeaderStartTime)

	entries, err := os.ReadDir(filepath.Join(agentDir, rescuedDirName))
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestShouldAttemptRescue_RequiresMatchingPGID(t *testing.T) {
	originalKill := killProcess
	originalGetpgid := getProcessGroupID
	originalLookup := lookupLeaseStartTime
	killProcess = func(pid int, sig syscall.Signal) error {
		return nil
	}
	getProcessGroupID = func(pid int) (int, error) {
		return pid + 1, nil
	}
	lookupLeaseStartTime = func(int) (string, error) { return "matching-start", nil }
	t.Cleanup(func() {
		killProcess = originalKill
		getProcessGroupID = originalGetpgid
		lookupLeaseStartTime = originalLookup
	})

	assert.False(t, shouldAttemptRescue(true, 12345, 12346, "matching-start"))
	assert.True(t, shouldAttemptRescue(true, 12345, 12345, "matching-start"))
}

func TestStepRun_BranchDriftRequiresManualRecoveryAndPreservesWorktree(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	agentDir, err := agentDir(env.run.IO, 2, "a1")
	require.NoError(t, err)
	allocation, err := worktreeFor(env.run.TaskPackage, 2, "a1")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(agentDir, 0o755))
	require.NoError(t, saveResumeState(agentDir, resumeState{
		ExpectedBaseSHA: env.run.TaskPackage.BaseSHA,
		StartedAt:       time.Now().Add(-2 * time.Hour).UTC(),
		Pid:             999999,
		LeaderStartTime: "stale-start",
		RetryCount:      0,
		LastHeartbeat:   time.Now().Add(-2 * time.Hour).UTC(),
	}))
	require.NoError(t, touchHeartbeat(agentDir, time.Now().Add(-2*time.Hour).UTC()))
	runCommand(t, allocation.Path, "git", "checkout", "-b", "manual-recovery-drift")
	driftedPath := filepath.Join(allocation.Path, "branch-drift.txt")
	require.NoError(t, os.WriteFile(driftedPath, []byte("preserve me\n"), 0o644))

	err = (Step{}).Run(context.Background(), env.run)
	var manual *agentrunner.ManualRecoveryRequiredError
	require.ErrorAs(t, err, &manual)
	assert.Equal(t, contracts.RollbackReasonLeaseFailure, manual.Reason)
	assert.FileExists(t, driftedPath)
	assert.Equal(t, "manual-recovery-drift", strings.TrimSpace(runCommand(t, allocation.Path, "git", "branch", "--show-current")))
	assert.NoFileExists(t, env.manifestPath)
	assert.NoDirExists(t, filepath.Join(agentDir, rescuedDirName))
}

func TestStepRun_QuiesceTimeoutRequiresManualRecoveryWithoutReset(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	agentDir, err := agentDir(env.run.IO, 2, "a1")
	require.NoError(t, err)
	allocation, err := worktreeFor(env.run.TaskPackage, 2, "a1")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(agentDir, 0o755))
	require.NoError(t, saveResumeState(agentDir, resumeState{
		ExpectedBaseSHA: env.run.TaskPackage.BaseSHA,
		StartedAt:       time.Now().Add(-2 * time.Hour).UTC(),
		Pid:             999999,
		LeaderStartTime: "stale-start",
		RetryCount:      0,
		LastHeartbeat:   time.Now().Add(-2 * time.Hour).UTC(),
	}))
	require.NoError(t, touchHeartbeat(agentDir, time.Now().Add(-2*time.Hour).UTC()))
	busyPath := filepath.Join(allocation.Path, "busy.txt")
	require.NoError(t, os.WriteFile(busyPath, []byte("still busy\n"), 0o644))

	originalWorktreePIDs := rescueWorktreeProcessIDs
	originalKillPID := rescueKillPID
	originalSleep := rescueSleep
	originalMaxWait := rescueQuiesceMaxWait
	originalInterval := rescueQuiesceInterval
	rescueWorktreeProcessIDs = func(context.Context, string) ([]int, error) { return []int{424242}, nil }
	rescueKillPID = func(int, syscall.Signal) error { return nil }
	rescueSleep = func(time.Duration) {}
	rescueQuiesceMaxWait = time.Nanosecond
	rescueQuiesceInterval = 0
	t.Cleanup(func() {
		rescueWorktreeProcessIDs = originalWorktreePIDs
		rescueKillPID = originalKillPID
		rescueSleep = originalSleep
		rescueQuiesceMaxWait = originalMaxWait
		rescueQuiesceInterval = originalInterval
	})

	err = (Step{}).Run(context.Background(), env.run)
	var manual *agentrunner.ManualRecoveryRequiredError
	require.ErrorAs(t, err, &manual)
	assert.Equal(t, contracts.RollbackReasonLeaseFailure, manual.Reason)
	assert.FileExists(t, busyPath)

	state, ok, readErr := loadResumeState(agentDir)
	require.NoError(t, readErr)
	require.True(t, ok)
	assert.Equal(t, 999999, state.Pid)
	assert.NoFileExists(t, env.manifestPath)
	assert.NoDirExists(t, filepath.Join(agentDir, rescuedDirName))
}

func TestStepRun_ZeroValuePreservesCustomStaleAfter(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	agentDir, err := agentDir(env.run.IO, 2, "a1")
	require.NoError(t, err)

	oldTime := time.Now().Add(-2 * time.Second).UTC()
	currentPGID, err := syscall.Getpgid(os.Getpid())
	require.NoError(t, err)
	startTime, err := lookupLeaseStartTime(os.Getpid())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(agentDir, 0o755))
	require.NoError(t, saveResumeState(agentDir, resumeState{
		ExpectedBaseSHA: env.run.TaskPackage.BaseSHA,
		StartedAt:       oldTime,
		Pid:             os.Getpid(),
		Pgid:            currentPGID,
		LeaderStartTime: startTime,
		RetryCount:      1,
		LastHeartbeat:   oldTime,
	}))
	require.NoError(t, touchHeartbeat(agentDir, oldTime))

	err = (Step{
		cfg:               env.run.Config,
		heartbeatInterval: 10 * time.Millisecond,
		staleAfter:        time.Second,
	}).Run(context.Background(), env.run)
	require.ErrorIs(t, err, ErrRescueAbortedLeaseActive)
}

func TestStepRun_RejectsTaskPackageRunIDMismatch(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	env.run.TaskPackage.RunID = contracts.RunID("2026-04-21-PR42-deadbee")

	err := (Step{}).Run(context.Background(), env.run)
	require.ErrorContains(t, err, "task package run_id mismatch")
	assert.NoFileExists(t, env.manifestPath)
}

func TestStepRun_PartialCustomizationDefaultsRunner(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)

	require.NotPanics(t, func() {
		err := (Step{
			cfg: env.run.Config,
			now: time.Now,
		}).Run(context.Background(), env.run)
		require.NoError(t, err)
	})
}

func TestStepRun_PersistsTerminalSuccessAfterParentCancellation(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	agentDir, err := agentDir(env.run.IO, 2, "a1")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	step := Step{
		cfg:    env.run.Config,
		now:    time.Now,
		runner: cancelAfterSuccessRunner{cancel: cancel, runID: env.run.TaskPackage.RunID, agent: env.run.Agent},
	}
	err = step.Run(ctx, env.run)
	require.NoError(t, err)

	manifest := readManifest(t, env.manifestPath)
	assert.Equal(t, contracts.ManifestKindSuccess, manifest.Kind)

	state, ok, err := loadResumeState(agentDir)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Zero(t, state.Pid)
	assert.Zero(t, state.Pgid)
	assert.Empty(t, state.LeaderStartTime)

	_, statErr := os.Stat(heartbeatPath(agentDir))
	require.Error(t, statErr)
	assert.True(t, os.IsNotExist(statErr))
}

func TestStep50GitHelpers_ReturnContextCancellation(t *testing.T) {
	wrapperDir := t.TempDir()
	wrapperPath := filepath.Join(wrapperDir, "git")
	wrapper := "#!/bin/sh\nsleep 5\nexit 1\n"
	require.NoError(t, os.WriteFile(wrapperPath, []byte(wrapper), 0o755))
	useFakeGitWrapper(t, wrapperPath)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, err := gitOutputBytesContext(ctx, t.TempDir(), "rev-list", "HEAD")
	require.ErrorIs(t, err, context.Canceled)

	ctx, cancel = context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	err = runGitCommand(ctx, t.TempDir(), "rev-list", "HEAD")
	require.ErrorIs(t, err, context.Canceled)
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

func TestStepRunParentCancelDoesNotWriteManifest(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-timeout.sh", 30)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	err := (Step{}).Run(ctx, env.run)
	require.ErrorIs(t, err, context.Canceled)

	_, statErr := os.Stat(env.manifestPath)
	require.Error(t, statErr)
	assert.True(t, os.IsNotExist(statErr))
	assertArtifactPresence(t, env.run.IO.RunDir(), false)
}

func TestStepRunMissingChecklistFailsClosed(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")
	t.Setenv("FAKE_SKIP_CHECKLIST", "1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)

	err := (Step{}).Run(context.Background(), env.run)
	require.ErrorContains(t, err, "missing checklist artifact")
}

func TestStepRun_RecreatesDeletedPass2Worktree(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	allocation, err := worktreeFor(env.run.TaskPackage, 2, "a1")
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(allocation.Path))

	require.NoError(t, (Step{}).Run(context.Background(), env.run))
	assert.FileExists(t, filepath.Join(allocation.Path, "implemented.txt"))
}

func TestStepRun_RecreatesDeletedPass2WorktreeWithInactiveResumeState(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	allocation, err := worktreeFor(env.run.TaskPackage, 2, "a1")
	require.NoError(t, err)
	agentDir, err := agentDir(env.run.IO, 2, "a1")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(agentDir, 0o755))
	require.NoError(t, saveResumeState(agentDir, resumeState{
		ExpectedBaseSHA: allocation.BaseSHA,
		RetryCount:      1,
	}))
	require.NoError(t, os.RemoveAll(allocation.Path))

	require.NoError(t, (Step{}).Run(context.Background(), env.run))
	assert.FileExists(t, filepath.Join(allocation.Path, "implemented.txt"))
}

func TestStepRun_MissingRescueWorktreeCapturesAdvancedBranchBeforeReset(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	allocation, err := worktreeFor(env.run.TaskPackage, 2, "a1")
	require.NoError(t, err)
	agentDir, err := agentDir(env.run.IO, 2, "a1")
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(allocation.Path, "rescuable.txt"), []byte("rescue me\n"), 0o644))
	runCommand(t, allocation.Path, "git", "add", "rescuable.txt")
	runCommand(t, allocation.Path, "git", "commit", "-m", "rescuable commit")
	advancedHead := strings.TrimSpace(runCommand(t, allocation.Path, "git", "rev-parse", "HEAD"))
	require.NotEqual(t, allocation.HeadSHA, advancedHead)

	oldTime := time.Now().Add(-2 * time.Hour).UTC()
	require.NoError(t, os.MkdirAll(agentDir, 0o755))
	require.NoError(t, saveResumeState(agentDir, resumeState{
		ExpectedBaseSHA: allocation.BaseSHA,
		StartedAt:       oldTime,
		Pid:             999999,
		LeaderStartTime: "stale-start",
		RetryCount:      0,
		LastHeartbeat:   oldTime,
	}))
	require.NoError(t, touchHeartbeat(agentDir, oldTime))

	originalWorktreePIDs := rescueWorktreeProcessIDs
	rescueWorktreeProcessIDs = func(context.Context, string) ([]int, error) { return nil, nil }
	t.Cleanup(func() {
		rescueWorktreeProcessIDs = originalWorktreePIDs
	})

	require.NoError(t, os.RemoveAll(allocation.Path))

	require.NoError(t, (Step{}).Run(context.Background(), env.run))

	entries := rescueDirEntries(t, agentDir)
	require.Len(t, entries, 1)
	rescueState, err := agentrunner.ReadRescueState(filepath.Join(agentDir, rescuedDirName, entries[0].Name(), "state.json"))
	require.NoError(t, err)
	assert.Equal(t, advancedHead, rescueState.RescuedHeadSHA)
	assert.Greater(t, rescueState.CommitCount, 0)
}

func TestStepRun_FinalizeFailureForcesImmediateRescueOnNextResume(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	agentDir, err := agentDir(env.run.IO, 2, "a1")
	require.NoError(t, err)

	t.Setenv("FAKE_SKIP_CHECKLIST", "1")
	err = (Step{}).Run(context.Background(), env.run)
	require.ErrorContains(t, err, "missing checklist artifact")

	state, ok, err := loadResumeState(agentDir)
	require.NoError(t, err)
	require.True(t, ok)
	assert.NotZero(t, state.Pid)
	_, statErr := os.Stat(heartbeatPath(agentDir))
	require.Error(t, statErr)
	assert.True(t, os.IsNotExist(statErr))

	originalWorktreePIDs := rescueWorktreeProcessIDs
	rescueWorktreeProcessIDs = func(context.Context, string) ([]int, error) { return nil, nil }
	t.Cleanup(func() {
		rescueWorktreeProcessIDs = originalWorktreePIDs
	})

	t.Setenv("FAKE_SKIP_CHECKLIST", "")
	require.NoError(t, (Step{}).Run(context.Background(), env.run))
	assert.Len(t, rescueDirEntries(t, agentDir), 1)
}

func TestStepRun_StopsHeartbeatBeforeSlowFinalize(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	agentDir, err := agentDir(env.run.IO, 2, "a1")
	require.NoError(t, err)

	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	wrapperDir := t.TempDir()
	writeContainsFakeGitWrapper(t, wrapperDir)
	useFakeGitWrapper(t, filepath.Join(wrapperDir, "git"))
	t.Setenv("REAL_GIT", realGit)
	t.Setenv("FAKE_GIT_SLEEP_ON_SUBSTRING", " rev-parse HEAD")
	t.Setenv("FAKE_GIT_SLEEP_SECONDS", "1")
	step := newStep(env.run.Config, stepOptions{
		now:               time.Now,
		heartbeatInterval: 10 * time.Millisecond,
		staleAfter:        time.Second,
	})
	require.NoError(t, step.Run(context.Background(), env.run))

	state, ok, err := loadResumeState(agentDir)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Zero(t, state.Pid)
	assert.Zero(t, state.Pgid)
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

func TestStepRun_RejectsDetachedForeignHead(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	runCommand(t, env.repoDir, "git", "checkout", "main")
	require.NoError(t, os.WriteFile(filepath.Join(env.repoDir, "foreign.txt"), []byte("foreign\n"), 0o644))
	runCommand(t, env.repoDir, "git", "add", "foreign.txt")
	runCommand(t, env.repoDir, "git", "commit", "-m", "foreign commit")
	foreignSHA := strings.TrimSpace(runCommand(t, env.repoDir, "git", "rev-parse", "HEAD"))
	runCommand(t, env.repoDir, "git", "checkout", "main")

	t.Setenv("FAKE_CHECKOUT_REF_BEFORE_COMMIT", foreignSHA)
	err := (Step{}).Run(context.Background(), env.run)
	require.ErrorContains(t, err, "current branch mismatch")
}

func TestStepRun_GitCommandsIgnoreInheritedGitDir(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	otherRepo := filepath.Join(t.TempDir(), "other-repo")
	initGitRepoWithWorktree(t, otherRepo, filepath.Join(t.TempDir(), "2026-04-21-PR99-deadbee-pass2-a1"))
	runCommand(t, otherRepo, "git", "commit", "--allow-empty", "-m", "other-head")
	otherHead := strings.TrimSpace(runCommand(t, otherRepo, "git", "rev-parse", "HEAD"))

	t.Setenv("GIT_DIR", filepath.Join(otherRepo, ".git"))
	t.Setenv("GIT_WORK_TREE", otherRepo)

	require.NoError(t, (Step{}).Run(context.Background(), env.run))

	manifest := readManifest(t, env.manifestPath)
	success := manifest.Value.(contracts.ManifestSuccess)
	assert.NotEqual(t, env.run.TaskPackage.BaseSHA, success.BaseSHA)
	assert.NotEqual(t, otherHead, success.HeadSHA)
}

func TestStepRun_RescueStartFailureLeavesNoPhantomLease(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	agentDir, err := agentDir(env.run.IO, 2, "a1")
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(agentDir, 0o755))
	require.NoError(t, saveResumeState(agentDir, resumeState{
		ExpectedBaseSHA: env.run.TaskPackage.BaseSHA,
		StartedAt:       time.Time{},
		Pid:             0,
		Pgid:            0,
		RetryCount:      1,
		LastHeartbeat:   time.Time{},
	}))

	failing := newStep(env.run.Config, stepOptions{
		now:               time.Now,
		heartbeatInterval: 10 * time.Millisecond,
		staleAfter:        time.Second,
		runner:            failBeforeStartRunner{},
	})
	err = failing.Run(context.Background(), env.run)
	require.ErrorContains(t, err, "synthetic start failure")

	state, ok, err := loadResumeState(agentDir)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 1, state.RetryCount)
	assert.Zero(t, state.Pid)
	assert.Zero(t, state.Pgid)

	_, statErr := os.Stat(heartbeatPath(agentDir))
	require.Error(t, statErr)
	assert.True(t, os.IsNotExist(statErr))

	require.NoError(t, (Step{}).Run(context.Background(), env.run))
}

func TestPerformRescue_RemovesIgnoredFiles(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	allocation, err := worktreeFor(env.run.TaskPackage, 2, "a1")
	require.NoError(t, err)
	agentDir, err := agentDir(env.run.IO, 2, "a1")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(agentDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(allocation.Path, ".gitignore"), []byte(".env.local\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(allocation.Path, ".env.local"), []byte("secret\n"), 0o644))

	step := newStep(env.run.Config, stepOptions{
		now:               time.Now,
		heartbeatInterval: 10 * time.Millisecond,
		staleAfter:        time.Second,
	})
	originalWorktreePIDs := rescueWorktreeProcessIDs
	rescueWorktreeProcessIDs = func(context.Context, string) ([]int, error) { return nil, nil }
	t.Cleanup(func() {
		rescueWorktreeProcessIDs = originalWorktreePIDs
	})
	_, err = step.performRescue(context.Background(), env.run, allocation, agentDir, resumeState{
		ExpectedBaseSHA: env.run.TaskPackage.BaseSHA,
		StartedAt:       time.Now().Add(-2 * time.Hour).UTC(),
		Pid:             999999,
		LeaderStartTime: "stale-start",
		RetryCount:      0,
		LastHeartbeat:   time.Now().Add(-2 * time.Hour).UTC(),
	})
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(allocation.Path, ".env.local"))
	matches, err := filepath.Glob(filepath.Join(agentDir, rescuedDirName, "*", "ignored", ".env.local"))
	require.NoError(t, err)
	require.Len(t, matches, 1)
	rescuedBytes, err := os.ReadFile(matches[0])
	require.NoError(t, err)
	assert.Equal(t, "secret\n", string(rescuedBytes))
}

func TestStepRun_KillsDetachedSetsidChildAfterSuccessfulExit(t *testing.T) {
	requireProcessInspection(t)
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	helperPath := writeDetachedSleepHelper(t, t.TempDir())
	pidPath := filepath.Join(t.TempDir(), "detached-child.pid")
	t.Setenv("FAKE_CLAUDE_WRITE_FILE", filepath.Join(env.run.TaskPackage.Worktrees[3].Path, "detached.txt"))
	t.Setenv("FAKE_DETACH_HELPER", helperPath)
	t.Setenv("FAKE_DETACHED_PID_PATH", pidPath)
	t.Setenv("FAKE_DETACH_DELAY", "250ms")

	originalCleanup := cleanupProcessTree
	cleanupCalled := make(chan struct{}, 1)
	cleanupProcessTree = func(lease agentrunner.ProcessLease, sessionID int, tracker *agentrunner.DescendantTracker) error {
		select {
		case cleanupCalled <- struct{}{}:
		default:
		}
		return originalCleanup(lease, sessionID, tracker)
	}
	t.Cleanup(func() {
		cleanupProcessTree = originalCleanup
	})

	require.NoError(t, (Step{}).Run(context.Background(), env.run))
	select {
	case <-cleanupCalled:
	default:
		t.Fatal("expected cleanupProcessTree to run before Step.Run returned")
	}

	pidBytes, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return processDead(pid)
	}, 10*time.Second, 20*time.Millisecond)
}

func TestStepRun_KillsFastDetachedSetsidChildAfterSuccessfulExit(t *testing.T) {
	if raceBuild {
		t.Skip("timing-sensitive detached-child regression is covered in non-race mode")
	}
	requireProcessInspection(t)
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	helperPath := writeDetachedSleepHelper(t, t.TempDir())
	pidPath := filepath.Join(t.TempDir(), "fast-detached-child.pid")
	t.Setenv("FAKE_CLAUDE_WRITE_FILE", filepath.Join(env.run.TaskPackage.Worktrees[3].Path, "fast-detached.txt"))
	t.Setenv("FAKE_DETACH_HELPER", helperPath)
	t.Setenv("FAKE_DETACHED_PID_PATH", pidPath)
	t.Setenv("FAKE_DETACH_DELAY", "50ms")

	originalCleanup := cleanupProcessTree
	cleanupCalled := make(chan struct{}, 1)
	cleanupProcessTree = func(lease agentrunner.ProcessLease, sessionID int, tracker *agentrunner.DescendantTracker) error {
		select {
		case cleanupCalled <- struct{}{}:
		default:
		}
		return originalCleanup(lease, sessionID, tracker)
	}
	t.Cleanup(func() {
		cleanupProcessTree = originalCleanup
	})

	require.NoError(t, (Step{}).Run(context.Background(), env.run))
	select {
	case <-cleanupCalled:
	default:
		t.Fatal("expected cleanupProcessTree to run before Step.Run returned")
	}

	pidBytes, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return processDead(pid)
	}, 10*time.Second, 20*time.Millisecond)
}

func requireProcessInspection(t *testing.T) {
	t.Helper()
	startTime, err := agentrunner.LookupProcessStartTime(os.Getpid())
	if err != nil || startTime == "" || strings.HasPrefix(startTime, "unavailable:") {
		t.Skipf("process inspection unavailable in this sandbox: %v", err)
	}
	requireProcessDescendantVisibility(t)
}

func requireProcessDescendantVisibility(t *testing.T) {
	t.Helper()
	cmd := exec.Command("sleep", "5")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	if !psShowsChildOfCurrentProcess(t, cmd.Process.Pid) {
		t.Skip("process descendant listing unavailable in this sandbox")
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}

func psShowsChildOfCurrentProcess(t *testing.T, childPID int) bool {
	t.Helper()
	out, err := exec.Command("ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		return false
	}
	parentPID := os.Getpid()
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		ppid, ppidErr := strconv.Atoi(fields[1])
		if pidErr == nil && ppidErr == nil && pid == childPID && ppid == parentPID {
			return true
		}
	}
	return false
}

type failBeforeStartRunner struct{}

func (failBeforeStartRunner) Run(context.Context, runnerRequest) (runnerResult, error) {
	return runnerResult{}, errors.New("synthetic start failure")
}

type cancelAfterSuccessRunner struct {
	cancel func()
	runID  contracts.RunID
	agent  contracts.AgentID
}

func (r cancelAfterSuccessRunner) Run(_ context.Context, req runnerRequest) (runnerResult, error) {
	startedAt := time.Now().Add(-time.Second).UTC()
	if req.OnStart != nil {
		if err := req.OnStart(agentrunner.ProcessLease{
			PID:       4242,
			PGID:      4242,
			StartTime: "Tue Apr 22 10:00:00 2026",
		}, startedAt); err != nil {
			return runnerResult{}, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(req.SessionPath), 0o755); err != nil {
		return runnerResult{}, err
	}
	if err := os.WriteFile(req.SessionPath, []byte("{\"event\":\"ok\"}\n"), 0o644); err != nil {
		return runnerResult{}, err
	}
	if err := os.WriteFile(filepath.Join(req.Workdir, "implemented.txt"), []byte("ok\n"), 0o644); err != nil {
		return runnerResult{}, err
	}
	if err := os.WriteFile(filepath.Join(req.Workdir, checklistFileName), []byte(`{"schema_version":"1","run_id":"`+string(r.runID)+`","pass":2,"agent":"`+string(r.agent)+`","items":[]}`), 0o644); err != nil {
		return runnerResult{}, err
	}
	_, err := gitOutputContext(context.Background(), strings.TrimSpace, req.Workdir, "add", "implemented.txt")
	if err != nil {
		return runnerResult{}, err
	}
	_, err = gitOutputContext(context.Background(), strings.TrimSpace, req.Workdir, "commit", "-m", "synthetic success")
	if err != nil {
		return runnerResult{}, err
	}
	if r.cancel != nil {
		r.cancel()
	}
	return runnerResult{
		StartedAt:  startedAt,
		FinishedAt: time.Now().UTC(),
		Lease: agentrunner.ProcessLease{
			PID:       4242,
			PGID:      4242,
			StartTime: "Tue Apr 22 10:00:00 2026",
		},
	}, nil
}

type stepTestEnv struct {
	run          RunContext
	manifestPath string
	repoDir      string
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

func newStepTestEnv(t *testing.T, script string, timeoutSeconds int) stepTestEnv {
	t.Helper()

	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	repoDir := t.TempDir()
	runID := contracts.RunID("2026-04-21-PR42-abcdef0")

	baseSHA := initGitRepoWithWorktree(t, repoDir, filepath.Join(worktreeBase, fmt.Sprintf("%s-pass2-a1", runID)))

	taskPackage := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runID,
		PR:                      42,
		Title:                   "step50 test",
		BaseSHA:                 baseSHA,
		BestBranch:              "best/main",
		ReconstructedTaskPrompt: "Implement the requested change safely.",
		CreatedAt:               time.Now().UTC(),
		Worktrees: []contracts.WorktreeAllocation{
			{Agent: "a1", Pass: 1, Path: filepath.Join(worktreeBase, fmt.Sprintf("%s-pass1-a1", runID)), Branch: "test/pass1/a1", BaseSHA: baseSHA, HeadSHA: baseSHA},
			{Agent: "a2", Pass: 1, Path: filepath.Join(worktreeBase, fmt.Sprintf("%s-pass1-a2", runID)), Branch: "test/pass1/a2", BaseSHA: baseSHA, HeadSHA: baseSHA},
			{Agent: "a3", Pass: 1, Path: filepath.Join(worktreeBase, fmt.Sprintf("%s-pass1-a3", runID)), Branch: "test/pass1/a3", BaseSHA: baseSHA, HeadSHA: baseSHA},
			{Agent: "a1", Pass: 2, Path: filepath.Join(worktreeBase, fmt.Sprintf("%s-pass2-a1", runID)), Branch: "test/pass2/a1", BaseSHA: baseSHA, HeadSHA: baseSHA},
			{Agent: "a2", Pass: 2, Path: filepath.Join(worktreeBase, fmt.Sprintf("%s-pass2-a2", runID)), Branch: "test/pass2/a2", BaseSHA: baseSHA, HeadSHA: baseSHA},
			{Agent: "a3", Pass: 2, Path: filepath.Join(worktreeBase, fmt.Sprintf("%s-pass2-a3", runID)), Branch: "test/pass2/a3", BaseSHA: baseSHA, HeadSHA: baseSHA},
		},
	}

	runIO, err := internalio.RunContextFromTaskPackage(taskPackage, runsBase, worktreeBase)
	require.NoError(t, err)
	writeCandidatesFile(t, runIO, nil)

	scriptPath := testScriptPath(t, script)
	cfg := &config.Config{
		Repo: config.RepoConfig{
			Root:          repoDir,
			DefaultBranch: "main",
			BestBranch:    "best/main",
		},
		RunsBasePath:     runsBase,
		WorktreeBasePath: worktreeBase,
		ClaudeCLIPath:    scriptPath,
		StepTimeouts: map[string]int{
			"step50": timeoutSeconds,
		},
	}
	run := RunContext{
		Config:      cfg,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		PR:          42,
		Pass:        2,
		Agent:       "a1",
		IO:          runIO,
		TaskPackage: &taskPackage,
	}
	manifestPath, err := runIO.ManifestPath(2, "a1")
	require.NoError(t, err)

	return stepTestEnv{
		run:          run,
		manifestPath: manifestPath,
		repoDir:      repoDir,
	}
}

func initGitRepoWithWorktree(t *testing.T, repoDir, worktreePath string) string {
	t.Helper()

	runCommand(t, "", "git", "init", "-b", "main", repoDir)
	runCommand(t, repoDir, "git", "config", "user.email", "test@example.com")
	runCommand(t, repoDir, "git", "config", "user.name", "Step50 Test")

	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644))
	runCommand(t, repoDir, "git", "add", "README.md")
	runCommand(t, repoDir, "git", "commit", "-m", "base commit")

	baseSHA := strings.TrimSpace(runCommand(t, repoDir, "git", "rev-parse", "HEAD"))
	runCommand(t, repoDir, "git", "worktree", "add", "-b", "test/pass2/a1", worktreePath, "HEAD")
	return baseSHA
}

func runCommand(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "%s %s failed: %s", name, strings.Join(args, " "), string(output))
	return string(output)
}

func writeDetachedSleepHelper(t *testing.T, dir string) string {
	t.Helper()
	sourcePath := filepath.Join(dir, "detached_sleep_helper.go")
	binaryPath := filepath.Join(dir, "detached-sleep-helper")
	source := `package main

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 3 {
		os.Exit(2)
	}
	if os.Getenv("DETACHED_SLEEP_CHILD") == "1" {
		if err := os.WriteFile(os.Args[1], []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
			os.Exit(1)
		}
		time.Sleep(60 * time.Second)
		return
	}

	delay, err := time.ParseDuration(os.Args[2])
	if err != nil {
		os.Exit(2)
	}
	cmd := exec.Command(os.Args[0], os.Args[1], os.Args[2])
	cmd.Env = append(os.Environ(), "DETACHED_SLEEP_CHILD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		os.Exit(1)
	}
	time.Sleep(delay)
}
`
	require.NoError(t, os.WriteFile(sourcePath, []byte(source), 0o644))

	cmd := exec.Command("go", "build", "-o", binaryPath, sourcePath)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	return binaryPath
}

func writeContainsFakeGitWrapper(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "git")
	script := `#!/bin/bash
set -euo pipefail
joined="$*"
if [[ -n "${FAKE_GIT_SLEEP_ON_SUBSTRING:-}" && "$joined" == *"${FAKE_GIT_SLEEP_ON_SUBSTRING}"* ]]; then
  sleep "${FAKE_GIT_SLEEP_SECONDS:-5}"
fi
exec "$REAL_GIT" "$@"
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
}

func useFakeGitWrapper(t *testing.T, wrapperPath string) {
	t.Helper()
	oldCommandContext := trustedGitCommandContext
	trustedGitCommandContext = func(ctx context.Context, name string, args ...string) (*exec.Cmd, error) {
		if name == "git" {
			return exec.CommandContext(ctx, wrapperPath, args...), nil
		}
		return oldCommandContext(ctx, name, args...)
	}
	t.Cleanup(func() {
		trustedGitCommandContext = oldCommandContext
	})
}

func useFakeStreamGitOutputWithLimit(t *testing.T, wrapperPath string) {
	t.Helper()
	oldStream := streamGitOutputWithLimit
	streamGitOutputWithLimit = func(ctx context.Context, worktreePath, errPrefix, destPath string, limit int64, args ...string) (int64, error) {
		cmd := exec.CommandContext(ctx, wrapperPath, append([]string{"-C", worktreePath}, args...)...)
		cmd.Env = os.Environ()
		out, err := cmd.Output()
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if err != nil {
			return 0, err
		}
		if int64(len(out)) > limit {
			return int64(len(out)), fmt.Errorf("%w: git %s bytes=%d limit=%d", agentrunner.ErrRescueDiffOverLimit, strings.Join(args, " "), len(out), limit)
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(destPath), 0o755))
		require.NoError(t, os.WriteFile(destPath, out, 0o644))
		return int64(len(out)), nil
	}
	t.Cleanup(func() {
		streamGitOutputWithLimit = oldStream
	})
}

func processDead(pid int) bool {
	if pid <= 0 {
		return true
	}
	err := syscall.Kill(pid, 0)
	return errors.Is(err, syscall.ESRCH)
}

func readManifest(t *testing.T, path string) contracts.Manifest {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var manifest contracts.Manifest
	require.NoError(t, contracts.DecodeStrictJSON(data, &manifest))
	return manifest
}

func assertArtifactPresence(t *testing.T, runDir string, shouldExist bool) {
	t.Helper()
	for _, rel := range []string{
		filepath.Join("50-pass2", "a1", "diff.patch"),
		filepath.Join("50-pass2", "a1", "checklist-result.json"),
	} {
		_, err := os.Stat(filepath.Join(runDir, rel))
		if shouldExist {
			require.NoError(t, err, rel)
			continue
		}
		require.Error(t, err, rel)
		assert.True(t, os.IsNotExist(err), rel)
	}
}

func testScriptPath(t *testing.T, name string) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return filepath.Join(wd, "testdata", name)
}

func writeCandidatesFile(t *testing.T, runIO internalio.RunContext, candidates []contracts.Candidate) {
	t.Helper()
	candidatesPath, err := runIO.ResolveRunRelative(filepath.Join("40", "candidates.json"))
	require.NoError(t, err)
	writeCandidatesFileAtPath(t, candidatesPath, runIO.RunID, candidates)
}

func writeCandidatesFileAtPath(t *testing.T, candidatesPath string, runID contracts.RunID, candidates []contracts.Candidate) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(candidatesPath), 0o755))
	doc := contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          runID,
		Candidates:     candidates,
		CandidatesHash: contracts.CanonicalCandidatesHash(candidates),
		CreatedAt:      time.Now().UTC(),
	}
	require.NoError(t, internalio.WriteJSONAtomic(candidatesPath, doc))
}

func writeCandidateSidecar(t *testing.T, runIO internalio.RunContext, candidate contracts.Candidate, body string) contracts.Candidate {
	t.Helper()
	path, err := runIO.ResolveRunRelative(candidate.ProposedBodyPath)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	candidate.ProposedBodySha256 = sha256Hex([]byte(body))
	return candidate
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
