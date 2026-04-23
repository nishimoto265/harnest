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
	binary, args, err := ImplementerCommand(agents.Profile{
		Provider: agents.ProviderClaude,
		Binary:   "claude",
		Args:     []string{"--print"},
	}, "/tmp/worktree")
	require.NoError(t, err)
	assert.Equal(t, "claude", binary)
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
		"--dangerously-bypass-approvals-and-sandbox",
		"--skip-git-repo-check",
		"-C", "/tmp/worktree",
		"--profile", "ci",
		"-",
	}, args)
}
