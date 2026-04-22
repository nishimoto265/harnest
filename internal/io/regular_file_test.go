package io

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenValidatedRegularFile_RejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(target, []byte("secret\n"), 0o600))

	path := filepath.Join(root, "candidate.md")
	require.NoError(t, os.Symlink(target, path))

	_, err := OpenValidatedRegularFile(path, root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "candidate.md")
}

func TestOpenValidatedRegularFile_RejectsHardLink(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(source, []byte("secret\n"), 0o600))

	path := filepath.Join(root, "candidate.md")
	require.NoError(t, os.Link(source, path))

	_, err := OpenValidatedRegularFile(path, root)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPathIdentityChanged)
}

func TestInspectFileLock_DoesNotCreateMissingLock(t *testing.T) {
	runsBase := t.TempDir()
	lockPath := filepath.Join(runsBase, "promotion.lock")

	lock, exists, err := InspectFileLock(lockPath)
	require.NoError(t, err)
	assert.False(t, exists)
	assert.Nil(t, lock)
	assert.NoFileExists(t, lockPath)
}
