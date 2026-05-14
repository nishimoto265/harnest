package io

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppendJSONLAndReadJSONL(t *testing.T) {
	path := filepath.Join(realTempDir(t), "records.jsonl")
	require.NoError(t, AppendJSONL(path, testJSONLRecord{Name: "alpha"}))
	require.NoError(t, AppendJSONL(path, testJSONLRecord{Name: "beta"}))

	records, err := ReadJSONL[testJSONLRecord](path)
	require.NoError(t, err)
	require.Len(t, records, 2)
	assert.Equal(t, "alpha", records[0].Name)
	assert.Equal(t, "beta", records[1].Name)
}

func TestAppendJSONL_RejectsEntryTooLarge(t *testing.T) {
	path := filepath.Join(realTempDir(t), "records.jsonl")
	err := AppendJSONL(path, testJSONLRecord{Name: strings.Repeat("a", JSONLMaxLineBytes)})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEntryTooLarge)
}

func TestAppendJSONL_SyncsParentDirectory(t *testing.T) {
	path := filepath.Join(realTempDir(t), "records.jsonl")

	originalSync := directorySync
	var synced []string
	directorySync = func(path string) error {
		synced = append(synced, path)
		return nil
	}
	t.Cleanup(func() {
		directorySync = originalSync
	})

	require.NoError(t, AppendJSONL(path, testJSONLRecord{Name: "alpha"}))
	require.Equal(t, []string{filepath.Dir(path)}, synced)
}

func TestAppendRegistryEntry_SyncsParentDirectory(t *testing.T) {
	path := filepath.Join(realTempDir(t), "rules-registry.jsonl")
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

	originalSync := directorySync
	var synced []string
	directorySync = func(path string) error {
		synced = append(synced, path)
		return nil
	}
	t.Cleanup(func() {
		directorySync = originalSync
	})

	_, err := AppendRegistryEntry(path, entry)
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Dir(path)}, synced)
}

func TestAppendJSONL_RollsBackPartialWrite(t *testing.T) {
	path := filepath.Join(realTempDir(t), "records.jsonl")
	require.NoError(t, AppendJSONL(path, testJSONLRecord{Name: "alpha"}))

	originalOpen := appendJSONLOpenFile
	failFile := &failingAppendFile{
		remaining: 2,
		err:       errors.New("injected write failure"),
	}
	appendJSONLOpenFile = func(path string) (appendJSONLFile, error) {
		file, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, defaultFilePerm)
		if err != nil {
			return nil, err
		}
		failFile.File = file
		return failFile, nil
	}
	t.Cleanup(func() {
		appendJSONLOpenFile = originalOpen
	})
	originalDirectorySync := directorySync
	var synced []string
	directorySync = func(path string) error {
		synced = append(synced, path)
		return nil
	}
	t.Cleanup(func() {
		directorySync = originalDirectorySync
	})

	infoBefore, err := os.Stat(path)
	require.NoError(t, err)

	err = AppendJSONL(path, testJSONLRecord{Name: "beta"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "injected write failure")

	infoAfter, statErr := os.Stat(path)
	require.NoError(t, statErr)
	assert.Equal(t, infoBefore.Size(), infoAfter.Size())
	assert.Equal(t, 1, failFile.truncateCalls)
	assert.Equal(t, 1, failFile.syncCalls)
	assert.Equal(t, []string{filepath.Dir(path)}, synced)

	records, readErr := ReadJSONL[testJSONLRecord](path)
	require.NoError(t, readErr)
	require.Len(t, records, 1)
	assert.Equal(t, "alpha", records[0].Name)
}

func TestReadJSONL_StrictDecodeFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		line string
		want error
	}{
		{
			name: "duplicate key",
			line: `{"name":"a","name":"b"}` + "\n",
			want: contracts.ErrDuplicateJSONKey,
		},
		{
			name: "trailing json",
			line: `{"name":"a"}{"name":"b"}` + "\n",
			want: contracts.ErrTrailingJSON,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(realTempDir(t), "records.jsonl")
			require.NoError(t, os.WriteFile(path, []byte(tt.line), defaultFilePerm))

			_, err := ReadJSONL[testJSONLRecord](path)
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.want)
		})
	}
}
