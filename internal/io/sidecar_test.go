package io

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteAndReadSidecar(t *testing.T) {
	ctx := newTestRunContext(t)
	content := strings.Repeat("x", JSONLMaxLineBytes+32)
	sum := sha256.Sum256([]byte(content))
	sha256Hex := hex.EncodeToString(sum[:])

	sidecarDir, err := ctx.ResolveRunRelative("30/reasons")
	require.NoError(t, err)
	sidecarPath, err := WriteSidecar(sidecarDir, sha256Hex, content)
	require.NoError(t, err)

	relPath, err := SidecarRefPath(ctx.RunDir(), sidecarPath)
	require.NoError(t, err)

	readBack, err := ReadSidecar(ctx, contracts.OverflowRef{
		Path:   relPath,
		Sha256: sha256Hex,
	})
	require.NoError(t, err)
	assert.Equal(t, content, readBack)
}

func TestReadSidecar_RejectsDigestMismatch(t *testing.T) {
	ctx := newTestRunContext(t)
	sidecarDir, err := ctx.ResolveRunRelative("40")
	require.NoError(t, err)
	sum := sha256.Sum256([]byte("hello"))
	sidecarPath, err := WriteSidecar(sidecarDir, hex.EncodeToString(sum[:]), "hello")
	require.NoError(t, err)

	relPath, err := SidecarRefPath(ctx.RunDir(), sidecarPath)
	require.NoError(t, err)

	_, err = ReadSidecar(ctx, contracts.OverflowRef{
		Path:   relPath,
		Sha256: strings.Repeat("f", 64),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSidecarDigestMismatch)
}

func TestReadSidecar_RejectsSymlinkTarget(t *testing.T) {
	ctx := newTestRunContext(t)
	escapeDir := filepath.Join(realTempDir(t), "escape")
	require.NoError(t, os.MkdirAll(escapeDir, 0o755))
	external := filepath.Join(escapeDir, "secret.txt")
	require.NoError(t, os.WriteFile(external, []byte("secret"), 0o644))

	linkPath, err := ctx.ResolveRunRelative("30/reasons/linked.txt")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(linkPath), 0o755))
	require.NoError(t, os.Symlink(external, linkPath))

	_, err = ReadSidecar(ctx, contracts.OverflowRef{
		Path:   "30/reasons/linked.txt",
		Sha256: strings.Repeat("a", 64),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafePath)
}

func TestWriteSidecar_RejectsSymlinkedDirectory(t *testing.T) {
	ctx := newTestRunContext(t)
	escapeDir := filepath.Join(realTempDir(t), "escape")
	require.NoError(t, os.MkdirAll(escapeDir, 0o755))

	sidecarDir, err := ctx.ResolveRunRelative("30/reasons")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(sidecarDir), 0o755))
	require.NoError(t, os.Symlink(escapeDir, sidecarDir))

	sum := sha256.Sum256([]byte("hello"))
	_, err = WriteSidecar(sidecarDir, hex.EncodeToString(sum[:]), "hello")
	require.Error(t, err)
	assert.NoFileExists(t, filepath.Join(escapeDir, hex.EncodeToString(sum[:])+".txt"))
}

func TestSidecarRefPath_RejectsSymlinkEscapes(t *testing.T) {
	ctx := newTestRunContext(t)
	escapeDir := filepath.Join(realTempDir(t), "escape")
	require.NoError(t, os.MkdirAll(escapeDir, 0o755))
	targetPath := filepath.Join(escapeDir, "sidecar.txt")
	require.NoError(t, os.WriteFile(targetPath, []byte("hello"), 0o644))

	linkPath, err := ctx.ResolveRunRelative("30/reasons/linked.txt")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(linkPath), 0o755))
	require.NoError(t, os.Symlink(targetPath, linkPath))

	_, err = SidecarRefPath(ctx.RunDir(), linkPath)
	require.Error(t, err)
}
