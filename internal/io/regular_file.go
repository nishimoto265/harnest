package io

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/nishimoto265/auto-improve/internal/contracts"
)

var ErrRegularFileEscapesRoot = fmt.Errorf("%w: file escapes trusted root", ErrPathIdentityChanged)

// OpenValidatedRegularFile reads a regular file under the trusted root without
// following symlinks at open time. The file must resolve under runsBaseRoot,
// must not be a symlink, and must not have multiple hard links.
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
		return nil, err
	}
	rel, err := filepath.Rel(realRoot, realPath)
	if err != nil {
		return nil, err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil, fmt.Errorf("%w: root=%s path=%s rel=%s", ErrRegularFileEscapesRoot, runsBaseRoot, path, rel)
	}

	file, _, err := openTrackedFileNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s", ErrPathIdentityChanged, path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("%w: missing stat_t", ErrPathIdentityChanged)
	}
	if stat.Nlink > 1 {
		return nil, fmt.Errorf("%w: %s", ErrPathIdentityChanged, path)
	}

	return io.ReadAll(file)
}
