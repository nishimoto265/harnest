package io

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

var ErrPathIdentityChanged = errors.New("io: file path identity changed during operation")

type fileIdentity struct {
	dev uint64
	ino uint64
}

func EnsureNoSymlinkPathComponents(path string) error {
	return ensureNoSymlinkPathComponents(path)
}

func openFileNoFollow(path string, flags int, perm os.FileMode) (*os.File, error) {
	if err := ensureNoSymlinkPathComponents(path); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW, uint32(perm))
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("io: open file without follow returned nil: %s", path)
	}
	return file, nil
}

func openTrackedFileNoFollow(path string, flags int, perm os.FileMode) (*os.File, fileIdentity, error) {
	file, err := openFileNoFollow(path, flags, perm)
	if err != nil {
		return nil, fileIdentity{}, err
	}
	identity, err := fileIdentityFromFile(file)
	if err != nil {
		_ = file.Close()
		return nil, fileIdentity{}, err
	}
	if err := ensurePathMatchesIdentity(path, identity); err != nil {
		_ = file.Close()
		return nil, fileIdentity{}, err
	}
	return file, identity, nil
}

func fileIdentityFromFile(file *os.File) (fileIdentity, error) {
	info, err := file.Stat()
	if err != nil {
		return fileIdentity{}, err
	}
	return fileIdentityFromInfo(info)
}

func fileIdentityFromPath(path string) (fileIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return fileIdentity{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fileIdentity{}, fmt.Errorf("%w: %s", ErrPathIdentityChanged, path)
	}
	return fileIdentityFromInfo(info)
}

func fileIdentityFromInfo(info os.FileInfo) (fileIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileIdentity{}, fmt.Errorf("%w: missing stat_t", ErrPathIdentityChanged)
	}
	return fileIdentity{
		dev: uint64(stat.Dev),
		ino: uint64(stat.Ino),
	}, nil
}

func ensurePathMatchesIdentity(path string, expected fileIdentity) error {
	if err := ensureNoSymlinkPathComponents(path); err != nil {
		return err
	}
	actual, err := fileIdentityFromPath(path)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("%w: path=%s", ErrPathIdentityChanged, path)
	}
	return nil
}

func ensureWritableParentDir(path string) error {
	parent := filepath.Dir(path)
	if err := ensureNoSymlinkPathComponents(parent); err != nil {
		return err
	}
	if err := os.MkdirAll(parent, defaultDirectoryPerm); err != nil {
		return err
	}
	return ensureNoSymlinkPathComponents(parent)
}
