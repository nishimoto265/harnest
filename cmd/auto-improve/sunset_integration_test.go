package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationSunsetSubprocessArchivesDeprecatedRule(t *testing.T) {
	requireIntegrationEnv(t)

	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	writeTestConfig(t, root, runsBase, worktreeBase)
	seedIntegrationDeprecatedRule(t, filepath.Join(runsBase, "rules-registry.jsonl"), "rule-1")

	bin := buildIntegrationBinary(t)
	cmd := exec.Command(bin, "sunset")
	cmd.Dir = root
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Run(), "stdout=%s stderr=%s", stdout.String(), stderr.String())

	lines, err := internalio.RegistryLines(filepath.Join(runsBase, "rules-registry.jsonl"))
	require.NoError(t, err)
	require.Len(t, lines, 3)
	archived, ok := lines[2].Entry.Value.(contracts.RuleRegistryArchived)
	require.True(t, ok)
	assert.Equal(t, "rule-1", archived.RuleID)
	assert.FileExists(t, filepath.Join(runsBase, "last-sunset-at"))
}
