package archive

import (
	"context"
	"fmt"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/nishimoto265/auto-improve/internal/steps/step70_decide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunSunsetWithLock_BlocksOnNeedsManualRecoveryStateWithoutSentinelFile(t *testing.T) {
	runsBase := realTempDir(t)
	worktreeBase := realTempDir(t)
	runCtx, err := internalio.NewRunContext("2026-04-21-PR77-deadbee", runsBase, worktreeBase)
	require.NoError(t, err)
	writer, err := state.NewWriterPath(filepath.Join(runsBase, "processed.jsonl"))
	require.NoError(t, err)
	require.NoError(t, writer.Append(contracts.StateEntry{
		Kind: contracts.StateKindNeedsManualRecovery,
		Value: contracts.StateEntryNeedsManualRecovery{
			Kind:       contracts.StateKindNeedsManualRecovery,
			PR:         77,
			RunID:      runCtx.RunID,
			Step:       contracts.FailedStep70,
			Reason:     contracts.RollbackReasonTransactionalFailure,
			FailedStep: contracts.FailedStep70,
			At:         time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}))

	result, err := RunSunsetWithLock(context.Background(), Opts{
		RunsBase:    runsBase,
		SunsetRunID: "sunset-blocked",
		Transitions: []Transition{deprecateTransition("rule-1")},
		Now:         func() time.Time { return time.Date(2026, 4, 21, 13, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, err)
	assert.Empty(t, result.AppendedOpIDs)
	assert.NoFileExists(t, filepath.Join(runsBase, "rules-registry.jsonl"))
}
func TestRunSunsetWithLock_BlocksOnAnyNeedsManualRecoveryState(t *testing.T) {
	for _, tc := range []struct {
		name       string
		runID      contracts.RunID
		pr         int
		step       contracts.FailedStep
		reason     contracts.RollbackReason
		failedStep contracts.FailedStep
	}{
		{
			name:       "step20 rescue loop",
			runID:      "2026-04-21-PR20-deadbee",
			pr:         20,
			step:       contracts.FailedStep20,
			reason:     contracts.RollbackReasonWorktreeRescueLoop,
			failedStep: contracts.FailedStep20,
		},
		{
			name:       "step50 rescue loop",
			runID:      "2026-04-21-PR50-deadbee",
			pr:         50,
			step:       contracts.FailedStep50,
			reason:     contracts.RollbackReasonWorktreeRescueLoop,
			failedStep: contracts.FailedStep50,
		},
		{
			name:       "step70 transactional failure",
			runID:      "2026-04-21-PR70-deadbee",
			pr:         70,
			step:       contracts.FailedStep70,
			reason:     contracts.RollbackReasonTransactionalFailure,
			failedStep: contracts.FailedStep70,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runsBase := realTempDir(t)
			worktreeBase := realTempDir(t)
			runCtx, err := internalio.NewRunContext(tc.runID, runsBase, worktreeBase)
			require.NoError(t, err)
			writer, err := state.NewWriterPath(filepath.Join(runsBase, "processed.jsonl"))
			require.NoError(t, err)
			require.NoError(t, writer.Append(contracts.StateEntry{
				Kind: contracts.StateKindNeedsManualRecovery,
				Value: contracts.StateEntryNeedsManualRecovery{
					Kind:       contracts.StateKindNeedsManualRecovery,
					PR:         tc.pr,
					RunID:      runCtx.RunID,
					Step:       tc.step,
					Reason:     tc.reason,
					FailedStep: tc.failedStep,
					At:         time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
				},
			}))

			result, err := RunSunsetWithLock(context.Background(), Opts{
				RunsBase:    runsBase,
				SunsetRunID: "sunset-blocked",
				Transitions: []Transition{deprecateTransition("rule-1")},
				Now:         func() time.Time { return time.Date(2026, 4, 21, 13, 0, 0, 0, time.UTC) },
			})
			require.NoError(t, err)
			assert.Empty(t, result.AppendedOpIDs)
			assert.NoFileExists(t, filepath.Join(runsBase, "rules-registry.jsonl"))
		})
	}
}
func TestRunSunsetWithLock_ProceedsAfterFinalizeCleanupWritesClearedMarker(t *testing.T) {
	runsBase := realTempDir(t)
	worktreeBase := realTempDir(t)
	seedArchiveRuleState(t, filepath.Join(runsBase, "rules-registry.jsonl"), "rule-1", contracts.RuleStatusActive)
	runCtx, err := internalio.NewRunContext("2026-04-21-PR78-deadbee", runsBase, worktreeBase)
	require.NoError(t, err)
	writer, err := state.NewWriterPath(filepath.Join(runsBase, "processed.jsonl"))
	require.NoError(t, err)
	require.NoError(t, writer.Append(contracts.StateEntry{
		Kind: contracts.StateKindNeedsManualRecovery,
		Value: contracts.StateEntryNeedsManualRecovery{
			Kind:       contracts.StateKindNeedsManualRecovery,
			PR:         78,
			RunID:      runCtx.RunID,
			Step:       contracts.FailedStep70,
			Reason:     contracts.RollbackReasonTransactionalFailure,
			FailedStep: contracts.FailedStep70,
			At:         time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}))
	require.NoError(t, step70_decide.FinalizeCleanup(runCtx, nil))

	result, err := RunSunsetWithLock(context.Background(), Opts{
		RunsBase:    runsBase,
		SunsetRunID: "sunset-cleared",
		Transitions: []Transition{deprecateTransition("rule-1")},
		Now:         func() time.Time { return time.Date(2026, 4, 21, 13, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, err)
	require.Len(t, result.AppendedOpIDs, 1)
	assert.FileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelClearedFilename(runCtx.RunID)))
}
func TestRunSunsetWithLock_ReturnsDivergedMarkerError(t *testing.T) {
	runsBase := realTempDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, divergedMarkerFile), []byte("diverged\n"), 0o644))

	result, err := RunSunsetWithLock(context.Background(), Opts{
		RunsBase:    runsBase,
		SunsetRunID: "sunset-diverged",
		AutoPlan:    true,
		Now:         func() time.Time { return time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC) },
	})
	require.ErrorIs(t, err, ErrStaleMarkerDiverged)
	assert.Empty(t, result.AppendedOpIDs)
}
func TestRunSunsetWithLock_ForceBypassesGate(t *testing.T) {
	runsBase := realTempDir(t)
	registryPath := filepath.Join(runsBase, "rules-registry.jsonl")
	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	seedArchiveRuleState(t, registryPath, "rule-1", contracts.RuleStatusDeprecated)
	require.NoError(t, writeLastSunsetAt(runsBase, now))

	result, err := RunSunsetWithLock(context.Background(), Opts{
		RunsBase:    runsBase,
		SunsetRunID: "sunset-gated",
		AutoPlan:    true,
		Now:         func() time.Time { return now.Add(time.Hour) },
	})
	require.NoError(t, err)
	assert.Empty(t, result.AppendedOpIDs)

	result, err = RunSunsetWithLock(context.Background(), Opts{
		RunsBase:    runsBase,
		SunsetRunID: "sunset-forced",
		AutoPlan:    true,
		Force:       true,
		Now:         func() time.Time { return now.Add(2 * time.Hour) },
	})
	require.NoError(t, err)
	require.Len(t, result.AppendedOpIDs, 1)
	assert.Len(t, readRegistryLinesForTest(t, registryPath), 3)
}
func TestRunSunsetWithLock_RunFailureWithoutProgressClearsMarker(t *testing.T) {
	runsBase := realTempDir(t)
	registryPath := filepath.Join(runsBase, "rules-registry.jsonl")
	seedArchiveRuleState(t, registryPath, "rule-1", contracts.RuleStatusActive)

	original := appendRegistryEntry
	appendRegistryEntry = func(string, contracts.RuleRegistryEntry) (contracts.RegistryAppendResult, error) {
		return contracts.RegistryAppendResult{}, fmt.Errorf("injected append failure")
	}
	t.Cleanup(func() {
		appendRegistryEntry = original
	})

	result, err := RunSunsetWithLock(context.Background(), Opts{
		RunsBase:    runsBase,
		SunsetRunID: "sunset-fails",
		Transitions: []Transition{deprecateTransition("rule-1")},
		Now:         func() time.Time { return time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC) },
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "injected append failure")
	assert.Empty(t, result.AppendedOpIDs)
	assert.NoFileExists(t, filepath.Join(runsBase, markerFilename))
	assert.NoFileExists(t, filepath.Join(runsBase, divergedMarkerFile))
	assert.Len(t, readRegistryLinesForTest(t, registryPath), 1)
}
func TestRunSunsetWithLock_StopsMutatingWhenSentinelAppearsMidRun(t *testing.T) {
	runsBase := realTempDir(t)
	seedArchiveRuleState(t, filepath.Join(runsBase, "rules-registry.jsonl"), "rule-1", contracts.RuleStatusActive)
	original := appendRegistryEntry
	appendCount := 0
	appendRegistryEntry = func(path string, entry contracts.RuleRegistryEntry) (contracts.RegistryAppendResult, error) {
		result, err := original(path, entry)
		if err == nil {
			appendCount++
			if appendCount == 1 {
				require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runsBase, "needs-recovery", "other-run.json"), contracts.NeedsRecoverySentinel{
					RunID:      "2026-04-21-PR99-deadbee",
					PR:         99,
					Reason:     contracts.RollbackReasonTransactionalFailure,
					FailedStep: contracts.FailedStep70,
					CreatedAt:  time.Date(2026, 4, 21, 10, 0, 1, 0, time.UTC),
				}))
			}
		}
		return result, err
	}
	t.Cleanup(func() {
		appendRegistryEntry = original
	})

	result, err := RunSunsetWithLock(context.Background(), Opts{
		RunsBase:    runsBase,
		SunsetRunID: "sunset-midrun",
		Transitions: []Transition{deprecateTransition("rule-1"), archiveTransition("rule-1", contracts.RuleStatusDeprecated)},
		Now:         func() time.Time { return time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, err)
	assert.Len(t, result.AppendedOpIDs, 1)
	assert.FileExists(t, filepath.Join(runsBase, markerFilename))
	assert.NoFileExists(t, filepath.Join(runsBase, lastSunsetFilename))
	assert.Len(t, readRegistryLinesForTest(t, filepath.Join(runsBase, "rules-registry.jsonl")), 2)
}
