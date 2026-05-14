package io

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppendRegistryEntryCASAndIndexRebuild(t *testing.T) {
	registryPath := filepath.Join(realTempDir(t), "rules-registry.jsonl")
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
	assert.EqualValues(t, 0, firstResult.Offset)
	assert.Len(t, firstResult.Sha256, 64)

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
	assert.Greater(t, secondResult.Offset, int64(0))

	mismatch := contracts.RuleRegistryEntry{
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
			PrevHash:       strings.Repeat("f", 64),
			ByRunID:        "2026-04-21-PR3-cdef012",
			At:             time.Unix(300, 0).UTC(),
		},
	}

	_, err = AppendRegistryEntry(registryPath, mismatch)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRegistryCASMismatch)

	indexPath := filepath.Join(filepath.Dir(registryPath), "rules-idempotency-index.jsonl")
	indexEntries, err := RebuildIdempotencyIndex(registryPath, indexPath)
	require.NoError(t, err)
	require.Len(t, indexEntries, 2)
	assert.Equal(t, strings.Repeat("2", 64), indexEntries[0].IdempotencyKey)
	assert.Equal(t, strings.Repeat("4", 64), indexEntries[1].IdempotencyKey)

	loadedIndex, err := ReadJSONL[contracts.RuleIdempotencyIndexEntry](indexPath)
	require.NoError(t, err)
	require.Len(t, loadedIndex, 2)
	assert.Equal(t, indexEntries, loadedIndex)
}

func TestReadRegistryLinesRejectsUnterminatedFinalLine(t *testing.T) {
	registryPath := filepath.Join(realTempDir(t), "rules-registry.jsonl")
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
	payload, err := marshalJSONLRecord(entry)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(registryPath, payload, 0o644))

	_, err = readRegistryLines(registryPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unterminated final line")
}

func TestReadRegistryLinesRejectsBrokenPrevHashChain(t *testing.T) {
	registryPath := filepath.Join(realTempDir(t), "rules-registry.jsonl")
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
	firstPayload, err := marshalJSONLRecord(first)
	require.NoError(t, err)
	firstSum := sha256.Sum256(firstPayload)
	require.NotEqual(t, hex.EncodeToString(firstSum[:]), strings.Repeat("f", 64))

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
			PrevHash:       strings.Repeat("f", 64),
			ByRunID:        "2026-04-21-PR2-bcdef01",
			At:             time.Unix(200, 0).UTC(),
		},
	}
	secondPayload, err := marshalJSONLRecord(second)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(registryPath, append(append(firstPayload, '\n'), append(secondPayload, '\n')...), 0o644))

	_, err = readRegistryLines(registryPath)
	require.ErrorIs(t, err, ErrRegistryCASMismatch)
	assert.Contains(t, err.Error(), "expected_prev_hash")
}

func TestAppendRegistryPayloadRollsBackPartialRecordWrite(t *testing.T) {
	registryPath := filepath.Join(realTempDir(t), "rules-registry.jsonl")
	original := []byte("{\"existing\":true}\n")
	require.NoError(t, os.WriteFile(registryPath, original, 0o644))

	file, err := os.OpenFile(registryPath, os.O_RDWR|os.O_APPEND, defaultFilePerm)
	require.NoError(t, err)
	failFile := &failingAppendFile{
		File:      file,
		remaining: len(`{"new":true}`),
		err:       errors.New("injected write failure"),
	}
	err = appendRegistryPayload(registryPath, failFile, []byte(`{"new":true}`))
	require.Error(t, err)
	require.NoError(t, failFile.Close())

	data, readErr := os.ReadFile(registryPath)
	require.NoError(t, readErr)
	assert.Equal(t, original, data)
	assert.Equal(t, 1, failFile.truncateCalls)
	assert.Equal(t, 1, failFile.syncCalls)
}

func TestAppendRegistryEntry_ConcurrentCASAllowsSingleWinner(t *testing.T) {
	registryPath := filepath.Join(realTempDir(t), "rules-registry.jsonl")
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

	start := make(chan struct{})
	results := make([]error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			_, results[idx] = AppendRegistryEntry(registryPath, entry)
		}(i)
	}
	close(start)
	wg.Wait()

	successes := 0
	for _, err := range results {
		if err == nil {
			successes++
			continue
		}
		assert.True(t, errors.Is(err, ErrRegistryCASMismatch) || os.IsNotExist(err))
	}
	assert.Equal(t, 1, successes)

	lines, err := readRegistryLines(registryPath)
	require.NoError(t, err)
	require.Len(t, lines, 1)
}

func TestAppendRegistryEntry_FailsClosedWhenRegistryPathIdentityChanges(t *testing.T) {
	registryPath := filepath.Join(realTempDir(t), "rules-registry.jsonl")
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

	replacement := filepath.Join(realTempDir(t), "replacement-registry.jsonl")
	require.NoError(t, os.WriteFile(replacement, nil, defaultFilePerm))
	originalHook := registryBeforeAppendHook
	registryBeforeAppendHook = func() error {
		return os.Rename(replacement, registryPath)
	}
	t.Cleanup(func() {
		registryBeforeAppendHook = originalHook
	})

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

	_, err = AppendRegistryEntry(registryPath, second)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPathIdentityChanged)

	lines, readErr := readRegistryLines(registryPath)
	require.NoError(t, readErr)
	assert.Empty(t, lines)
}
