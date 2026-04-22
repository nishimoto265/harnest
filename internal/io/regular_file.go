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

// OpenValidatedRegularFile reads a regular file beneath runsBaseRoot while
// rejecting symlink escapes and multi-link files.
func OpenValidatedRegularFile(path, runsBaseRoot string) ([]byte, error) {
	if err := contracts.EnsureCleanAbsolutePath(path); err != nil {
		return nil, err
	}
	if err := contracts.EnsureCleanAbsolutePath(runsBaseRoot); err != nil {
		return nil, err
	}

	realRoot, err := filepath.EvalSymlinks(runsBaseRoot)
	if err != nil {
		return nil, err
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		if symlinkErr := ensureNoSymlinkPathComponents(path); symlinkErr != nil {
			return nil, unsafePathError(path, symlinkErr)
		}
		return nil, err
	}
	if !pathWithinBase(realPath, realRoot) {
		return nil, unsafePathError(path, fmt.Errorf("resolved path escapes root: %s", realPath))
	}
	if err := ensureNoSymlinkPathComponents(path); err != nil {
		return nil, unsafePathError(path, err)
	}

	file, _, err := openTrackedFileNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		if errors.Is(err, ErrPathIdentityChanged) || errors.Is(err, syscall.ELOOP) {
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
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, unsafePathError(path, fmt.Errorf("missing stat_t"))
	}
	if stat.Nlink > 1 {
		return nil, unsafePathError(path, fmt.Errorf("multiple hard links"))
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
