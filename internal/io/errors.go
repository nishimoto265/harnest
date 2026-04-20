package io

import (
	"errors"

	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
)

const (
	JSONLMaxLineBytes      = 4 * 1024
	RegistryTailScanN      = 2000
	sidecarFilenameExt     = ".txt"
	defaultDirectoryPerm   = 0o755
	defaultFilePerm        = 0o644
	sidecarDigestHexLength = 64
)

var (
	ErrInvalidPass              = errors.New("io: invalid pass")
	ErrWorktreePathUnavailable  = errors.New("io: worktree path is unavailable in this run context")
	ErrSidecarDigestMismatch    = errors.New("io: sidecar sha256 does not match content")
	ErrRegistryCASMismatch      = errors.New("io: registry compare-and-swap failed")
	ErrRegistryUnsupportedKind  = errors.New("io: unsupported registry entry kind")
	ErrRegistryIndexOffsetDrift = errors.New("io: registry index offset/hash mismatch")

	ErrNotScorable   = stepio.ErrNotScorable
	ErrEntryTooLarge = stepio.ErrEntryTooLarge
)
