package judges

import (
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

// TestDefaultRubricPath_EmbeddedMaterializesOutsideRepo verifies the rubric
// resolver survives an "installed binary" scenario where the current working
// directory and the repo source tree are unrelated — runtime.Caller would
// historically point at a path that does not exist at runtime.
func TestDefaultRubricPath_EmbeddedMaterializesOutsideRepo(t *testing.T) {
	dir := t.TempDir()
	SetDefaultRubricDirForTest(dir)
	t.Cleanup(func() { SetDefaultRubricDirForTest("") })

	path, err := DefaultRubricPath()
	require.NoError(t, err)

	assert.True(t, filepath.IsAbs(path), "rubric path must be absolute: %s", path)
	assert.True(t, strings.HasPrefix(path, dir), "rubric path must live under override dir: dir=%s path=%s", dir, path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, defaultRubricContent, data, "materialized rubric must match embedded bytes")
}

// TestDefaultRubricPath_IsIdempotent ensures repeated calls return the same
// content-addressed path and do not rewrite the file when it already matches.
func TestDefaultRubricPath_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	SetDefaultRubricDirForTest(dir)
	t.Cleanup(func() { SetDefaultRubricDirForTest("") })

	first, err := DefaultRubricPath()
	require.NoError(t, err)
	info1, err := os.Stat(first)
	require.NoError(t, err)

	// Touch nothing, invoke again — should return the same path.
	second, err := DefaultRubricPath()
	require.NoError(t, err)
	assert.Equal(t, first, second)

	info2, err := os.Stat(second)
	require.NoError(t, err)
	// Atomic-rename avoidance keeps the on-disk file untouched when content matches.
	assert.Equal(t, info1.ModTime(), info2.ModTime())
}

// TestEmbeddedRubricMatchesRepoCopy ensures the embedded rubric in
// internal/judges/rubrics/default.md stays byte-identical to the canonical
// repository rubric at rubrics/default.md so edits to one do not silently
// bypass the other.
func TestEmbeddedRubricMatchesRepoCopy(t *testing.T) {
	// Walk up from cwd until we find a directory containing rubrics/default.md.
	// Skips cleanly when the test runs outside a repo checkout (installed binary).
	cwd, err := os.Getwd()
	require.NoError(t, err)
	dir := cwd
	for {
		candidate := filepath.Join(dir, "rubrics", "default.md")
		if data, err := os.ReadFile(candidate); err == nil {
			assert.Equal(t, data, defaultRubricContent, "internal/judges/rubrics/default.md drifted from rubrics/default.md")
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("repo-root rubrics/default.md not reachable from cwd; installed-binary layout")
		}
		dir = parent
	}
}

// TestExpectedComplianceRuleIDs_MatchesDefault ensures the fallback rule-id
// set still resolves against the embedded rubric path.
func TestExpectedComplianceRuleIDs_MatchesDefault(t *testing.T) {
	dir := t.TempDir()
	SetDefaultRubricDirForTest(dir)
	t.Cleanup(func() { SetDefaultRubricDirForTest("") })

	path, err := DefaultRubricPath()
	require.NoError(t, err)

	ruleIDs, err := ExpectedComplianceRuleIDs(path)
	require.NoError(t, err)
	assert.Equal(t, []string{stubRuleID}, ruleIDs)
}

func TestResolveRunRubricPath_EmbedsActiveRules(t *testing.T) {
	dir := t.TempDir()
	SetDefaultRubricDirForTest(dir)
	t.Cleanup(func() { SetDefaultRubricDirForTest("") })

	runCtx, err := internalio.NewRunContext("2026-04-23-PR1-deadbee", filepath.Join(dir, "runs"), filepath.Join(dir, "worktrees"))
	require.NoError(t, err)

	ruleBody := []byte("# Companion Rule\n\nWhen a diff changes `app/message.txt`, it must also change `app/details.txt`.\n")
	rulePath := filepath.Join(runCtx.RunsBase, "rules", "r-sync-message-details.md")
	require.NoError(t, internalio.WriteAtomic(rulePath, ruleBody))
	registryEntry := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         "r-sync-message-details",
			RulePath:       "rules/r-sync-message-details.md",
			Sha256:         runtimeRubricSHA256Hex(ruleBody),
			IdempotencyKey: strings.Repeat("a", 64),
			ByRunID:        "2026-04-23-PR1-feedbee",
			At:             time.Date(2026, 4, 23, 7, 0, 0, 0, time.UTC),
			VersionSeq:     1,
		},
	}
	result, err := internalio.AppendRegistryEntry(runCtx.RulesRegistryPath(), registryEntry)
	require.NoError(t, err)
	require.NotEmpty(t, result.Sha256)

	path, err := ResolveRunRubricPath(runCtx)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(path, runCtx.RunDir()))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(data)
	assert.Contains(t, text, "## Active Rule IDs")
	assert.Contains(t, text, "- r-sync-message-details")
	assert.Contains(t, text, "## Active Rules")
	assert.Contains(t, text, "### r-sync-message-details")
	assert.Contains(t, text, "When a diff changes `app/message.txt`, it must also change `app/details.txt`.")

	ruleIDs, err := ExpectedComplianceRuleIDs(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"r-sync-message-details"}, ruleIDs)
}

func TestResolveRunRubricPath_RejectsBrokenRegistryPrevHashChain(t *testing.T) {
	dir := t.TempDir()
	SetDefaultRubricDirForTest(dir)
	t.Cleanup(func() { SetDefaultRubricDirForTest("") })

	runCtx, err := internalio.NewRunContext("2026-04-23-PR1-deadbee", filepath.Join(dir, "runs"), filepath.Join(dir, "worktrees"))
	require.NoError(t, err)

	firstBody := []byte("# First Rule\n")
	require.NoError(t, internalio.WriteAtomic(filepath.Join(runCtx.RunsBase, "rules", "r-first.md"), firstBody))
	_, err = internalio.AppendRegistryEntry(runCtx.RulesRegistryPath(), contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         "r-first",
			RulePath:       "rules/r-first.md",
			Sha256:         runtimeRubricSHA256Hex(firstBody),
			IdempotencyKey: strings.Repeat("1", 64),
			ByRunID:        "2026-04-23-PR1-feedbee",
			At:             time.Date(2026, 4, 23, 7, 0, 0, 0, time.UTC),
			VersionSeq:     1,
		},
	})
	require.NoError(t, err)

	secondBody := []byte("# Second Rule\n")
	require.NoError(t, internalio.WriteAtomic(filepath.Join(runCtx.RunsBase, "rules", "r-second.md"), secondBody))
	err = internalio.AppendJSONL(runCtx.RulesRegistryPath(), contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         "r-second",
			RulePath:       "rules/r-second.md",
			Sha256:         runtimeRubricSHA256Hex(secondBody),
			IdempotencyKey: strings.Repeat("2", 64),
			ByRunID:        "2026-04-23-PR1-feedbee",
			At:             time.Date(2026, 4, 23, 7, 5, 0, 0, time.UTC),
			VersionSeq:     2,
			PrevHash:       strings.Repeat("f", 64),
		},
	})
	require.NoError(t, err)

	path, err := ResolveRunRubricPath(runCtx)
	require.ErrorIs(t, err, internalio.ErrRegistryCASMismatch)
	assert.Empty(t, path)
}

func TestResolveRunRubricPath_PrefersRunPolicySnapshot(t *testing.T) {
	dir := t.TempDir()
	SetDefaultRubricDirForTest(dir)
	t.Cleanup(func() { SetDefaultRubricDirForTest("") })

	runCtx, err := internalio.NewRunContext("2026-04-23-PR1-deadbee", filepath.Join(dir, "runs"), filepath.Join(dir, "worktrees"))
	require.NoError(t, err)

	globalBody := []byte("# Global Rule\n")
	require.NoError(t, internalio.WriteAtomic(filepath.Join(runCtx.RunsBase, "rules", "r-global.md"), globalBody))
	_, err = internalio.AppendRegistryEntry(runCtx.RulesRegistryPath(), contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         "r-global",
			RulePath:       "rules/r-global.md",
			Sha256:         runtimeRubricSHA256Hex(globalBody),
			IdempotencyKey: strings.Repeat("a", 64),
			ByRunID:        "2026-04-23-PR1-feedbee",
			At:             time.Date(2026, 4, 23, 7, 0, 0, 0, time.UTC),
			VersionSeq:     1,
		},
	})
	require.NoError(t, err)

	snapshotBody := []byte("# Snapshot Rule\n")
	require.NoError(t, internalio.WriteAtomic(filepath.Join(runCtx.PolicySnapshotRulesDir(), "r-snapshot.md"), snapshotBody))
	_, err = internalio.AppendRegistryEntry(runCtx.PolicySnapshotRegistryPath(), contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         "r-snapshot",
			RulePath:       "rules/r-snapshot.md",
			Sha256:         runtimeRubricSHA256Hex(snapshotBody),
			IdempotencyKey: strings.Repeat("b", 64),
			ByRunID:        "2026-04-23-PR1-feedbee",
			At:             time.Date(2026, 4, 23, 7, 0, 0, 0, time.UTC),
			VersionSeq:     1,
		},
	})
	require.NoError(t, err)

	path, err := ResolveRunRubricPath(runCtx)
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(data)
	assert.Contains(t, text, "r-snapshot")
	assert.NotContains(t, text, "r-global")
}
