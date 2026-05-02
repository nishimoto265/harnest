package archive

import (
	"context"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
	"time"
)

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
func TestBuildTransitionPlan_ArchivesDeprecatedRulesDeterministically(t *testing.T) {
	runsBase := realTempDir(t)
	registryPath := filepath.Join(runsBase, "rules-registry.jsonl")
	first := appendArchiveSeedAdded(t, registryPath, "rule-z", 1, "")
	appendArchiveSeedAdded(t, registryPath, "rule-a", 2, first.Sha256)
	deprecated := deprecateTransition("rule-a")
	entry, err := buildRegistryEntry(registryPath, deprecated, "seed-deprecate", ComputeOpID("seed-deprecate", deprecated.RuleID, transitionKey(deprecated)), time.Date(2026, 4, 21, 8, 30, 0, 0, time.UTC))
	require.NoError(t, err)
	third, err := internalio.AppendRegistryEntry(registryPath, entry)
	require.NoError(t, err)
	appendArchiveSeedAdded(t, registryPath, "rule-m", 4, third.Sha256)
	archived := archiveTransition("rule-m", contracts.RuleStatusActive)
	entry, err = buildRegistryEntry(registryPath, archived, "seed-archive", ComputeOpID("seed-archive", archived.RuleID, transitionKey(archived)), time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	_, err = internalio.AppendRegistryEntry(registryPath, entry)
	require.NoError(t, err)

	transitions, err := BuildTransitionPlan(runsBase)
	require.NoError(t, err)
	require.Len(t, transitions, 1)

	assert.Equal(t, archiveTransition("rule-a", contracts.RuleStatusDeprecated), transitions[0])
}
