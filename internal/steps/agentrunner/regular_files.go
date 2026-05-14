package agentrunner

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

var ErrArtifactMultipleLinks = fmt.Errorf("%w: multiple hard links", ErrArtifactNotRegular)

type regularFileIdentity struct {
	dev uint64
	ino uint64
}

func OpenValidatedRegularFile(path string) (*os.File, os.FileMode, int64, error) {
	identity, perm, size, err := validatedRegularFileIdentity(path)
	if err != nil {
		return nil, 0, 0, err
	}
	file, err := internalio.OpenFileNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, 0, 0, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, 0, 0, err
	}
	if err := verifyRegularFileIdentity(path, info, identity); err != nil {
		_ = file.Close()
		return nil, 0, 0, err
	}
	return file, perm, size, nil
}

func validatedRegularFileIdentity(path string) (regularFileIdentity, os.FileMode, int64, error) {
	if err := internalio.EnsureNoSymlinkPathComponents(filepath.Dir(path)); err != nil {
		return regularFileIdentity{}, 0, 0, fmt.Errorf("%w: %s", ErrArtifactNotRegular, path)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return regularFileIdentity{}, 0, 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return regularFileIdentity{}, 0, 0, fmt.Errorf("%w: %s", ErrArtifactNotRegular, path)
	}
	return regularFileIdentityFromInfo(path, info)
}

func regularFileIdentityFromInfo(path string, info os.FileInfo) (regularFileIdentity, os.FileMode, int64, error) {
	if !info.Mode().IsRegular() {
		return regularFileIdentity{}, 0, 0, fmt.Errorf("%w: %s", ErrArtifactNotRegular, path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return regularFileIdentity{}, 0, 0, fmt.Errorf("%w: %s", ErrArtifactNotRegular, path)
	}
	if stat.Nlink > 1 {
		return regularFileIdentity{}, 0, 0, fmt.Errorf("%w: %s", ErrArtifactMultipleLinks, path)
	}
	return regularFileIdentity{
		dev: uint64(stat.Dev),
		ino: uint64(stat.Ino),
	}, info.Mode().Perm(), info.Size(), nil
}

func verifyRegularFileIdentity(path string, info os.FileInfo, expected regularFileIdentity) error {
	if err := internalio.EnsureNoSymlinkPathComponents(filepath.Dir(path)); err != nil {
		return fmt.Errorf("%w: %s", ErrArtifactNotRegular, path)
	}
	identity, _, _, err := regularFileIdentityFromInfo(path, info)
	if err != nil {
		return err
	}
	if identity != expected {
		return fmt.Errorf("%w: %s", ErrArtifactNotRegular, path)
	}
	return nil
}
