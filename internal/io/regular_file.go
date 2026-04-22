package io

import (
	"errors"
	"fmt"
	stdio "io"
	"os"
	"syscall"
)

// ErrNotRegularFile indicates that a path pointed to something other than a
// regular file — a symlink, named pipe, directory, device, or similar. Callers
// that refuse to read non-regular files (rule sidecars, candidate bodies,
// staged artifacts) wrap this error.
var ErrNotRegularFile = errors.New("io: not a regular file")

// ErrMultipleHardLinks indicates that a path has nlink > 1. Rescue and
// candidate material must be single-link to prevent an attacker from having a
// second handle on the same inode that swaps contents mid-read.
var ErrMultipleHardLinks = fmt.Errorf("%w: multiple hard links", ErrNotRegularFile)

type regularIdentity struct {
	dev uint64
	ino uint64
}

// OpenValidatedRegularFile opens `path` after verifying (via Lstat) that it is
// not a symlink, is a regular file, and has exactly one hard link. It then
// re-verifies (via Stat on the open handle) that the opened inode matches the
// pre-open identity — catching TOCTOU races where `path` is swapped between
// Lstat and Open.
//
// Returns ErrNotRegularFile (wrapped) if the path is a symlink, directory,
// device, pipe, socket, etc.; ErrMultipleHardLinks if nlink > 1.
func OpenValidatedRegularFile(path string) (*os.File, os.FileMode, int64, error) {
	identity, perm, size, err := lstatRegularFileIdentity(path)
	if err != nil {
		return nil, 0, 0, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, 0, 0, err
	}
	if err := verifyRegularIdentity(path, info, identity); err != nil {
		_ = file.Close()
		return nil, 0, 0, err
	}
	return file, perm, size, nil
}

// ReadValidatedRegularFile is a convenience wrapper around
// OpenValidatedRegularFile that returns the entire file contents. Callers pass
// a cap to bound memory usage; a zero or negative cap disables the cap.
func ReadValidatedRegularFile(path string, cap int64) ([]byte, error) {
	file, _, size, err := OpenValidatedRegularFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if cap > 0 && size > cap {
		return nil, fmt.Errorf("io: regular file exceeds cap: path=%s size=%d cap=%d", path, size, cap)
	}
	var limit int64 = size + 1
	if cap > 0 {
		limit = cap + 1
	}
	data, err := stdio.ReadAll(stdio.LimitReader(file, limit))
	if err != nil {
		return nil, err
	}
	if cap > 0 && int64(len(data)) > cap {
		return nil, fmt.Errorf("io: regular file exceeds cap: path=%s cap=%d", path, cap)
	}
	return data, nil
}

func lstatRegularFileIdentity(path string) (regularIdentity, os.FileMode, int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return regularIdentity{}, 0, 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return regularIdentity{}, 0, 0, fmt.Errorf("%w: symlink: %s", ErrNotRegularFile, path)
	}
	return identityFromInfo(path, info)
}

func identityFromInfo(path string, info os.FileInfo) (regularIdentity, os.FileMode, int64, error) {
	if !info.Mode().IsRegular() {
		return regularIdentity{}, 0, 0, fmt.Errorf("%w: mode=%s: %s", ErrNotRegularFile, info.Mode().String(), path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return regularIdentity{}, 0, 0, fmt.Errorf("%w: missing stat_t: %s", ErrNotRegularFile, path)
	}
	if stat.Nlink > 1 {
		return regularIdentity{}, 0, 0, fmt.Errorf("%w: path=%s nlink=%d", ErrMultipleHardLinks, path, stat.Nlink)
	}
	return regularIdentity{dev: uint64(stat.Dev), ino: uint64(stat.Ino)}, info.Mode().Perm(), info.Size(), nil
}

func verifyRegularIdentity(path string, info os.FileInfo, expected regularIdentity) error {
	got, _, _, err := identityFromInfo(path, info)
	if err != nil {
		return err
	}
	if got != expected {
		return fmt.Errorf("%w: TOCTOU inode mismatch: %s", ErrNotRegularFile, path)
	}
	return nil
}
