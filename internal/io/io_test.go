package io

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	stdio "io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testJSONLRecord struct {
	Name string `json:"name"`
}

func realTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return real
}

func TestWriteAtomic_PreservesSiblingTempsAndWritesFile(t *testing.T) {
	dir := realTempDir(t)
	target := filepath.Join(dir, "manifest.json")
	staleA := target + ".tmp-1-1-aaaa"
	staleB := target + ".tmp-1-2-bbbb"
	require.NoError(t, os.WriteFile(staleA, []byte("stale"), defaultFilePerm))
	require.NoError(t, os.WriteFile(staleB, []byte("stale"), defaultFilePerm))

	require.NoError(t, WriteAtomic(target, []byte(`{"ok":true}`)))

	data, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, string(data))
	assert.FileExists(t, staleA)
	assert.FileExists(t, staleB)
}

func TestWriteAtomic_RenameFailureCleansTemp(t *testing.T) {
	dir := realTempDir(t)
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

func TestWriteAtomic_ConcurrentWritersDoNotDeletePeerTemps(t *testing.T) {
	dir := realTempDir(t)
	target := filepath.Join(dir, "manifest.json")

	originalRename := atomicRename
	originalAfterTempCreate := atomicAfterTempCreate
	enteredRename := make(chan string, 2)
	releaseRename := make(chan struct{})
	firstTempCreated := make(chan string, 1)
	secondTempCreated := make(chan string, 1)
	releaseFirstWriter := make(chan struct{})
	var tempCreateMu sync.Mutex
	tempCreateCount := 0
	atomicRename = func(oldPath, newPath string) error {
		enteredRename <- oldPath
		<-releaseRename
		return originalRename(oldPath, newPath)
	}
	t.Cleanup(func() {
		atomicRename = originalRename
		atomicAfterTempCreate = originalAfterTempCreate
	})
	atomicAfterTempCreate = func(tmpPath string) {
		tempCreateMu.Lock()
		tempCreateCount++
		count := tempCreateCount
		tempCreateMu.Unlock()
		switch count {
		case 1:
			firstTempCreated <- tmpPath
			<-releaseFirstWriter
		case 2:
			secondTempCreated <- tmpPath
		}
	}

	errs := make(chan error, 2)
	go func() {
		errs <- WriteAtomic(target, []byte(`{"writer":"one"}`))
	}()
	firstTemp := <-firstTempCreated
	assert.FileExists(t, firstTemp)
	go func() {
		errs <- WriteAtomic(target, []byte(`{"writer":"two"}`))
	}()
	secondTemp := <-secondTempCreated
	assert.FileExists(t, secondTemp)

	close(releaseFirstWriter)

	<-enteredRename
	<-enteredRename
	assert.FileExists(t, secondTemp)
	close(releaseRename)

	require.NoError(t, <-errs)
	require.NoError(t, <-errs)

	data, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Contains(t, []string{`{"writer":"one"}`, `{"writer":"two"}`}, string(data))
}

func TestWriteAtomic_FailsClosedWhenParentDirectoryChangesBeforeTempCreate(t *testing.T) {
	root := realTempDir(t)
	parent := filepath.Join(root, "safe")
	target := filepath.Join(parent, "manifest.json")
	escape := filepath.Join(root, "escape")
	require.NoError(t, os.MkdirAll(parent, 0o755))
	require.NoError(t, os.MkdirAll(escape, 0o755))

	originalHook := atomicAfterParentValidated
	atomicAfterParentValidated = func(string) {
		moved := parent + ".moved"
		require.NoError(t, os.Rename(parent, moved))
		require.NoError(t, os.Symlink(escape, parent))
	}
	t.Cleanup(func() {
		atomicAfterParentValidated = originalHook
	})

	err := WriteAtomic(target, []byte(`{"ok":true}`))
	require.Error(t, err)
	assert.NoFileExists(t, filepath.Join(escape, "manifest.json"))
}

func TestAppendJSONLAndReadJSONL(t *testing.T) {
	path := filepath.Join(realTempDir(t), "records.jsonl")
	require.NoError(t, AppendJSONL(path, testJSONLRecord{Name: "alpha"}))
	require.NoError(t, AppendJSONL(path, testJSONLRecord{Name: "beta"}))

	records, err := ReadJSONL[testJSONLRecord](path)
	require.NoError(t, err)
	require.Len(t, records, 2)
	assert.Equal(t, "alpha", records[0].Name)
	assert.Equal(t, "beta", records[1].Name)
}

func TestAppendJSONL_RejectsEntryTooLarge(t *testing.T) {
	path := filepath.Join(realTempDir(t), "records.jsonl")
	err := AppendJSONL(path, testJSONLRecord{Name: strings.Repeat("a", JSONLMaxLineBytes)})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEntryTooLarge)
}

func TestAppendJSONL_SyncsParentDirectory(t *testing.T) {
	path := filepath.Join(realTempDir(t), "records.jsonl")

	originalSync := directorySync
	var synced []string
	directorySync = func(path string) error {
		synced = append(synced, path)
		return nil
	}
	t.Cleanup(func() {
		directorySync = originalSync
	})

	require.NoError(t, AppendJSONL(path, testJSONLRecord{Name: "alpha"}))
	require.Equal(t, []string{filepath.Dir(path)}, synced)
}

func TestAppendJSONL_RollsBackPartialWrite(t *testing.T) {
	path := filepath.Join(realTempDir(t), "records.jsonl")
	require.NoError(t, AppendJSONL(path, testJSONLRecord{Name: "alpha"}))

	originalOpen := appendJSONLOpenFile
	failFile := &failingAppendFile{
		remaining: 2,
		err:       errors.New("injected write failure"),
	}
	appendJSONLOpenFile = func(path string) (appendJSONLFile, error) {
		file, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, defaultFilePerm)
		if err != nil {
			return nil, err
		}
		failFile.File = file
		return failFile, nil
	}
	t.Cleanup(func() {
		appendJSONLOpenFile = originalOpen
	})
	originalDirectorySync := directorySync
	var synced []string
	directorySync = func(path string) error {
		synced = append(synced, path)
		return nil
	}
	t.Cleanup(func() {
		directorySync = originalDirectorySync
	})

	infoBefore, err := os.Stat(path)
	require.NoError(t, err)

	err = AppendJSONL(path, testJSONLRecord{Name: "beta"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "injected write failure")

	infoAfter, statErr := os.Stat(path)
	require.NoError(t, statErr)
	assert.Equal(t, infoBefore.Size(), infoAfter.Size())
	assert.Equal(t, 1, failFile.truncateCalls)
	assert.Equal(t, 1, failFile.syncCalls)
	assert.Equal(t, []string{filepath.Dir(path)}, synced)

	records, readErr := ReadJSONL[testJSONLRecord](path)
	require.NoError(t, readErr)
	require.Len(t, records, 1)
	assert.Equal(t, "alpha", records[0].Name)
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
			path := filepath.Join(realTempDir(t), "records.jsonl")
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

func TestAcquirePromotionLock(t *testing.T) {
	ctx := newTestRunContext(t)
	lock, err := AcquirePromotionLock(ctx)
	require.NoError(t, err)
	require.NoError(t, lock.Unlock())

	_, err = os.Stat(ctx.PromotionLockPath())
	require.NoError(t, err)
}

func TestAcquireFileLock_RejectsSymlinkSwapWhileAnotherHolderOwnsLock(t *testing.T) {
	lockPath := filepath.Join(realTempDir(t), "promotion.lock")
	firstLock, err := AcquireFileLock(lockPath)
	require.NoError(t, err)
	defer func() {
		_ = firstLock.Unlock()
	}()

	replacement := filepath.Join(realTempDir(t), "replacement.lock")
	require.NoError(t, os.WriteFile(replacement, []byte("replacement\n"), defaultFilePerm))
	require.NoError(t, os.Remove(lockPath))
	require.NoError(t, os.Symlink(replacement, lockPath))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = AcquireFileLockContext(ctx, lockPath)
	require.Error(t, err)
	assert.False(t, errors.Is(err, context.DeadlineExceeded))
}

func TestOpenFileNoFollow_DoesNotLeakFDToChildProcess(t *testing.T) {
	path := filepath.Join(realTempDir(t), "artifact.txt")
	require.NoError(t, os.WriteFile(path, []byte("hello\n"), defaultFilePerm))

	file, err := openFileNoFollow(path, os.O_RDONLY, 0)
	require.NoError(t, err)
	defer file.Close()

	fd := int(file.Fd())
	cmd := exec.Command("/bin/sh", "-c", fmt.Sprintf("test -r /dev/fd/%d", fd))
	err = cmd.Run()
	require.Error(t, err)
}

func TestOpenValidatedRegularFile_RejectsMultiLinkFile(t *testing.T) {
	ctx := newTestRunContext(t)
	sharedPath := filepath.Join(realTempDir(t), "shared.md")
	require.NoError(t, os.WriteFile(sharedPath, []byte("secret\n"), defaultFilePerm))

	runPath, err := ctx.ResolveRunRelative("40/candidates/linked.md")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(runPath), defaultDirectoryPerm))
	require.NoError(t, os.Link(sharedPath, runPath))

	_, err = OpenValidatedRegularFile(runPath, ctx.RunDir())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafePath)
}

func TestOpenValidatedRegularFile_FailsClosedWhenParentDirectoryChangesBeforeOpen(t *testing.T) {
	root := realTempDir(t)
	parent := filepath.Join(root, "40", "candidates")
	path := filepath.Join(parent, "candidate.md")
	escape := filepath.Join(realTempDir(t), "escape")
	require.NoError(t, os.MkdirAll(parent, 0o755))
	require.NoError(t, os.MkdirAll(escape, 0o755))
	require.NoError(t, os.WriteFile(path, []byte("safe\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(escape, "candidate.md"), []byte("escape\n"), 0o644))

	originalHook := validatedRegularFileBeforeOpen
	validatedRegularFileBeforeOpen = func(string) {
		moved := parent + ".moved"
		require.NoError(t, os.Rename(parent, moved))
		require.NoError(t, os.Symlink(escape, parent))
	}
	t.Cleanup(func() {
		validatedRegularFileBeforeOpen = originalHook
	})

	_, err := OpenValidatedRegularFile(path, root)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafePath)
}

func TestOpenValidatedRegularFile_RejectsOversizedFile(t *testing.T) {
	ctx := newTestRunContext(t)
	path, err := ctx.ResolveRunRelative("40/candidates/large.md")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(50*1024*1024))
	require.NoError(t, file.Close())

	_, err = OpenValidatedRegularFile(path, ctx.RunDir())
	require.ErrorIs(t, err, ErrFileTooLarge)
}

func TestAppendRegistryEntryCASAndIndexRebuild(t *testing.T) {
	registryPath := filepath.Join(realTempDir(t), "rules-registry.jsonl")
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

func TestReadRegistryLinesRejectsUnterminatedFinalLine(t *testing.T) {
	registryPath := filepath.Join(realTempDir(t), "rules-registry.jsonl")
	entry := contracts.RuleRegistryEntry{
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
	payload, err := marshalJSONLRecord(entry)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(registryPath, payload, 0o644))

	_, err = readRegistryLines(registryPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unterminated final line")
}

func TestAppendRegistryPayloadRollsBackPartialRecordWrite(t *testing.T) {
	registryPath := filepath.Join(realTempDir(t), "rules-registry.jsonl")
	original := []byte("{\"existing\":true}\n")
	require.NoError(t, os.WriteFile(registryPath, original, 0o644))

	file, err := os.OpenFile(registryPath, os.O_RDWR|os.O_APPEND, defaultFilePerm)
	require.NoError(t, err)
	failFile := &failingAppendFile{
		File:      file,
		remaining: len(`{"new":true}`),
		err:       errors.New("injected write failure"),
	}
	err = appendRegistryPayload(registryPath, failFile, []byte(`{"new":true}`))
	require.Error(t, err)
	require.NoError(t, failFile.Close())

	data, readErr := os.ReadFile(registryPath)
	require.NoError(t, readErr)
	assert.Equal(t, original, data)
	assert.Equal(t, 1, failFile.truncateCalls)
	assert.Equal(t, 1, failFile.syncCalls)
}

func TestEnsureVerifiedIdempotencyIndex_RebuildsCorruptIndex(t *testing.T) {
	registryPath := filepath.Join(realTempDir(t), "rules-registry.jsonl")
	indexPath := filepath.Join(filepath.Dir(registryPath), "rules-idempotency-index.jsonl")

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

	require.NoError(t, AppendJSONL(indexPath, contracts.RuleIdempotencyIndexEntry{
		IdempotencyKey: strings.Repeat("2", 64),
		RegistryOffset: firstResult.Offset,
		RegistrySha256: strings.Repeat("f", 64),
		Kind:           contracts.RegistryKindAdded,
		At:             time.Unix(100, 0).UTC(),
	}))

	indexEntries, rebuilt, err := EnsureVerifiedIdempotencyIndex(registryPath, indexPath)
	require.NoError(t, err)
	assert.True(t, rebuilt)
	require.Len(t, indexEntries, 2)
	assert.Equal(t, firstResult.Offset, indexEntries[0].RegistryOffset)
	assert.Equal(t, secondResult.Offset, indexEntries[1].RegistryOffset)
	loadedIndex, err := ReadJSONL[contracts.RuleIdempotencyIndexEntry](indexPath)
	require.NoError(t, err)
	assert.Equal(t, indexEntries, loadedIndex)
}

func TestSyncIdempotencyIndex_RebuildDoesNotDuplicateCurrentEntry(t *testing.T) {
	registryPath := filepath.Join(realTempDir(t), "rules-registry.jsonl")
	indexPath := filepath.Join(filepath.Dir(registryPath), "rules-idempotency-index.jsonl")

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
	_, err = RebuildIdempotencyIndex(registryPath, indexPath)
	require.NoError(t, err)

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

	require.NoError(t, os.Remove(indexPath))
	require.NoError(t, SyncIdempotencyIndex(registryPath, indexPath, second, secondResult))

	loadedIndex, err := ReadJSONL[contracts.RuleIdempotencyIndexEntry](indexPath)
	require.NoError(t, err)
	require.Len(t, loadedIndex, 2)
	assert.Equal(t, secondResult.Offset, loadedIndex[1].RegistryOffset)
}

func TestAppendRegistryEntry_ConcurrentCASAllowsSingleWinner(t *testing.T) {
	registryPath := filepath.Join(realTempDir(t), "rules-registry.jsonl")
	entry := contracts.RuleRegistryEntry{
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

	start := make(chan struct{})
	results := make([]error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			_, results[idx] = AppendRegistryEntry(registryPath, entry)
		}(i)
	}
	close(start)
	wg.Wait()

	successes := 0
	for _, err := range results {
		if err == nil {
			successes++
			continue
		}
		assert.True(t, errors.Is(err, ErrRegistryCASMismatch) || os.IsNotExist(err))
	}
	assert.Equal(t, 1, successes)

	lines, err := readRegistryLines(registryPath)
	require.NoError(t, err)
	require.Len(t, lines, 1)
}

func TestAppendRegistryEntry_FailsClosedWhenRegistryPathIdentityChanges(t *testing.T) {
	registryPath := filepath.Join(realTempDir(t), "rules-registry.jsonl")
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

	replacement := filepath.Join(realTempDir(t), "replacement-registry.jsonl")
	require.NoError(t, os.WriteFile(replacement, nil, defaultFilePerm))
	originalHook := registryBeforeAppendHook
	registryBeforeAppendHook = func() error {
		return os.Rename(replacement, registryPath)
	}
	t.Cleanup(func() {
		registryBeforeAppendHook = originalHook
	})

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

	_, err = AppendRegistryEntry(registryPath, second)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPathIdentityChanged)

	lines, readErr := readRegistryLines(registryPath)
	require.NoError(t, readErr)
	assert.Empty(t, lines)
}

func TestSyncIdempotencyIndex_ConcurrentAppendDeduplicatesOffset(t *testing.T) {
	registryPath := filepath.Join(realTempDir(t), "rules-registry.jsonl")
	indexPath := filepath.Join(filepath.Dir(registryPath), "rules-idempotency-index.jsonl")
	entry := contracts.RuleRegistryEntry{
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
	result, err := AppendRegistryEntry(registryPath, entry)
	require.NoError(t, err)
	_, err = RebuildIdempotencyIndex(registryPath, indexPath)
	require.NoError(t, err)

	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			errs[idx] = SyncIdempotencyIndex(registryPath, indexPath, entry, result)
		}(i)
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		require.True(t, err == nil || os.IsNotExist(err))
	}

	rows, err := ReadJSONL[contracts.RuleIdempotencyIndexEntry](indexPath)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestSyncIdempotencyIndex_FailsClosedWhenIndexPathIdentityChanges(t *testing.T) {
	registryPath := filepath.Join(realTempDir(t), "rules-registry.jsonl")
	indexPath := filepath.Join(filepath.Dir(registryPath), "rules-idempotency-index.jsonl")
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
	_, err = RebuildIdempotencyIndex(registryPath, indexPath)
	require.NoError(t, err)

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
	require.NoError(t, SyncIdempotencyIndex(registryPath, indexPath, second, secondResult))

	third := contracts.RuleRegistryEntry{
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
			PrevHash:       secondResult.Sha256,
			ByRunID:        "2026-04-21-PR3-cdef012",
			At:             time.Unix(300, 0).UTC(),
		},
	}
	thirdResult, err := AppendRegistryEntry(registryPath, third)
	require.NoError(t, err)

	replacement := filepath.Join(realTempDir(t), "replacement-index.jsonl")
	require.NoError(t, os.WriteFile(replacement, nil, defaultFilePerm))
	originalAppendHook := idempotencyIndexBeforeAppendHook
	originalRewriteHook := idempotencyIndexBeforeRewriteHook
	idempotencyIndexBeforeRewriteHook = func() error {
		if err := os.Remove(indexPath); err != nil {
			return err
		}
		return os.Rename(replacement, indexPath)
	}
	t.Cleanup(func() {
		idempotencyIndexBeforeAppendHook = originalAppendHook
		idempotencyIndexBeforeRewriteHook = originalRewriteHook
	})

	err = SyncIdempotencyIndex(registryPath, indexPath, third, thirdResult)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPathIdentityChanged)

	rows, readErr := ReadJSONL[contracts.RuleIdempotencyIndexEntry](indexPath)
	require.NoError(t, readErr)
	assert.Empty(t, rows)
}

func newTestRunContext(t *testing.T) RunContext {
	t.Helper()

	runsBase := realTempDir(t)
	worktreeBase := realTempDir(t)
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

type failingAppendFile struct {
	*os.File
	remaining     int
	err           error
	syncCalls     int
	truncateCalls int
}

func (f *failingAppendFile) Write(p []byte) (int, error) {
	if f.remaining <= 0 {
		return 0, f.err
	}
	if len(p) > f.remaining {
		n, err := f.File.Write(p[:f.remaining])
		f.remaining -= n
		if err != nil {
			return n, err
		}
		return n, f.err
	}
	n, err := f.File.Write(p)
	f.remaining -= n
	if err != nil {
		return n, err
	}
	if f.remaining == 0 {
		return n, f.err
	}
	return n, nil
}

func (f *failingAppendFile) Sync() error {
	f.syncCalls++
	return f.File.Sync()
}

func (f *failingAppendFile) Truncate(size int64) error {
	f.truncateCalls++
	return f.File.Truncate(size)
}

var _ appendJSONLFile = (*failingAppendFile)(nil)
var _ stdio.Writer = (*failingAppendFile)(nil)
