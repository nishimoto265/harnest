package io

import (
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	stdio "io"
	"os"
	"path/filepath"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	"golang.org/x/sys/unix"
)

var (
	atomicNowFunc                           = time.Now
	atomicRenameat                          = unix.Renameat
	atomicRand                 stdio.Reader = crand.Reader
	directorySync                           = syncDirectory
	atomicAfterParentValidated              = func(string) {}
	atomicAfterTempCreate                   = func(string) {}
)

// WriteAtomic writes data to `<path>.tmp-<pid>-<ms>-<rand>` and renames it into
// place.
func WriteAtomic(path string, data []byte) error {
	if err := contracts.EnsureCleanAbsolutePath(path); err != nil {
		return err
	}
	if err := ensureWritableParentDir(path); err != nil {
		return err
	}
	parent := filepath.Dir(path)
	atomicAfterParentValidated(parent)

	parentDir, err := openDirectoryNoFollow(parent)
	if err != nil {
		return err
	}
	defer parentDir.Close()

	tmpPath, err := newAtomicTempPath(path)
	if err != nil {
		return err
	}
	tmpName := filepath.Base(tmpPath)

	fd, err := unix.Openat(int(parentDir.Fd()), tmpName, unix.O_CREAT|unix.O_WRONLY|unix.O_EXCL|unix.O_CLOEXEC, defaultFilePerm)
	if err != nil {
		return err
	}
	tmpFile := os.NewFile(uintptr(fd), tmpPath)
	if tmpFile == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("io: open temp file returned nil: %s", tmpPath)
	}
	atomicAfterTempCreate(tmpPath)

	cleanupTmp := func() {
		_ = unix.Unlinkat(int(parentDir.Fd()), tmpName, 0)
	}

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		cleanupTmp()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		cleanupTmp()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		cleanupTmp()
		return err
	}

	if err := atomicRenameat(int(parentDir.Fd()), tmpName, int(parentDir.Fd()), filepath.Base(path)); err != nil {
		cleanupTmp()
		return err
	}
	if err := parentDir.Sync(); err != nil {
		return err
	}
	return nil
}

func newAtomicTempPath(path string) (string, error) {
	var entropy [4]byte
	if _, err := stdio.ReadFull(atomicRand, entropy[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"%s.tmp-%d-%d-%s",
		path,
		os.Getpid(),
		atomicNowFunc().UnixMilli(),
		hex.EncodeToString(entropy[:]),
	), nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
