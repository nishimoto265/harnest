package step20_implement

import (
	"bytes"
	"context"
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

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStepRun(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "Test User")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test User")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")

	cases := []struct {
		name       string
		timeoutSec int
		env        map[string]string
		prepare    func(t *testing.T, fx *testFixture)
		assertion  func(t *testing.T, fx *testFixture, err error)
	}{
		{
			name:       "success with commit",
			timeoutSec: 5,
			env: map[string]string{
				"FAKE_CLAUDE_STDOUT": `{"event":"ok"}` + "\n",
				"FAKE_CLAUDE_COMMIT": "1",
			},
			prepare: func(t *testing.T, fx *testFixture) {
				t.Setenv("FAKE_CLAUDE_WRITE_FILE", filepath.Join(fx.worktree, "changed.txt"))
			},
			assertion: func(t *testing.T, fx *testFixture, err error) {
				require.NoError(t, err)
				manifest := fx.readManifest(t)
				success := manifest.Value.(contracts.ManifestSuccess)
				require.NotEqual(t, fx.baseSHA, success.HeadSHA)
				require.FileExists(t, fx.diffPath())
				require.FileExists(t, fx.checklistPath())
				require.FileExists(t, fx.sessionPath())
			},
		},
		{
			name:       "success without commit",
			timeoutSec: 5,
			env: map[string]string{
				"FAKE_CLAUDE_STDOUT": `{"event":"noop"}` + "\n",
			},
			assertion: func(t *testing.T, fx *testFixture, err error) {
				require.NoError(t, err)
				manifest := fx.readManifest(t)
				failure := manifest.Value.(contracts.ManifestError)
				require.Equal(t, 0, failure.ExitCode)
				require.Equal(t, "unknown", failure.Reason)
				require.Contains(t, failure.Detail, "no diff")
				assert.NoFileExists(t, fx.diffPath())
			},
		},
		{
			name:       "error rate limit",
			timeoutSec: 5,
			env: map[string]string{
				"FAKE_CLAUDE_STDERR":    "rate_limit exceeded\n",
				"FAKE_CLAUDE_EXIT_CODE": "1",
			},
			assertion: func(t *testing.T, fx *testFixture, err error) {
				require.NoError(t, err)
				manifest := fx.readManifest(t)
				failure := manifest.Value.(contracts.ManifestError)
				require.Equal(t, "rate_limit", failure.Reason)
			},
		},
		{
			name:       "timeout",
			timeoutSec: 1,
			env: map[string]string{
				"FAKE_CLAUDE_SLEEP_SECONDS": "2",
			},
			assertion: func(t *testing.T, fx *testFixture, err error) {
				require.NoError(t, err)
				manifest := fx.readManifest(t)
				timeout := manifest.Value.(contracts.ManifestTimeout)
				require.Equal(t, 1, timeout.TimeoutSeconds)
			},
		},
		{
			name:       "active lease aborts without rewriting state",
			timeoutSec: 5,
			prepare: func(t *testing.T, fx *testFixture) {
				fx.seedActiveLeaseState(t)
			},
			assertion: func(t *testing.T, fx *testFixture, err error) {
				require.ErrorIs(t, err, ErrRescueAbortedLeaseActive)

				_, statErr := os.Stat(fx.manifestPath())
				require.Error(t, statErr)
				require.True(t, os.IsNotExist(statErr))

				stateBytes, readErr := os.ReadFile(fx.statePath())
				require.NoError(t, readErr)
				require.Equal(t, fx.stateSnapshot, stateBytes)

				info, infoErr := os.Stat(fx.heartbeatLeasePath())
				require.NoError(t, infoErr)
				require.True(t, info.ModTime().Equal(fx.heartbeatSnapshotModTime))
			},
		},
		{
			name:       "rescue then success",
			timeoutSec: 5,
			env: map[string]string{
				"FAKE_CLAUDE_STDOUT": `{"event":"rescued"}` + "\n",
				"FAKE_CLAUDE_COMMIT": "1",
			},
			prepare: func(t *testing.T, fx *testFixture) {
				t.Setenv("FAKE_CLAUDE_WRITE_FILE", filepath.Join(fx.worktree, "rescued.txt"))
				stubQuiescentRescueWorktree(t)
				fx.seedResumeState(t, 0)
			},
			assertion: func(t *testing.T, fx *testFixture, err error) {
				require.NoError(t, err)
				manifest := fx.readManifest(t)
				_, ok := manifest.Value.(contracts.ManifestSuccess)
				require.True(t, ok)

				state, ok, readErr := loadResumeState(fx.agentDir)
				require.NoError(t, readErr)
				require.True(t, ok)
				require.Equal(t, 1, state.RetryCount)

				rescueDir := latestRescueDir(t, fx.agentDir)
				require.FileExists(t, filepath.Join(rescueDir, "commits.bundle"))
				require.FileExists(t, filepath.Join(rescueDir, "state.json"))
			},
		},
		{
			name:       "missing heartbeat rescues stale state",
			timeoutSec: 5,
			env: map[string]string{
				"FAKE_CLAUDE_STDOUT": `{"event":"missing-heartbeat"}` + "\n",
				"FAKE_CLAUDE_COMMIT": "1",
			},
			prepare: func(t *testing.T, fx *testFixture) {
				t.Setenv("FAKE_CLAUDE_WRITE_FILE", filepath.Join(fx.worktree, "missing-heartbeat.txt"))
				stubQuiescentRescueWorktree(t)
				fx.seedResumeStateWithoutHeartbeat(t, 0)
			},
			assertion: func(t *testing.T, fx *testFixture, err error) {
				require.NoError(t, err)
				manifest := fx.readManifest(t)
				_, ok := manifest.Value.(contracts.ManifestSuccess)
				require.True(t, ok)

				state, ok, readErr := loadResumeState(fx.agentDir)
				require.NoError(t, readErr)
				require.True(t, ok)
				require.Equal(t, 1, state.RetryCount)

				rescueDir := latestRescueDir(t, fx.agentDir)
				require.FileExists(t, filepath.Join(rescueDir, "commits.bundle"))
				require.FileExists(t, filepath.Join(rescueDir, "state.json"))
			},
		},
		{
			name:       "session transcript is truncated on fresh attempt",
			timeoutSec: 5,
			env: map[string]string{
				"FAKE_CLAUDE_STDOUT": `{"event":"fresh-attempt"}` + "\n",
			},
			prepare: func(t *testing.T, fx *testFixture) {
				require.NoError(t, os.MkdirAll(filepath.Dir(fx.sessionPath()), 0o755))
				require.NoError(t, os.WriteFile(fx.sessionPath(), []byte("stale-attempt\n"), 0o644))
			},
			assertion: func(t *testing.T, fx *testFixture, err error) {
				require.NoError(t, err)
				sessionBytes, readErr := os.ReadFile(fx.sessionPath())
				require.NoError(t, readErr)
				require.Equal(t, `{"event":"fresh-attempt"}`+"\n", string(sessionBytes))
			},
		},
		{
			name:       "rescue exhausted",
			timeoutSec: 5,
			prepare: func(t *testing.T, fx *testFixture) {
				fx.seedResumeState(t, 3)
			},
			assertion: func(t *testing.T, fx *testFixture, err error) {
				var exhausted *RescueExhaustedError
				require.Error(t, err)
				require.True(t, errors.As(err, &exhausted))
				require.Equal(t, fx.run.Agent, exhausted.Rescue.Agent)
				require.Equal(t, 3, exhausted.Rescue.RetryCount)
				_, statErr := os.Stat(fx.manifestPath())
				require.Error(t, statErr)
				require.True(t, os.IsNotExist(statErr))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newTestFixture(t, tc.timeoutSec)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			if tc.prepare != nil {
				tc.prepare(t, fx)
			}
			err := fx.step.Run(context.Background(), fx.run)
			tc.assertion(t, fx, err)
		})
	}
}

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
	originalGroupKill := rescueKillProcessGroupUntilGone
	originalWorktreePIDs := rescueWorktreeProcessIDs
	killProcess = func(int, syscall.Signal) error { return nil }
	lookupLeaseStartTime = func(int) (string, error) { return "recycled-start", nil }
	groupKillCalls := 0
	rescueKillProcessGroupUntilGone = func(int, time.Duration, time.Duration) error {
		groupKillCalls++
		return nil
	}
	rescueWorktreeProcessIDs = func(context.Context, string) ([]int, error) { return nil, nil }
	t.Cleanup(func() {
		killProcess = originalKillProcess
		lookupLeaseStartTime = originalLookupStartTime
		rescueKillProcessGroupUntilGone = originalGroupKill
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

func TestWorktreeProcessIDs_RequiresLsof(t *testing.T) {
	originalLookPath := rescueExecLookPath
	rescueExecLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() {
		rescueExecLookPath = originalLookPath
	})

	_, err := worktreeProcessIDs(context.Background(), t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lsof is required")
}

func TestWorktreeProcessIDs_DoesNotMatchArgvOnlyReferences(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	fx := newTestFixture(t, 5)
	fakeLsof := filepath.Join(t.TempDir(), "fake-lsof.sh")
	require.NoError(t, os.WriteFile(fakeLsof, []byte("#!/bin/sh\nexit 1\n"), 0o755))

	originalLookPath := rescueExecLookPath
	rescueExecLookPath = func(string) (string, error) { return fakeLsof, nil }
	t.Cleanup(func() {
		rescueExecLookPath = originalLookPath
	})

	cmd := exec.Command(python, "-c", "import time; time.sleep(60)", fx.worktree)
	require.NoError(t, cmd.Start())
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	pids, err := worktreeProcessIDs(context.Background(), fx.worktree)
	require.NoError(t, err)
	assert.Empty(t, pids)
	assert.True(t, pidAlive(cmd.Process.Pid))
}

type testFixture struct {
	step     *Step
	run      RunContext
	cfg      *config.Config
	runIO    internalio.RunContext
	baseSHA  string
	agentDir string
	worktree string

	stateSnapshot            []byte
	heartbeatSnapshotModTime time.Time
}

func newTestFixture(t *testing.T, timeoutSec int) *testFixture {
	t.Helper()

	root := t.TempDir()
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))

	repoRoot := mustRepoRoot(t)
	scriptPath := writeFakeClaudeScript(t, root)

	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	runIO, err := internalio.NewRunContext(runID, runsBase, worktreeBase)
	require.NoError(t, err)

	worktree := filepath.Join(worktreeBase, "repo-a1")
	baseSHA := initGitRepo(t, worktree, "auto-improve/"+string(runID)+"/pass1/a1")
	pkg := buildTaskPackage(t, runID, worktreeBase, worktree, baseSHA)

	cfg := &config.Config{
		Repo: config.RepoConfig{
			Root:          repoRoot,
			DefaultBranch: "main",
			BestBranch:    "best",
		},
		Worktree:                  config.WorktreeConfig{Base: worktreeBase},
		Paths:                     config.PathsConfig{Runs: runsBase},
		ClaudeCLIPath:             scriptPath,
		RescueMaxRetries:          3,
		RegistryHighThreshold:     config.DefaultRegistryHighThreshold,
		RegistryCriticalThreshold: config.DefaultRegistryCriticalThreshold,
		StepTimeouts: map[string]int{
			"step20": timeoutSec,
		},
	}

	step := newStep(cfg, stepOptions{
		now:               time.Now,
		heartbeatInterval: 10 * time.Millisecond,
		staleAfter:        time.Second,
	})
	agentDir, err := agentDir(runIO, 1, "a1")
	require.NoError(t, err)
	return &testFixture{
		step: step,
		run: RunContext{
			Config:      cfg,
			Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
			PR:          42,
			Pass:        1,
			Agent:       "a1",
			IO:          runIO,
			TaskPackage: &pkg,
		},
		cfg:      cfg,
		runIO:    runIO,
		baseSHA:  baseSHA,
		agentDir: agentDir,
		worktree: worktree,
	}
}

func (fx *testFixture) manifestPath() string {
	path, err := fx.runIO.ManifestPath(1, fx.run.Agent)
	if err != nil {
		panic(err)
	}
	return path
}

func (fx *testFixture) diffPath() string {
	path, err := artifactPath(fx.runIO, 1, fx.run.Agent, diffFileName)
	if err != nil {
		panic(err)
	}
	return path
}

func (fx *testFixture) checklistPath() string {
	path, err := artifactPath(fx.runIO, 1, fx.run.Agent, checklistFileName)
	if err != nil {
		panic(err)
	}
	return path
}

func (fx *testFixture) sessionPath() string {
	path, err := artifactPath(fx.runIO, 1, fx.run.Agent, sessionFileName)
	if err != nil {
		panic(err)
	}
	return path
}

func (fx *testFixture) statePath() string {
	return resumeStatePath(fx.agentDir)
}

func latestRescueDir(t *testing.T, agentDir string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(agentDir, rescuedDirName))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	return filepath.Join(agentDir, rescuedDirName, entries[0].Name())
}

func stubQuiescentRescueWorktree(t *testing.T) {
	t.Helper()
	originalWorktreePIDs := rescueWorktreeProcessIDs
	rescueWorktreeProcessIDs = func(context.Context, string) ([]int, error) { return nil, nil }
	t.Cleanup(func() {
		rescueWorktreeProcessIDs = originalWorktreePIDs
	})
}

func (fx *testFixture) heartbeatLeasePath() string {
	return heartbeatPath(fx.agentDir)
}

func (fx *testFixture) readManifest(t *testing.T) contracts.Manifest {
	t.Helper()
	manifest, err := internalio.ReadJSON[contracts.Manifest](fx.manifestPath())
	require.NoError(t, err)
	return manifest
}

func (fx *testFixture) seedResumeState(t *testing.T, retryCount int) {
	t.Helper()
	oldTime := time.Now().Add(-2 * time.Hour).UTC()
	require.NoError(t, os.MkdirAll(fx.agentDir, 0o755))
	require.NoError(t, saveResumeState(fx.agentDir, resumeState{
		ExpectedBaseSHA: fx.baseSHA,
		StartedAt:       oldTime,
		Pid:             999999,
		LeaderStartTime: "00000000000",
		RetryCount:      retryCount,
		LastHeartbeat:   oldTime,
	}))
	require.NoError(t, touchHeartbeat(fx.agentDir, oldTime))
}

func (fx *testFixture) seedResumeStateWithoutHeartbeat(t *testing.T, retryCount int) {
	t.Helper()
	oldTime := time.Now().Add(-2 * time.Hour).UTC()
	require.NoError(t, os.MkdirAll(fx.agentDir, 0o755))
	require.NoError(t, saveResumeState(fx.agentDir, resumeState{
		ExpectedBaseSHA: fx.baseSHA,
		StartedAt:       oldTime,
		Pid:             999999,
		LeaderStartTime: "00000000000",
		RetryCount:      retryCount,
		LastHeartbeat:   oldTime,
	}))
}

func (fx *testFixture) seedActiveLeaseState(t *testing.T) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, os.MkdirAll(fx.agentDir, 0o755))
	currentLeaderStart, err := agentrunner.LookupProcessStartTime(os.Getpid())
	require.NoError(t, err)
	require.NoError(t, saveResumeState(fx.agentDir, resumeState{
		ExpectedBaseSHA: fx.baseSHA,
		StartedAt:       now,
		Pid:             os.Getpid(),
		LeaderStartTime: currentLeaderStart,
		RetryCount:      1,
		LastHeartbeat:   now,
	}))
	require.NoError(t, touchHeartbeat(fx.agentDir, now))
	stateBytes, err := os.ReadFile(fx.statePath())
	require.NoError(t, err)
	fx.stateSnapshot = stateBytes
	info, err := os.Stat(fx.heartbeatLeasePath())
	require.NoError(t, err)
	fx.heartbeatSnapshotModTime = info.ModTime()
}

func writeFakeClaudeScript(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-claude.sh")
	script := `#!/bin/bash
set -euo pipefail
if [[ "${FAKE_CLAUDE_STDOUT:-}" != "" ]]; then
  printf '%s' "${FAKE_CLAUDE_STDOUT}"
fi
if [[ "${FAKE_CLAUDE_STDERR:-}" != "" ]]; then
  printf '%s' "${FAKE_CLAUDE_STDERR}" >&2
fi
if [[ "${FAKE_CLAUDE_CHECKLIST_JSON:-}" != "" ]]; then
  printf '%s' "${FAKE_CLAUDE_CHECKLIST_JSON}" > checklist-result.json
elif [[ "${FAKE_CLAUDE_MKFIFO_CHECKLIST:-0}" == "1" ]]; then
  rm -f checklist-result.json
  mkfifo checklist-result.json
elif [[ "${FAKE_SKIP_CHECKLIST:-0}" != "1" ]]; then
  cat > checklist-result.json <<EOF
{"schema_version":"1","run_id":"${FAKE_RUN_ID:-2026-04-21-PR42-abcdef0}","pass":1,"agent":"${FAKE_AGENT:-a1}","items":[]}
EOF
fi
if [[ "${FAKE_CLAUDE_WRITE_FILE:-}" != "" ]]; then
  if [[ "${FAKE_CLAUDE_WRITE_SIZE:-0}" != "0" ]]; then
    head -c "${FAKE_CLAUDE_WRITE_SIZE}" /dev/zero | tr '\0' 'x' > "${FAKE_CLAUDE_WRITE_FILE}"
  else
    printf 'dirty worktree\n' > "${FAKE_CLAUDE_WRITE_FILE}"
  fi
fi
if [[ "${FAKE_CLAUDE_COMMIT:-}" == "1" ]]; then
  git commit --allow-empty -m test >/dev/null 2>&1
fi
if [[ "${FAKE_CLAUDE_CHECKOUT_REF_BEFORE_EXIT:-}" != "" ]]; then
  git checkout "${FAKE_CLAUDE_CHECKOUT_REF_BEFORE_EXIT}" >/dev/null 2>&1
fi
if [[ "${FAKE_CLAUDE_FORK_SESSION_WRITER:-}" == "1" ]]; then
  (
    while true; do
      printf '{"event":"child-process"}\n'
      sleep 0.05
    done
  ) &
fi
if [[ "${FAKE_CLAUDE_BACKGROUND_SENTINEL_HELPER:-}" != "" ]]; then
  "${FAKE_CLAUDE_BACKGROUND_SENTINEL_HELPER}" \
    "${FAKE_CLAUDE_BACKGROUND_SENTINEL_PATH}" \
    "${FAKE_CLAUDE_BACKGROUND_SENTINEL_DELAY:-200ms}"
fi
if [[ "${FAKE_CLAUDE_DETACH_HELPER:-}" != "" ]]; then
  "${FAKE_CLAUDE_DETACH_HELPER}" \
    "${FAKE_CLAUDE_DETACHED_PID_PATH}" \
    "${FAKE_CLAUDE_DETACH_DELAY:-200ms}"
fi
if [[ "${FAKE_CLAUDE_SLEEP_SECONDS:-0}" != "0" ]]; then
  sleep "${FAKE_CLAUDE_SLEEP_SECONDS}"
fi
exit "${FAKE_CLAUDE_EXIT_CODE:-0}"
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

func writeBackgroundSentinelHelper(t *testing.T, dir string) string {
	t.Helper()
	sourcePath := filepath.Join(dir, "background_sentinel_helper.go")
	binaryPath := filepath.Join(dir, "background-sentinel-helper")
	source := `package main

import (
	"os"
	"os/exec"
	"strconv"
	"time"
)

func main() {
	if len(os.Args) < 3 {
		os.Exit(2)
	}
	if os.Getenv("BACKGROUND_SENTINEL_CHILD") == "1" {
		if err := os.WriteFile(os.Args[1]+".pid", []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
			os.Exit(1)
		}
		delay, err := time.ParseDuration(os.Args[2])
		if err != nil {
			os.Exit(2)
		}
		time.Sleep(delay)
		if err := os.WriteFile(os.Args[1], []byte("background-child\n"), 0o644); err != nil {
			os.Exit(1)
		}
		return
	}

	cmd := exec.Command(os.Args[0], os.Args[1], os.Args[2])
	cmd.Env = append(os.Environ(), "BACKGROUND_SENTINEL_CHILD=1")
	if err := cmd.Start(); err != nil {
		os.Exit(1)
	}
}
`
	require.NoError(t, os.WriteFile(sourcePath, []byte(source), 0o644))

	cmd := exec.Command("go", "build", "-o", binaryPath, sourcePath)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return binaryPath
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
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return binaryPath
}

func writeDetachedWorktreeWriterHelper(t *testing.T, dir string) string {
	t.Helper()
	sourcePath := filepath.Join(dir, "detached_worktree_writer_helper.go")
	binaryPath := filepath.Join(dir, "detached-worktree-writer-helper")
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
	if os.Getenv("DETACHED_WORKTREE_WRITER_CHILD") == "1" {
		if err := os.WriteFile(os.Args[2], []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
			os.Exit(1)
		}
		file, err := os.OpenFile(os.Args[1], os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			os.Exit(1)
		}
		defer file.Close()
		for {
			if _, err := file.WriteString("ghost\n"); err != nil {
				os.Exit(1)
			}
			file.Sync()
			time.Sleep(25 * time.Millisecond)
		}
	}

	cmd := exec.Command(os.Args[0], os.Args[1], os.Args[2])
	cmd.Env = append(os.Environ(), "DETACHED_WORKTREE_WRITER_CHILD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		os.Exit(1)
	}
	time.Sleep(75 * time.Millisecond)
}
`
	require.NoError(t, os.WriteFile(sourcePath, []byte(source), 0o644))

	cmd := exec.Command("go", "build", "-o", binaryPath, sourcePath)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return binaryPath
}

func mustRepoRoot(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.TrimSpace(string(out))
}

func initGitRepo(t *testing.T, dir, branch string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "commit", "--allow-empty", "-m", "base")
	runGit(t, dir, "checkout", "-b", branch)
	return strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
}

func buildTaskPackage(t *testing.T, runID contracts.RunID, worktreeBase, pass1Path, baseSHA string) contracts.TaskPackage {
	t.Helper()
	pkg := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runID,
		PR:                      42,
		Title:                   "test",
		BaseSHA:                 baseSHA,
		BestBranch:              "best",
		ReconstructedTaskPrompt: "Implement the requested change.",
		CreatedAt:               time.Now().UTC(),
	}
	for pass := 1; pass <= 2; pass++ {
		for _, agent := range []contracts.AgentID{"a1", "a2", "a3"} {
			path := filepath.Join(worktreeBase, fmt.Sprintf("pass%d-%s", pass, agent))
			if pass == 1 && agent == "a1" {
				path = pass1Path
			} else {
				require.NoError(t, os.MkdirAll(path, 0o755))
			}
			pkg.Worktrees = append(pkg.Worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    path,
				Branch:  fmt.Sprintf("auto-improve/%s/pass%d/%s", runID, pass, agent),
				BaseSHA: baseSHA,
				HeadSHA: baseSHA,
			})
		}
	}
	return pkg
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return string(out)
}

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

// TestWriteSuccessArtifacts_RefusesSuccessWhenHeadUnchangedAndDirty (r12/H3):
// previously this was
// TestWriteSuccessArtifacts_CapturesDirtyTrackedDiffWhenHeadIsUnchanged and
// expected the step to promote a dirty-but-uncommitted tree as a success.
// That behavior would silently adopt work the agent never committed. After
// the H3 fix the step refuses and writes an error manifest instead.
func TestWriteSuccessArtifacts_RefusesSuccessWhenHeadUnchangedAndDirty(t *testing.T) {
	fx := newTestFixture(t, 5)
	require.NoError(t, os.WriteFile(filepath.Join(fx.worktree, "README.md"), []byte("dirty worktree\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(fx.worktree, checklistFileName), []byte(`{"schema_version":"1","run_id":"2026-04-21-PR42-abcdef0","pass":1,"agent":"a1","items":[]}`), 0o644))

	err := fx.step.writeSuccessArtifacts(context.Background(), fx.run, fx.run.TaskPackage.Worktrees[0], runnerResult{
		StartedAt:  time.Now().Add(-time.Second).UTC(),
		FinishedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	manifest := fx.readManifest(t)
	errManifest, ok := manifest.Value.(contracts.ManifestError)
	require.True(t, ok, "expected error manifest when HEAD==BaseSHA and worktree dirty, got: %T", manifest.Value)
	assert.Contains(t, errManifest.Detail, "without committing")
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
	}, time.Second, 10*time.Millisecond)

	err := fx.step.Run(context.Background(), fx.run)
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
	t.Setenv("REAL_GIT", realGit)
	t.Setenv("FAKE_GIT_LOG", logPath)
	t.Setenv("FAKE_GIT_SLEEP_ON_PREFIX", "diff HEAD --binary --no-ext-diff --no-textconv")
	t.Setenv("FAKE_GIT_SLEEP_SECONDS", "5")
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))

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
	}, 3*time.Second, 10*time.Millisecond)

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

func TestStepRunCancelsChildProcessGroupOnContextCancellation(t *testing.T) {
	fx := newTestFixture(t, 5)
	t.Setenv("FAKE_CLAUDE_FORK_SESSION_WRITER", "1")
	t.Setenv("FAKE_CLAUDE_SLEEP_SECONDS", "5")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- fx.step.Run(ctx, fx.run)
	}()

	require.Eventually(t, func() bool {
		sessionBytes, err := os.ReadFile(fx.sessionPath())
		if err != nil {
			return false
		}
		return bytes.Count(sessionBytes, []byte("{\"event\":\"child-process\"}\n")) >= 2
	}, time.Second, 10*time.Millisecond)

	cancel()

	err := <-errCh
	require.ErrorIs(t, err, context.Canceled)

	before, readErr := os.ReadFile(fx.sessionPath())
	require.NoError(t, readErr)

	time.Sleep(250 * time.Millisecond)

	after, readErr := os.ReadFile(fx.sessionPath())
	require.NoError(t, readErr)
	require.Equal(t, before, after)

	_, statErr := os.Stat(fx.manifestPath())
	require.Error(t, statErr)
	require.True(t, os.IsNotExist(statErr))
}

func TestStepRunSweepsGrandchildrenAfterSuccessfulExit(t *testing.T) {
	// Detached helpers may race with cleanup; disable M5 fail-closed
	// to keep this test focused on the grandchild-sweep assertion.
	originalFailClosed := cleanupProcessTreeFailClosed
	cleanupProcessTreeFailClosed = false
	t.Cleanup(func() { cleanupProcessTreeFailClosed = originalFailClosed })

	fx := newTestFixture(t, 5)
	helperPath := writeBackgroundSentinelHelper(t, t.TempDir())
	sentinelPath := filepath.Join(t.TempDir(), "background-child.txt")
	pidPath := sentinelPath + ".pid"
	t.Setenv("FAKE_CLAUDE_WRITE_FILE", filepath.Join(fx.worktree, "background.txt"))
	t.Setenv("FAKE_CLAUDE_COMMIT", "1")
	t.Setenv("FAKE_CLAUDE_BACKGROUND_SENTINEL_HELPER", helperPath)
	t.Setenv("FAKE_CLAUDE_BACKGROUND_SENTINEL_PATH", sentinelPath)
	t.Setenv("FAKE_CLAUDE_BACKGROUND_SENTINEL_DELAY", "200ms")

	err := fx.step.Run(context.Background(), fx.run)
	require.NoError(t, err)

	pidBytes, readErr := os.ReadFile(pidPath)
	require.NoError(t, readErr)
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	require.NoError(t, parseErr)
	require.Eventually(t, func() bool {
		return !pidAlive(pid)
	}, 2*time.Second, 20*time.Millisecond)

	manifest := fx.readManifest(t)
	_, ok := manifest.Value.(contracts.ManifestSuccess)
	require.True(t, ok)
}

func TestStepRunKillsDetachedSetsidChildAfterSuccessfulExit(t *testing.T) {
	originalFailClosed := cleanupProcessTreeFailClosed
	cleanupProcessTreeFailClosed = false
	t.Cleanup(func() { cleanupProcessTreeFailClosed = originalFailClosed })

	fx := newTestFixture(t, 5)
	t.Setenv("FAKE_CLAUDE_COMMIT", "1")
	helperPath := writeDetachedSleepHelper(t, t.TempDir())
	pidPath := filepath.Join(t.TempDir(), "detached-child.pid")
	t.Setenv("FAKE_CLAUDE_WRITE_FILE", filepath.Join(fx.worktree, "detached.txt"))
	t.Setenv("FAKE_CLAUDE_DETACH_HELPER", helperPath)
	t.Setenv("FAKE_CLAUDE_DETACHED_PID_PATH", pidPath)
	t.Setenv("FAKE_CLAUDE_DETACH_DELAY", "250ms")

	require.NoError(t, fx.step.Run(context.Background(), fx.run))

	pidBytes, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return !pidAlive(pid)
	}, 2*time.Second, 20*time.Millisecond)
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
	artifacts, err := copyUntrackedFiles(ctx, fx.worktree, rescueDir)
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
	t.Setenv("FAKE_CLAUDE_WRITE_FILE", filepath.Join(fx.worktree, "huge.bin"))
	t.Setenv("FAKE_CLAUDE_WRITE_SIZE", fmt.Sprintf("%d", (16<<20)+1))
	t.Setenv("FAKE_CLAUDE_COMMIT", "1")

	err := fx.step.Run(context.Background(), fx.run)
	require.Error(t, err)
	assert.ErrorIs(t, err, agentrunner.ErrSuccessDiffOverflow)
	assert.NoFileExists(t, fx.manifestPath())
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
		LeaderStartTime: "00000000000",
		RetryCount:      0,
		LastHeartbeat:   time.Now().Add(-2 * time.Hour).UTC(),
	})
	var manual *agentrunner.ManualRecoveryRequiredError
	require.ErrorAs(t, err, &manual)

	assert.Equal(t, "main", strings.TrimSpace(runGit(t, fx.worktree, "branch", "--show-current")))
	assert.Equal(t, foreignSHA, strings.TrimSpace(runGit(t, fx.worktree, "rev-parse", "main")))
}

func TestPerformRescue_PreservesIgnoredFiles(t *testing.T) {
	fx := newTestFixture(t, 5)
	allocation, err := worktreeFor(fx.run.TaskPackage, 1, "a1")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(fx.worktree, ".gitignore"), []byte(".env.local\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(fx.worktree, ".env.local"), []byte("secret\n"), 0o644))

	_, err = fx.step.performRescue(context.Background(), fx.run, allocation, fx.agentDir, resumeState{
		ExpectedBaseSHA: fx.baseSHA,
		StartedAt:       time.Now().Add(-2 * time.Hour).UTC(),
		Pid:             999999,
		LeaderStartTime: "00000000000",
		RetryCount:      0,
		LastHeartbeat:   time.Now().Add(-2 * time.Hour).UTC(),
	})
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(fx.worktree, ".env.local"))
}

func TestPerformRescue_KillsDetachedWorktreeWriterBeforeCapture(t *testing.T) {
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
		LeaderStartTime: "00000000000",
		RetryCount:      0,
		LastHeartbeat:   time.Now().Add(-2 * time.Hour).UTC(),
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return !pidAlive(childPID)
	}, 2*time.Second, 20*time.Millisecond)
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
	t.Setenv("REAL_GIT", realGit)
	t.Setenv("FAKE_GIT_LOG", logPath)
	t.Setenv("FAKE_GIT_SLEEP_ON_PREFIX", "rev-parse HEAD")
	t.Setenv("FAKE_GIT_SLEEP_SECONDS", "5")
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))

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
	}, 3*time.Second, 10*time.Millisecond)

	cancel()

	err = <-errCh
	require.ErrorIs(t, err, context.Canceled)

	_, statErr := os.Stat(fx.manifestPath())
	require.Error(t, statErr)
	require.True(t, os.IsNotExist(statErr))

	_, statErr = os.Stat(fx.diffPath())
	require.Error(t, statErr)
	require.True(t, os.IsNotExist(statErr))
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
	t.Setenv("FAKE_CLAUDE_COMMIT", "1")

	require.NoError(t, fx.step.Run(context.Background(), fx.run))

	manifest := fx.readManifest(t)
	success := manifest.Value.(contracts.ManifestSuccess)
	// HEAD should advance to the new commit made inside the allocation
	// worktree, not match the foreign GIT_DIR's HEAD.
	assert.NotEqual(t, fx.baseSHA, success.HeadSHA, "agent commit must advance HEAD")
	assert.NotEqual(t, otherHead, success.HeadSHA)
}

func TestRenderPrompt_UsesChecklistAtWorktreeRoot(t *testing.T) {
	fx := newTestFixture(t, 5)
	promptText, err := renderPrompt(fx.cfg, promptData{
		TaskPackage: fx.run.TaskPackage,
		Agent:       fx.run.Agent,
		OutputDir:   manifestPrefix(fx.run.Pass, fx.run.Agent),
		TaskPrompt:  "Implement the requested change.",
	})
	require.NoError(t, err)
	assert.Contains(t, promptText, "checklist_output_path: checklist-result.json")
	assert.Contains(t, promptText, "checklist-result.json in the worktree root")
}

type failBeforeStartRunner struct{}

func (failBeforeStartRunner) Run(context.Context, runnerRequest) (runnerResult, error) {
	return runnerResult{}, errors.New("synthetic start failure")
}

func TestPidAliveTreatsEPERMAsAlive(t *testing.T) {
	originalKill := killProcess
	killProcess = func(pid int, sig syscall.Signal) error {
		return syscall.EPERM
	}
	t.Cleanup(func() {
		killProcess = originalKill
	})

	require.True(t, pidAlive(12345))
}

func TestShouldAttemptRescue_RequiresMatchingPGID(t *testing.T) {
	originalKill := killProcess
	originalGetpgid := getProcessGroupID
	killProcess = func(pid int, sig syscall.Signal) error {
		return nil
	}
	getProcessGroupID = func(pid int) (int, error) {
		return pid + 1, nil
	}
	t.Cleanup(func() {
		killProcess = originalKill
		getProcessGroupID = originalGetpgid
	})

	assert.False(t, shouldAttemptRescue(true, 12345, 12346, ""))
	assert.True(t, shouldAttemptRescue(true, 12345, 12345, ""))
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
	}, time.Second, 10*time.Millisecond)

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

func writeFakeGitWrapper(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "git")
	script := `#!/bin/bash
set -euo pipefail
joined="$*"
printf '%s\n' "$joined" >> "$FAKE_GIT_LOG"
if [[ -n "${FAKE_GIT_SLEEP_ON_PREFIX:-}" && "$joined" == "${FAKE_GIT_SLEEP_ON_PREFIX}"* ]]; then
  sleep "${FAKE_GIT_SLEEP_SECONDS:-5}"
fi
exec "$REAL_GIT" "$@"
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
}
