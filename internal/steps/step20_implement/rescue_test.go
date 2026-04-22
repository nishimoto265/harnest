package step20_implement

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
