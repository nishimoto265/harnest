package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nishimoto265/harnest/internal/config"
	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/steps/step70_decide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoverClearDivergedSunsetClearsMarkerAndUnblocksStep70(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "sunset-running.marker.diverged"), []byte("diverged\n"), 0o644))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	blocked, err := step70_decide.SentinelExists(runsBase)
	require.NoError(t, err)
	require.True(t, blocked)

	var stdout bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"recover", "--clear-diverged-sunset"})
	require.NoError(t, cmd.Execute())

	assert.NoFileExists(t, filepath.Join(runsBase, "sunset-running.marker.diverged"))
	blocked, err = step70_decide.SentinelExists(runsBase)
	require.NoError(t, err)
	assert.False(t, blocked)

	var payload map[string]string
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &payload))
	assert.Equal(t, "diverged_sunset_cleared", payload["event"])
	assert.Equal(t, runsBase, payload["runs_base"])
}

func TestRecoverClearDivergedSunsetRefusesWhenSunsetTransactionStillOpen(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "sunset-running.marker"), []byte("running\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "sunset-running.marker.diverged"), []byte("diverged\n"), 0o644))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--clear-diverged-sunset"})
	err = cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sunset-running.marker still exists")
	assert.FileExists(t, filepath.Join(runsBase, "sunset-running.marker.diverged"))
}

func TestRecoverClearDivergedSunsetFailsFastWhenPromotionLockHeld(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	lockPath := filepath.Join(runsBase, "promotion.lock")
	lock, err := internalio.AcquireFileLock(lockPath)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, lock.Unlock())
	}()

	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--clear-diverged-sunset"})
	err = cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "promotion.lock is held by another process")
}

func TestRecoverPolicyBranchFallsBackToConfigWhenSnapshotBranchEmpty(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	ctx := mustNewRunCtx(t, runID, runsBase, worktreeBase)
	require.NoError(t, os.MkdirAll(filepath.Join(ctx.RunDir(), "policy"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ctx.RunDir(), "policy", "snapshot.json"), []byte(`{"schema_version":"1","policy_branch":"","registry_head":""}`), 0o644))

	branch, meta, err := recoverPolicyBranch(ctx, config.Config{
		Repo: config.RepoConfig{PolicyBranch: "auto-improve/policy"},
	})
	require.NoError(t, err)
	require.NotNil(t, meta)
	assert.Equal(t, "auto-improve/policy", branch)
}
