package archive

import (
	"context"
	"fmt"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/steps/step70_decide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
	"time"
)

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
