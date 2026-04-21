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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunSunsetWithLock_Respects24hGate(t *testing.T) {
	runsBase := t.TempDir()
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
	assert.Len(t, lines, 1)
}

func TestRunSunsetWithLock_ReconcilesCompletedMarker(t *testing.T) {
	runsBase := t.TempDir()
	registryPath := filepath.Join(runsBase, "rules-registry.jsonl")
	staleRunID := "stale-run"
	transition := deprecateTransition("rule-1")

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
	assert.Len(t, lines, 1)
}

func TestRunSunsetWithLock_ReconcilesInterruptedMarker(t *testing.T) {
	runsBase := t.TempDir()
	registryPath := filepath.Join(runsBase, "rules-registry.jsonl")
	staleRunID := "stale-run"
	first := deprecateTransition("rule-1")
	second := archiveTransition("rule-1", contracts.RuleStatusDeprecated)

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
	require.Len(t, lines, 2)
	assert.Equal(t, ComputeOpID(staleRunID, first.RuleID, transitionKey(first)), opIDFromEntry(lines[0].Entry))
	assert.Equal(t, ComputeOpID(staleRunID, second.RuleID, transitionKey(second)), opIDFromEntry(lines[1].Entry))
}

func TestRunSunsetWithLock_ReconcilesUsingPersistedTransitionPlan(t *testing.T) {
	runsBase := t.TempDir()
	registryPath := filepath.Join(runsBase, "rules-registry.jsonl")
	staleRunID := "stale-run"
	persisted := []Transition{deprecateTransition("rule-1")}
	current := []Transition{
		deprecateTransition("rule-1"),
		archiveTransition("rule-1", contracts.RuleStatusDeprecated),
	}

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
	require.Len(t, lines, 1)
	entry := lines[0].Entry.Value.(contracts.RuleRegistryStatusChanged)
	assert.Equal(t, staleRunID, entry.BySunsetRunID)
	assert.Equal(t, time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC), entry.At)
}

func TestRunSunset_PerOpIdempotency(t *testing.T) {
	runsBase := t.TempDir()
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
	assert.Len(t, lines, 1)
}

func TestRunSunset_RegistryChain(t *testing.T) {
	runsBase := t.TempDir()
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
	require.Len(t, lines, 2)

	first := lines[0].Entry.Value.(contracts.RuleRegistryStatusChanged)
	second := lines[1].Entry.Value.(contracts.RuleRegistryArchived)
	assert.EqualValues(t, 1, first.VersionSeq)
	assert.EqualValues(t, 2, second.VersionSeq)
	assert.Equal(t, lines[0].Sha256, second.PrevHash)
}

func TestRunSunset_IndexSyncFailureDoesNotAbortCommittedRegistryAppend(t *testing.T) {
	runsBase := t.TempDir()
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
	runsBase := t.TempDir()
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
	runsBase := t.TempDir()
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

func TestRunSunsetWithLock_StopsMutatingWhenSentinelAppearsMidRun(t *testing.T) {
	runsBase := t.TempDir()
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
	assert.Len(t, readRegistryLinesForTest(t, filepath.Join(runsBase, "rules-registry.jsonl")), 1)
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
