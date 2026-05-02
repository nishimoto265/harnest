package io

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcquirePromotionLock(t *testing.T) {
	ctx := newTestRunContext(t)
	lock, err := AcquirePromotionLock(ctx)
	require.NoError(t, err)
	require.NoError(t, lock.Unlock())

	_, err = os.Stat(ctx.PromotionLockPath())
	require.NoError(t, err)
}

func TestAcquireFileLock_RejectsSymlinkSwapWhileAnotherHolderOwnsLock(t *testing.T) {
	lockPath := filepath.Join(realTempDir(t), "promotion.lock")
	firstLock, err := AcquireFileLock(lockPath)
	require.NoError(t, err)
	defer func() {
		_ = firstLock.Unlock()
	}()

	replacement := filepath.Join(realTempDir(t), "replacement.lock")
	require.NoError(t, os.WriteFile(replacement, []byte("replacement\n"), defaultFilePerm))
	require.NoError(t, os.Remove(lockPath))
	require.NoError(t, os.Symlink(replacement, lockPath))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = AcquireFileLockContext(ctx, lockPath)
	require.Error(t, err)
	assert.False(t, errors.Is(err, context.DeadlineExceeded))
}

func TestOpenFileNoFollow_DoesNotLeakFDToChildProcess(t *testing.T) {
	path := filepath.Join(realTempDir(t), "artifact.txt")
	require.NoError(t, os.WriteFile(path, []byte("hello\n"), defaultFilePerm))

	file, err := openFileNoFollow(path, os.O_RDONLY, 0)
	require.NoError(t, err)
	defer file.Close()

	fd := int(file.Fd())
	cmd := exec.Command("/bin/sh", "-c", fmt.Sprintf("test -r /dev/fd/%d", fd))
	err = cmd.Run()
	require.Error(t, err)
}

func TestOpenValidatedRegularFile_RejectsMultiLinkFile(t *testing.T) {
	ctx := newTestRunContext(t)
	sharedPath := filepath.Join(realTempDir(t), "shared.md")
	require.NoError(t, os.WriteFile(sharedPath, []byte("secret\n"), defaultFilePerm))

	runPath, err := ctx.ResolveRunRelative("40/candidates/linked.md")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(runPath), defaultDirectoryPerm))
	require.NoError(t, os.Link(sharedPath, runPath))

	_, err = OpenValidatedRegularFile(runPath, ctx.RunDir())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafePath)
}

func TestOpenValidatedRegularFile_FailsClosedWhenParentDirectoryChangesBeforeOpen(t *testing.T) {
	root := realTempDir(t)
	parent := filepath.Join(root, "40", "candidates")
	path := filepath.Join(parent, "candidate.md")
	escape := filepath.Join(realTempDir(t), "escape")
	require.NoError(t, os.MkdirAll(parent, 0o755))
	require.NoError(t, os.MkdirAll(escape, 0o755))
	require.NoError(t, os.WriteFile(path, []byte("safe\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(escape, "candidate.md"), []byte("escape\n"), 0o644))

	originalHook := validatedRegularFileBeforeOpen
	validatedRegularFileBeforeOpen = func(string) {
		moved := parent + ".moved"
		require.NoError(t, os.Rename(parent, moved))
		require.NoError(t, os.Symlink(escape, parent))
	}
	t.Cleanup(func() {
		validatedRegularFileBeforeOpen = originalHook
	})

	_, err := OpenValidatedRegularFile(path, root)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafePath)
}

func TestOpenValidatedRegularFile_RejectsOversizedFile(t *testing.T) {
	ctx := newTestRunContext(t)
	path, err := ctx.ResolveRunRelative("40/candidates/large.md")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(50*1024*1024))
	require.NoError(t, file.Close())

	_, err = OpenValidatedRegularFile(path, ctx.RunDir())
	require.ErrorIs(t, err, ErrFileTooLarge)
}
