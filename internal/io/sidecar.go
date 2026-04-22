package io

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
)

func WriteSidecar(dir, sha256Hex string, content string) (string, error) {
	if err := contracts.EnsureCleanAbsolutePath(dir); err != nil {
		return "", err
	}
	if err := ensureNoSymlinkPathComponents(dir); err != nil {
		return "", err
	}
	if len(sha256Hex) != sidecarDigestHexLength {
		return "", ErrSidecarDigestMismatch
	}
	actual := sha256.Sum256([]byte(content))
	if sha256Hex != hex.EncodeToString(actual[:]) {
		return "", ErrSidecarDigestMismatch
	}
	path := filepath.Join(dir, sha256Hex+sidecarFilenameExt)
	if err := WriteAtomic(path, []byte(content)); err != nil {
		return "", err
	}
	return path, nil
}

func ReadSidecar(ctx RunContext, ref contracts.OverflowRef) (string, error) {
	if err := ref.Validate(); err != nil {
		return "", err
	}
	if err := validateSidecarRefPath(ref.Path); err != nil {
		return "", err
	}
	path, err := ctx.ResolveRunRelative(ref.Path)
	if err != nil {
		return "", err
	}
	data, err := ReadValidatedRegularFile(path)
	if err != nil {
		return "", err
	}
	actual := sha256.Sum256(data)
	if ref.Sha256 != hex.EncodeToString(actual[:]) {
		return "", ErrSidecarDigestMismatch
	}
	return string(data), nil
}

func SidecarRefPath(runDir, absolutePath string) (string, error) {
	if err := contracts.EnsureCleanAbsolutePath(runDir); err != nil {
		return "", err
	}
	if err := contracts.EnsureCleanAbsolutePath(absolutePath); err != nil {
		return "", err
	}
	if err := ensureNoSymlinkPathComponents(absolutePath); err != nil {
		return "", err
	}
	realRunDir, err := filepath.EvalSymlinks(runDir)
	if err != nil {
		return "", err
	}
	realPath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realRunDir, realPath)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(rel, "..") {
		return "", contracts.ErrPathRelativeBadPrefix
	}
	if err := contracts.EnsureCleanRelativePath(rel); err != nil {
		return "", err
	}
	return rel, nil
}

func validateSidecarRefPath(path string) error {
	if !strings.HasSuffix(path, sidecarFilenameExt) {
		return fmt.Errorf("%w: invalid sidecar extension: %s", ErrUnsafePath, path)
	}
	for _, prefix := range []string{"30/reasons", "40", "60/reasons", "processed-details"} {
		if err := contracts.EnsureRelativePathUnderPrefix(path, prefix); err == nil {
			return nil
		}
	}
	return fmt.Errorf("%w: invalid sidecar prefix: %s", ErrUnsafePath, path)
}

func ensureNoSymlinkPathComponents(path string) error {
	cleaned := filepath.Clean(path)
	var pending []string
	current := cleaned
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("io: symlinked path component: %s", current)
			}
			break
		}
		if !os.IsNotExist(err) {
			return err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		pending = append(pending, filepath.Base(current))
		current = parent
	}
	for i := len(pending) - 1; i >= 0; i-- {
		current = filepath.Join(current, pending[i])
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("io: symlinked path component: %s", current)
		}
	}
	return nil
}
