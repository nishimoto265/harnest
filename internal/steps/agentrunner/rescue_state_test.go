package agentrunner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeDirtyStateDetectsTrackedContentDrift(t *testing.T) {
	repoDir := initDirtyStateTestRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("changed one\n"), 0o644))
	first, _, err := ComputeDirtyState(context.Background(), repoDir)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("changed two\n"), 0o644))
	second, _, err := ComputeDirtyState(context.Background(), repoDir)
	require.NoError(t, err)

	assert.NotEqual(t, first, second)
}

func TestComputeDirtyStateDetectsUntrackedContentDrift(t *testing.T) {
	repoDir := initDirtyStateTestRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "scratch.txt"), []byte("one\n"), 0o644))
	first, _, err := ComputeDirtyState(context.Background(), repoDir)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "scratch.txt"), []byte("two\n"), 0o644))
	second, _, err := ComputeDirtyState(context.Background(), repoDir)
	require.NoError(t, err)

	assert.NotEqual(t, first, second)
}

func TestComputeDirtyStateDetectsIgnoredContentDrift(t *testing.T) {
	repoDir := initDirtyStateTestRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, ".gitignore"), []byte(".env.local\n"), 0o644))
	runGit(t, repoDir, "git", "add", ".gitignore")
	runGit(t, repoDir, "git", "commit", "-m", "ignore env")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, ".env.local"), []byte("one\n"), 0o644))
	first, _, err := ComputeDirtyState(context.Background(), repoDir)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(repoDir, ".env.local"), []byte("two\n"), 0o644))
	second, _, err := ComputeDirtyState(context.Background(), repoDir)
	require.NoError(t, err)

	assert.NotEqual(t, first, second)
}

func TestComputeDirtyStateHandlesOversizedUntrackedWithoutHashingContent(t *testing.T) {
	repoDir := initDirtyStateTestRepo(t)
	hugePath := filepath.Join(repoDir, "huge.bin")
	file, err := os.Create(hugePath)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(RescueDiffLimitBytes+1))
	require.NoError(t, file.Close())

	first, _, err := ComputeDirtyState(context.Background(), repoDir)
	require.NoError(t, err)

	file, err = os.OpenFile(hugePath, os.O_WRONLY, 0)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(RescueDiffLimitBytes+2))
	require.NoError(t, file.Close())
	second, _, err := ComputeDirtyState(context.Background(), repoDir)
	require.NoError(t, err)

	assert.NotEqual(t, first, second)
}

func initDirtyStateTestRepo(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	runGit(t, "", "git", "init", "-b", "main", repoDir)
	runGit(t, repoDir, "git", "config", "user.email", "test@example.com")
	runGit(t, repoDir, "git", "config", "user.name", "Agent Runner Test")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644))
	runGit(t, repoDir, "git", "add", "README.md")
	runGit(t, repoDir, "git", "commit", "-m", "base")
	return repoDir
}
