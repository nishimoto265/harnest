package io

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureVerifiedIdempotencyIndex_RebuildsCorruptIndex(t *testing.T) {
	registryPath := filepath.Join(realTempDir(t), "rules-registry.jsonl")
	indexPath := filepath.Join(filepath.Dir(registryPath), "rules-idempotency-index.jsonl")

	first := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         "rule-1",
			RulePath:       "rules/rule-1.md",
			Sha256:         strings.Repeat("1", 64),
			IdempotencyKey: strings.Repeat("2", 64),
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        "2026-04-21-PR1-abcdef0",
			At:             time.Unix(100, 0).UTC(),
		},
	}
	firstResult, err := AppendRegistryEntry(registryPath, first)
	require.NoError(t, err)

	second := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindUpdated,
		Value: contracts.RuleRegistryUpdated{
			Kind:           contracts.RegistryKindUpdated,
			SchemaVersion:  "1",
			RuleID:         "rule-1",
			RulePath:       "rules/rule-1.md",
			Sha256:         strings.Repeat("3", 64),
			PrevSha256:     strings.Repeat("1", 64),
			IdempotencyKey: strings.Repeat("4", 64),
			VersionSeq:     2,
			PrevHash:       firstResult.Sha256,
			ByRunID:        "2026-04-21-PR2-bcdef01",
			At:             time.Unix(200, 0).UTC(),
		},
	}
	secondResult, err := AppendRegistryEntry(registryPath, second)
	require.NoError(t, err)

	require.NoError(t, AppendJSONL(indexPath, contracts.RuleIdempotencyIndexEntry{
		IdempotencyKey: strings.Repeat("2", 64),
		RegistryOffset: firstResult.Offset,
		RegistrySha256: strings.Repeat("f", 64),
		Kind:           contracts.RegistryKindAdded,
		At:             time.Unix(100, 0).UTC(),
	}))

	indexEntries, rebuilt, err := EnsureVerifiedIdempotencyIndex(registryPath, indexPath)
	require.NoError(t, err)
	assert.True(t, rebuilt)
	require.Len(t, indexEntries, 2)
	assert.Equal(t, firstResult.Offset, indexEntries[0].RegistryOffset)
	assert.Equal(t, secondResult.Offset, indexEntries[1].RegistryOffset)
	loadedIndex, err := ReadJSONL[contracts.RuleIdempotencyIndexEntry](indexPath)
	require.NoError(t, err)
	assert.Equal(t, indexEntries, loadedIndex)
}

func TestSyncIdempotencyIndex_RebuildDoesNotDuplicateCurrentEntry(t *testing.T) {
	registryPath := filepath.Join(realTempDir(t), "rules-registry.jsonl")
	indexPath := filepath.Join(filepath.Dir(registryPath), "rules-idempotency-index.jsonl")

	first := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         "rule-1",
			RulePath:       "rules/rule-1.md",
			Sha256:         strings.Repeat("1", 64),
			IdempotencyKey: strings.Repeat("2", 64),
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        "2026-04-21-PR1-abcdef0",
			At:             time.Unix(100, 0).UTC(),
		},
	}
	firstResult, err := AppendRegistryEntry(registryPath, first)
	require.NoError(t, err)
	_, err = RebuildIdempotencyIndex(registryPath, indexPath)
	require.NoError(t, err)

	second := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindUpdated,
		Value: contracts.RuleRegistryUpdated{
			Kind:           contracts.RegistryKindUpdated,
			SchemaVersion:  "1",
			RuleID:         "rule-1",
			RulePath:       "rules/rule-1.md",
			Sha256:         strings.Repeat("3", 64),
			PrevSha256:     strings.Repeat("1", 64),
			IdempotencyKey: strings.Repeat("4", 64),
			VersionSeq:     2,
			PrevHash:       firstResult.Sha256,
			ByRunID:        "2026-04-21-PR2-bcdef01",
			At:             time.Unix(200, 0).UTC(),
		},
	}
	secondResult, err := AppendRegistryEntry(registryPath, second)
	require.NoError(t, err)

	require.NoError(t, os.Remove(indexPath))
	require.NoError(t, SyncIdempotencyIndex(registryPath, indexPath, second, secondResult))

	loadedIndex, err := ReadJSONL[contracts.RuleIdempotencyIndexEntry](indexPath)
	require.NoError(t, err)
	require.Len(t, loadedIndex, 2)
	assert.Equal(t, secondResult.Offset, loadedIndex[1].RegistryOffset)
}

func TestSyncIdempotencyIndex_ConcurrentAppendDeduplicatesOffset(t *testing.T) {
	registryPath := filepath.Join(realTempDir(t), "rules-registry.jsonl")
	indexPath := filepath.Join(filepath.Dir(registryPath), "rules-idempotency-index.jsonl")
	entry := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         "rule-1",
			RulePath:       "rules/rule-1.md",
			Sha256:         strings.Repeat("1", 64),
			IdempotencyKey: strings.Repeat("2", 64),
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        "2026-04-21-PR1-abcdef0",
			At:             time.Unix(100, 0).UTC(),
		},
	}
	result, err := AppendRegistryEntry(registryPath, entry)
	require.NoError(t, err)
	_, err = RebuildIdempotencyIndex(registryPath, indexPath)
	require.NoError(t, err)

	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			errs[idx] = SyncIdempotencyIndex(registryPath, indexPath, entry, result)
		}(i)
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		require.True(t, err == nil || os.IsNotExist(err))
	}

	rows, err := ReadJSONL[contracts.RuleIdempotencyIndexEntry](indexPath)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestSyncIdempotencyIndex_FailsClosedWhenIndexPathIdentityChanges(t *testing.T) {
	registryPath := filepath.Join(realTempDir(t), "rules-registry.jsonl")
	indexPath := filepath.Join(filepath.Dir(registryPath), "rules-idempotency-index.jsonl")
	first := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         "rule-1",
			RulePath:       "rules/rule-1.md",
			Sha256:         strings.Repeat("1", 64),
			IdempotencyKey: strings.Repeat("2", 64),
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        "2026-04-21-PR1-abcdef0",
			At:             time.Unix(100, 0).UTC(),
		},
	}
	firstResult, err := AppendRegistryEntry(registryPath, first)
	require.NoError(t, err)
	_, err = RebuildIdempotencyIndex(registryPath, indexPath)
	require.NoError(t, err)

	second := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindUpdated,
		Value: contracts.RuleRegistryUpdated{
			Kind:           contracts.RegistryKindUpdated,
			SchemaVersion:  "1",
			RuleID:         "rule-1",
			RulePath:       "rules/rule-1.md",
			Sha256:         strings.Repeat("3", 64),
			PrevSha256:     strings.Repeat("1", 64),
			IdempotencyKey: strings.Repeat("4", 64),
			VersionSeq:     2,
			PrevHash:       firstResult.Sha256,
			ByRunID:        "2026-04-21-PR2-bcdef01",
			At:             time.Unix(200, 0).UTC(),
		},
	}
	secondResult, err := AppendRegistryEntry(registryPath, second)
	require.NoError(t, err)
	require.NoError(t, SyncIdempotencyIndex(registryPath, indexPath, second, secondResult))

	third := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindUpdated,
		Value: contracts.RuleRegistryUpdated{
			Kind:           contracts.RegistryKindUpdated,
			SchemaVersion:  "1",
			RuleID:         "rule-1",
			RulePath:       "rules/rule-1.md",
			Sha256:         strings.Repeat("5", 64),
			PrevSha256:     strings.Repeat("3", 64),
			IdempotencyKey: strings.Repeat("6", 64),
			VersionSeq:     3,
			PrevHash:       secondResult.Sha256,
			ByRunID:        "2026-04-21-PR3-cdef012",
			At:             time.Unix(300, 0).UTC(),
		},
	}
	thirdResult, err := AppendRegistryEntry(registryPath, third)
	require.NoError(t, err)

	replacement := filepath.Join(realTempDir(t), "replacement-index.jsonl")
	require.NoError(t, os.WriteFile(replacement, nil, defaultFilePerm))
	originalAppendHook := idempotencyIndexBeforeAppendHook
	originalRewriteHook := idempotencyIndexBeforeRewriteHook
	idempotencyIndexBeforeRewriteHook = func() error {
		if err := os.Remove(indexPath); err != nil {
			return err
		}
		return os.Rename(replacement, indexPath)
	}
	t.Cleanup(func() {
		idempotencyIndexBeforeAppendHook = originalAppendHook
		idempotencyIndexBeforeRewriteHook = originalRewriteHook
	})

	err = SyncIdempotencyIndex(registryPath, indexPath, third, thirdResult)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPathIdentityChanged)

	rows, readErr := ReadJSONL[contracts.RuleIdempotencyIndexEntry](indexPath)
	require.NoError(t, readErr)
	assert.Empty(t, rows)
}

func TestRegistryLookupLinesByIdempotencyIndex_UsesTailScanBelowMandatoryThreshold(t *testing.T) {
	registryPath := filepath.Join(realTempDir(t), "rules-registry.jsonl")
	writeRegistryChain(t, registryPath, RegistryMandatoryIndexAt-1)
	indexPath := filepath.Join(filepath.Dir(registryPath), "rules-idempotency-index.jsonl")
	require.NoError(t, os.Mkdir(indexPath, 0o755))

	lines, err := RegistryLookupLinesByIdempotencyIndex(registryPath, indexPath)
	require.NoError(t, err)
	require.Len(t, lines, RegistryMandatoryIndexAt-1)
}

func TestRegistryLookupLinesByIdempotencyIndex_FailsClosedAtMandatoryThresholdWhenIndexUnavailable(t *testing.T) {
	registryPath := filepath.Join(realTempDir(t), "rules-registry.jsonl")
	writeRegistryChain(t, registryPath, RegistryMandatoryIndexAt)
	indexPath := filepath.Join(filepath.Dir(registryPath), "rules-idempotency-index.jsonl")
	require.NoError(t, os.Mkdir(indexPath, 0o755))

	_, err := RegistryLookupLinesByIdempotencyIndex(registryPath, indexPath)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRegistryIdempotencyIndexUnavailable)
}

func writeRegistryChain(t *testing.T, registryPath string, count int) {
	t.Helper()

	var builder strings.Builder
	prevRegistryHash := ""
	prevRuleSHA := ""
	for i := 0; i < count; i++ {
		ruleSHA := fmt.Sprintf("%064x", i+1)
		idempotencyKey := fmt.Sprintf("%064x", i+10001)
		var entry contracts.RuleRegistryEntry
		if i == 0 {
			entry = contracts.RuleRegistryEntry{
				Kind: contracts.RegistryKindAdded,
				Value: contracts.RuleRegistryAdded{
					Kind:           contracts.RegistryKindAdded,
					SchemaVersion:  "1",
					RuleID:         "rule-1",
					RulePath:       "rules/rule-1.md",
					Sha256:         ruleSHA,
					IdempotencyKey: idempotencyKey,
					VersionSeq:     1,
					PrevHash:       "",
					ByRunID:        contracts.RunID(fmt.Sprintf("2026-04-21-PR1-%07x", i)),
					At:             time.Unix(100+int64(i), 0).UTC(),
				},
			}
		} else {
			entry = contracts.RuleRegistryEntry{
				Kind: contracts.RegistryKindUpdated,
				Value: contracts.RuleRegistryUpdated{
					Kind:           contracts.RegistryKindUpdated,
					SchemaVersion:  "1",
					RuleID:         "rule-1",
					RulePath:       "rules/rule-1.md",
					Sha256:         ruleSHA,
					PrevSha256:     prevRuleSHA,
					IdempotencyKey: idempotencyKey,
					VersionSeq:     int64(i + 1),
					PrevHash:       prevRegistryHash,
					ByRunID:        contracts.RunID(fmt.Sprintf("2026-04-21-PR1-%07x", i)),
					At:             time.Unix(100+int64(i), 0).UTC(),
				},
			}
		}
		_, err := contracts.MarshalStrict(entry)
		require.NoError(t, err)
		payload, err := contracts.CanonicalMarshal(entry)
		require.NoError(t, err)
		sum := sha256.Sum256(payload)
		prevRegistryHash = hex.EncodeToString(sum[:])
		prevRuleSHA = ruleSHA
		builder.Write(payload)
		builder.WriteByte('\n')
	}
	require.NoError(t, os.WriteFile(registryPath, []byte(builder.String()), defaultFilePerm))
}
