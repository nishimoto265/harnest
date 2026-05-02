package step50_implement

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
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	}, processTestEventuallyTimeout, 10*time.Millisecond)

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
