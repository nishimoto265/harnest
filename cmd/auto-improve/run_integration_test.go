package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationRunWithPreflightSubprocess(t *testing.T) {
	requireIntegrationEnv(t)

	env := newCLIIntegrationEnv(t, 0)
	ensureIntegrationRepoGitHubConfig(t, filepath.Join(env.root, "config.yaml"), "owner/repo")
	installPreflightRuntimeTools(t, env.binDir)
	bin := buildIntegrationBinary(t)

	cmd := exec.Command(bin, "run", "--pr", "43", "--with-preflight")
	cmd.Dir = env.root
	cmd.Env = env.env
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Run(), "stdout=%s stderr=%s", stdout.String(), stderr.String())

	namespacedRuns := cliIntegrationEnv{runsBase: filepath.Join(env.root, "owner__repo", "runs")}
	runDirs := namespacedRuns.runDirs(t)
	require.Len(t, runDirs, 1)
	assert.FileExists(t, filepath.Join(runDirs[0], "70", "decision.json"))
}

func TestIntegrationRunFailsClosedOnBrokenPolicyBranch(t *testing.T) {
	requireIntegrationEnv(t)

	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(filepath.Join(runsBase, "rules"), 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))

	writeTestConfig(t, root, runsBase, worktreeBase)
	localRule := "# Local fallback rule\n\nbody\n"
	localRegistry := "{\"kind\":\"added\",\"schema_version\":\"1\",\"rule_id\":\"r-local\",\"rule_path\":\"rules/r-local.md\",\"sha256\":\"" + sha256String(localRule) + "\",\"idempotency_key\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"version_seq\":1,\"prev_hash\":\"\",\"by_run_id\":\"2026-04-23-PR1-feedbee\",\"at\":\"2026-04-23T08:00:00Z\"}\n"
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "rules-registry.jsonl"), []byte(localRegistry), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "rules", "r-local.md"), []byte(localRule), 0o644))
	appendPolicyBranchConfig(t, filepath.Join(root, "config.yaml"), "auto-improve/policy")

	bin := buildIntegrationBinary(t)
	binDir := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	copyExecutable(t, filepath.Join(mustRepoRoot(t), "internal", "orchestrator", "testdata", "bin", "gh"), filepath.Join(binDir, "gh"))
	writeExecutable(t, filepath.Join(binDir, "git"), brokenPolicyGitScript())
	claudePath := filepath.Join(binDir, "claude")
	writeExecutable(t, claudePath, fakeClaudeScript(0))
	writeIntegrationAgentsConfig(t, root, claudePath)

	cmd := exec.Command(bin, "run", "--pr", "1")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		integrationTrustedPathEnvVar+"="+trustedPathWithFakeBin(binDir),
		"AUTO_IMPROVE_TEST_BASE_SHA="+strings.Repeat("a", 40),
		"AUTO_IMPROVE_TEST_TARGET_SHA="+strings.Repeat("b", 40),
		"AUTO_IMPROVE_TEST_MERGE_SHA="+strings.Repeat("c", 40),
		"AUTO_IMPROVE_TEST_BEST_SHA="+strings.Repeat("d", 40),
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	require.Error(t, err)
	assert.Contains(t, stdout.String()+stderr.String(), "active rule body sha mismatch")

	registryBytes, readErr := os.ReadFile(filepath.Join(runsBase, "rules-registry.jsonl"))
	require.NoError(t, readErr)
	assert.Equal(t, localRegistry, string(registryBytes))
	ruleBytes, readErr := os.ReadFile(filepath.Join(runsBase, "rules", "r-local.md"))
	require.NoError(t, readErr)
	assert.Equal(t, localRule, string(ruleBytes))
}
