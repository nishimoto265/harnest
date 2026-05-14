package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoverMarkManualAbortRenamesSentinel(t *testing.T) {
	root, runsBase, worktreeBase, runID := seedRecoverActionRun(t)
	runDir := filepath.Join(runsBase, string(runID))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))
	intention := seedRecoverIntention(runID, contracts.IntentionStagePlanning, strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 64))
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "70", "intention.json"), intention))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--run", string(runID), "--mark-manual-abort"})
	require.NoError(t, cmd.Execute())

	assert.NoFileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)))
	assert.FileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelAbortedFilename(runID)))
	decision, err := internalio.ReadJSON[contracts.Decision](filepath.Join(runDir, "70", "decision.json"))
	require.NoError(t, err)
	assert.Equal(t, contracts.DecisionActionRollback, decision.Action)
}

func TestRecoverMarkManualAbortAllowsPersistedIntentionWithoutSentinel(t *testing.T) {
	root, runsBase, worktreeBase, runID := seedRecoverActionRun(t)
	runDir := filepath.Join(runsBase, string(runID))
	intention := seedRecoverIntention(runID, contracts.IntentionStageNeedsManualRecovery, strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 64))
	intention.RecoveryReason = contracts.RollbackReasonTransactionalFailure
	intention.FailedStep = contracts.FailedStep70
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "70", "intention.json"), intention))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--run", string(runID), "--mark-manual-abort"})
	require.NoError(t, cmd.Execute())

	decision, err := internalio.ReadJSON[contracts.Decision](filepath.Join(runDir, "70", "decision.json"))
	require.NoError(t, err)
	assert.Equal(t, contracts.DecisionActionRollback, decision.Action)
}

func TestRecoverClearSentinelAppendsCompletedForNonTerminalRun(t *testing.T) {
	root, runsBase, worktreeBase, runID := seedRecoverActionRun(t)
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))
	ctx := mustNewRunCtx(t, runID, runsBase, worktreeBase)
	require.NoError(t, state.NewWriter(ctx).Append(contracts.StateEntry{
		Kind: contracts.StateKindStarted,
		Value: contracts.StateEntryStarted{
			Kind:  contracts.StateKindStarted,
			PR:    52,
			RunID: runID,
			Step:  contracts.FailedStep10,
			At:    time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--run", string(runID), "--clear-sentinel"})
	require.NoError(t, cmd.Execute())

	assert.NoFileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)))
	assert.FileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelClearedFilename(runID)))
	events, err := state.ScanEventsForRun(ctx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindCompleted, events[len(events)-1].Kind)
}

func TestRecoverClearSentinelDoesNotAppendCompletedAfterTerminalActionWithTrailingWarning(t *testing.T) {
	root, runsBase, worktreeBase, runID := seedRecoverActionRun(t)
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))
	ctx := mustNewRunCtx(t, runID, runsBase, worktreeBase)
	writer := state.NewWriter(ctx)
	require.NoError(t, writer.Append(contracts.StateEntry{
		Kind: contracts.StateKindCompleted,
		Value: contracts.StateEntryCompleted{
			Kind:   contracts.StateKindCompleted,
			PR:     52,
			RunID:  runID,
			Step:   contracts.FailedStep70,
			Detail: "already_terminal",
			At:     time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}))
	pr := 52
	source := contracts.WarningSourceStep70
	step := contracts.FailedStep70
	count := int64(1501)
	require.NoError(t, writer.Append(contracts.StateEntry{
		Kind: contracts.StateKindWarningRegistrySizeHigh,
		Value: contracts.StateEntryWarning{
			Kind:   contracts.StateKindWarningRegistrySizeHigh,
			PR:     &pr,
			RunID:  &runID,
			Source: &source,
			Step:   &step,
			Count:  &count,
			At:     time.Date(2026, 4, 21, 12, 1, 0, 0, time.UTC),
		},
	}))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--run", string(runID), "--clear-sentinel"})
	require.NoError(t, cmd.Execute())

	events, err := state.ScanEventsForRun(ctx, runID)
	require.NoError(t, err)
	completedCount := 0
	for _, event := range events {
		if event.Kind == contracts.StateKindCompleted {
			completedCount++
			completed, ok := event.Value.(contracts.StateEntryCompleted)
			require.True(t, ok)
			assert.NotEqual(t, "sentinel_manually_cleared", completed.Detail)
		}
	}
	assert.Equal(t, 1, completedCount)
	assert.Equal(t, contracts.StateKindWarningRegistrySizeHigh, events[len(events)-1].Kind)
	assert.NoFileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)))
}

func TestRecoverClearSentinelAllowsMissingTaskPackageAndCandidates(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	runID := contracts.RunID("2026-04-21-PR52-abcdef0")
	require.NoError(t, os.MkdirAll(filepath.Join(runsBase, "needs-recovery"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(runsBase, string(runID), "70"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))

	ctx := mustNewRunCtx(t, runID, runsBase, worktreeBase)
	require.NoError(t, state.NewWriter(ctx).Append(contracts.StateEntry{
		Kind: contracts.StateKindStarted,
		Value: contracts.StateEntryStarted{
			Kind:  contracts.StateKindStarted,
			PR:    52,
			RunID: runID,
			Step:  contracts.FailedStep10,
			At:    time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--run", string(runID), "--clear-sentinel"})
	require.NoError(t, cmd.Execute())

	assert.NoFileExists(t, filepath.Join(runsBase, string(runID), "task-package.json"))
	assert.NoFileExists(t, filepath.Join(runsBase, string(runID), "40", "candidates.json"))
	assert.NoFileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)))
	assert.FileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelClearedFilename(runID)))
}

func TestRecoverClearSentinelRejectsAbortedSentinel(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	runID := contracts.RunID("2026-04-21-PR52-abcdef0")
	require.NoError(t, os.MkdirAll(filepath.Join(runsBase, "needs-recovery"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(runsBase, string(runID), "70"), 0o755))
	abortedName := contracts.NeedsRecoverySentinelAbortedFilename(runID)
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", abortedName), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"manual_abort_pending_cleanup","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))

	ctx := mustNewRunCtx(t, runID, runsBase, worktreeBase)
	require.NoError(t, state.NewWriter(ctx).Append(contracts.StateEntry{
		Kind: contracts.StateKindStarted,
		Value: contracts.StateEntryStarted{
			Kind:  contracts.StateKindStarted,
			PR:    52,
			RunID: runID,
			Step:  contracts.FailedStep10,
			At:    time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--run", string(runID), "--clear-sentinel"})
	err = cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--finalize-cleanup")

	assert.FileExists(t, filepath.Join(runsBase, "needs-recovery", abortedName))
	assert.NoFileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelClearedFilename(runID)))
}
