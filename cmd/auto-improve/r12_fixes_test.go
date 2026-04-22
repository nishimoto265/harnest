package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRecoverInspectDoesNotCreatePromotionLockOnFreshRunsBase covers M1.
// Before the fix, `recover --inspect` called AcquireFileLock on
// promotion.lock, which has O_CREATE set and materializes the file every
// time. After the fix, InspectFileLock opens read-only; if the file does
// not exist it simply proceeds without creating it.
func TestRecoverInspectDoesNotCreatePromotionLockOnFreshRunsBase(t *testing.T) {
	root := t.TempDir()
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
	_, preErr := os.Stat(lockPath)
	require.True(t, os.IsNotExist(preErr), "promotion.lock must not exist before test")

	var stdout bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"recover", "--inspect"})
	require.NoError(t, cmd.Execute())

	_, postErr := os.Stat(lockPath)
	assert.True(t, os.IsNotExist(postErr),
		"recover --inspect must not materialize promotion.lock on fresh runs_base; stat err=%v", postErr)
}
