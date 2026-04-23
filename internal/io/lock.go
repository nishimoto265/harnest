package io

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
)

type FileLock struct {
	path string
	file *os.File
	held bool
}

var activeLockPaths sync.Map

func AcquirePromotionLock(ctx RunContext) (*FileLock, error) {
	return AcquireFileLock(ctx.PromotionLockPath())
}

func TryAcquireFileLock(path string) (*FileLock, bool, error) {
	if err := ensureWritableParentDir(path); err != nil {
		return nil, false, err
	}
	return tryLockFile(path, os.O_CREATE|os.O_RDWR, defaultFilePerm, syscall.LOCK_EX)
}

// InspectFileLock acquires a shared non-blocking lock on an existing lock file
// without creating it. The bool reports whether the lock file already exists.
func InspectFileLock(path string) (*FileLock, bool, error) {
	f, identity, err := openTrackedFileNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, true, nil
		}
		return nil, false, err
	}
	if err := ensurePathMatchesIdentity(path, identity); err != nil {
		_ = f.Close()
		if errors.Is(err, ErrPathIdentityChanged) || os.IsNotExist(err) {
			return nil, false, err
		}
		return nil, false, err
	}
	return &FileLock{path: path, file: f}, true, nil
}

func AcquireFileLock(path string) (*FileLock, error) {
	return acquireFileLock(path, false, nil)
}

func AcquireFileLockContext(ctx context.Context, path string) (*FileLock, error) {
	return acquireFileLock(path, true, ctx)
}

func acquireFileLock(path string, nonBlocking bool, ctx context.Context) (*FileLock, error) {
	if err := ensureWritableParentDir(path); err != nil {
		return nil, err
	}
	for {
		lock, acquired, err := tryLockFile(path, os.O_CREATE|os.O_RDWR, defaultFilePerm, syscall.LOCK_EX)
		if err != nil {
			return nil, err
		}
		if acquired {
			return lock, nil
		}
		if nonBlocking {
			if ctx != nil {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(50 * time.Millisecond):
				}
				continue
			}
			return nil, syscall.EWOULDBLOCK
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func tryLockFile(path string, flags int, perm os.FileMode, lockMode int) (*FileLock, bool, error) {
	f, identity, err := openTrackedFileNoFollow(path, flags, perm)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), lockMode|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if err := ensurePathMatchesIdentity(path, identity); err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		if errors.Is(err, ErrPathIdentityChanged) || os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if err := registerActiveLockPath(path, identity); err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		return nil, false, err
	}
	return &FileLock{path: path, file: f, held: true}, true, nil
}

func registerActiveLockPath(path string, identity fileIdentity) error {
	if current, ok := activeLockPaths.Load(path); ok {
		if heldIdentity, ok := current.(fileIdentity); ok && heldIdentity != identity {
			return fmt.Errorf("%w: path=%s", ErrPathIdentityChanged, path)
		}
	}
	activeLockPaths.Store(path, identity)
	return nil
}

func unregisterActiveLockPath(path string, file *os.File) {
	if file == nil {
		activeLockPaths.Delete(path)
		return
	}
	identity, err := fileIdentityFromFile(file)
	if err != nil {
		activeLockPaths.Delete(path)
		return
	}
	if current, ok := activeLockPaths.Load(path); ok {
		if heldIdentity, ok := current.(fileIdentity); ok && heldIdentity == identity {
			activeLockPaths.Delete(path)
		}
	}
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
	if l.held {
		unregisterActiveLockPath(l.path, l.file)
		l.held = false
	}
	if err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN); err != nil {
		_ = l.file.Close()
		return err
	}
	err := l.file.Close()
	l.file = nil
	return err
}
