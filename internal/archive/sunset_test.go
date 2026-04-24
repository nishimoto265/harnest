package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/nishimoto265/auto-improve/internal/steps/step70_decide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func realTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return real
}

func TestRunSunsetWithLock_Respects24hGate(t *testing.T) {
	runsBase := realTempDir(t)
	seedArchiveRuleState(t, filepath.Join(runsBase, "rules-registry.jsonl"), "rule-1", contracts.RuleStatusActive)
	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	opts := Opts{
		RunsBase:    runsBase,
		SunsetRunID: "sunset-1",
		Transitions: []Transition{deprecateTransition("rule-1")},
		Now:         func() time.Time { return now },
	}
	result, err := RunSunsetWithLock(context.Background(), opts)
	require.NoError(t, err)
	require.Len(t, result.AppendedOpIDs, 1)

	now = now.Add(time.Hour)
	opts.SunsetRunID = "sunset-2"
	result, err = RunSunsetWithLock(context.Background(), opts)
	require.NoError(t, err)
	assert.Empty(t, result.AppendedOpIDs)

	lines := readRegistryLinesForTest(t, filepath.Join(runsBase, "rules-registry.jsonl"))
	assert.Len(t, lines, 2)
}

func TestRunSunsetWithLock_ReconcilesCompletedMarker(t *testing.T) {
	runsBase := realTempDir(t)
	registryPath := filepath.Join(runsBase, "rules-registry.jsonl")
	staleRunID := "stale-run"
	transition := deprecateTransition("rule-1")
	seedArchiveRuleState(t, registryPath, "rule-1", contracts.RuleStatusActive)

	entry, err := buildRegistryEntry(registryPath, transition, staleRunID, ComputeOpID(staleRunID, transition.RuleID, transitionKey(transition)), time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	_, err = internalio.AppendRegistryEntry(registryPath, entry)
	require.NoError(t, err)

	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runsBase, markerFilename), sunsetMarker{
		RecordedStartTime: time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
		SunsetRunID:       staleRunID,
		Transitions:       []Transition{transition},
	}))

	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	result, err := RunSunsetWithLock(context.Background(), Opts{
		RunsBase:    runsBase,
		SunsetRunID: "current-run",
		Transitions: []Transition{transition},
		Now:         func() time.Time { return now },
	})
	require.NoError(t, err)
	assert.Empty(t, result.AppendedOpIDs)
	assert.NoFileExists(t, filepath.Join(runsBase, markerFilename))

	lines := readRegistryLinesForTest(t, registryPath)
	assert.Len(t, lines, 2)
}

func TestRunSunsetWithLock_ReconcilesInterruptedMarker(t *testing.T) {
	runsBase := realTempDir(t)
	registryPath := filepath.Join(runsBase, "rules-registry.jsonl")
	staleRunID := "stale-run"
	first := deprecateTransition("rule-1")
	second := archiveTransition("rule-1", contracts.RuleStatusDeprecated)
	seedArchiveRuleState(t, registryPath, "rule-1", contracts.RuleStatusActive)

	entry, err := buildRegistryEntry(registryPath, first, staleRunID, ComputeOpID(staleRunID, first.RuleID, transitionKey(first)), time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	_, err = internalio.AppendRegistryEntry(registryPath, entry)
	require.NoError(t, err)

	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runsBase, markerFilename), sunsetMarker{
		RecordedStartTime: time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
		SunsetRunID:       staleRunID,
		Transitions:       []Transition{first, second},
	}))

	result, err := RunSunsetWithLock(context.Background(), Opts{
		RunsBase:    runsBase,
		SunsetRunID: "current-run",
		Transitions: []Transition{first, second},
		Now:         func() time.Time { return time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, err)
	assert.Empty(t, result.AppendedOpIDs)
	assert.NoFileExists(t, filepath.Join(runsBase, markerFilename))

	lines := readRegistryLinesForTest(t, registryPath)
	require.Len(t, lines, 3)
	assert.Equal(t, ComputeOpID(staleRunID, first.RuleID, transitionKey(first)), opIDFromEntry(lines[1].Entry))
	assert.Equal(t, ComputeOpID(staleRunID, second.RuleID, transitionKey(second)), opIDFromEntry(lines[2].Entry))
}

func TestRunSunsetWithLock_ReconcilesOwnPartialTailProgress(t *testing.T) {
	runsBase := realTempDir(t)
	registryPath := filepath.Join(runsBase, "rules-registry.jsonl")
	staleRunID := "stale-run"
	transitions := []Transition{
		deprecateTransition("rule-1"),
		archiveTransition("rule-1", contracts.RuleStatusDeprecated),
		restoreTransition("rule-1"),
	}
	seedArchiveRuleState(t, registryPath, "rule-1", contracts.RuleStatusActive)

	require.NoError(t, writeMarker(Opts{
		RunsBase:    runsBase,
		SunsetRunID: staleRunID,
		Transitions: transitions,
		Now:         func() time.Time { return time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC) },
	}))

	for _, transition := range transitions[:2] {
		entry, err := buildRegistryEntry(registryPath, transition, staleRunID, ComputeOpID(staleRunID, transition.RuleID, transitionKey(transition)), time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC))
		require.NoError(t, err)
		_, err = internalio.AppendRegistryEntry(registryPath, entry)
		require.NoError(t, err)
	}

	result, err := RunSunsetWithLock(context.Background(), Opts{
		RunsBase:    runsBase,
		SunsetRunID: "current-run",
		Transitions: transitions,
		Now:         func() time.Time { return time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, err)
	assert.Empty(t, result.AppendedOpIDs)
	assert.NoFileExists(t, filepath.Join(runsBase, markerFilename))

	lines := readRegistryLinesForTest(t, registryPath)
	require.Len(t, lines, 4)
	assert.Equal(t, ComputeOpID(staleRunID, transitions[2].RuleID, transitionKey(transitions[2])), opIDFromEntry(lines[3].Entry))
}

func TestRunSunsetWithLock_DetectsStaleMarkerRegistryDivergence(t *testing.T) {
	runsBase := realTempDir(t)
	registryPath := filepath.Join(runsBase, "rules-registry.jsonl")
	staleRunID := "stale-run"
	first := deprecateTransition("rule-1")
	second := archiveTransition("rule-1", contracts.RuleStatusDeprecated)

	initialAdd := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         "rule-1",
			RulePath:       "rules/rule-1.md",
			Sha256:         fmt.Sprintf("%064x", 1),
			IdempotencyKey: fmt.Sprintf("%064x", 1001),
			VersionSeq:     1,
			ByRunID:        "2026-04-21-PR1-aaaaaaa",
			At:             time.Date(2026, 4, 21, 8, 0, 0, 0, time.UTC),
		},
	}
	_, err := internalio.AppendRegistryEntry(registryPath, initialAdd)
	require.NoError(t, err)

	require.NoError(t, writeMarker(Opts{
		RunsBase:    runsBase,
		SunsetRunID: staleRunID,
		Transitions: []Transition{first, second},
		Now:         func() time.Time { return time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC) },
	}))

	entry, err := buildRegistryEntry(registryPath, first, staleRunID, ComputeOpID(staleRunID, first.RuleID, transitionKey(first)), time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	_, err = internalio.AppendRegistryEntry(registryPath, entry)
	require.NoError(t, err)

	update := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindUpdated,
		Value: contracts.RuleRegistryUpdated{
			Kind:           contracts.RegistryKindUpdated,
			SchemaVersion:  "1",
			RuleID:         "rule-1",
			RulePath:       "rules/rule-1.md",
			Sha256:         fmt.Sprintf("%064x", 2),
			PrevSha256:     fmt.Sprintf("%064x", 1),
			IdempotencyKey: fmt.Sprintf("%064x", 1002),
			VersionSeq:     3,
			PrevHash:       readRegistryLinesForTest(t, registryPath)[1].Sha256,
			ByRunID:        "2026-04-21-PR2-bbbbbbb",
			At:             time.Date(2026, 4, 21, 9, 30, 0, 0, time.UTC),
		},
	}
	_, err = internalio.AppendRegistryEntry(registryPath, update)
	require.NoError(t, err)

	result, err := RunSunsetWithLock(context.Background(), Opts{
		RunsBase:    runsBase,
		SunsetRunID: "current-run",
		Transitions: []Transition{first, second},
		Now:         func() time.Time { return time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC) },
	})
	require.ErrorIs(t, err, ErrStaleMarkerDiverged)
	assert.Empty(t, result.AppendedOpIDs)
	assert.NoFileExists(t, filepath.Join(runsBase, markerFilename))
	assert.FileExists(t, filepath.Join(runsBase, divergedMarkerFile))
	blocked, sentinelErr := step70_decide.SentinelExists(runsBase)
	require.NoError(t, sentinelErr)
	assert.True(t, blocked)
	assert.Len(t, readRegistryLinesForTest(t, registryPath), 3)
}

func TestRunSunsetWithLock_ReconcilesUsingPersistedTransitionPlan(t *testing.T) {
	runsBase := realTempDir(t)
	registryPath := filepath.Join(runsBase, "rules-registry.jsonl")
	staleRunID := "stale-run"
	persisted := []Transition{deprecateTransition("rule-1")}
	current := []Transition{
		deprecateTransition("rule-1"),
		archiveTransition("rule-1", contracts.RuleStatusDeprecated),
	}
	seedArchiveRuleState(t, registryPath, "rule-1", contracts.RuleStatusActive)

	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runsBase, markerFilename), sunsetMarker{
		RecordedStartTime: time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
		SunsetRunID:       staleRunID,
		Transitions:       persisted,
	}))

	result, err := RunSunsetWithLock(context.Background(), Opts{
		RunsBase:    runsBase,
		SunsetRunID: "current-run",
		Transitions: current,
		Now:         func() time.Time { return time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, err)
	assert.Empty(t, result.AppendedOpIDs)

	lines := readRegistryLinesForTest(t, registryPath)
	require.Len(t, lines, 2)
	entry := lines[1].Entry.Value.(contracts.RuleRegistryStatusChanged)
	assert.Equal(t, staleRunID, entry.BySunsetRunID)
	assert.Equal(t, time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC), entry.At)
}

func TestRunSunset_PerOpIdempotency(t *testing.T) {
	runsBase := realTempDir(t)
	seedArchiveRuleState(t, filepath.Join(runsBase, "rules-registry.jsonl"), "rule-1", contracts.RuleStatusActive)
	opts := Opts{
		RunsBase:    runsBase,
		SunsetRunID: "sunset-1",
		Transitions: []Transition{deprecateTransition("rule-1")},
		Now:         func() time.Time { return time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC) },
	}
	first, err := RunSunset(context.Background(), opts)
	require.NoError(t, err)
	require.Len(t, first.AppendedOpIDs, 1)

	second, err := RunSunset(context.Background(), opts)
	require.NoError(t, err)
	assert.Empty(t, second.AppendedOpIDs)
	assert.Len(t, second.SkippedOpIDs, 1)

	lines := readRegistryLinesForTest(t, filepath.Join(runsBase, "rules-registry.jsonl"))
	assert.Len(t, lines, 2)
}

func TestComputeOpID_UsesUnambiguousTupleEncoding(t *testing.T) {
	first := ComputeOpID("ab", "c", "d")
	second := ComputeOpID("a", "bc", "d")
	assert.NotEqual(t, first, second)
}

// F19: registry rows written with the pre-length-prefixed plain-concat op-id
// encoding must still be recognised as already-applied so an upgrade mid-sunset
// does not duplicate transitions or strand them behind permanent "diverged"
// markers.
func TestRunSunset_RecognisesLegacyOpIDEncoding(t *testing.T) {
	runsBase := realTempDir(t)
	registryPath := filepath.Join(runsBase, "rules-registry.jsonl")
	sunsetRunID := "legacy-sunset-run"
	transition := deprecateTransition("rule-1")
	seedArchiveRuleState(t, registryPath, "rule-1", contracts.RuleStatusActive)

	legacyOpID := computeLegacyOpID(sunsetRunID, transition.RuleID, transitionKey(transition))
	entry, err := buildRegistryEntry(registryPath, transition, sunsetRunID, legacyOpID, time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	_, err = internalio.AppendRegistryEntry(registryPath, entry)
	require.NoError(t, err)

	result, err := RunSunset(context.Background(), Opts{
		RunsBase:    runsBase,
		SunsetRunID: sunsetRunID,
		Transitions: []Transition{transition},
		Now:         func() time.Time { return time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, err)
	assert.Empty(t, result.AppendedOpIDs, "legacy encoded entry must suppress re-application")
	require.Len(t, result.SkippedOpIDs, 1)
	assert.Equal(t, legacyOpID, result.SkippedOpIDs[0])

	lines := readRegistryLinesForTest(t, registryPath)
	assert.Len(t, lines, 2, "no duplicate entry appended after recognising legacy op-id")
}

// F19: stale marker replay must honour both the current and legacy op-id
// encodings when computing progress, otherwise an interrupted sunset that was
// partially committed under the old scheme would be flagged as diverged.
func TestRunSunsetWithLock_ReconcilesMarkerWithLegacyOpIDProgress(t *testing.T) {
	runsBase := realTempDir(t)
	registryPath := filepath.Join(runsBase, "rules-registry.jsonl")
	sunsetRunID := "legacy-marker-run"
	first := deprecateTransition("rule-1")
	second := archiveTransition("rule-1", contracts.RuleStatusDeprecated)
	seedArchiveRuleState(t, registryPath, "rule-1", contracts.RuleStatusActive)

	legacyOpID := computeLegacyOpID(sunsetRunID, first.RuleID, transitionKey(first))
	entry, err := buildRegistryEntry(registryPath, first, sunsetRunID, legacyOpID, time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	_, err = internalio.AppendRegistryEntry(registryPath, entry)
	require.NoError(t, err)

	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runsBase, markerFilename), sunsetMarker{
		RecordedStartTime: time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
		SunsetRunID:       sunsetRunID,
		Transitions:       []Transition{first, second},
	}))

	result, err := RunSunsetWithLock(context.Background(), Opts{
		RunsBase:    runsBase,
		SunsetRunID: "current-run",
		Transitions: []Transition{first, second},
		Now:         func() time.Time { return time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, err)
	assert.Empty(t, result.AppendedOpIDs)
	assert.NoFileExists(t, filepath.Join(runsBase, markerFilename))
	assert.NoFileExists(t, filepath.Join(runsBase, divergedMarkerFile))

	lines := readRegistryLinesForTest(t, registryPath)
	require.Len(t, lines, 3)
	// Second transition is written under the new length-prefixed encoding; the
	// first (legacy) row is retained as-is.
	assert.Equal(t, legacyOpID, opIDFromEntry(lines[1].Entry))
	assert.Equal(t, ComputeOpID(sunsetRunID, second.RuleID, transitionKey(second)), opIDFromEntry(lines[2].Entry))
}

func TestRunSunset_RejectsTransitionForMissingRule(t *testing.T) {
	runsBase := realTempDir(t)
	_, err := RunSunset(context.Background(), Opts{
		RunsBase:    runsBase,
		SunsetRunID: "sunset-missing",
		Transitions: []Transition{deprecateTransition("missing-rule")},
		Now:         func() time.Time { return time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC) },
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rule not found")
}

func TestRunSunset_RegistryChain(t *testing.T) {
	runsBase := realTempDir(t)
	seedArchiveRuleState(t, filepath.Join(runsBase, "rules-registry.jsonl"), "rule-1", contracts.RuleStatusActive)
	opts := Opts{
		RunsBase:    runsBase,
		SunsetRunID: "sunset-1",
		Transitions: []Transition{
			deprecateTransition("rule-1"),
			archiveTransition("rule-1", contracts.RuleStatusDeprecated),
		},
		Now: func() time.Time { return time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC) },
	}
	_, err := RunSunset(context.Background(), opts)
	require.NoError(t, err)

	lines := readRegistryLinesForTest(t, filepath.Join(runsBase, "rules-registry.jsonl"))
	require.Len(t, lines, 3)

	first := lines[1].Entry.Value.(contracts.RuleRegistryStatusChanged)
	second := lines[2].Entry.Value.(contracts.RuleRegistryArchived)
	assert.EqualValues(t, 2, first.VersionSeq)
	assert.EqualValues(t, 3, second.VersionSeq)
	assert.Equal(t, lines[1].Sha256, second.PrevHash)
}

func TestRunSunset_IndexSyncFailureDoesNotAbortCommittedRegistryAppend(t *testing.T) {
	runsBase := realTempDir(t)
	registryPath := filepath.Join(runsBase, "rules-registry.jsonl")
	writeArchiveSeedRegistryAdds(t, registryPath, 1499)
	require.NoError(t, os.MkdirAll(filepath.Join(runsBase, "rules-idempotency-index.jsonl"), 0o755))

	result, err := RunSunset(context.Background(), Opts{
		RunsBase:    runsBase,
		SunsetRunID: "sunset-final",
		Transitions: []Transition{deprecateTransition("seed-1498")},
		Now:         func() time.Time { return time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, err)
	assert.Len(t, result.AppendedOpIDs, 1)

	lines := readRegistryLinesForTest(t, registryPath)
	assert.Len(t, lines, 1500)
}

func TestRunSunsetWithLock_ReconcilesLegacyTwoLineMarker(t *testing.T) {
	runsBase := realTempDir(t)
	path := filepath.Join(runsBase, markerFilename)
	require.NoError(t, os.WriteFile(path, []byte("2026-04-21T09:00:00Z\nlegacy-run\n"), 0o644))

	result, err := RunSunsetWithLock(context.Background(), Opts{
		RunsBase:    runsBase,
		SunsetRunID: "sunset-now",
		Transitions: nil,
		Now:         func() time.Time { return time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, err)
	assert.Empty(t, result.AppendedOpIDs)
	assert.NoFileExists(t, path)

	lastSunset, err := os.ReadFile(filepath.Join(runsBase, lastSunsetFilename))
	require.NoError(t, err)
	assert.Contains(t, string(lastSunset), "2026-04-21T09:00:00Z")
}

func TestRunSunsetWithLock_AutoTickTimesOutOnPromotionLock(t *testing.T) {
	runsBase := realTempDir(t)
	lock, err := internalio.AcquireFileLock(filepath.Join(runsBase, "promotion.lock"))
	require.NoError(t, err)
	defer func() { _ = lock.Unlock() }()

	start := time.Now()
	result, err := RunSunsetWithLock(context.Background(), Opts{
		RunsBase:    runsBase,
		SunsetRunID: "sunset-timeout",
		Transitions: nil,
		Now:         func() time.Time { return time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC) },
		LockTimeout: 100 * time.Millisecond,
	})
	require.NoError(t, err)
	assert.Empty(t, result.AppendedOpIDs)
	assert.Less(t, time.Since(start), time.Second)
}

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

func readRegistryLinesForTest(t *testing.T, path string) []registryLine {
	t.Helper()
	lines, err := readRegistryLines(path)
	require.NoError(t, err)
	return lines
}

func deprecateTransition(ruleID string) Transition {
	return Transition{
		RuleID:     ruleID,
		PrevStatus: contracts.RuleStatusActive,
		NewStatus:  contracts.RuleStatusDeprecated,
		Kind:       contracts.RegistryKindStatusChanged,
		Transition: contracts.SunsetTransitionDeprecate,
	}
}

func archiveTransition(ruleID string, prev contracts.RuleStatus) Transition {
	return Transition{
		RuleID:     ruleID,
		PrevStatus: prev,
		NewStatus:  contracts.RuleStatusArchived,
		Kind:       contracts.RegistryKindArchived,
		Transition: contracts.SunsetTransitionArchive,
	}
}

func restoreTransition(ruleID string) Transition {
	return Transition{
		RuleID:     ruleID,
		PrevStatus: contracts.RuleStatusArchived,
		NewStatus:  contracts.RuleStatusActive,
		Kind:       contracts.RegistryKindRestored,
		Transition: contracts.SunsetTransitionActivate,
	}
}

func opIDFromEntry(entry contracts.RuleRegistryEntry) string {
	switch v := entry.Value.(type) {
	case contracts.RuleRegistryStatusChanged:
		return v.OpID
	case contracts.RuleRegistryArchived:
		return v.OpID
	case contracts.RuleRegistryRestored:
		return v.OpID
	default:
		return ""
	}
}

func writeArchiveSeedRegistryAdds(t *testing.T, path string, count int) {
	t.Helper()

	var (
		buffer   bytes.Buffer
		prevHash string
	)
	for i := 0; i < count; i++ {
		entry := contracts.RuleRegistryEntry{
			Kind: contracts.RegistryKindAdded,
			Value: contracts.RuleRegistryAdded{
				Kind:           contracts.RegistryKindAdded,
				SchemaVersion:  "1",
				RuleID:         fmt.Sprintf("seed-%04d", i),
				RulePath:       fmt.Sprintf("rules/seed-%04d.md", i),
				Sha256:         fmt.Sprintf("%064x", i+1),
				IdempotencyKey: fmt.Sprintf("%064x", i+1000),
				VersionSeq:     int64(i + 1),
				PrevHash:       prevHash,
				ByRunID:        contracts.RunID(fmt.Sprintf("2026-04-21-PR%02d-abcdef0", (i%90)+10)),
				At:             time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
			},
		}
		var line bytes.Buffer
		require.NoError(t, contracts.EncodeStrict(&line, entry))
		payload := bytes.TrimSuffix(line.Bytes(), []byte{'\n'})
		_, err := buffer.Write(payload)
		require.NoError(t, err)
		require.NoError(t, buffer.WriteByte('\n'))

		sum := sha256.Sum256(payload)
		prevHash = hex.EncodeToString(sum[:])
	}
	require.NoError(t, internalio.WriteAtomic(path, buffer.Bytes()))
}

func seedArchiveRuleState(t *testing.T, registryPath, ruleID string, status contracts.RuleStatus) {
	t.Helper()

	added := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         ruleID,
			RulePath:       fmt.Sprintf("rules/%s.md", ruleID),
			Sha256:         fmt.Sprintf("%064x", 1),
			IdempotencyKey: fmt.Sprintf("%064x", 1001),
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        "2026-04-21-PR1-aaaaaaa",
			At:             time.Date(2026, 4, 21, 8, 0, 0, 0, time.UTC),
		},
	}
	result, err := internalio.AppendRegistryEntry(registryPath, added)
	require.NoError(t, err)

	if status == contracts.RuleStatusActive {
		return
	}

	entry := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindStatusChanged,
		Value: contracts.RuleRegistryStatusChanged{
			Kind:          contracts.RegistryKindStatusChanged,
			SchemaVersion: "1",
			RuleID:        ruleID,
			PrevStatus:    contracts.RuleStatusActive,
			NewStatus:     status,
			Transition:    contracts.SunsetTransitionDeprecate,
			OpID:          ComputeOpID("seed-sunset", ruleID, string(contracts.SunsetTransitionDeprecate)),
			VersionSeq:    2,
			PrevHash:      result.Sha256,
			BySunsetRunID: "seed-sunset",
			At:            time.Date(2026, 4, 21, 8, 30, 0, 0, time.UTC),
		},
	}
	_, err = internalio.AppendRegistryEntry(registryPath, entry)
	require.NoError(t, err)
}
