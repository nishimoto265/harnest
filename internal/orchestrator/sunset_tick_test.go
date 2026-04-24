package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunSunsetTick_AppendsLifecycleTransitionFromRegistryState(t *testing.T) {
	runsBase := realSunsetTickTempDir(t, "runs")
	worktreeBase := realSunsetTickTempDir(t, "worktrees")
	chdirWithSunsetTickConfig(t, runsBase, worktreeBase)
	seedSunsetTickDeprecatedRule(t, filepath.Join(runsBase, "rules-registry.jsonl"), "rule-1")

	require.NoError(t, RunSunsetTick(context.Background()))

	lines, err := internalio.RegistryLines(filepath.Join(runsBase, "rules-registry.jsonl"))
	require.NoError(t, err)
	require.Len(t, lines, 3)
	transition, ok := lines[2].Entry.Value.(contracts.RuleRegistryArchived)
	require.True(t, ok)
	assert.Equal(t, "rule-1", transition.RuleID)
	assert.Equal(t, contracts.RuleStatusDeprecated, transition.PrevStatus)
	assert.Equal(t, contracts.RuleStatusArchived, transition.NewStatus)
	assert.FileExists(t, filepath.Join(runsBase, "last-sunset-at"))
}

func TestRunSunsetTick_NoPlanDoesNotAdvanceGate(t *testing.T) {
	runsBase := realSunsetTickTempDir(t, "runs")
	worktreeBase := realSunsetTickTempDir(t, "worktrees")
	chdirWithSunsetTickConfig(t, runsBase, worktreeBase)

	require.NoError(t, RunSunsetTick(context.Background()))
	assert.NoFileExists(t, filepath.Join(runsBase, "last-sunset-at"))

	seedSunsetTickDeprecatedRule(t, filepath.Join(runsBase, "rules-registry.jsonl"), "rule-later")
	require.NoError(t, RunSunsetTick(context.Background()))

	lines, err := internalio.RegistryLines(filepath.Join(runsBase, "rules-registry.jsonl"))
	require.NoError(t, err)
	require.Len(t, lines, 3)
	assert.FileExists(t, filepath.Join(runsBase, "last-sunset-at"))
}

func chdirWithSunsetTickConfig(t *testing.T, runsBase, worktreeBase string) {
	t.Helper()
	dir := t.TempDir()
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })
	config := fmt.Sprintf("paths:\n  runs: %q\nworktree:\n  base: %q\n", runsBase, worktreeBase)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(config), 0o644))
}

func realSunsetTickTempDir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	real, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return real
}

func seedSunsetTickDeprecatedRule(t *testing.T, registryPath, ruleID string) {
	t.Helper()
	added := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         ruleID,
			RulePath:       "rules/" + ruleID + ".md",
			Sha256:         strings.Repeat("1", 64),
			IdempotencyKey: strings.Repeat("a", 64),
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        "2026-04-21-PR1-abcdef0",
			At:             time.Date(2026, 4, 21, 8, 0, 0, 0, time.UTC),
		},
	}
	result, err := internalio.AppendRegistryEntry(registryPath, added)
	require.NoError(t, err)
	deprecated := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindStatusChanged,
		Value: contracts.RuleRegistryStatusChanged{
			Kind:          contracts.RegistryKindStatusChanged,
			SchemaVersion: "1",
			RuleID:        ruleID,
			PrevStatus:    contracts.RuleStatusActive,
			NewStatus:     contracts.RuleStatusDeprecated,
			Transition:    contracts.SunsetTransitionDeprecate,
			OpID:          strings.Repeat("b", 64),
			VersionSeq:    2,
			PrevHash:      result.Sha256,
			BySunsetRunID: "seed-sunset",
			At:            time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
		},
	}
	_, err = internalio.AppendRegistryEntry(registryPath, deprecated)
	require.NoError(t, err)
}
