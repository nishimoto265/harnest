package io

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"golang.org/x/sys/unix"
)

func EnsureChildDirNoFollow(parentPath, childName string, perm os.FileMode) error {
	name, err := validateChildName(childName)
	if err != nil {
		return err
	}
	parentDir, err := openDirectoryNoFollow(parentPath)
	if err != nil {
		return err
	}
	defer parentDir.Close()

	if err := unix.Mkdirat(int(parentDir.Fd()), name, uint32(perm)); err != nil && !errors.Is(err, syscall.EEXIST) {
		return err
	}
	if err := verifyChildDirNoFollow(parentDir, name); err != nil {
		return err
	}
	return parentDir.Sync()
}

func EnsureDirNoFollow(path string, perm os.FileMode) error {
	if err := contracts.EnsureCleanAbsolutePath(path); err != nil {
		return err
	}
	cleaned := canonicalizeTrustedPathPrefix(path)
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() {
		_ = unix.Close(fd)
	}()

	for _, component := range splitAbsolutePath(cleaned) {
		if err := unix.Mkdirat(fd, component, uint32(perm)); err != nil && !errors.Is(err, syscall.EEXIST) {
			return err
		}
		nextFD, err := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		_ = unix.Close(fd)
		fd = nextFD
	}
	return unix.Fsync(fd)
}

func CreateChildDirNoFollow(parentPath, childName string, perm os.FileMode) error {
	name, err := validateChildName(childName)
	if err != nil {
		return err
	}
	parentDir, err := openDirectoryNoFollow(parentPath)
	if err != nil {
		return err
	}
	defer parentDir.Close()

	if err := unix.Mkdirat(int(parentDir.Fd()), name, uint32(perm)); err != nil {
		return err
	}
	if err := verifyChildDirNoFollow(parentDir, name); err != nil {
		return err
	}
	return parentDir.Sync()
}

func RenameNoFollow(oldPath, newPath string) error {
	if err := contracts.EnsureCleanAbsolutePath(oldPath); err != nil {
		return err
	}
	if err := contracts.EnsureCleanAbsolutePath(newPath); err != nil {
		return err
	}
	oldPath = canonicalizeTrustedPathPrefix(oldPath)
	newPath = canonicalizeTrustedPathPrefix(newPath)
	oldName, err := validateChildName(filepath.Base(oldPath))
	if err != nil {
		return err
	}
	newName, err := validateChildName(filepath.Base(newPath))
	if err != nil {
		return err
	}

	oldParent, err := openDirectoryNoFollow(filepath.Dir(oldPath))
	if err != nil {
		return err
	}
	defer oldParent.Close()
	newParent, err := openDirectoryNoFollow(filepath.Dir(newPath))
	if err != nil {
		return err
	}
	defer newParent.Close()

	var stat unix.Stat_t
	if err := unix.Fstatat(int(oldParent.Fd()), oldName, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT == unix.S_IFLNK {
		return fmt.Errorf("%w: source is a symlink: %s", ErrUnsafePath, oldPath)
	}
	if err := unix.Renameat(int(oldParent.Fd()), oldName, int(newParent.Fd()), newName); err != nil {
		return err
	}
	if err := oldParent.Sync(); err != nil {
		return err
	}
	if filepath.Dir(oldPath) != filepath.Dir(newPath) {
		return newParent.Sync()
	}
	return nil
}

func validateChildName(name string) (string, error) {
	cleaned := filepath.Clean(name)
	if cleaned != name || cleaned == "." || cleaned == ".." || strings.ContainsRune(cleaned, filepath.Separator) {
		return "", fmt.Errorf("%w: invalid child directory name %q", ErrUnsafePath, name)
	}
	return cleaned, nil
}

func verifyChildDirNoFollow(parentDir *os.File, name string) error {
	fd, err := unix.Openat(int(parentDir.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	return unix.Close(fd)
}
