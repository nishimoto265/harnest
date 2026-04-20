package io

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
)

func WriteSidecar(dir, sha256Hex string, content string) (string, error) {
	if err := contracts.EnsureCleanAbsolutePath(dir); err != nil {
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
	path, err := ctx.ResolveRunRelative(ref.Path)
	if err != nil {
		return "", err
	}
	data, err := osReadFile(path)
	if err != nil {
		return "", err
	}
	actual := sha256.Sum256(data)
	if ref.Sha256 != hex.EncodeToString(actual[:]) {
		return "", ErrSidecarDigestMismatch
	}
	return string(data), nil
}

var osReadFile = func(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func SidecarRefPath(runDir, absolutePath string) (string, error) {
	if err := contracts.EnsureCleanAbsolutePath(runDir); err != nil {
		return "", err
	}
	if err := contracts.EnsureCleanAbsolutePath(absolutePath); err != nil {
		return "", err
	}
	rel, err := filepath.Rel(runDir, absolutePath)
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
