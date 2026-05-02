package io

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadManifestHelpers(t *testing.T) {
	ctx := newTestRunContext(t)
	success := contracts.Manifest{
		Kind: contracts.ManifestKindSuccess,
		Value: contracts.ManifestSuccess{
			Kind:          contracts.ManifestKindSuccess,
			SchemaVersion: "1",
			RunID:         ctx.RunID,
			Pass:          1,
			Agent:         "a1",
			BranchName:    "run/a1",
			HeadSHA:       strings.Repeat("1", 40),
			BaseSHA:       strings.Repeat("2", 40),
			DiffPath:      "20-pass1/a1/diff.patch",
			SessionPath:   "20-pass1/a1/session.jsonl",
			ChecklistPath: "20-pass1/a1/checklist-result.json",
			PromptVersion: "prompt-v1",
			StartedAt:     time.Unix(100, 0).UTC(),
			FinishedAt:    time.Unix(200, 0).UTC(),
		},
	}
	successPath, err := ctx.ManifestPath(1, "a1")
	require.NoError(t, err)
	require.NoError(t, WriteJSONAtomic(successPath, success))

	manifest, err := LoadFinalizedManifest(ctx, 1, "a1")
	require.NoError(t, err)
	require.NotNil(t, manifest)
	if loaded, ok := manifest.Value.(contracts.ManifestSuccess); assert.True(t, ok) {
		assert.Equal(t, success.Value.(contracts.ManifestSuccess).HeadSHA, loaded.HeadSHA)
	}

	scorable, err := LoadScorableManifest(ctx, 1, "a1")
	require.NoError(t, err)
	require.NotNil(t, scorable)
	assert.Equal(t, "run/a1", scorable.BranchName)

	errorManifest := contracts.Manifest{
		Kind: contracts.ManifestKindError,
		Value: contracts.ManifestError{
			Kind:          contracts.ManifestKindError,
			SchemaVersion: "1",
			RunID:         ctx.RunID,
			Pass:          2,
			Agent:         "a2",
			ExitCode:      1,
			Reason:        "unknown",
			StartedAt:     time.Unix(100, 0).UTC(),
			FinishedAt:    time.Unix(200, 0).UTC(),
		},
	}
	errorPath, err := ctx.ManifestPath(2, "a2")
	require.NoError(t, err)
	require.NoError(t, WriteJSONAtomic(errorPath, errorManifest))

	_, err = LoadScorableManifest(ctx, 2, "a2")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotScorable)
}

func TestRunContextResolveAndWorktreePaths(t *testing.T) {
	runsBase := realTempDir(t)
	worktreeBase := realTempDir(t)
	pkg := testTaskPackage(t, runsBase, worktreeBase)
	ctx, err := RunContextFromTaskPackage(pkg, runsBase, worktreeBase)
	require.NoError(t, err)

	abs, err := ctx.ResolveRunRelative("20-pass1/a1/diff.patch")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(runsBase, string(pkg.RunID), "20-pass1", "a1", "diff.patch"), abs)

	pass1Path, err := ctx.Pass1WorktreePath("a1")
	require.NoError(t, err)
	assert.Equal(t, pkg.Worktrees[0].Path, pass1Path)

	pass2Path, err := ctx.Pass2WorktreePath("a1")
	require.NoError(t, err)
	assert.Equal(t, pkg.Worktrees[3].Path, pass2Path)
}

func TestRunContextFromTaskPackage_RejectsWorktreeOutsideConfiguredBase(t *testing.T) {
	runsBase := realTempDir(t)
	worktreeBase := realTempDir(t)
	pkg := testTaskPackage(t, runsBase, worktreeBase)
	pkg.Worktrees[0].Path = filepath.Join(realTempDir(t), "escaped-pass1-a1")

	_, err := RunContextFromTaskPackage(pkg, runsBase, worktreeBase)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrWorktreeBaseMismatch)
}

func TestRunContextFromTaskPackage_UsesPersistedWorktreeBaseForResume(t *testing.T) {
	runsBase := realTempDir(t)
	currentWorktreeBase := realTempDir(t)
	persistedWorktreeBase := filepath.Join(realTempDir(t), "persisted-worktrees")
	pkg := testTaskPackage(t, runsBase, persistedWorktreeBase)

	_, err := RunContextFromTaskPackage(pkg, runsBase, currentWorktreeBase)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrWorktreeBaseMismatch)
}

func TestRunContextFromTaskPackage_RejectsTamperedConfiguredWorktreeBase(t *testing.T) {
	runsBase := realTempDir(t)
	worktreeBase := realTempDir(t)
	pkg := testTaskPackage(t, runsBase, worktreeBase)
	pkg.Worktrees[0].Path = filepath.Join("/tmp", "foreign-base", fmt.Sprintf("%s-pass1-a1", pkg.RunID))

	_, err := RunContextFromTaskPackage(pkg, runsBase, worktreeBase)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrWorktreeBaseMismatch)
}

func TestRunContextFromTaskPackage_RejectsLegacyNestedWorktreePathShape(t *testing.T) {
	runsBase := realTempDir(t)
	worktreeBase := realTempDir(t)
	pkg := testTaskPackage(t, runsBase, worktreeBase)
	pkg.Worktrees[0].Path = filepath.Join(worktreeBase, "pass1", "a1")

	_, err := RunContextFromTaskPackage(pkg, runsBase, worktreeBase)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrWorktreeBaseMismatch)
}

func TestLoadFinalizedManifest_RejectsCrossRunManifestIdentity(t *testing.T) {
	ctx := newTestRunContext(t)
	otherRunID := contracts.RunID("2026-04-21-PR99-deadbee")
	success := contracts.Manifest{
		Kind: contracts.ManifestKindSuccess,
		Value: contracts.ManifestSuccess{
			Kind:          contracts.ManifestKindSuccess,
			SchemaVersion: "1",
			RunID:         otherRunID,
			Pass:          1,
			Agent:         "a1",
			BranchName:    "run/a1",
			HeadSHA:       strings.Repeat("1", 40),
			BaseSHA:       strings.Repeat("2", 40),
			DiffPath:      "20-pass1/a1/diff.patch",
			SessionPath:   "20-pass1/a1/session.jsonl",
			ChecklistPath: "20-pass1/a1/checklist-result.json",
			PromptVersion: "prompt-v1",
			StartedAt:     time.Unix(100, 0).UTC(),
			FinishedAt:    time.Unix(200, 0).UTC(),
		},
	}
	manifestPath, err := ctx.ManifestPath(1, "a1")
	require.NoError(t, err)
	require.NoError(t, WriteJSONAtomic(manifestPath, success))

	_, err = LoadFinalizedManifest(ctx, 1, "a1")
	require.ErrorContains(t, err, "manifest run_id mismatch")
}
