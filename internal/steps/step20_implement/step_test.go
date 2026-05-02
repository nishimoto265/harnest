package step20_implement

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/contracts"
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
