package io

import (
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	stdio "io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
)

var (
	atomicNowFunc              = time.Now
	atomicRename               = os.Rename
	atomicRand    stdio.Reader = crand.Reader
)

// WriteAtomic writes data to `<path>.tmp-<pid>-<ms>-<rand>` and renames it into
// place. Any pre-existing tmp siblings are removed before the write begins.
func WriteAtomic(path string, data []byte) error {
	if err := contracts.EnsureCleanAbsolutePath(path); err != nil {
		return err
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, defaultDirectoryPerm); err != nil {
		return err
	}
	if err := cleanupAtomicTmpSiblings(path); err != nil {
		return err
	}

	tmpPath, err := newAtomicTempPath(path)
	if err != nil {
		return err
	}

	tmpFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, defaultFilePerm)
	if err != nil {
		return err
	}

	cleanupTmp := func() {
		_ = os.Remove(tmpPath)
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

	if err := atomicRename(tmpPath, path); err != nil {
		cleanupTmp()
		return err
	}
	if err := syncDirectory(parent); err != nil {
		return err
	}
	return nil
}

func cleanupAtomicTmpSiblings(path string) error {
	parent := filepath.Dir(path)
	base := filepath.Base(path)

	entries, err := os.ReadDir(parent)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	prefix := base + ".tmp-"
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		if err := os.Remove(filepath.Join(parent, entry.Name())); err != nil && !os.IsNotExist(err) {
			return err
		}
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
