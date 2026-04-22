package io

import (
	"errors"
	"fmt"
	stdio "io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/nishimoto265/auto-improve/internal/contracts"
)

const maxValidatedRegularFileBytes int64 = 10 * 1024 * 1024

var validatedRegularFileBeforeOpen = func(string) {}

// OpenValidatedRegularFile reads a regular file beneath runsBaseRoot while
// rejecting symlink escapes, TOCTOU path swaps, multi-link files, and oversized
// payloads.
func OpenValidatedRegularFile(path, runsBaseRoot string) ([]byte, error) {
	return readValidatedRegularFile(path, runsBaseRoot, true)
}

// ReadValidatedRegularFile reads a regular file by absolute path using the same
// no-follow and identity checks as OpenValidatedRegularFile, but without a
// runs-base containment requirement.
func ReadValidatedRegularFile(path string) ([]byte, error) {
	return readValidatedRegularFile(path, "", false)
}

func readValidatedRegularFile(path, runsBaseRoot string, enforceBase bool) ([]byte, error) {
	if err := contracts.EnsureCleanAbsolutePath(path); err != nil {
		return nil, err
	}
	path = canonicalizeTrustedPathPrefix(path)
	if enforceBase {
		if err := contracts.EnsureCleanAbsolutePath(runsBaseRoot); err != nil {
			return nil, err
		}
		runsBaseRoot = canonicalizeTrustedPathPrefix(runsBaseRoot)
		if err := ensureNoSymlinkPathComponents(runsBaseRoot); err != nil {
			return nil, unsafePathError(path, err)
		}
		if !pathWithinBase(path, runsBaseRoot) {
			return nil, unsafePathError(path, fmt.Errorf("path escapes root: %s", path))
		}
	}

	validatedRegularFileBeforeOpen(path)
	file, _, err := openTrackedFileNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		if isUnsafeRegularFileOpenError(err) {
			return nil, unsafePathError(path, err)
		}
		return nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, unsafePathError(path, fmt.Errorf("not a regular file"))
	}
	if info.Size() > maxValidatedRegularFileBytes {
		return nil, fmt.Errorf("%w: path=%s size=%d limit=%d", ErrFileTooLarge, path, info.Size(), maxValidatedRegularFileBytes)
	}
	return stdio.ReadAll(file)
}

func unsafePathError(path string, cause error) error {
	return fmt.Errorf("%w: path=%s: %v", ErrUnsafePath, path, cause)
}

func pathWithinBase(path, base string) bool {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func isUnsafeRegularFileOpenError(err error) bool {
	return err != nil && (errors.Is(err, ErrUnsafePath) || errors.Is(err, ErrPathIdentityChanged) || errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR))
}
