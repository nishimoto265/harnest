package agentrunner

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadChecklistArtifact_RejectsMismatchedMetadata(t *testing.T) {
	worktreePath := t.TempDir()
	checklistPath := filepath.Join(worktreePath, "checklist-result.json")
	require.NoError(t, internalio.WriteJSONAtomic(checklistPath, contracts.ChecklistResult{
		SchemaVersion: "1",
		RunID:         "2026-04-21-PR42-abcdef0",
		Pass:          2,
		Agent:         "a2",
		Items:         []contracts.ChecklistItem{},
	}))

	_, err := LoadChecklistArtifact(worktreePath, "checklist-result.json", "step20", "2026-04-21-PR42-abcdef0", 1, "a1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checklist pass mismatch")
}

func TestSuccessDiffBytes_PreservesLeadingWhitespaceInUntrackedFilename(t *testing.T) {
	repoDir := t.TempDir()
	runGit(t, "", "git", "init", "-b", "main", repoDir)
	runGit(t, repoDir, "git", "config", "user.email", "test@example.com")
	runGit(t, repoDir, "git", "config", "user.name", "Agent Runner Test")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644))
	runGit(t, repoDir, "git", "add", "README.md")
	runGit(t, repoDir, "git", "commit", "-m", "base")

	baseSHA := strings.TrimSpace(runGit(t, repoDir, "git", "rev-parse", "HEAD"))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, " leading.txt"), []byte("hello\n"), 0o644))

	diff, err := SuccessDiffBytes(context.Background(), repoDir, baseSHA, "test")
	require.NoError(t, err)
	assert.Contains(t, string(diff), "diff --git a/ leading.txt b/ leading.txt")
}

func TestSuccessDiffBytes_ExcludesOnlyPolicyOverlayArtifacts(t *testing.T) {
	repoDir := t.TempDir()
	runGit(t, "", "git", "init", "-b", "main", repoDir)
	runGit(t, repoDir, "git", "config", "user.email", "test@example.com")
	runGit(t, repoDir, "git", "config", "user.name", "Agent Runner Test")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644))
	runGit(t, repoDir, "git", "add", "README.md")
	runGit(t, repoDir, "git", "commit", "-m", "base")
	baseSHA := strings.TrimSpace(runGit(t, repoDir, "git", "rev-parse", "HEAD"))

	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".auto-improve", "lessons"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "auto-improve", "rules"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "docs", "harness-eval", "checklists"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "docs", "frontend-rules"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, ".auto-improve", "lessons", "local.md"), []byte("lesson\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "auto-improve", "rules-registry.jsonl"), []byte("{}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "auto-improve", "rules", "r-local.md"), []byte("rule\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "auto-improve", "app.go"), []byte("package autoimprove\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "docs", "harness-eval", "checklists", "rules-checklist.md"), []byte("checklist\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "docs", "frontend-rules", "rule.md"), []byte("frontend rule\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "scripts", "generate-checklist.sh"), []byte("#!/bin/sh\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "app.go"), []byte("package main\n"), 0o644))

	diff, err := SuccessDiffBytes(context.Background(), repoDir, baseSHA, "test")
	require.NoError(t, err)

	text := string(diff)
	assert.Contains(t, text, "app.go")
	assert.Contains(t, text, "docs/harness-eval/checklists/rules-checklist.md")
	assert.Contains(t, text, "docs/frontend-rules/rule.md")
	assert.Contains(t, text, "scripts/generate-checklist.sh")
	assert.Contains(t, text, "auto-improve/app.go")
	assert.NotContains(t, text, ".auto-improve")
	assert.NotContains(t, text, "auto-improve/rules")
}

func TestWriteSuccessDiff_CapsHugeUntrackedPatch(t *testing.T) {
	repoDir := t.TempDir()
	runGit(t, "", "git", "init", "-b", "main", repoDir)
	runGit(t, repoDir, "git", "config", "user.email", "test@example.com")
	runGit(t, repoDir, "git", "config", "user.name", "Agent Runner Test")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644))
	runGit(t, repoDir, "git", "add", "README.md")
	runGit(t, repoDir, "git", "commit", "-m", "base")

	baseSHA := strings.TrimSpace(runGit(t, repoDir, "git", "rev-parse", "HEAD"))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "huge.txt"), []byte(strings.Repeat("x", maxSuccessDiffBytes)), 0o644))

	destPath := filepath.Join(t.TempDir(), "diff.patch")
	err := WriteSuccessDiff(context.Background(), repoDir, baseSHA, "test", destPath)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSuccessDiffOverflow)
	_, statErr := os.Stat(destPath)
	assert.Error(t, statErr)
	assert.True(t, os.IsNotExist(statErr))
}

func TestWriteSuccessDiff_SyncsTempBeforeRenameAndParentDirAfter(t *testing.T) {
	repoDir := t.TempDir()
	runGit(t, "", "git", "init", "-b", "main", repoDir)
	runGit(t, repoDir, "git", "config", "user.email", "test@example.com")
	runGit(t, repoDir, "git", "config", "user.name", "Agent Runner Test")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\nchange\n"), 0o644))
	runGit(t, repoDir, "git", "add", "README.md")
	runGit(t, repoDir, "git", "commit", "-m", "base")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\nchanged\n"), 0o644))

	baseSHA := strings.TrimSpace(runGit(t, repoDir, "git", "rev-parse", "HEAD"))
	destPath := filepath.Join(t.TempDir(), "diff.patch")

	originalSync := writeSuccessDiffFileSync
	originalRename := writeSuccessDiffRename
	originalSyncDir := writeSuccessDiffSyncDir
	t.Cleanup(func() {
		writeSuccessDiffFileSync = originalSync
		writeSuccessDiffRename = originalRename
		writeSuccessDiffSyncDir = originalSyncDir
	})

	var order []string
	writeSuccessDiffFileSync = func(f *os.File) error {
		order = append(order, "temp-sync")
		return nil
	}
	writeSuccessDiffRename = func(oldPath, newPath string) error {
		order = append(order, "rename")
		return os.Rename(oldPath, newPath)
	}
	writeSuccessDiffSyncDir = func(path string) error {
		order = append(order, "dir-sync")
		return nil
	}

	err := WriteSuccessDiff(context.Background(), repoDir, baseSHA, "test", destPath)
	require.NoError(t, err)
	assert.Equal(t, []string{"temp-sync", "rename", "dir-sync"}, order)
}

func TestWriteSuccessDiff_RejectsOversizedUntrackedFileBeforeSnapshotCopy(t *testing.T) {
	repoDir := t.TempDir()
	runGit(t, "", "git", "init", "-b", "main", repoDir)
	runGit(t, repoDir, "git", "config", "user.email", "test@example.com")
	runGit(t, repoDir, "git", "config", "user.name", "Agent Runner Test")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644))
	runGit(t, repoDir, "git", "add", "README.md")
	runGit(t, repoDir, "git", "commit", "-m", "base")

	baseSHA := strings.TrimSpace(runGit(t, repoDir, "git", "rev-parse", "HEAD"))
	hugePath := filepath.Join(repoDir, "oversized.bin")
	file, err := os.Create(hugePath)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(100<<20))
	require.NoError(t, file.Close())

	destDir := t.TempDir()
	destPath := filepath.Join(destDir, "diff.patch")
	originalSnapshotCopy := snapshotCopyOpenFile
	copyCalls := 0
	snapshotCopyOpenFile = func(ctx context.Context, dst io.Writer, src *os.File, sizeLimit int64) (int64, error) {
		copyCalls++
		return originalSnapshotCopy(ctx, dst, src, sizeLimit)
	}
	t.Cleanup(func() {
		snapshotCopyOpenFile = originalSnapshotCopy
	})

	err = WriteSuccessDiff(context.Background(), repoDir, baseSHA, "test", destPath)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSuccessDiffOverflow)
	assert.Zero(t, copyCalls)
	entries, readErr := os.ReadDir(destDir)
	require.NoError(t, readErr)
	assert.Empty(t, entries)
}

func TestWriteSuccessDiff_RemovesTempFileWhenRenameFails(t *testing.T) {
	repoDir := t.TempDir()
	runGit(t, "", "git", "init", "-b", "main", repoDir)
	runGit(t, repoDir, "git", "config", "user.email", "test@example.com")
	runGit(t, repoDir, "git", "config", "user.name", "Agent Runner Test")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644))
	runGit(t, repoDir, "git", "add", "README.md")
	runGit(t, repoDir, "git", "commit", "-m", "base")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\nchanged\n"), 0o644))

	baseSHA := strings.TrimSpace(runGit(t, repoDir, "git", "rev-parse", "HEAD"))
	destPath := filepath.Join(t.TempDir(), "diff.patch")

	originalRename := writeSuccessDiffRename
	t.Cleanup(func() {
		writeSuccessDiffRename = originalRename
	})

	renameErr := errors.New("rename failed")
	tempPath := ""
	writeSuccessDiffRename = func(oldPath, newPath string) error {
		tempPath = oldPath
		return renameErr
	}

	err := WriteSuccessDiff(context.Background(), repoDir, baseSHA, "test", destPath)
	require.ErrorIs(t, err, renameErr)
	require.NotEmpty(t, tempPath)
	_, statErr := os.Stat(tempPath)
	require.Error(t, statErr)
	assert.True(t, os.IsNotExist(statErr))
}

func TestSnapshotDiffableArtifact_RejectsGrowthBeyondLimitDuringCopy(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})

	originalOpen := snapshotOpenValidatedRegularFile
	snapshotOpenValidatedRegularFile = func(string) (*os.File, os.FileMode, int64, error) {
		return reader, 0o644, 1, nil
	}
	t.Cleanup(func() {
		snapshotOpenValidatedRegularFile = originalOpen
	})

	go func() {
		_, _ = writer.Write([]byte(strings.Repeat("x", maxSuccessDiffBytes+1)))
		_ = writer.Close()
	}()

	_, diffable, err := snapshotDiffableArtifact(context.Background(), "/tmp/virtual-source", t.TempDir(), "growing.txt")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSuccessDiffOverflow)
	assert.False(t, diffable)
}

func TestWriteSuccessDiff_SkipsSymlinkedUntrackedFile(t *testing.T) {
	repoDir := t.TempDir()
	runGit(t, "", "git", "init", "-b", "main", repoDir)
	runGit(t, repoDir, "git", "config", "user.email", "test@example.com")
	runGit(t, repoDir, "git", "config", "user.name", "Agent Runner Test")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644))
	runGit(t, repoDir, "git", "add", "README.md")
	runGit(t, repoDir, "git", "commit", "-m", "base")

	baseSHA := strings.TrimSpace(runGit(t, repoDir, "git", "rev-parse", "HEAD"))
	require.NoError(t, os.Symlink("/etc/hosts", filepath.Join(repoDir, "loot")))

	diff, err := SuccessDiffBytes(context.Background(), repoDir, baseSHA, "test")
	require.NoError(t, err)
	assert.NotContains(t, string(diff), "loot")
}

func TestWriteSuccessDiff_SkipsHardlinkedUntrackedFile(t *testing.T) {
	repoDir := t.TempDir()
	runGit(t, "", "git", "init", "-b", "main", repoDir)
	runGit(t, repoDir, "git", "config", "user.email", "test@example.com")
	runGit(t, repoDir, "git", "config", "user.name", "Agent Runner Test")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644))
	runGit(t, repoDir, "git", "add", "README.md")
	runGit(t, repoDir, "git", "commit", "-m", "base")

	baseSHA := strings.TrimSpace(runGit(t, repoDir, "git", "rev-parse", "HEAD"))
	externalPath := filepath.Join(t.TempDir(), "external.txt")
	require.NoError(t, os.WriteFile(externalPath, []byte("top-secret\n"), 0o644))
	require.NoError(t, os.Link(externalPath, filepath.Join(repoDir, "loot")))

	diff, err := SuccessDiffBytes(context.Background(), repoDir, baseSHA, "test")
	require.NoError(t, err)
	assert.NotContains(t, string(diff), "top-secret")
	assert.NotContains(t, string(diff), "loot")
}

func TestWriteSuccessDiff_PreservesExecutableBitForUntrackedFile(t *testing.T) {
	repoDir := t.TempDir()
	runGit(t, "", "git", "init", "-b", "main", repoDir)
	runGit(t, repoDir, "git", "config", "user.email", "test@example.com")
	runGit(t, repoDir, "git", "config", "user.name", "Agent Runner Test")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644))
	runGit(t, repoDir, "git", "add", "README.md")
	runGit(t, repoDir, "git", "commit", "-m", "base")

	baseSHA := strings.TrimSpace(runGit(t, repoDir, "git", "rev-parse", "HEAD"))
	scriptPath := filepath.Join(repoDir, "script.sh")
	require.NoError(t, os.WriteFile(scriptPath, []byte("#!/bin/sh\necho hi\n"), 0o755))

	diff, err := SuccessDiffBytes(context.Background(), repoDir, baseSHA, "test")
	require.NoError(t, err)
	assert.Contains(t, string(diff), "new file mode 100755")
}

func TestLoadChecklistArtifact_RejectsFIFO(t *testing.T) {
	worktreePath := t.TempDir()
	checklistPath := filepath.Join(worktreePath, "checklist-result.json")
	require.NoError(t, syscall.Mkfifo(checklistPath, 0o644))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := LoadChecklistArtifactContext(ctx, worktreePath, "checklist-result.json", "step20", "2026-04-21-PR42-abcdef0", 1, "a1")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrArtifactNotRegular))
}

func TestLoadChecklistArtifact_RejectsOversizedFile(t *testing.T) {
	worktreePath := t.TempDir()
	checklistPath := filepath.Join(worktreePath, "checklist-result.json")
	file, err := os.Create(checklistPath)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(50<<20))
	require.NoError(t, file.Close())

	_, err = LoadChecklistArtifact(worktreePath, "checklist-result.json", "step20", "2026-04-21-PR42-abcdef0", 1, "a1")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrArtifactTooLarge)
}

func TestLoadChecklistArtifactContext_RejectsHugeFileWithinTTL(t *testing.T) {
	worktreePath := t.TempDir()
	checklistPath := filepath.Join(worktreePath, "checklist-result.json")
	file, err := os.Create(checklistPath)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(100<<20))
	require.NoError(t, file.Close())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	_, err = LoadChecklistArtifactContext(ctx, worktreePath, "checklist-result.json", "step20", "2026-04-21-PR42-abcdef0", 1, "a1")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrArtifactTooLarge)
	assert.Less(t, time.Since(start), 10*time.Second)
}

func runGit(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "%s %s failed: %s", name, strings.Join(args, " "), string(output))
	return string(output)
}
