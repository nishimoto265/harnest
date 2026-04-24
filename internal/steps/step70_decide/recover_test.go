package step70_decide

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoverMutationDoesNotBypassLockHeldByAnotherGoroutine(t *testing.T) {
	runCtx, _, _, _, _ := newFixture(t, "PR124")
	sentinelPath := filepath.Join(runCtx.RunsBase, needsRecoveryDir, contracts.NeedsRecoverySentinelFilename(runCtx.RunID))
	require.NoError(t, writeSentinel(runCtx.RunsBase, runCtx.RunID, 124, contracts.RollbackReasonTransactionalFailure, contracts.FailedStep70, fixedNow()()))

	ready := make(chan *internalio.FileLock, 1)
	release := make(chan struct{})
	holderErr := make(chan error, 1)
	go func() {
		lock, err := internalio.AcquireFileLock(runCtx.PromotionLockPath())
		if err != nil {
			holderErr <- err
			close(ready)
			return
		}
		ready <- lock
		<-release
		holderErr <- lock.Unlock()
	}()

	holderLock, ok := <-ready
	require.True(t, ok)
	require.NotNil(t, holderLock)
	require.True(t, internalio.IsFileLockHeld(runCtx.PromotionLockPath()))

	err := RecoverClearSentinelLocked(runCtx, nil)
	require.ErrorContains(t, err, "requires held promotion.lock")
	assert.FileExists(t, sentinelPath)

	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- RecoverClearSentinel(runCtx)
	}()
	select {
	case err := <-mutationDone:
		require.Failf(t, "recover mutation bypassed another holder", "err=%v", err)
	case <-time.After(150 * time.Millisecond):
	}
	assert.FileExists(t, sentinelPath)

	close(release)
	require.NoError(t, <-holderErr)
	require.NoError(t, <-mutationDone)
	assert.NoFileExists(t, sentinelPath)
}

func TestRecoverClearSentinelAndTerminalizeKeepsSentinelWhenCompletedAppendFails(t *testing.T) {
	runCtx, _, _, _, _ := newFixture(t, "PR125")
	sentinelPath := filepath.Join(runCtx.RunsBase, needsRecoveryDir, contracts.NeedsRecoverySentinelFilename(runCtx.RunID))
	clearedPath := filepath.Join(runCtx.RunsBase, needsRecoveryDir, contracts.NeedsRecoverySentinelClearedFilename(runCtx.RunID))
	require.NoError(t, writeSentinel(runCtx.RunsBase, runCtx.RunID, 125, contracts.RollbackReasonTransactionalFailure, contracts.FailedStep70, fixedNow()()))
	require.NoError(t, os.MkdirAll(runCtx.ProcessedPath(), 0o755))

	lock, err := internalio.AcquireFileLock(runCtx.PromotionLockPath())
	require.NoError(t, err)
	defer func() { _ = lock.Unlock() }()

	err = RecoverClearSentinelAndTerminalizeLocked(runCtx, lock, 125, true, fixedNow()())
	require.Error(t, err)
	assert.FileExists(t, sentinelPath)
	assert.NoFileExists(t, clearedPath)
}
