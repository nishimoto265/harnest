package step20_implement

import (
	"context"
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

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStepRun_BranchDriftRequiresManualRecoveryAndPreservesWorktree(t *testing.T) {
	fx := newTestFixture(t, 5)
	fx.seedResumeState(t, 0)
	runGit(t, fx.worktree, "checkout", "-b", "manual-recovery-drift")
	driftedPath := filepath.Join(fx.worktree, "branch-drift.txt")
	require.NoError(t, os.WriteFile(driftedPath, []byte("preserve me\n"), 0o644))

	err := fx.step.Run(context.Background(), fx.run)
	var manual *agentrunner.ManualRecoveryRequiredError
	require.ErrorAs(t, err, &manual)
	assert.Equal(t, contracts.RollbackReasonLeaseFailure, manual.Reason)
	assert.FileExists(t, driftedPath)
	assert.Equal(t, "manual-recovery-drift", strings.TrimSpace(runGit(t, fx.worktree, "branch", "--show-current")))

	state, ok, readErr := loadResumeState(fx.agentDir)
	require.NoError(t, readErr)
	require.True(t, ok)
	assert.Equal(t, 999999, state.Pid)

	_, statErr := os.Stat(fx.manifestPath())
	require.Error(t, statErr)
	assert.True(t, os.IsNotExist(statErr))
	_, rescueErr := os.Stat(filepath.Join(fx.agentDir, rescuedDirName))
	require.Error(t, rescueErr)
	assert.True(t, os.IsNotExist(rescueErr))
}

func TestStepRun_MissingRescueWorktreeCapturesAdvancedBranchBeforeReset(t *testing.T) {
	env := newEnsureEnv(t)
	allocation, err := worktreeFor(&env.taskPackage, 1, "a1")
	require.NoError(t, err)
	agentDir, err := agentDir(env.runCtx.IO, 1, "a1")
	require.NoError(t, err)
	env.cfg.ClaudeCLIPath = writeFakeClaudeScript(t, t.TempDir())

	require.NoError(t, os.WriteFile(filepath.Join(allocation.Path, "rescuable.txt"), []byte("rescue me\n"), 0o644))
	runGit(t, allocation.Path, "add", "rescuable.txt")
	runGit(t, allocation.Path, "commit", "-m", "rescuable commit")
	advancedHead := strings.TrimSpace(runGit(t, allocation.Path, "rev-parse", "HEAD"))
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
	stubQuiescentRescueWorktree(t)

	require.NoError(t, os.RemoveAll(allocation.Path))
	t.Setenv("FAKE_CLAUDE_STDOUT", `{"event":"retry-after-rescue"}`+"\n")
	t.Setenv("FAKE_CLAUDE_WRITE_FILE", filepath.Join(allocation.Path, "retry.txt"))

	step := newStep(env.cfg, stepOptions{
		now:               time.Now,
		heartbeatInterval: 10 * time.Millisecond,
		staleAfter:        time.Second,
	})
	require.NoError(t, step.Run(context.Background(), env.runCtx))

	rescueState, err := agentrunner.ReadRescueState(filepath.Join(latestRescueDir(t, agentDir), "state.json"))
	require.NoError(t, err)
	assert.Equal(t, advancedHead, rescueState.RescuedHeadSHA)
	assert.Greater(t, rescueState.CommitCount, 0)
}

func TestStepRun_QuiesceTimeoutRequiresManualRecoveryWithoutReset(t *testing.T) {
	fx := newTestFixture(t, 5)
	fx.seedResumeState(t, 0)
	busyPath := filepath.Join(fx.worktree, "busy.txt")
	require.NoError(t, os.WriteFile(busyPath, []byte("still busy\n"), 0o644))

	originalWorktreePIDs := rescueWorktreeProcessIDs
	originalKillPID := rescueKillPID
	originalSleep := rescueSleep
	originalMaxWait := rescueQuiesceMaxWait
	originalInterval := rescueQuiesceInterval
	rescueWorktreeProcessIDs = func(context.Context, string) ([]int, error) {
		return []int{424242}, nil
	}
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

	err := fx.step.Run(context.Background(), fx.run)
	var manual *agentrunner.ManualRecoveryRequiredError
	require.ErrorAs(t, err, &manual)
	assert.Equal(t, contracts.RollbackReasonLeaseFailure, manual.Reason)
	assert.FileExists(t, busyPath)

	state, ok, readErr := loadResumeState(fx.agentDir)
	require.NoError(t, readErr)
	require.True(t, ok)
	assert.Equal(t, 999999, state.Pid)

	_, statErr := os.Stat(fx.manifestPath())
	require.Error(t, statErr)
	assert.True(t, os.IsNotExist(statErr))
	_, rescueErr := os.Stat(filepath.Join(fx.agentDir, rescuedDirName))
	require.Error(t, rescueErr)
	assert.True(t, os.IsNotExist(rescueErr))
}

func TestEnsureRescueLeaseQuiesced_SkipsRecycledProcessGroup(t *testing.T) {
	originalKillProcess := killProcess
	originalLookupStartTime := lookupLeaseStartTime
	originalWorktreePIDs := rescueWorktreeProcessIDs
	killProcess = func(int, syscall.Signal) error { return nil }
	lookupLeaseStartTime = func(int) (string, error) { return "recycled-start", nil }
	groupKillCalls := 0
	originalKillPID := rescueKillPID
	rescueKillPID = func(int, syscall.Signal) error {
		groupKillCalls++
		return nil
	}
	rescueWorktreeProcessIDs = func(context.Context, string) ([]int, error) { return nil, nil }
	t.Cleanup(func() {
		killProcess = originalKillProcess
		lookupLeaseStartTime = originalLookupStartTime
		rescueKillPID = originalKillPID
		rescueWorktreeProcessIDs = originalWorktreePIDs
	})

	err := ensureRescueLeaseQuiesced(context.Background(), t.TempDir(), resumeState{
		Pid:             1234,
		Pgid:            1234,
		LeaderStartTime: "original-start",
	})
	require.NoError(t, err)
	assert.Zero(t, groupKillCalls)
}

func TestStepRunMissingChecklistFailsClosed(t *testing.T) {
	t.Setenv("FAKE_SKIP_CHECKLIST", "1")

	fx := newTestFixture(t, 5)
	err := fx.step.Run(context.Background(), fx.run)
	require.ErrorContains(t, err, "missing checklist artifact")
}

func TestStepRunReturnsLeaseContendedDuringConcurrentStartup(t *testing.T) {
	fx := newTestFixture(t, 3)
	t.Setenv("FAKE_CLAUDE_STDOUT", `{"event":"slow"}`+"\n")
	t.Setenv("FAKE_CLAUDE_SLEEP_SECONDS", "1")

	firstErrCh := make(chan error, 1)
	go func() {
		firstErrCh <- fx.step.Run(context.Background(), fx.run)
	}()

	require.Eventually(t, func() bool {
		_, err := os.Stat(fx.sessionPath())
		return err == nil
	}, processTestEventuallyTimeout, 10*time.Millisecond)

	err := fx.step.Run(context.Background(), fx.run)
	require.ErrorIs(t, err, ErrAgentLeaseContended)
	require.NoError(t, <-firstErrCh)
}

func TestStepRunSerializesWorktreeRecreationUnderRescueLock(t *testing.T) {
	root := t.TempDir()
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	repoDir := filepath.Join(root, "repo")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	require.NoError(t, os.MkdirAll(repoDir, 0o755))

	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	runGit(t, repoDir, "init", "-b", "main")
	runGit(t, repoDir, "config", "user.name", "Test User")
	runGit(t, repoDir, "config", "user.email", "test@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644))
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "base")
	baseSHA := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
	worktreePath := filepath.Join(worktreeBase, string(runID)+"-pass1-a1")
	branch := "auto-improve/" + string(runID) + "/pass1/a1"
	runGit(t, repoDir, "worktree", "add", "-b", branch, worktreePath, baseSHA)
	runIO, err := internalio.NewRunContext(runID, runsBase, worktreeBase)
	require.NoError(t, err)
	pkg := buildTaskPackage(t, runID, worktreeBase, worktreePath, baseSHA)
	scriptPath := writeFakeClaudeScript(t, root)
	cfg := &config.Config{
		Repo: config.RepoConfig{
			Root:          repoDir,
			DefaultBranch: "main",
			BestBranch:    "best",
		},
		Worktree:      config.WorktreeConfig{Base: worktreeBase},
		Paths:         config.PathsConfig{Runs: runsBase},
		ClaudeCLIPath: scriptPath,
		StepTimeouts: map[string]int{
			"step20": 5,
		},
	}
	step := newStep(cfg, stepOptions{
		now:               time.Now,
		heartbeatInterval: 10 * time.Millisecond,
		staleAfter:        time.Second,
	})
	run := RunContext{
		Config:      cfg,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		PR:          42,
		Pass:        1,
		Agent:       "a1",
		IO:          runIO,
		TaskPackage: &pkg,
	}

	require.NoError(t, os.RemoveAll(worktreePath))
	t.Setenv("FAKE_CLAUDE_WRITE_FILE", filepath.Join(worktreePath, "recreated.txt"))

	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	wrapperDir := t.TempDir()
	logPath := filepath.Join(wrapperDir, "git.log")
	writeFakeGitWrapper(t, wrapperDir)
	useFakeGitWrapper(t, filepath.Join(wrapperDir, "git"))
	t.Setenv("REAL_GIT", realGit)
	t.Setenv("FAKE_GIT_LOG", logPath)
	t.Setenv("FAKE_GIT_SLEEP_ON_PREFIX", "worktree add")
	t.Setenv("FAKE_GIT_SLEEP_SECONDS", "1")
	firstErrCh := make(chan error, 1)
	go func() {
		firstErrCh <- step.Run(context.Background(), run)
	}()

	require.Eventually(t, func() bool {
		logBytes, readErr := os.ReadFile(logPath)
		if readErr != nil {
			return false
		}
		return strings.Contains(string(logBytes), "worktree add")
	}, processTestEventuallyTimeout, 10*time.Millisecond)

	err = step.Run(context.Background(), run)
	require.ErrorIs(t, err, ErrAgentLeaseContended)
	require.NoError(t, <-firstErrCh)
}

func TestStepRunRescueHonorsContextCancellationBeforeReset(t *testing.T) {
	fx := newTestFixture(t, 5)
	fx.seedResumeState(t, 0)

	realGit, err := exec.LookPath("git")
	require.NoError(t, err)

	wrapperDir := t.TempDir()
	logPath := filepath.Join(wrapperDir, "git.log")
	writeFakeGitWrapper(t, wrapperDir)
	useFakeGitWrapper(t, filepath.Join(wrapperDir, "git"))
	useFakeStreamGitOutputWithLimit(t, filepath.Join(wrapperDir, "git"))
	t.Setenv("REAL_GIT", realGit)
	t.Setenv("FAKE_GIT_LOG", logPath)
	t.Setenv("FAKE_GIT_SLEEP_ON_SUBSTRING", " diff HEAD --binary --no-ext-diff --no-textconv")
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
		return strings.Contains(string(logBytes), "diff HEAD --binary --no-ext-diff --no-textconv")
	}, processTestEventuallyTimeout, 10*time.Millisecond)

	cancel()

	err = <-errCh
	require.ErrorIs(t, err, context.Canceled)

	logBytes, readErr := os.ReadFile(logPath)
	require.NoError(t, readErr)
	require.NotContains(t, string(logBytes), "reset --hard")
	require.NotContains(t, string(logBytes), "clean -ffdx")

	_, statErr := os.Stat(fx.manifestPath())
	require.Error(t, statErr)
	require.True(t, os.IsNotExist(statErr))
}

func TestPerformRescue_BranchDriftRequiresManualRecoveryInsteadOfResettingMain(t *testing.T) {
	fx := newTestFixture(t, 5)
	allocation, err := worktreeFor(fx.run.TaskPackage, 1, "a1")
	require.NoError(t, err)
	runGit(t, fx.worktree, "checkout", "main")
	require.NoError(t, os.WriteFile(filepath.Join(fx.worktree, "foreign.txt"), []byte("foreign\n"), 0o644))
	runGit(t, fx.worktree, "add", "foreign.txt")
	runGit(t, fx.worktree, "commit", "-m", "foreign commit")
	foreignSHA := strings.TrimSpace(runGit(t, fx.worktree, "rev-parse", "main"))

	_, err = fx.step.performRescue(context.Background(), fx.run, allocation, fx.agentDir, resumeState{
		ExpectedBaseSHA: fx.baseSHA,
		StartedAt:       time.Now().Add(-2 * time.Hour).UTC(),
		Pid:             999999,
		LeaderStartTime: "stale-start",
		RetryCount:      0,
		LastHeartbeat:   time.Now().Add(-2 * time.Hour).UTC(),
	})
	var manual *agentrunner.ManualRecoveryRequiredError
	require.ErrorAs(t, err, &manual)

	assert.Equal(t, "main", strings.TrimSpace(runGit(t, fx.worktree, "branch", "--show-current")))
	assert.Equal(t, foreignSHA, strings.TrimSpace(runGit(t, fx.worktree, "rev-parse", "main")))
}

func TestPerformRescue_RemovesIgnoredFiles(t *testing.T) {
	fx := newTestFixture(t, 5)
	stubQuiescentRescueWorktree(t)
	allocation, err := worktreeFor(fx.run.TaskPackage, 1, "a1")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(fx.worktree, ".gitignore"), []byte(".env.local\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(fx.worktree, ".env.local"), []byte("secret\n"), 0o644))

	_, err = fx.step.performRescue(context.Background(), fx.run, allocation, fx.agentDir, resumeState{
		ExpectedBaseSHA: fx.baseSHA,
		StartedAt:       time.Now().Add(-2 * time.Hour).UTC(),
		Pid:             999999,
		LeaderStartTime: "stale-start",
		RetryCount:      0,
		LastHeartbeat:   time.Now().Add(-2 * time.Hour).UTC(),
	})
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(fx.worktree, ".env.local"))
	matches, err := filepath.Glob(filepath.Join(fx.agentDir, rescuedDirName, "*", "ignored", ".env.local"))
	require.NoError(t, err)
	require.Len(t, matches, 1)
	rescuedBytes, err := os.ReadFile(matches[0])
	require.NoError(t, err)
	assert.Equal(t, "secret\n", string(rescuedBytes))
}

func TestPerformRescue_RequiresManualRecoveryForUnverifiedDetachedWorktreeWriter(t *testing.T) {
	fx := newTestFixture(t, 5)
	allocation, err := worktreeFor(fx.run.TaskPackage, 1, "a1")
	require.NoError(t, err)
	helperPath := writeDetachedWorktreeWriterHelper(t, t.TempDir())
	targetPath := filepath.Join(fx.worktree, "ghost.txt")
	pidPath := filepath.Join(t.TempDir(), "writer.pid")

	cmd := exec.Command(helperPath, targetPath, pidPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: 0}
	require.NoError(t, cmd.Start())
	parentPID := cmd.Process.Pid
	parentPGID, err := syscall.Getpgid(parentPID)
	require.NoError(t, err)
	require.NoError(t, cmd.Wait())

	pidBytes, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	childPID, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	require.NoError(t, err)
	t.Cleanup(func() {
		if pidAlive(childPID) {
			_ = syscall.Kill(childPID, syscall.SIGKILL)
		}
	})

	originalWorktreePIDs := rescueWorktreeProcessIDs
	rescueWorktreeProcessIDs = func(context.Context, string) ([]int, error) {
		if pidAlive(childPID) {
			return []int{childPID}, nil
		}
		return nil, nil
	}
	t.Cleanup(func() {
		rescueWorktreeProcessIDs = originalWorktreePIDs
	})

	_, err = fx.step.performRescue(context.Background(), fx.run, allocation, fx.agentDir, resumeState{
		ExpectedBaseSHA: fx.baseSHA,
		StartedAt:       time.Now().Add(-2 * time.Hour).UTC(),
		Pid:             parentPID,
		Pgid:            parentPGID,
		RetryCount:      0,
		LastHeartbeat:   time.Now().Add(-2 * time.Hour).UTC(),
	})
	require.Error(t, err)
	var manual *agentrunner.ManualRecoveryRequiredError
	require.ErrorAs(t, err, &manual)
	assert.Equal(t, contracts.RollbackReasonLeaseFailure, manual.Reason)
	assert.True(t, pidAlive(childPID), "unverified lsof worktree PID must not be SIGKILLed")
}

func TestStepRun_RescueStartFailureLeavesNoPhantomLease(t *testing.T) {
	fx := newTestFixture(t, 5)
	fx.seedResumeState(t, 0)
	stubQuiescentRescueWorktree(t)

	failingStep := newStep(fx.cfg, stepOptions{
		now:               time.Now,
		heartbeatInterval: 10 * time.Millisecond,
		staleAfter:        time.Second,
		runner:            failBeforeStartRunner{},
	})
	err := failingStep.Run(context.Background(), fx.run)
	require.ErrorContains(t, err, "synthetic start failure")

	state, ok, err := loadResumeState(fx.agentDir)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 1, state.RetryCount)
	assert.Zero(t, state.Pid)
	assert.Zero(t, state.Pgid)

	_, statErr := os.Stat(fx.heartbeatLeasePath())
	require.Error(t, statErr)
	require.True(t, os.IsNotExist(statErr))

	require.NoError(t, fx.step.Run(context.Background(), fx.run))
}

func TestStepRun_RecreatesMissingWorktreeBeforeLaunch(t *testing.T) {
	root := t.TempDir()
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	repoDir := filepath.Join(root, "repo")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	require.NoError(t, os.MkdirAll(repoDir, 0o755))

	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	runGit(t, repoDir, "init", "-b", "main")
	runGit(t, repoDir, "config", "user.name", "Test User")
	runGit(t, repoDir, "config", "user.email", "test@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644))
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "base")
	baseSHA := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
	worktreePath := filepath.Join(worktreeBase, string(runID)+"-pass1-a1")
	branch := "auto-improve/" + string(runID) + "/pass1/a1"
	runGit(t, repoDir, "worktree", "add", "-b", branch, worktreePath, baseSHA)
	runIO, err := internalio.NewRunContext(runID, runsBase, worktreeBase)
	require.NoError(t, err)
	pkg := buildTaskPackage(t, runID, worktreeBase, worktreePath, baseSHA)
	scriptPath := writeFakeClaudeScript(t, root)
	cfg := &config.Config{
		Repo: config.RepoConfig{
			Root:          repoDir,
			DefaultBranch: "main",
			BestBranch:    "best",
		},
		Worktree:      config.WorktreeConfig{Base: worktreeBase},
		Paths:         config.PathsConfig{Runs: runsBase},
		ClaudeCLIPath: scriptPath,
		StepTimeouts: map[string]int{
			"step20": 5,
		},
	}
	step := newStep(cfg, stepOptions{
		now:               time.Now,
		heartbeatInterval: 10 * time.Millisecond,
		staleAfter:        time.Second,
	})
	run := RunContext{
		Config:      cfg,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		PR:          42,
		Pass:        1,
		Agent:       "a1",
		IO:          runIO,
		TaskPackage: &pkg,
	}
	require.NoError(t, os.RemoveAll(worktreePath))
	t.Setenv("FAKE_CLAUDE_WRITE_FILE", filepath.Join(worktreePath, "recreated.txt"))

	err = step.Run(context.Background(), run)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(worktreePath, "recreated.txt"))
	manifestPath, manifestErr := runIO.ManifestPath(1, "a1")
	require.NoError(t, manifestErr)
	manifest, readErr := internalio.ReadJSON[contracts.Manifest](manifestPath)
	require.NoError(t, readErr)
	assert.Equal(t, contracts.ManifestKindSuccess, manifest.Kind)
}

func TestStepRun_RecreatesMissingWorktreeWithInactiveResumeState(t *testing.T) {
	root := t.TempDir()
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	repoDir := filepath.Join(root, "repo")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	require.NoError(t, os.MkdirAll(repoDir, 0o755))

	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	runGit(t, repoDir, "init", "-b", "main")
	runGit(t, repoDir, "config", "user.name", "Test User")
	runGit(t, repoDir, "config", "user.email", "test@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644))
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "base")
	baseSHA := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
	worktreePath := filepath.Join(worktreeBase, string(runID)+"-pass1-a1")
	branch := "auto-improve/" + string(runID) + "/pass1/a1"
	runGit(t, repoDir, "worktree", "add", "-b", branch, worktreePath, baseSHA)
	runIO, err := internalio.NewRunContext(runID, runsBase, worktreeBase)
	require.NoError(t, err)
	pkg := buildTaskPackage(t, runID, worktreeBase, worktreePath, baseSHA)
	scriptPath := writeFakeClaudeScript(t, root)
	cfg := &config.Config{
		Repo: config.RepoConfig{
			Root:          repoDir,
			DefaultBranch: "main",
			BestBranch:    "best",
		},
		Worktree:      config.WorktreeConfig{Base: worktreeBase},
		Paths:         config.PathsConfig{Runs: runsBase},
		ClaudeCLIPath: scriptPath,
		StepTimeouts: map[string]int{
			"step20": 5,
		},
	}
	run := RunContext{
		Config:      cfg,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		PR:          42,
		Pass:        1,
		Agent:       "a1",
		IO:          runIO,
		TaskPackage: &pkg,
	}
	agentDir, err := agentDir(runIO, 1, "a1")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(agentDir, 0o755))
	require.NoError(t, saveResumeState(agentDir, resumeState{
		ExpectedBaseSHA: baseSHA,
		RetryCount:      1,
	}))
	require.NoError(t, os.RemoveAll(worktreePath))
	t.Setenv("FAKE_CLAUDE_WRITE_FILE", filepath.Join(worktreePath, "recreated.txt"))

	require.NoError(t, newStep(cfg, stepOptions{now: time.Now, heartbeatInterval: 10 * time.Millisecond, staleAfter: time.Second}).Run(context.Background(), run))
	assert.FileExists(t, filepath.Join(worktreePath, "recreated.txt"))
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

func TestStepRunResumeStatePersistsChildPIDAndPGID(t *testing.T) {
	fx := newTestFixture(t, 5)
	t.Setenv("FAKE_CLAUDE_SLEEP_SECONDS", "1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- fx.step.Run(ctx, fx.run)
	}()

	require.Eventually(t, func() bool {
		state, ok, err := loadResumeState(fx.agentDir)
		if err != nil || !ok {
			return false
		}
		return state.Pid > 0 && state.Pgid > 0 && state.LeaderStartTime != ""
	}, processTestEventuallyTimeout, 10*time.Millisecond)

	state, ok, err := loadResumeState(fx.agentDir)
	require.NoError(t, err)
	require.True(t, ok)
	assert.NotEqual(t, os.Getpid(), state.Pid)
	assert.NotZero(t, state.Pgid)
	assert.NotEmpty(t, state.LeaderStartTime)

	cancel()
	require.ErrorIs(t, <-errCh, context.Canceled)
}

func TestResumeIfNeeded_RescuesRecycledPIDWhenLeaderStartTimeDiffers(t *testing.T) {
	fx := newTestFixture(t, 5)
	stubQuiescentRescueWorktree(t)

	oldTime := time.Now().Add(-2 * time.Hour).UTC()
	currentPGID, err := syscall.Getpgid(os.Getpid())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(fx.agentDir, 0o755))
	require.NoError(t, saveResumeState(fx.agentDir, resumeState{
		ExpectedBaseSHA: fx.baseSHA,
		StartedAt:       oldTime,
		Pid:             os.Getpid(),
		Pgid:            currentPGID,
		LeaderStartTime: "stale-start-time",
		RetryCount:      0,
		LastHeartbeat:   oldTime,
	}))
	require.NoError(t, touchHeartbeat(fx.agentDir, oldTime))

	originalKill := killProcess
	originalGetpgid := getProcessGroupID
	originalLookup := lookupLeaseStartTime
	killProcess = func(int, syscall.Signal) error { return nil }
	getProcessGroupID = func(int) (int, error) { return currentPGID, nil }
	lookupLeaseStartTime = func(int) (string, error) { return "current-start-time", nil }
	t.Cleanup(func() {
		killProcess = originalKill
		getProcessGroupID = originalGetpgid
		lookupLeaseStartTime = originalLookup
	})

	allocation, err := worktreeFor(fx.run.TaskPackage, fx.run.Pass, fx.run.Agent)
	require.NoError(t, err)

	retryCount, err := fx.step.resumeIfNeeded(context.Background(), fx.run, allocation, fx.agentDir)
	require.NoError(t, err)
	assert.Equal(t, 1, retryCount)

	state, ok, err := loadResumeState(fx.agentDir)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Zero(t, state.Pid)
	assert.Zero(t, state.Pgid)
	assert.Empty(t, state.LeaderStartTime)

	rescueDir := latestRescueDir(t, fx.agentDir)
	require.FileExists(t, filepath.Join(rescueDir, "state.json"))
}
