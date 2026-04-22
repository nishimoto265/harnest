package io

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOpenValidatedRegularFile_RejectsSymlink covers H1/H2/L3. A naive
// os.ReadFile follows symlinks, which would let an attacker bait the step50
// rule loader into reading arbitrary files outside runs_base by placing a
// symlink at 40/candidates/<id>.md. OpenValidatedRegularFile must refuse.
func TestOpenValidatedRegularFile_RejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("sensitive"), 0o600))
	link := filepath.Join(dir, "link.txt")
	require.NoError(t, os.Symlink(secret, link))

	_, _, _, err := OpenValidatedRegularFile(link)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrNotRegularFile, "symlink must be refused, got: %v", err)
}

func TestOpenValidatedRegularFile_AcceptsPlainFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "body.md")
	require.NoError(t, os.WriteFile(path, []byte("hello"), 0o600))
	f, perm, size, err := OpenValidatedRegularFile(path)
	require.NoError(t, err)
	defer f.Close()
	require.EqualValues(t, 0o600, perm)
	require.EqualValues(t, 5, size)
}

func TestOpenValidatedRegularFile_RejectsHardLink(t *testing.T) {
	dir := t.TempDir()
	primary := filepath.Join(dir, "primary.md")
	require.NoError(t, os.WriteFile(primary, []byte("x"), 0o600))
	secondary := filepath.Join(dir, "secondary.md")
	require.NoError(t, os.Link(primary, secondary))

	_, _, _, err := OpenValidatedRegularFile(primary)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrMultipleHardLinks)
}

func TestOpenValidatedRegularFile_RejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.Mkdir(sub, 0o755))
	_, _, _, err := OpenValidatedRegularFile(sub)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrNotRegularFile)
}

// TestOpenValidatedRegularFile_RejectsFifo verifies non-regular inode types.
func TestOpenValidatedRegularFile_RejectsFifo(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("mkfifo unsupported: " + err.Error())
	}
	_, _, _, err := OpenValidatedRegularFile(fifo)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrNotRegularFile)
}

func TestReadValidatedRegularFile_EnforcesCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.bin")
	data := make([]byte, 2048)
	for i := range data {
		data[i] = byte(i % 256)
	}
	require.NoError(t, os.WriteFile(path, data, 0o600))

	_, err := ReadValidatedRegularFile(path, 1024)
	require.Error(t, err, "file exceeding cap must fail")

	got, err := ReadValidatedRegularFile(path, 4096)
	require.NoError(t, err)
	require.Equal(t, data, got)
}

func TestReadValidatedRegularFile_RejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.txt")
	require.NoError(t, os.WriteFile(real, []byte("body"), 0o600))
	link := filepath.Join(dir, "link.txt")
	require.NoError(t, os.Symlink(real, link))
	_, err := ReadValidatedRegularFile(link, 1024)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNotRegularFile), "expected ErrNotRegularFile, got: %v", err)
}

// TestInspectFileLock_DoesNotCreateFile covers M1: recover --inspect must not
// materialize promotion.lock on a fresh runs_base.
func TestInspectFileLock_DoesNotCreateFile(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "promotion.lock")
	lock, acquired, err := InspectFileLock(lockPath)
	require.NoError(t, err)
	require.False(t, acquired)
	require.Nil(t, lock)
	_, statErr := os.Stat(lockPath)
	require.True(t, os.IsNotExist(statErr), "InspectFileLock must not create promotion.lock; got stat err=%v", statErr)
}

func TestInspectFileLock_AcquiresSharedWhenFileExists(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "promotion.lock")
	require.NoError(t, os.WriteFile(lockPath, nil, 0o644))

	lock, acquired, err := InspectFileLock(lockPath)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, lock)
	t.Cleanup(func() { _ = lock.Unlock() })

	// Two inspect locks (both LOCK_SH) can coexist.
	lock2, acquired2, err := InspectFileLock(lockPath)
	require.NoError(t, err)
	require.True(t, acquired2)
	t.Cleanup(func() { _ = lock2.Unlock() })
}

func TestInspectFileLock_SkipsWhenExclusiveHeld(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "promotion.lock")
	excl, err := AcquireFileLock(lockPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = excl.Unlock() })

	lock, acquired, err := InspectFileLock(lockPath)
	require.NoError(t, err)
	require.False(t, acquired, "inspect must non-blocking refuse when promoter holds exclusive")
	require.Nil(t, lock)
}
