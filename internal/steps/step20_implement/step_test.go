package step20_implement

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStepRun(t *testing.T) {
	script := writeFakeClaudeScript(t)

	tests := []struct {
		name           string
		mode           string
		timeout        int
		sleep          string
		setupResume    func(t *testing.T, fx testFixture)
		assertion      func(t *testing.T, fx testFixture, err error)
		expectErr      bool
		writeChecklist bool
	}{
		{
			name:    "success with commit",
			mode:    "success_commit",
			timeout: 5,
			assertion: func(t *testing.T, fx testFixture, err error) {
				require.NoError(t, err)
				manifest := readManifest(t, fx.run.IO, 1, "a1")
				success, ok := manifest.Value.(contracts.ManifestSuccess)
				require.True(t, ok)
				assert.NotEqual(t, success.BaseSHA, success.HeadSHA)
				assert.FileExists(t, filepath.Join(fx.run.IO.RunDir(), success.DiffPath))
				assert.FileExists(t, filepath.Join(fx.run.IO.RunDir(), success.SessionPath))
				assert.FileExists(t, filepath.Join(fx.run.IO.RunDir(), success.ChecklistPath))
			},
		},
		{
			name:    "success without commit",
			mode:    "success_no_commit",
			timeout: 5,
			assertion: func(t *testing.T, fx testFixture, err error) {
				require.NoError(t, err)
				manifest := readManifest(t, fx.run.IO, 1, "a1")
				success, ok := manifest.Value.(contracts.ManifestSuccess)
				require.True(t, ok)
				assert.Equal(t, success.BaseSHA, success.HeadSHA)
				diffPath := filepath.Join(fx.run.IO.RunDir(), success.DiffPath)
				data, readErr := os.ReadFile(diffPath)
				require.NoError(t, readErr)
				assert.Empty(t, string(data))
			},
		},
		{
			name:    "rate limit error",
			mode:    "rate_limit",
			timeout: 5,
			assertion: func(t *testing.T, fx testFixture, err error) {
				require.NoError(t, err)
				manifest := readManifest(t, fx.run.IO, 1, "a1")
				failure, ok := manifest.Value.(contracts.ManifestError)
				require.True(t, ok)
				assert.Equal(t, "rate_limit", failure.Reason)
				assert.Equal(t, 1, failure.ExitCode)
			},
		},
		{
			name:    "timeout",
			mode:    "timeout",
			timeout: 1,
			sleep:   "2",
			assertion: func(t *testing.T, fx testFixture, err error) {
				require.NoError(t, err)
				manifest := readManifest(t, fx.run.IO, 1, "a1")
				timeoutManifest, ok := manifest.Value.(contracts.ManifestTimeout)
				require.True(t, ok)
				assert.Equal(t, 1, timeoutManifest.TimeoutSeconds)
			},
		},
		{
			name:    "rescue then success",
			mode:    "success_commit",
			timeout: 5,
			setupResume: func(t *testing.T, fx testFixture) {
				headBefore := gitOutputForTest(t, fx.worktreePath, "rev-parse", "HEAD")
				runGit(t, fx.worktreePath, "commit", "--allow-empty", "-m", "stale-work")
				paths, err := agentPathsFor(fx.run.IO, 1, "a1")
				require.NoError(t, err)
				require.NoError(t, ensureDir(paths.dir))
				staleAt := time.Now().Add(-10 * time.Minute)
				require.NoError(t, internalio.WriteJSONAtomic(paths.resumeStatePath, resumeState{
					ExpectedBaseSHA: headBefore,
					StartedAt:       staleAt,
					PID:             999999,
					RetryCount:      0,
					LastHeartbeat:   staleAt,
				}))
				require.NoError(t, internalio.WriteAtomic(paths.heartbeatPath, []byte{}))
				require.NoError(t, os.Chtimes(paths.heartbeatPath, staleAt, staleAt))
			},
			assertion: func(t *testing.T, fx testFixture, err error) {
				require.NoError(t, err)
				manifest := readManifest(t, fx.run.IO, 1, "a1")
				success, ok := manifest.Value.(contracts.ManifestSuccess)
				require.True(t, ok)
				assert.NotEqual(t, success.BaseSHA, success.HeadSHA)

				paths, pathErr := agentPathsFor(fx.run.IO, 1, "a1")
				require.NoError(t, pathErr)
				state, ok, stateErr := loadResumeState(paths.resumeStatePath)
				require.NoError(t, stateErr)
				require.True(t, ok)
				assert.Equal(t, 1, state.RetryCount)
				entries, readDirErr := os.ReadDir(paths.rescuedDir)
				require.NoError(t, readDirErr)
				assert.NotEmpty(t, entries)
			},
		},
		{
			name:    "rescue exhausted",
			mode:    "success_commit",
			timeout: 5,
			setupResume: func(t *testing.T, fx testFixture) {
				paths, err := agentPathsFor(fx.run.IO, 1, "a1")
				require.NoError(t, err)
				require.NoError(t, ensureDir(paths.dir))
				staleAt := time.Now().Add(-10 * time.Minute)
				require.NoError(t, internalio.WriteJSONAtomic(paths.resumeStatePath, resumeState{
					ExpectedBaseSHA: fx.baseSHA,
					StartedAt:       staleAt,
					PID:             999999,
					RetryCount:      3,
					LastHeartbeat:   staleAt,
				}))
				require.NoError(t, internalio.WriteAtomic(paths.heartbeatPath, []byte{}))
				require.NoError(t, os.Chtimes(paths.heartbeatPath, staleAt, staleAt))
			},
			assertion: func(t *testing.T, fx testFixture, err error) {
				require.Error(t, err)
				var exhausted *RescueExhaustedError
				require.True(t, errors.As(err, &exhausted))
				assert.Equal(t, contracts.AgentID("a1"), exhausted.Agent)
				assert.Equal(t, 3, exhausted.RetryCount)

				manifestPath, pathErr := fx.run.IO.ManifestPath(1, "a1")
				require.NoError(t, pathErr)
				_, statErr := os.Stat(manifestPath)
				assert.True(t, os.IsNotExist(statErr))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FAKE_CLAUDE_MODE", tt.mode)
			t.Setenv("FAKE_CLAUDE_SLEEP", tt.sleep)

			fixture := newTestFixture(t, script, tt.timeout)
			if tt.setupResume != nil {
				tt.setupResume(t, fixture)
			}

			step := NewStep(fixture.cfg)
			err := step.Run(context.Background(), fixture.run)
			if tt.expectErr {
				require.Error(t, err)
			}
			tt.assertion(t, fixture, err)
		})
	}
}

type testFixture struct {
	cfg          *config.Config
	run          RunContext
	baseSHA      string
	worktreePath string
}

func newTestFixture(t *testing.T, claudeScript string, timeoutSeconds int) testFixture {
	t.Helper()

	runsBase := filepath.Join(t.TempDir(), "runs")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")
	targetWorktree := filepath.Join(worktreeBase, "2026-04-21-PR42-abcdef0-pass1-a1")
	baseSHA := initGitRepo(t, targetWorktree)

	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	pkg := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runID,
		PR:                      42,
		Title:                   "step20 test",
		BaseSHA:                 baseSHA,
		BestBranch:              "best",
		ReconstructedTaskPrompt: "Implement the requested change.",
		CreatedAt:               time.Now().UTC(),
		Worktrees:               make([]contracts.WorktreeAllocation, 0, 6),
	}
	for pass := 1; pass <= 2; pass++ {
		for _, agent := range []contracts.AgentID{"a1", "a2", "a3"} {
			path := filepath.Join(worktreeBase, fmt.Sprintf("%s-pass%d-%s", runID, pass, agent))
			if pass == 1 && agent == "a1" {
				path = targetWorktree
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

	runIO, err := internalio.RunContextFromTaskPackage(pkg, runsBase, worktreeBase)
	require.NoError(t, err)

	cfg := &config.Config{
		Repo: config.RepoConfig{
			Root:       projectRoot(t),
			BestBranch: "best",
		},
		Worktree: config.WorktreeConfig{
			Base: worktreeBase,
		},
		Paths: config.PathsConfig{
			Runs: runsBase,
		},
		ClaudeCLIPath:             claudeScript,
		RescueMaxRetries:          3,
		RegistryHighThreshold:     config.DefaultRegistryHighThreshold,
		RegistryCriticalThreshold: config.DefaultRegistryCriticalThreshold,
		StepTimeouts: map[string]int{
			"step10": 5,
			"step20": timeoutSeconds,
			"step30": 5,
			"step40": 5,
			"step50": 5,
			"step60": 5,
			"step70": 5,
		},
	}

	return testFixture{
		cfg: cfg,
		run: RunContext{
			Config:      cfg,
			PR:          42,
			Pass:        1,
			Agent:       "a1",
			IO:          runIO,
			TaskPackage: &pkg,
		},
		baseSHA:      baseSHA,
		worktreePath: targetWorktree,
	}
}

func readManifest(t *testing.T, runIO internalio.RunContext, pass int, agent contracts.AgentID) contracts.Manifest {
	t.Helper()
	path, err := runIO.ManifestPath(pass, agent)
	require.NoError(t, err)
	manifest, err := internalio.ReadJSON[contracts.Manifest](path)
	require.NoError(t, err)
	return manifest
}

func initGitRepo(t *testing.T, path string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(path, 0o755))
	runGit(t, path, "init", "-b", "main")
	runGit(t, path, "config", "user.email", "step20@example.com")
	runGit(t, path, "config", "user.name", "Step20 Test")
	require.NoError(t, os.WriteFile(filepath.Join(path, "README.md"), []byte("base\n"), 0o644))
	runGit(t, path, "add", "README.md")
	runGit(t, path, "commit", "-m", "base")
	return gitOutputForTest(t, path, "rev-parse", "HEAD")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, string(output))
}

func gitOutputForTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, string(output))
	return string(bytesTrimSpace(output))
}

func bytesTrimSpace(data []byte) []byte {
	start := 0
	for start < len(data) && (data[start] == ' ' || data[start] == '\n' || data[start] == '\t' || data[start] == '\r') {
		start++
	}
	end := len(data)
	for end > start && (data[end-1] == ' ' || data[end-1] == '\n' || data[end-1] == '\t' || data[end-1] == '\r') {
		end--
	}
	return data[start:end]
}

func writeFakeClaudeScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-claude.sh")
	script := `#!/bin/sh
set -eu
printf '%s\n' '{"event":"session"}'
case "${FAKE_CLAUDE_MODE:-success_no_commit}" in
  success_commit)
    git commit --allow-empty -m "${FAKE_CLAUDE_COMMIT_MESSAGE:-test}" >/dev/null 2>&1
    ;;
  success_no_commit)
    ;;
  rate_limit)
    echo "rate_limit exceeded" >&2
    exit 1
    ;;
  timeout)
    sleep "${FAKE_CLAUDE_SLEEP:-2}"
    ;;
  *)
    echo "unknown mode: ${FAKE_CLAUDE_MODE}" >&2
    exit 2
    ;;
esac
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

func projectRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		readme := filepath.Join(dir, "README.md")
		promptsDir := filepath.Join(dir, "prompts")
		if _, readmeErr := os.Stat(readme); readmeErr == nil {
			if info, promptsErr := os.Stat(promptsDir); promptsErr == nil && info.IsDir() {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("project root not found from %s", dir)
		}
		dir = parent
	}
}
