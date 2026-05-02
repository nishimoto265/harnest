package io

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteAtomic_PreservesSiblingTempsAndWritesFile(t *testing.T) {
	dir := realTempDir(t)
	target := filepath.Join(dir, "manifest.json")
	staleA := target + ".tmp-1-1-aaaa"
	staleB := target + ".tmp-1-2-bbbb"
	require.NoError(t, os.WriteFile(staleA, []byte("stale"), defaultFilePerm))
	require.NoError(t, os.WriteFile(staleB, []byte("stale"), defaultFilePerm))

	require.NoError(t, WriteAtomic(target, []byte(`{"ok":true}`)))

	data, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, string(data))
	assert.FileExists(t, staleA)
	assert.FileExists(t, staleB)
}

func TestWriteAtomic_RenameFailureCleansTemp(t *testing.T) {
	dir := realTempDir(t)
	target := filepath.Join(dir, "decision.json")

	originalRename := atomicRename
	atomicRename = func(oldPath, newPath string) error {
		return errors.New("rename failed")
	}
	t.Cleanup(func() {
		atomicRename = originalRename
	})

	err := WriteAtomic(target, []byte(`{"ok":true}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rename failed")

	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	assert.Empty(t, entries)
}

func TestWriteAtomic_ConcurrentWritersDoNotDeletePeerTemps(t *testing.T) {
	dir := realTempDir(t)
	target := filepath.Join(dir, "manifest.json")

	originalRename := atomicRename
	originalAfterTempCreate := atomicAfterTempCreate
	enteredRename := make(chan string, 2)
	releaseRename := make(chan struct{})
	firstTempCreated := make(chan string, 1)
	secondTempCreated := make(chan string, 1)
	releaseFirstWriter := make(chan struct{})
	var tempCreateMu sync.Mutex
	tempCreateCount := 0
	atomicRename = func(oldPath, newPath string) error {
		enteredRename <- oldPath
		<-releaseRename
		return originalRename(oldPath, newPath)
	}
	t.Cleanup(func() {
		atomicRename = originalRename
		atomicAfterTempCreate = originalAfterTempCreate
	})
	atomicAfterTempCreate = func(tmpPath string) {
		tempCreateMu.Lock()
		tempCreateCount++
		count := tempCreateCount
		tempCreateMu.Unlock()
		switch count {
		case 1:
			firstTempCreated <- tmpPath
			<-releaseFirstWriter
		case 2:
			secondTempCreated <- tmpPath
		}
	}

	errs := make(chan error, 2)
	go func() {
		errs <- WriteAtomic(target, []byte(`{"writer":"one"}`))
	}()
	firstTemp := <-firstTempCreated
	assert.FileExists(t, firstTemp)
	go func() {
		errs <- WriteAtomic(target, []byte(`{"writer":"two"}`))
	}()
	secondTemp := <-secondTempCreated
	assert.FileExists(t, secondTemp)

	close(releaseFirstWriter)

	<-enteredRename
	<-enteredRename
	assert.FileExists(t, secondTemp)
	close(releaseRename)

	require.NoError(t, <-errs)
	require.NoError(t, <-errs)

	data, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Contains(t, []string{`{"writer":"one"}`, `{"writer":"two"}`}, string(data))
}

func TestWriteAtomic_FailsClosedWhenParentDirectoryChangesBeforeTempCreate(t *testing.T) {
	root := realTempDir(t)
	parent := filepath.Join(root, "safe")
	target := filepath.Join(parent, "manifest.json")
	escape := filepath.Join(root, "escape")
	require.NoError(t, os.MkdirAll(parent, 0o755))
	require.NoError(t, os.MkdirAll(escape, 0o755))

	originalHook := atomicAfterParentValidated
	atomicAfterParentValidated = func(string) {
		moved := parent + ".moved"
		require.NoError(t, os.Rename(parent, moved))
		require.NoError(t, os.Symlink(escape, parent))
	}
	t.Cleanup(func() {
		atomicAfterParentValidated = originalHook
	})

	err := WriteAtomic(target, []byte(`{"ok":true}`))
	require.Error(t, err)
	assert.NoFileExists(t, filepath.Join(escape, "manifest.json"))
}
