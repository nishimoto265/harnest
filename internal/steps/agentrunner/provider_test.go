package agentrunner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/agents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImplementerCommandClaude(t *testing.T) {
	dir := t.TempDir()
	claudePath := filepath.Join(dir, "claude")
	require.NoError(t, os.WriteFile(claudePath, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	binary, args, err := ImplementerCommand(agents.Profile{
		Provider: agents.ProviderClaude,
		Binary:   claudePath,
		Args:     []string{"--print"},
	}, "/tmp/worktree")
	require.NoError(t, err)
	assert.Equal(t, claudePath, binary)
	assert.Equal(t, []string{"--print"}, args)
}

func TestImplementerCommandCodex(t *testing.T) {
	dir := t.TempDir()
	codexPath := filepath.Join(dir, "codex")
	nodePath := filepath.Join(dir, "node")
	require.NoError(t, os.WriteFile(codexPath, []byte("#!/usr/bin/env node\n"), 0o755))
	require.NoError(t, os.WriteFile(nodePath, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	binary, args, err := ImplementerCommand(agents.Profile{
		Provider: agents.ProviderCodex,
		Binary:   codexPath,
		Args:     []string{"--profile", "ci"},
	}, "/tmp/worktree")
	require.NoError(t, err)
	assert.Equal(t, nodePath, binary)
	assert.Equal(t, []string{
		codexPath,
		"exec",
		"--full-auto",
		"--skip-git-repo-check",
		"-C", "/tmp/worktree",
		"--profile", "ci",
		"-",
	}, args)
}

func TestImplementerCommandCodexDangerousBypassRequiresProfileOptIn(t *testing.T) {
	dir := t.TempDir()
	codexPath := filepath.Join(dir, "codex")
	nodePath := filepath.Join(dir, "node")
	require.NoError(t, os.WriteFile(codexPath, []byte("#!/usr/bin/env node\n"), 0o755))
	require.NoError(t, os.WriteFile(nodePath, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	binary, args, err := ImplementerCommand(agents.Profile{
		Provider: agents.ProviderCodex,
		Binary:   codexPath,
		Args:     []string{"--dangerously-bypass-approvals-and-sandbox"},
	}, "/tmp/worktree")
	require.NoError(t, err)
	assert.Equal(t, nodePath, binary)
	assert.Equal(t, []string{
		codexPath,
		"exec",
		"--full-auto",
		"--skip-git-repo-check",
		"-C", "/tmp/worktree",
		"--dangerously-bypass-approvals-and-sandbox",
		"-",
	}, args)
}

func TestPrepareProviderBinary_IgnoresParentPathShadow(t *testing.T) {
	shadowDir := t.TempDir()
	shadowPath := filepath.Join(shadowDir, "shadow-agent")
	require.NoError(t, os.WriteFile(shadowPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", shadowDir)

	binary, args, err := PrepareProviderBinary(agents.ProviderClaude, filepath.Base(shadowPath))

	require.Error(t, err)
	assert.Empty(t, binary)
	assert.Empty(t, args)
	assert.Contains(t, err.Error(), "trusted PATH")
}
