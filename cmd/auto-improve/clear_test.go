package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClearCommandArchivesRepoStateAndRemovesRegistration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AUTO_IMPROVE_HOME", home)
	seedClearRepoState(t, home)
	require.NoError(t, writeRepositoryRegistration(home, repoRegistration{
		Slug:          "owner/repo",
		URL:           "https://github.com/owner/repo",
		Root:          filepath.Join(home, "repos", "owner", "repo"),
		DefaultBranch: "main",
		RunsBase:      filepath.Join(home, "runs", "owner__repo", "runs"),
		WorktreeBase:  filepath.Join(home, "worktrees", "owner__repo", "worktrees"),
		UpdatedAt:     time.Now().UTC(),
	}))

	var stdout bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"clear", "https://github.com/owner/repo"})

	require.NoError(t, cmd.Execute())
	assert.NoDirExists(t, filepath.Join(home, "repos", "owner", "repo"))
	assert.NoDirExists(t, filepath.Join(home, "runs", "owner__repo"))
	assert.NoDirExists(t, filepath.Join(home, "worktrees", "owner__repo"))

	archive := singleClearArchive(t, home, "owner__repo")
	assert.FileExists(t, filepath.Join(archive, "repo", "README.md"))
	assert.FileExists(t, filepath.Join(archive, "runs", "runs", "processed.jsonl"))
	assert.FileExists(t, filepath.Join(archive, "worktrees", "worktrees", "marker"))
	assert.FileExists(t, filepath.Join(archive, "clear-metadata.json"))
	assert.FileExists(t, filepath.Join(archive, "repositories.removed.yaml"))

	registrations, err := readRepositoryRegistrations(home)
	require.NoError(t, err)
	assert.Empty(t, registrations)
	assert.Contains(t, stdout.String(), "harnest clear: archived generated state")
}

func TestClearCommandDryRunDoesNotMoveState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AUTO_IMPROVE_HOME", home)
	seedClearRepoState(t, home)

	var stdout bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"clear", "https://github.com/owner/repo", "--dry-run"})

	require.NoError(t, cmd.Execute())
	assert.DirExists(t, filepath.Join(home, "repos", "owner", "repo"))
	assert.DirExists(t, filepath.Join(home, "runs", "owner__repo"))
	assert.DirExists(t, filepath.Join(home, "worktrees", "owner__repo"))
	assert.NoDirExists(t, filepath.Join(home, "archives"))
	assert.Contains(t, stdout.String(), "harnest clear: dry run")
	assert.Contains(t, stdout.String(), "would archive")
}

func TestClearCommandAllArchivesGeneratedHomeState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AUTO_IMPROVE_HOME", home)
	seedClearRepoState(t, home)
	require.NoError(t, writeRepositoryRegistration(home, repoRegistration{
		Slug:          "owner/repo",
		URL:           "https://github.com/owner/repo",
		Root:          filepath.Join(home, "repos", "owner", "repo"),
		DefaultBranch: "main",
		RunsBase:      filepath.Join(home, "runs", "owner__repo", "runs"),
		WorktreeBase:  filepath.Join(home, "worktrees", "owner__repo", "worktrees"),
		UpdatedAt:     time.Now().UTC(),
	}))

	var stdout bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"clear", "--all"})

	require.NoError(t, cmd.Execute())
	assert.NoDirExists(t, filepath.Join(home, "repos"))
	assert.NoDirExists(t, filepath.Join(home, "runs"))
	assert.NoDirExists(t, filepath.Join(home, "worktrees"))
	assert.NoFileExists(t, filepath.Join(home, "repositories.yaml"))

	archive := singleClearAllArchive(t, home)
	assert.FileExists(t, filepath.Join(archive, "repos", "owner", "repo", "README.md"))
	assert.FileExists(t, filepath.Join(archive, "runs", "owner__repo", "runs", "processed.jsonl"))
	assert.FileExists(t, filepath.Join(archive, "worktrees", "owner__repo", "worktrees", "marker"))
	assert.FileExists(t, filepath.Join(archive, "repositories.yaml"))
	assert.FileExists(t, filepath.Join(archive, "clear-metadata.json"))
	assert.Contains(t, stdout.String(), "mode: all")
}

func seedClearRepoState(t *testing.T, home string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(home, "repos", "owner", "repo"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, "repos", "owner", "repo", "README.md"), []byte("repo\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(home, "runs", "owner__repo", "runs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, "runs", "owner__repo", "runs", "processed.jsonl"), []byte("{}\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(home, "worktrees", "owner__repo", "worktrees"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, "worktrees", "owner__repo", "worktrees", "marker"), []byte("worktree\n"), 0o644))
}

func singleClearArchive(t *testing.T, home, namespace string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(home, "archives", "cleared", "*", namespace))
	require.NoError(t, err)
	require.Len(t, matches, 1)
	return matches[0]
}

func singleClearAllArchive(t *testing.T, home string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(home, "archives", "cleared", "*"))
	require.NoError(t, err)
	require.Len(t, matches, 1)
	return matches[0]
}
