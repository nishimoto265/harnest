package archive

import (
	"context"
	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunSunsetWithLock_AutoPlanBuiltAfterLockAcquired(t *testing.T) {
	runsBase := realTempDir(t)
	registryPath := filepath.Join(runsBase, "rules-registry.jsonl")
	seedArchiveRuleState(t, registryPath, "rule-1", contracts.RuleStatusActive)

	lock, err := internalio.AcquireFileLock(filepath.Join(runsBase, "promotion.lock"))
	require.NoError(t, err)

	resultCh := make(chan struct {
		result Result
		err    error
	}, 1)
	go func() {
		result, err := RunSunsetWithLock(context.Background(), Opts{
			RunsBase:    runsBase,
			SunsetRunID: "sunset-auto",
			AutoPlan:    true,
			Force:       true,
			Now:         func() time.Time { return time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC) },
		})
		resultCh <- struct {
			result Result
			err    error
		}{result: result, err: err}
	}()

	deprecated := deprecateTransition("rule-1")
	entry, err := buildRegistryEntry(registryPath, deprecated, "seed-deprecate", ComputeOpID("seed-deprecate", deprecated.RuleID, transitionKey(deprecated)), time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	_, err = internalio.AppendRegistryEntry(registryPath, entry)
	require.NoError(t, err)
	require.NoError(t, lock.Unlock())

	select {
	case got := <-resultCh:
		require.NoError(t, got.err)
		require.Len(t, got.result.AppendedOpIDs, 1)
	case <-time.After(time.Second):
		t.Fatal("sunset did not finish after promotion.lock was released")
	}

	lines := readRegistryLinesForTest(t, registryPath)
	require.Len(t, lines, 3)
	archived, ok := lines[2].Entry.Value.(contracts.RuleRegistryArchived)
	require.True(t, ok)
	assert.Equal(t, "rule-1", archived.RuleID)
}
func TestRunSunsetWithLock_AutoPlanOnlyArchivesDeprecatedRules(t *testing.T) {
	runsBase := realTempDir(t)
	registryPath := filepath.Join(runsBase, "rules-registry.jsonl")
	first := appendArchiveSeedAdded(t, registryPath, "active-rule", 1, "")
	appendArchiveSeedAdded(t, registryPath, "deprecated-rule", 2, first.Sha256)
	deprecated := deprecateTransition("deprecated-rule")
	entry, err := buildRegistryEntry(registryPath, deprecated, "seed-deprecate", ComputeOpID("seed-deprecate", deprecated.RuleID, transitionKey(deprecated)), time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	_, err = internalio.AppendRegistryEntry(registryPath, entry)
	require.NoError(t, err)

	result, err := RunSunsetWithLock(context.Background(), Opts{
		RunsBase:    runsBase,
		SunsetRunID: "sunset-auto",
		AutoPlan:    true,
		Now:         func() time.Time { return time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, err)
	require.Len(t, result.AppendedOpIDs, 1)

	lines := readRegistryLinesForTest(t, registryPath)
	require.Len(t, lines, 4)
	archived, ok := lines[3].Entry.Value.(contracts.RuleRegistryArchived)
	require.True(t, ok)
	assert.Equal(t, "deprecated-rule", archived.RuleID)

	transitions, err := BuildTransitionPlan(runsBase)
	require.NoError(t, err)
	assert.Empty(t, transitions)
}
func TestRunSunsetWithLock_EmptyPlanDoesNotAdvanceGate(t *testing.T) {
	runsBase := realTempDir(t)
	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)

	result, err := RunSunsetWithLock(context.Background(), Opts{
		RunsBase:    runsBase,
		SunsetRunID: "sunset-empty",
		Now:         func() time.Time { return now },
	})
	require.NoError(t, err)
	assert.Empty(t, result.AppendedOpIDs)
	assert.NoFileExists(t, filepath.Join(runsBase, lastSunsetFilename))
	assert.NoFileExists(t, filepath.Join(runsBase, markerFilename))

	registryPath := filepath.Join(runsBase, "rules-registry.jsonl")
	seedArchiveRuleState(t, registryPath, "rule-later", contracts.RuleStatusActive)
	result, err = RunSunsetWithLock(context.Background(), Opts{
		RunsBase:    runsBase,
		SunsetRunID: "sunset-later",
		Transitions: []Transition{deprecateTransition("rule-later")},
		Now:         func() time.Time { return now.Add(time.Hour) },
	})
	require.NoError(t, err)
	require.Len(t, result.AppendedOpIDs, 1)
	assert.FileExists(t, filepath.Join(runsBase, lastSunsetFilename))
	assert.Len(t, readRegistryLinesForTest(t, registryPath), 2)
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
