package io

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type FileLock struct {
	path string
	file *os.File
}

func AcquirePromotionLock(ctx RunContext) (*FileLock, error) {
	return AcquireFileLock(ctx.PromotionLockPath())
}

func AcquireFileLock(path string) (*FileLock, error) {
	return acquireFileLock(path, false, nil)
}

func AcquireFileLockContext(ctx context.Context, path string) (*FileLock, error) {
	return acquireFileLock(path, true, ctx)
}

func acquireFileLock(path string, nonBlocking bool, ctx context.Context) (*FileLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), defaultDirectoryPerm); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, defaultFilePerm)
	if err != nil {
		return nil, err
	}
	mode := syscall.LOCK_EX
	if nonBlocking {
		mode |= syscall.LOCK_NB
		for {
			if err := syscall.Flock(int(f.Fd()), mode); err == nil {
				return &FileLock{path: path, file: f}, nil
			} else if !errors.Is(err, syscall.EWOULDBLOCK) {
				_ = f.Close()
				return nil, err
			}
			if ctx != nil {
				select {
				case <-ctx.Done():
					_ = f.Close()
					return nil, ctx.Err()
				case <-time.After(50 * time.Millisecond):
				}
				continue
			}
		}
	}
	if err := syscall.Flock(int(f.Fd()), mode); err != nil {
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
