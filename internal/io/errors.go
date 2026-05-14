package io

import (
	"errors"

	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
)

const (
	JSONLMaxLineBytes      = 4 * 1024
	RegistryTailScanN      = 2000
	sidecarFilenameExt     = ".txt"
	defaultDirectoryPerm   = 0o700
	defaultFilePerm        = 0o600
	sidecarDigestHexLength = 64
)

var (
	ErrInvalidPass              = errors.New("io: invalid pass")
	ErrUnsafePath               = errors.New("io: unsafe path")
	ErrWorktreePathUnavailable  = errors.New("io: worktree path is unavailable in this run context")
	ErrWorktreePathEscapesBase  = errors.New("io: worktree path escapes configured worktree_base")
	ErrWorktreeBaseMismatch     = errors.New("io: persisted worktree_base does not match configured worktree_base")
	ErrSidecarDigestMismatch    = errors.New("io: sidecar sha256 does not match content")
	ErrRegistryCASMismatch      = errors.New("io: registry compare-and-swap failed")
	ErrRegistryUnsupportedKind  = errors.New("io: unsupported registry entry kind")
	ErrRegistryIndexOffsetDrift = errors.New("io: registry index offset/hash mismatch")
	ErrFileTooLarge             = errors.New("io: file too large")

	ErrNotScorable   = stepio.ErrNotScorable
	ErrEntryTooLarge = stepio.ErrEntryTooLarge
)
