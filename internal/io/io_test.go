package io

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testJSONLRecord struct {
	Name string `json:"name"`
}

func TestWriteAtomic_RemovesStaleTempsAndWritesFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "manifest.json")
	staleA := target + ".tmp-1-1-aaaa"
	staleB := target + ".tmp-1-2-bbbb"
	require.NoError(t, os.WriteFile(staleA, []byte("stale"), defaultFilePerm))
	require.NoError(t, os.WriteFile(staleB, []byte("stale"), defaultFilePerm))

	require.NoError(t, WriteAtomic(target, []byte(`{"ok":true}`)))

	data, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, string(data))
	_, err = os.Stat(staleA)
	assert.Error(t, err)
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(staleB)
	assert.Error(t, err)
	assert.True(t, os.IsNotExist(err))
}

func TestWriteAtomic_RenameFailureCleansTemp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "decision.json")

	originalRename := atomicRename
	atomicRename = func(oldPath, newPath string) error {
		return errors.New("rename failed")
	}
	t.Cleanup(func() {
		atomicRename = originalRename
	})

	err := WriteAtomic(target, []byte(`{"ok":true}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rename failed")

	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	assert.Empty(t, entries)
}

func TestAppendJSONLAndReadJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "records.jsonl")
	require.NoError(t, AppendJSONL(path, testJSONLRecord{Name: "alpha"}))
	require.NoError(t, AppendJSONL(path, testJSONLRecord{Name: "beta"}))

	records, err := ReadJSONL[testJSONLRecord](path)
	require.NoError(t, err)
	require.Len(t, records, 2)
	assert.Equal(t, "alpha", records[0].Name)
	assert.Equal(t, "beta", records[1].Name)
}

func TestAppendJSONL_RejectsEntryTooLarge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "records.jsonl")
	err := AppendJSONL(path, testJSONLRecord{Name: strings.Repeat("a", JSONLMaxLineBytes)})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEntryTooLarge)
}

func TestReadJSONL_StrictDecodeFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		line string
		want error
	}{
		{
			name: "duplicate key",
			line: `{"name":"a","name":"b"}` + "\n",
			want: contracts.ErrDuplicateJSONKey,
		},
		{
			name: "trailing json",
			line: `{"name":"a"}{"name":"b"}` + "\n",
			want: contracts.ErrTrailingJSON,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "records.jsonl")
			require.NoError(t, os.WriteFile(path, []byte(tt.line), defaultFilePerm))

			_, err := ReadJSONL[testJSONLRecord](path)
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.want)
		})
	}
}

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
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
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

func TestAcquirePromotionLock(t *testing.T) {
	ctx := newTestRunContext(t)
	lock, err := AcquirePromotionLock(ctx)
	require.NoError(t, err)
	require.NoError(t, lock.Unlock())

	_, err = os.Stat(ctx.PromotionLockPath())
	require.NoError(t, err)
}

func TestAppendRegistryEntryCASAndIndexRebuild(t *testing.T) {
	registryPath := filepath.Join(t.TempDir(), "rules-registry.jsonl")
	first := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         "rule-1",
			RulePath:       "rules/rule-1.md",
			Sha256:         strings.Repeat("1", 64),
			IdempotencyKey: strings.Repeat("2", 64),
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        "2026-04-21-PR1-abcdef0",
			At:             time.Unix(100, 0).UTC(),
		},
	}

	firstResult, err := AppendRegistryEntry(registryPath, first)
	require.NoError(t, err)
	assert.EqualValues(t, 0, firstResult.Offset)
	assert.Len(t, firstResult.Sha256, 64)

	second := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindUpdated,
		Value: contracts.RuleRegistryUpdated{
			Kind:           contracts.RegistryKindUpdated,
			SchemaVersion:  "1",
			RuleID:         "rule-1",
			RulePath:       "rules/rule-1.md",
			Sha256:         strings.Repeat("3", 64),
			PrevSha256:     strings.Repeat("1", 64),
			IdempotencyKey: strings.Repeat("4", 64),
			VersionSeq:     2,
			PrevHash:       firstResult.Sha256,
			ByRunID:        "2026-04-21-PR2-bcdef01",
			At:             time.Unix(200, 0).UTC(),
		},
	}

	secondResult, err := AppendRegistryEntry(registryPath, second)
	require.NoError(t, err)
	assert.Greater(t, secondResult.Offset, int64(0))

	mismatch := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindUpdated,
		Value: contracts.RuleRegistryUpdated{
			Kind:           contracts.RegistryKindUpdated,
			SchemaVersion:  "1",
			RuleID:         "rule-1",
			RulePath:       "rules/rule-1.md",
			Sha256:         strings.Repeat("5", 64),
			PrevSha256:     strings.Repeat("3", 64),
			IdempotencyKey: strings.Repeat("6", 64),
			VersionSeq:     3,
			PrevHash:       strings.Repeat("f", 64),
			ByRunID:        "2026-04-21-PR3-cdef012",
			At:             time.Unix(300, 0).UTC(),
		},
	}

	_, err = AppendRegistryEntry(registryPath, mismatch)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRegistryCASMismatch)

	indexPath := filepath.Join(filepath.Dir(registryPath), "rules-idempotency-index.jsonl")
	indexEntries, err := RebuildIdempotencyIndex(registryPath, indexPath)
	require.NoError(t, err)
	require.Len(t, indexEntries, 2)
	assert.Equal(t, strings.Repeat("2", 64), indexEntries[0].IdempotencyKey)
	assert.Equal(t, strings.Repeat("4", 64), indexEntries[1].IdempotencyKey)

	loadedIndex, err := ReadJSONL[contracts.RuleIdempotencyIndexEntry](indexPath)
	require.NoError(t, err)
	require.Len(t, loadedIndex, 2)
	assert.Equal(t, indexEntries, loadedIndex)
}

func newTestRunContext(t *testing.T) RunContext {
	t.Helper()

	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	ctx, err := NewRunContext("2026-04-21-PR42-abcdef0", runsBase, worktreeBase)
	require.NoError(t, err)
	return ctx
}

func testTaskPackage(t *testing.T, runsBase, worktreeBase string) contracts.TaskPackage {
	t.Helper()

	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	worktrees := make([]contracts.WorktreeAllocation, 0, 6)
	for pass := 1; pass <= 2; pass++ {
		for agentNum := 1; agentNum <= 3; agentNum++ {
			agent := contracts.AgentID(fmt.Sprintf("a%d", agentNum))
			worktrees = append(worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    filepath.Join(worktreeBase, fmt.Sprintf("%s-pass%d-%s", runID, pass, agent)),
				Branch:  fmt.Sprintf("run/%s/pass%d", agent, pass),
				BaseSHA: strings.Repeat("1", 40),
				HeadSHA: strings.Repeat("1", 40),
			})
		}
	}

	pkg := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runID,
		PR:                      42,
		Title:                   "test",
		BaseSHA:                 strings.Repeat("1", 40),
		BestBranch:              "best/main",
		ReconstructedTaskPrompt: "do thing",
		Worktrees:               worktrees,
		CreatedAt:               time.Unix(100, 0).UTC(),
	}
	require.NoError(t, pkg.Validate())
	return pkg
}
