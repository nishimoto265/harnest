package agentrunner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
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
