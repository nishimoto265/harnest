package main

import (
	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func realTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return real
}
func writeTestConfig(t *testing.T, root, runsBase, worktreeBase string) {
	t.Helper()
	configPath := filepath.Join(root, "config.yaml")
	content := "repo:\n" +
		"  root: " + root + "\n" +
		"  default_branch: main\n" +
		"  best_branch: harnest/best\n" +
		"paths:\n" +
		"  runs: " + runsBase + "\n" +
		"worktree:\n" +
		"  base: " + worktreeBase + "\n"
	require.NoError(t, os.WriteFile(configPath, []byte(content), 0o644))
}
func mustNewRunCtx(t *testing.T, runID contracts.RunID, runsBase, worktreeBase string) internalio.RunContext {
	t.Helper()
	ctx, err := internalio.NewRunContext(runID, runsBase, worktreeBase)
	require.NoError(t, err)
	return ctx
}
