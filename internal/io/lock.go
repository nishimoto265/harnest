package io

import (
	"os"
	"path/filepath"
	"syscall"
)

type FileLock struct {
	path string
	file *os.File
}

func AcquirePromotionLock(ctx RunContext) (*FileLock, error) {
	return AcquireFileLock(ctx.PromotionLockPath())
}

func AcquireFileLock(path string) (*FileLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), defaultDirectoryPerm); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, defaultFilePerm)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &FileLock{path: path, file: f}, nil
}

func (l *FileLock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

func (l *FileLock) Unlock() error {
	if l == nil || l.file == nil {
		return nil
	}
	if err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN); err != nil {
		_ = l.file.Close()
		return err
	}
	err := l.file.Close()
	l.file = nil
	return err
}
