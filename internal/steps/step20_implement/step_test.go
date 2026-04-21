package step20_implement

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
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
				success := manifest.Value.(contracts.ManifestSuccess)
				require.Equal(t, fx.baseSHA, success.HeadSHA)
				diffBytes, readErr := os.ReadFile(fx.diffPath())
				require.NoError(t, readErr)
				require.Empty(t, diffBytes)
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
			name:       "rescue then success",
			timeoutSec: 5,
			env: map[string]string{
				"FAKE_CLAUDE_STDOUT": `{"event":"rescued"}` + "\n",
			},
			prepare: func(t *testing.T, fx *testFixture) {
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

				rescuedEntries, readDirErr := os.ReadDir(filepath.Join(fx.agentDir, rescuedDirName))
				require.NoError(t, readDirErr)
				require.NotEmpty(t, rescuedEntries)
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
		{
			name:       "missing heartbeat stale rescue then success",
			timeoutSec: 5,
			env: map[string]string{
				"FAKE_CLAUDE_STDOUT": `{"event":"missing-heartbeat-rescued"}` + "\n",
			},
			prepare: func(t *testing.T, fx *testFixture) {
				fx.seedResumeStateWithOptions(t, 0, false, 999999)
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

				rescueState := fx.readLatestRescueState(t)
				require.Equal(t, "none", rescueState.BundleMode)
			},
		},
		{
			name:       "rescue aborts when lease becomes active after flock",
			timeoutSec: 5,
			prepare: func(t *testing.T, fx *testFixture) {
				fx.seedResumeState(t, 0)
				hookTime := time.Now().Add(-30 * time.Second).UTC().Truncate(time.Second)
				activeState := resumeState{
					ExpectedBaseSHA: fx.baseSHA,
					StartedAt:       hookTime,
					Pid:             os.Getpid(),
					RetryCount:      7,
					LastHeartbeat:   hookTime,
				}
				fx.step.hooks.afterRescueLock = func(agentDir string) error {
					if err := touchHeartbeat(agentDir, hookTime); err != nil {
						return err
					}
					return saveResumeState(agentDir, activeState)
				}
			},
			assertion: func(t *testing.T, fx *testFixture, err error) {
				require.Error(t, err)
				require.ErrorIs(t, err, ErrRescueAbortedLeaseActive)
				_, statErr := os.Stat(fx.manifestPath())
				require.Error(t, statErr)
				require.True(t, os.IsNotExist(statErr))

				state, ok, readErr := loadResumeState(fx.agentDir)
				require.NoError(t, readErr)
				require.True(t, ok)
				require.Equal(t, os.Getpid(), state.Pid)
				require.Equal(t, 7, state.RetryCount)

				info, infoErr := os.Stat(filepath.Join(fx.agentDir, heartbeatFileName))
				require.NoError(t, infoErr)
				require.WithinDuration(t, state.LastHeartbeat, info.ModTime(), time.Second)
			},
		},
		{
			name:       "rescue falls back to full head bundle",
			timeoutSec: 5,
			env: map[string]string{
				"FAKE_CLAUDE_STDOUT": `{"event":"fallback"}` + "\n",
			},
			prepare: func(t *testing.T, fx *testFixture) {
				fx.seedResumeState(t, 0)
				orig := runGitCommand
				runGitCommand = func(ctx context.Context, worktreePath string, args ...string) ([]byte, error) {
					if len(args) >= 2 && args[0] == "rev-list" && args[1] == fx.baseSHA+"..HEAD" {
						return nil, fmt.Errorf("step20: git rev-list %s..HEAD: exit status 128: bad revision", fx.baseSHA)
					}
					return orig(ctx, worktreePath, args...)
				}
				t.Cleanup(func() {
					runGitCommand = orig
				})
			},
			assertion: func(t *testing.T, fx *testFixture, err error) {
				require.NoError(t, err)
				manifest := fx.readManifest(t)
				_, ok := manifest.Value.(contracts.ManifestSuccess)
				require.True(t, ok)

				rescueState := fx.readLatestRescueState(t)
				require.Equal(t, "full_head", rescueState.BundleMode)
				require.Equal(t, fx.baseSHA, rescueState.RescuedHeadSHA)
				require.GreaterOrEqual(t, rescueState.CommitCount, 1)
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

func TestStepRunReturnsContendedLeaseOnConcurrentStartup(t *testing.T) {
	fx := newTestFixture(t, 5)
	runner := &blockingRunner{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	fx.step.runner = runner

	firstErr := make(chan error, 1)
	go func() {
		firstErr <- fx.step.Run(context.Background(), fx.run)
	}()

	<-runner.started
	secondErr := fx.step.Run(context.Background(), fx.run)
	require.Error(t, secondErr)
	require.ErrorIs(t, secondErr, ErrAgentLeaseContended)

	close(runner.release)
	require.NoError(t, <-firstErr)
}

func TestStepRunDoesNotResetWorktreeAfterRescueContextCancellation(t *testing.T) {
	fx := newTestFixture(t, 5)
	fx.seedResumeState(t, 0)

	scratchPath := filepath.Join(fx.worktree, "scratch.txt")
	require.NoError(t, os.WriteFile(scratchPath, []byte("keep me"), 0o644))

	ctx, cancel := context.WithCancel(context.Background())
	orig := runGitCommand
	resetCalled := false
	runGitCommand = func(ctx context.Context, worktreePath string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "HEAD" {
			out, err := orig(ctx, worktreePath, args...)
			cancel()
			return out, err
		}
		if len(args) >= 2 && args[0] == "reset" && args[1] == "--hard" {
			resetCalled = true
		}
		return orig(ctx, worktreePath, args...)
	}
	t.Cleanup(func() {
		runGitCommand = orig
		cancel()
	})

	err := fx.step.Run(ctx, fx.run)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, resetCalled)

	bytes, readErr := os.ReadFile(scratchPath)
	require.NoError(t, readErr)
	require.Equal(t, "keep me", string(bytes))
}

func TestCommandRunnerTruncatesSessionFilePerAttempt(t *testing.T) {
	root := t.TempDir()
	scriptPath := writeFakeClaudeScript(t, root)
	sessionPath := filepath.Join(root, sessionFileName)
	require.NoError(t, os.WriteFile(sessionPath, []byte("stale\n"), 0o644))
	t.Setenv("FAKE_CLAUDE_STDOUT", "fresh\n")

	runner := commandRunner{now: time.Now}
	_, err := runner.Run(context.Background(), runnerRequest{
		Binary:      scriptPath,
		Workdir:     root,
		Prompt:      "test",
		SessionPath: sessionPath,
		Timeout:     time.Second,
	})
	require.NoError(t, err)

	bytes, readErr := os.ReadFile(sessionPath)
	require.NoError(t, readErr)
	require.Equal(t, "fresh\n", string(bytes))
}

func TestPidAliveTreatsEPERMAsAlive(t *testing.T) {
	orig := processKiller
	processKiller = func(pid int, sig syscall.Signal) error {
		return syscall.EPERM
	}
	t.Cleanup(func() {
		processKiller = orig
	})

	require.True(t, pidAlive(1234))
}

type testFixture struct {
	step     *Step
	run      RunContext
	cfg      *config.Config
	runIO    internalio.RunContext
	baseSHA  string
	agentDir string
	worktree string
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

func (fx *testFixture) readManifest(t *testing.T) contracts.Manifest {
	t.Helper()
	manifest, err := internalio.ReadJSON[contracts.Manifest](fx.manifestPath())
	require.NoError(t, err)
	return manifest
}

func (fx *testFixture) seedResumeState(t *testing.T, retryCount int) {
	t.Helper()
	fx.seedResumeStateWithOptions(t, retryCount, true, 999999)
}

func (fx *testFixture) seedResumeStateWithOptions(t *testing.T, retryCount int, writeHeartbeat bool, pid int) {
	t.Helper()
	oldTime := time.Now().Add(-2 * time.Hour).UTC()
	require.NoError(t, os.MkdirAll(fx.agentDir, 0o755))
	require.NoError(t, saveResumeState(fx.agentDir, resumeState{
		ExpectedBaseSHA: fx.baseSHA,
		StartedAt:       oldTime,
		Pid:             pid,
		RetryCount:      retryCount,
		LastHeartbeat:   oldTime,
	}))
	if writeHeartbeat {
		require.NoError(t, touchHeartbeat(fx.agentDir, oldTime))
	}
}

func (fx *testFixture) readLatestRescueState(t *testing.T) rescueStateFile {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(fx.agentDir, rescuedDirName))
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	latest := entries[len(entries)-1].Name()
	state, readErr := internalio.ReadJSON[rescueStateFile](filepath.Join(fx.agentDir, rescuedDirName, latest, "state.json"))
	require.NoError(t, readErr)
	return state
}

type blockingRunner struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingRunner) Run(ctx context.Context, req runnerRequest) (runnerResult, error) {
	if err := os.WriteFile(req.SessionPath, []byte("{\"event\":\"blocked\"}\n"), 0o644); err != nil {
		return runnerResult{}, err
	}
	r.once.Do(func() {
		close(r.started)
	})
	select {
	case <-r.release:
	case <-ctx.Done():
		return runnerResult{}, ctx.Err()
	}
	now := time.Now().UTC()
	return runnerResult{
		StartedAt:  now,
		FinishedAt: now,
	}, nil
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
fi
if [[ "${FAKE_CLAUDE_COMMIT:-}" == "1" ]]; then
  git commit --allow-empty -m test >/dev/null 2>&1
fi
if [[ "${FAKE_CLAUDE_SLEEP_SECONDS:-0}" != "0" ]]; then
  sleep "${FAKE_CLAUDE_SLEEP_SECONDS}"
fi
exit "${FAKE_CLAUDE_EXIT_CODE:-0}"
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
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
