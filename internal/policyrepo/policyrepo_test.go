package policyrepo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHydrateFromBranchCopiesPolicyFilesToRunsBase(t *testing.T) {
	repoRoot := newClonedRepoWithPolicyBranch(t)
	runsBase := filepath.Join(t.TempDir(), "runs")
	require.NoError(t, os.MkdirAll(filepath.Join(runsBase, "rules"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, registryLocalName), []byte("stale\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, rulesLocalDirName, "stale.md"), []byte("stale\n"), 0o644))

	require.NoError(t, HydrateFromBranch(context.Background(), repoRoot, "policy", runsBase))

	registryBytes, err := os.ReadFile(filepath.Join(runsBase, registryLocalName))
	require.NoError(t, err)
	assert.Contains(t, string(registryBytes), `"rule_id":"r-sync-message-details"`)

	ruleBytes, err := os.ReadFile(filepath.Join(runsBase, rulesLocalDirName, "r-sync-message-details.md"))
	require.NoError(t, err)
	assert.Contains(t, string(ruleBytes), "Sync companion files")
	assert.NoFileExists(t, filepath.Join(runsBase, rulesLocalDirName, "stale.md"))
}

func TestHydrateFromBranchFetchesAndLoadsWhilePromotionLockHeld(t *testing.T) {
	repoRoot := newClonedRepoWithPolicyBranch(t)
	runsBase := filepath.Join(t.TempDir(), "runs")
	lockPath := filepath.Join(runsBase, "promotion.lock")
	originalRunGit := runGit
	checked := 0
	runGit = func(ctx context.Context, env []string, args ...string) ([]byte, error) {
		for _, arg := range args {
			if arg == "fetch" || arg == "ls-tree" || arg == "show" {
				checked++
				assert.True(t, internalio.IsFileLockHeld(lockPath), "policy snapshot git read %q must run under promotion.lock", arg)
				break
			}
		}
		return originalRunGit(ctx, env, args...)
	}
	t.Cleanup(func() {
		runGit = originalRunGit
	})

	require.NoError(t, HydrateFromBranch(context.Background(), repoRoot, "policy", runsBase))
	assert.GreaterOrEqual(t, checked, 3)
}

func TestHydrateAndSnapshotFromBranchCopiesPolicyFilesToRunDir(t *testing.T) {
	repoRoot := newClonedRepoWithPolicyBranch(t)
	runsBase := filepath.Join(t.TempDir(), "runs")
	runID := contracts.RunID("2026-04-23-PR2-feedbee")
	runCtx, err := internalio.NewRunContext(runID, runsBase, filepath.Join(t.TempDir(), "worktrees"))
	require.NoError(t, err)
	runDir := runCtx.RunDir()
	require.NoError(t, os.MkdirAll(runDir, 0o755))

	require.NoError(t, HydrateAndSnapshotFromBranch(context.Background(), repoRoot, "policy", runsBase, runDir))

	registryBytes, err := os.ReadFile(filepath.Join(runDir, "policy", registryLocalName))
	require.NoError(t, err)
	assert.Contains(t, string(registryBytes), `"rule_id":"r-sync-message-details"`)
	ruleBytes, err := os.ReadFile(filepath.Join(runDir, "policy", rulesLocalDirName, "r-sync-message-details.md"))
	require.NoError(t, err)
	assert.Contains(t, string(ruleBytes), "Sync companion files")
	meta, ok, err := LoadSnapshotMetadata(runCtx)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "policy", meta.PolicyBranch)
	assert.NotEmpty(t, meta.PolicyHead)
	assert.NotEmpty(t, meta.RegistryHead)

	require.NoError(t, os.WriteFile(filepath.Join(runsBase, rulesLocalDirName, "r-sync-message-details.md"), []byte("stale global body\n"), 0o644))
	active, err := LoadActiveRulesForRun(runCtx)
	require.NoError(t, err)
	require.Len(t, active, 1)
	assert.Equal(t, "r-sync-message-details", active[0].RuleID)
	assert.Contains(t, active[0].Body, "Sync companion files")
	assert.NotContains(t, active[0].Body, "stale global body")
}

func TestPublishSnapshotPushesRunsBasePolicyToBranch(t *testing.T) {
	repoRoot := newClonedRepoWithPolicyBranch(t)
	runsBase := filepath.Join(t.TempDir(), "runs")
	require.NoError(t, os.MkdirAll(filepath.Join(runsBase, rulesLocalDirName), 0o755))
	const updatedRule = "# Updated rule\n\nnew body\n"
	registry := "{\"kind\":\"added\",\"schema_version\":\"1\",\"rule_id\":\"r-sync-message-details\",\"rule_path\":\"rules/r-sync-message-details.md\",\"sha256\":\"" + sha256Hex([]byte(updatedRule)) + "\",\"idempotency_key\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"version_seq\":1,\"prev_hash\":\"\",\"by_run_id\":\"2026-04-23-PR1-feedbee\",\"at\":\"2026-04-23T08:00:00Z\"}\n"
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, registryLocalName), []byte(registry), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, rulesLocalDirName, "r-sync-message-details.md"), []byte(updatedRule), 0o644))

	headBefore := strings.TrimSpace(string(mustGitOutput(t, repoRoot, "rev-parse", "origin/policy")))
	newHead, err := PublishSnapshot(context.Background(), repoRoot, "policy", headBefore, runsBase, "2026-04-23-PR2-adopt")
	require.NoError(t, err)
	assert.NotEqual(t, headBefore, newHead)

	mustGit(t, repoRoot, "fetch", "--no-tags", "origin", "policy")
	body := string(mustGitOutput(t, repoRoot, "show", "origin/policy:"+RulesRepoDirRelPath+"/r-sync-message-details.md"))
	assert.Contains(t, body, "# Updated rule")
}

func TestHydrateFromBranchKeepsPreviousLocalPolicyWhenRemoteSnapshotIsInvalid(t *testing.T) {
	repoRoot := newClonedRepoWithPolicyBranch(t)
	runsBase := filepath.Join(t.TempDir(), "runs")
	require.NoError(t, os.MkdirAll(filepath.Join(runsBase, rulesLocalDirName), 0o755))
	const localRule = "# Local rule\n\nbody\n"
	localRegistry := "{\"kind\":\"added\",\"schema_version\":\"1\",\"rule_id\":\"r-local\",\"rule_path\":\"rules/r-local.md\",\"sha256\":\"" + sha256Hex([]byte(localRule)) + "\",\"idempotency_key\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"version_seq\":1,\"prev_hash\":\"\",\"by_run_id\":\"2026-04-23-PR1-feedbee\",\"at\":\"2026-04-23T08:00:00Z\"}\n"
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, registryLocalName), []byte(localRegistry), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, rulesLocalDirName, "r-local.md"), []byte(localRule), 0o644))

	mustGit(t, repoRoot, "checkout", "policy")
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, RepoDirName, "rules-registry.jsonl"), []byte("{\"kind\":\"added\",\"schema_version\":\"1\",\"rule_id\":\"r-bad\",\"rule_path\":\"rules/r-bad.md\",\"sha256\":\"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff\",\"idempotency_key\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"version_seq\":1,\"prev_hash\":\"\",\"by_run_id\":\"2026-04-23-PR1-feedbee\",\"at\":\"2026-04-23T08:00:00Z\"}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, RepoDirName, "rules", "r-bad.md"), []byte("# bad\n"), 0o644))
	mustGit(t, repoRoot, "add", RepoDirName)
	mustGit(t, repoRoot, "commit", "-m", "break policy snapshot")
	mustGit(t, repoRoot, "push", "origin", "policy")

	err := HydrateFromBranch(context.Background(), repoRoot, "policy", runsBase)
	require.Error(t, err)

	registryBytes, readErr := os.ReadFile(filepath.Join(runsBase, registryLocalName))
	require.NoError(t, readErr)
	assert.Equal(t, localRegistry, string(registryBytes))
	ruleBytes, readErr := os.ReadFile(filepath.Join(runsBase, rulesLocalDirName, "r-local.md"))
	require.NoError(t, readErr)
	assert.Equal(t, localRule, string(ruleBytes))
}

func TestHydrateFromBranchRejectsEmptyPolicyBranch(t *testing.T) {
	repoRoot := newClonedRepoWithPolicyBranch(t)
	runsBase := filepath.Join(t.TempDir(), "runs")
	mustGit(t, repoRoot, "checkout", "--orphan", "empty-policy")
	mustGit(t, repoRoot, "rm", "-rf", ".")
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, ".gitkeep"), []byte("\n"), 0o644))
	mustGit(t, repoRoot, "add", ".gitkeep")
	mustGit(t, repoRoot, "commit", "-m", "empty policy")
	mustGit(t, repoRoot, "push", "origin", "empty-policy")

	err := HydrateFromBranch(context.Background(), repoRoot, "empty-policy", runsBase)
	require.Error(t, err)
	assert.ErrorContains(t, err, "no managed policy files")
}

func TestPublishSnapshotRejectsMissingLocalRegistry(t *testing.T) {
	repoRoot := newClonedRepoWithPolicyBranch(t)
	runsBase := filepath.Join(t.TempDir(), "runs")
	require.NoError(t, os.MkdirAll(filepath.Join(runsBase, rulesLocalDirName), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, rulesLocalDirName, "r-sync-message-details.md"), []byte("# orphan\n"), 0o644))
	headBefore := strings.TrimSpace(string(mustGitOutput(t, repoRoot, "rev-parse", "origin/policy")))

	_, err := PublishSnapshot(context.Background(), repoRoot, "policy", headBefore, runsBase, "2026-04-23-PR2-adopt")
	require.Error(t, err)
}

func TestPreparedPublishCleanupCanRetryAfterFailure(t *testing.T) {
	originalRemove := removePreparedPublishWorktree
	calls := 0
	removePreparedPublishWorktree = func(repoRoot, path string) error {
		calls++
		if calls == 1 {
			return errors.New("transient cleanup failure")
		}
		return nil
	}
	t.Cleanup(func() {
		removePreparedPublishWorktree = originalRemove
	})

	worktreeDir := filepath.Join(t.TempDir(), "policy-worktree")
	require.NoError(t, os.MkdirAll(worktreeDir, 0o755))
	plan := &PreparedPublish{
		RepoRoot:    "repo",
		worktreeDir: worktreeDir,
	}

	require.Error(t, plan.Cleanup())
	assert.False(t, plan.cleaned)
	require.NoError(t, plan.Cleanup())
	assert.True(t, plan.cleaned)
	assert.Equal(t, 2, calls)
}

func TestLoadLocalSnapshotAllowsRegistryOnlyWithNoActiveRules(t *testing.T) {
	runsBase := filepath.Join(t.TempDir(), "runs")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	entry := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindArchived,
		Value: contracts.RuleRegistryArchived{
			Kind:          contracts.RegistryKindArchived,
			SchemaVersion: "1",
			RuleID:        "r-archived",
			PrevStatus:    contracts.RuleStatusActive,
			NewStatus:     contracts.RuleStatusArchived,
			OpID:          strings.Repeat("a", 64),
			VersionSeq:    1,
			PrevHash:      "",
			BySunsetRunID: "sunset-2026-04-23",
			At:            mustTime("2026-04-23T08:00:00Z"),
		},
	}
	payload, err := contracts.CanonicalMarshal(entry)
	require.NoError(t, err)
	registry := string(payload) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, registryLocalName), []byte(registry), 0o644))

	snap, err := loadLocalSnapshot(runsBase)
	require.NoError(t, err)
	assert.Equal(t, registry, string(snap.registry))
	assert.Empty(t, snap.rules)
}

func mustTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func newClonedRepoWithPolicyBranch(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")
	mustGit(t, root, "init", "--bare", origin)
	mustGit(t, root, "clone", origin, work)
	mustGit(t, work, "config", "user.name", "Test User")
	mustGit(t, work, "config", "user.email", "test@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(work, "README.md"), []byte("# repo\n"), 0o644))
	mustGit(t, work, "add", "README.md")
	mustGit(t, work, "commit", "-m", "initial")
	mustGit(t, work, "push", "origin", "HEAD:main")
	mustGit(t, work, "checkout", "--orphan", "policy")
	mustGit(t, work, "rm", "-rf", ".")
	require.NoError(t, os.MkdirAll(filepath.Join(work, RepoDirName, "rules"), 0o755))
	const seedRule = "# Sync companion files\n\nWhen a diff changes `app/message.txt`, it must also change `app/details.txt`.\n"
	registry := "{\"kind\":\"added\",\"schema_version\":\"1\",\"rule_id\":\"r-sync-message-details\",\"rule_path\":\"rules/r-sync-message-details.md\",\"sha256\":\"" + sha256Hex([]byte(seedRule)) + "\",\"idempotency_key\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"version_seq\":1,\"prev_hash\":\"\",\"by_run_id\":\"2026-04-23-PR1-feedbee\",\"at\":\"2026-04-23T08:00:00Z\"}\n"
	require.NoError(t, os.WriteFile(filepath.Join(work, RepoDirName, "rules-registry.jsonl"), []byte(registry), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(work, RepoDirName, "rules", "r-sync-message-details.md"), []byte(seedRule), 0o644))
	mustGit(t, work, "add", RepoDirName)
	mustGit(t, work, "commit", "-m", "policy seed")
	mustGit(t, work, "push", "-u", "origin", "policy")
	mustGit(t, work, "fetch", "--no-tags", "origin", "policy")
	return work
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func mustGitOutput(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return out
}
