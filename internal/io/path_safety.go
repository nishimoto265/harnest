package io

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"golang.org/x/sys/unix"
)

var (
	ErrPathIdentityChanged     = errors.New("io: file path identity changed during operation")
	openFileNoFollowBeforeOpen = func(string) {}
)

type fileIdentity struct {
	dev uint64
	ino uint64
}

func EnsureNoSymlinkPathComponents(path string) error {
	return ensureNoSymlinkPathComponents(path)
}

func openFileNoFollow(path string, flags int, perm os.FileMode) (*os.File, error) {
	file, _, err := openTrackedFileNoFollow(path, flags, perm)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func OpenFileNoFollow(path string, flags int, perm os.FileMode) (*os.File, error) {
	return openFileNoFollow(path, flags, perm)
}

func openTrackedFileNoFollow(path string, flags int, perm os.FileMode) (*os.File, fileIdentity, error) {
	if err := contracts.EnsureCleanAbsolutePath(path); err != nil {
		return nil, fileIdentity{}, err
	}
	path = canonicalizeTrustedPathPrefix(path)
	openFileNoFollowBeforeOpen(path)

	parentDir, err := openDirectoryNoFollow(filepath.Dir(path))
	if err != nil {
		return nil, fileIdentity{}, err
	}
	defer parentDir.Close()

	fd, err := unix.Openat(int(parentDir.Fd()), filepath.Base(path), flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(perm))
	if err != nil {
		return nil, fileIdentity{}, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fileIdentity{}, fmt.Errorf("io: open file without follow returned nil: %s", path)
	}

	identity, err := fileIdentityFromFile(file)
	if err != nil {
		_ = file.Close()
		return nil, fileIdentity{}, err
	}
	if err := rejectMultipleHardLinks(file); err != nil {
		_ = file.Close()
		return nil, fileIdentity{}, err
	}
	if err := ensurePathMatchesIdentity(path, identity); err != nil {
		_ = file.Close()
		return nil, fileIdentity{}, err
	}
	return file, identity, nil
}

func openDirectoryNoFollow(path string) (*os.File, error) {
	if err := contracts.EnsureCleanAbsolutePath(path); err != nil {
		return nil, err
	}

	cleaned := canonicalizeTrustedPathPrefix(path)
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}

	for _, component := range splitAbsolutePath(cleaned) {
		nextFD, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, openErr
		}
		fd = nextFD
	}

	file := os.NewFile(uintptr(fd), cleaned)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("io: open directory without follow returned nil: %s", cleaned)
	}
	return file, nil
}

func splitAbsolutePath(path string) []string {
	trimmed := strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, string(filepath.Separator))
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

func rejectMultipleHardLinks(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%w: missing stat_t", ErrPathIdentityChanged)
	}
	if stat.Nlink > 1 {
		return fmt.Errorf("%w: multiple hard links", ErrUnsafePath)
	}
	return nil
}

func ensurePathMatchesIdentity(path string, expected fileIdentity) error {
	path = canonicalizeTrustedPathPrefix(path)
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
	parent := filepath.Dir(canonicalizeTrustedPathPrefix(path))
	if err := ensureNoSymlinkPathComponents(parent); err != nil {
		return err
	}
	if err := os.MkdirAll(parent, defaultDirectoryPerm); err != nil {
		return err
	}
	return ensureNoSymlinkPathComponents(parent)
}

func canonicalizeTrustedPathPrefix(path string) string {
	cleaned := filepath.Clean(path)
	if runtime.GOOS != "darwin" {
		return cleaned
	}
	for _, prefix := range []string{"/var", "/tmp"} {
		if cleaned == prefix || strings.HasPrefix(cleaned, prefix+string(filepath.Separator)) {
			return filepath.Clean(filepath.Join("/private", strings.TrimPrefix(cleaned, string(filepath.Separator))))
		}
	}
	return cleaned
}
