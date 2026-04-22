package step50_implement

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPerformRescue_DiffOverLimitRequiresManualRecovery(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	allocation, err := worktreeFor(env.run.TaskPackage, 2, "a1")
	require.NoError(t, err)
	agentDir, err := agentDir(env.run.IO, 2, "a1")
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
	originalWorktreePIDs := rescueWorktreeProcessIDs
	rescueWorktreeProcessIDs = func(context.Context, string) ([]int, error) { return nil, nil }
	t.Cleanup(func() {
		rescueWorktreeProcessIDs = originalWorktreePIDs
	})

	step := newStep(env.run.Config, stepOptions{now: time.Now, heartbeatInterval: 10 * time.Millisecond, staleAfter: time.Second})
	_, err = step.performRescue(context.Background(), env.run, allocation, agentDir, staleResumeState(env.run.TaskPackage.BaseSHA))
	require.Error(t, err)
	assert.ErrorIs(t, err, agentrunner.ErrRescueDiffOverLimit)
	var manual *agentrunner.ManualRecoveryRequiredError
	require.ErrorAs(t, err, &manual)
	assert.Empty(t, rescueDirEntries(t, agentDir))
}

func TestPerformRescue_AggregateStorageLimitRemovesPartialRescueDir(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	allocation, err := worktreeFor(env.run.TaskPackage, 2, "a1")
	require.NoError(t, err)
	agentDir, err := agentDir(env.run.IO, 2, "a1")
	require.NoError(t, err)

	for i := 0; i < 26; i++ {
		path := filepath.Join(allocation.Path, "bulk", "file-"+strconv.Itoa(i)+".bin")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		file, err := os.Create(path)
		require.NoError(t, err)
		require.NoError(t, file.Truncate(10<<20))
		require.NoError(t, file.Close())
	}
	originalWorktreePIDs := rescueWorktreeProcessIDs
	rescueWorktreeProcessIDs = func(context.Context, string) ([]int, error) { return nil, nil }
	t.Cleanup(func() {
		rescueWorktreeProcessIDs = originalWorktreePIDs
	})

	step := newStep(env.run.Config, stepOptions{now: time.Now, heartbeatInterval: 10 * time.Millisecond, staleAfter: time.Second})
	_, err = step.performRescue(context.Background(), env.run, allocation, agentDir, staleResumeState(env.run.TaskPackage.BaseSHA))
	require.Error(t, err)
	assert.ErrorIs(t, err, agentrunner.ErrRescueStorageOverLimit)
	var manual *agentrunner.ManualRecoveryRequiredError
	require.ErrorAs(t, err, &manual)
	assert.Empty(t, rescueDirEntries(t, agentDir))
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
