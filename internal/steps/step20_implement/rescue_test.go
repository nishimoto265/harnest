package step20_implement

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPerformRescue_DiffOverLimitRequiresManualRecovery(t *testing.T) {
	fx := newTestFixture(t, 5)
	allocation, err := worktreeFor(fx.run.TaskPackage, fx.run.Pass, fx.run.Agent)
	require.NoError(t, err)

	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	wrapperDir := t.TempDir()
	writeRescueGitWrapper(t, wrapperDir, `if [[ "$joined" == *" diff HEAD --binary --no-ext-diff --no-textconv" ]]; then
  head -c $(((32 << 20) + 1)) /dev/zero | tr '\0' 'x'
  exit 0
fi`)
	t.Setenv("REAL_GIT", realGit)
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	stubQuiescentRescueWorktree(t)

	_, err = fx.step.performRescue(context.Background(), fx.run, allocation, fx.agentDir, staleResumeState(fx.baseSHA))
	require.Error(t, err)
	assert.ErrorIs(t, err, agentrunner.ErrRescueDiffOverLimit)
	var manual *agentrunner.ManualRecoveryRequiredError
	require.ErrorAs(t, err, &manual)
	assert.Empty(t, rescueDirEntries(t, fx.agentDir))
}

func TestPerformRescue_AggregateStorageLimitRequiresManualRecovery(t *testing.T) {
	fx := newTestFixture(t, 5)
	allocation, err := worktreeFor(fx.run.TaskPackage, fx.run.Pass, fx.run.Agent)
	require.NoError(t, err)

	for i := 0; i < 26; i++ {
		path := filepath.Join(fx.worktree, "bulk", "file-"+strconv.Itoa(i)+".bin")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		file, err := os.Create(path)
		require.NoError(t, err)
		require.NoError(t, file.Truncate(10<<20))
		require.NoError(t, file.Close())
	}
	stubQuiescentRescueWorktree(t)

	_, err = fx.step.performRescue(context.Background(), fx.run, allocation, fx.agentDir, staleResumeState(fx.baseSHA))
	require.Error(t, err)
	assert.ErrorIs(t, err, agentrunner.ErrRescueStorageOverLimit)
	var manual *agentrunner.ManualRecoveryRequiredError
	require.ErrorAs(t, err, &manual)
	assert.Empty(t, rescueDirEntries(t, fx.agentDir))
}

func TestResumeIfNeeded_AdoptsExistingRescueAfterCrashBeforeResumeStateSave(t *testing.T) {
	fx := newTestFixture(t, 5)
	allocation, err := worktreeFor(fx.run.TaskPackage, fx.run.Pass, fx.run.Agent)
	require.NoError(t, err)
	stubQuiescentRescueWorktree(t)

	oldTime := time.Now().Add(-2 * time.Hour).UTC()
	require.NoError(t, saveResumeState(fx.agentDir, resumeState{
		ExpectedBaseSHA: fx.baseSHA,
		StartedAt:       oldTime,
		Pid:             999999,
		LeaderStartTime: "stale-start",
		RetryCount:      0,
		LastHeartbeat:   oldTime,
	}))
	require.NoError(t, touchHeartbeat(fx.agentDir, oldTime))
	require.NoError(t, os.WriteFile(filepath.Join(fx.worktree, "dirty.txt"), []byte("dirty\n"), 0o644))
	currentDirtyFingerprint, err := agentrunner.ComputeDirtyFingerprint(context.Background(), fx.worktree)
	require.NoError(t, err)

	rescueDir := filepath.Join(fx.agentDir, rescuedDirName, "existing-rescue")
	require.NoError(t, os.MkdirAll(rescueDir, 0o755))
	require.NoError(t, agentrunner.WriteRescueState(filepath.Join(rescueDir, "state.json"), agentrunner.RescueStateFile{
		ExpectedBaseSHA:  fx.baseSHA,
		RescuedHeadSHA:   allocation.BaseSHA,
		RetryCount:       1,
		CommitCount:      0,
		BundleMode:       agentrunner.RescueBundleModeNone,
		CreatedAt:        time.Now().UTC(),
		Artifacts:        []agentrunner.RescueArtifactDigest{},
		DirtyFingerprint: currentDirtyFingerprint,
	}))

	retryCount, err := fx.step.resumeIfNeeded(context.Background(), fx.run, allocation, fx.agentDir)
	require.NoError(t, err)
	assert.Equal(t, 1, retryCount)
	assert.NoFileExists(t, filepath.Join(fx.worktree, "dirty.txt"))

	state, ok, err := loadResumeState(fx.agentDir)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Zero(t, state.Pid)
	assert.Equal(t, 1, state.RetryCount)
	assert.Len(t, rescueDirEntries(t, fx.agentDir), 1)
}

func TestResumeIfNeeded_SkipsStaleRescueDirWhenDirtyFingerprintDrifts(t *testing.T) {
	fx := newTestFixture(t, 5)
	allocation, err := worktreeFor(fx.run.TaskPackage, fx.run.Pass, fx.run.Agent)
	require.NoError(t, err)
	stubQuiescentRescueWorktree(t)

	oldTime := time.Now().Add(-2 * time.Hour).UTC()
	require.NoError(t, saveResumeState(fx.agentDir, resumeState{
		ExpectedBaseSHA: fx.baseSHA,
		StartedAt:       oldTime,
		Pid:             999999,
		LeaderStartTime: "stale-start",
		RetryCount:      0,
		LastHeartbeat:   oldTime,
	}))
	require.NoError(t, touchHeartbeat(fx.agentDir, oldTime))
	require.NoError(t, os.WriteFile(filepath.Join(fx.worktree, "new-change.txt"), []byte("post-crash\n"), 0o644))

	rescueDir := filepath.Join(fx.agentDir, rescuedDirName, "stale-rescue")
	require.NoError(t, os.MkdirAll(rescueDir, 0o755))
	require.NoError(t, agentrunner.WriteRescueState(filepath.Join(rescueDir, "state.json"), agentrunner.RescueStateFile{
		ExpectedBaseSHA:  fx.baseSHA,
		RescuedHeadSHA:   allocation.BaseSHA,
		RetryCount:       1,
		CommitCount:      0,
		BundleMode:       agentrunner.RescueBundleModeNone,
		CreatedAt:        time.Now().UTC(),
		Artifacts:        []agentrunner.RescueArtifactDigest{},
		DirtyFingerprint: "deadbeef" + strings.Repeat("0", 56),
	}))

	retryCount, err := fx.step.resumeIfNeeded(context.Background(), fx.run, allocation, fx.agentDir)
	require.NoError(t, err)
	assert.Equal(t, 1, retryCount)
	// The stale rescue dir must be retained; a new rescue dir must be created
	// to capture the post-crash worktree state before reset --hard clears it.
	assert.GreaterOrEqual(t, len(rescueDirEntries(t, fx.agentDir)), 2, "new rescue dir must be captured when fingerprint drifts")
}

func TestEnsureRescueLeaseQuiesced_PreservesTimeoutSentinel(t *testing.T) {
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

	err := ensureRescueLeaseQuiesced(context.Background(), t.TempDir(), resumeState{
		Pid:             999999,
		LeaderStartTime: "stale-start",
	})
	require.Error(t, err)
	var manual *agentrunner.ManualRecoveryRequiredError
	require.ErrorAs(t, err, &manual)
	assert.True(t, errors.Is(err, agentrunner.ErrRescueLeaseQuiesceTimedOut))
}

func staleResumeState(baseSHA string) resumeState {
	oldTime := time.Now().Add(-2 * time.Hour).UTC()
	return resumeState{
		ExpectedBaseSHA: baseSHA,
		StartedAt:       oldTime,
		Pid:             999999,
		LeaderStartTime: "stale-start",
		RetryCount:      0,
		LastHeartbeat:   oldTime,
	}
}

func writeRescueGitWrapper(t *testing.T, dir, body string) {
	t.Helper()
	path := filepath.Join(dir, "git")
	script := "#!/bin/bash\nset -euo pipefail\njoined=\"$*\"\n" + body + "\nexec \"$REAL_GIT\" \"$@\"\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
}

func rescueDirEntries(t *testing.T, agentDir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(agentDir, rescuedDirName))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	return entries
}
