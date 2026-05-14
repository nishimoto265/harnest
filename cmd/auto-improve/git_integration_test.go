package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationFakeGitFixtureCreatesPlainDirectoriesNotWorktrees(t *testing.T) {
	requireIntegrationEnv(t)

	root := realTempDir(t)
	binDir := filepath.Join(root, "bin")
	stateDir := filepath.Join(root, "git-state")
	worktreePath := filepath.Join(root, "worktrees", "fake-pass1-a1")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(worktreePath), 0o755))
	copyExecutable(t, filepath.Join(mustRepoRoot(t), "internal", "orchestrator", "testdata", "bin", "git"), filepath.Join(binDir, "git"))

	cmd := exec.Command(filepath.Join(binDir, "git"), "-C", root, "worktree", "add", "-b", "auto-improve/fake/pass1/a1", worktreePath, strings.Repeat("a", 40))
	cmd.Env = append(os.Environ(), "AUTO_IMPROVE_GIT_STATE_DIR="+stateDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	assert.DirExists(t, worktreePath)
	assert.NoFileExists(t, filepath.Join(worktreePath, ".git"), "fake git records paths but does not create real git worktrees")

	removeCmd := exec.Command(filepath.Join(binDir, "git"), "-C", root, "worktree", "remove", "--force", worktreePath)
	removeCmd.Env = append(os.Environ(), "AUTO_IMPROVE_GIT_STATE_DIR="+stateDir)
	removeOut, removeErr := removeCmd.CombinedOutput()
	require.NoError(t, removeErr, string(removeOut))
	assert.NoDirExists(t, worktreePath)

	listCmd := exec.Command(filepath.Join(binDir, "git"), "-C", root, "worktree", "list", "--porcelain")
	listCmd.Env = append(os.Environ(), "AUTO_IMPROVE_GIT_STATE_DIR="+stateDir)
	listOut, listErr := listCmd.CombinedOutput()
	require.NoError(t, listErr, string(listOut))
	assert.NotContains(t, string(listOut), worktreePath)

	realGit := exec.Command("git", "-C", worktreePath, "rev-parse", "--is-inside-work-tree")
	realOut, realErr := realGit.CombinedOutput()
	require.Error(t, realErr, string(realOut))
}

func TestIntegrationRunUsesRealGitWorktreesWhenFakeGitIsAbsent(t *testing.T) {
	requireIntegrationEnv(t)

	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	repoRoot := filepath.Join(root, "repo")
	binDir := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	require.NoError(t, os.MkdirAll(repoRoot, 0o755))
	require.NoError(t, os.MkdirAll(binDir, 0o755))

	shas := seedRealGitIntegrationRepo(t, repoRoot)
	writeIntegrationConfigForRepo(t, root, repoRoot, runsBase, worktreeBase)
	copyExecutable(t, filepath.Join(mustRepoRoot(t), "internal", "orchestrator", "testdata", "bin", "gh"), filepath.Join(binDir, "gh"))
	claudePath := filepath.Join(binDir, "claude")
	writeExecutable(t, claudePath, fakeClaudeScript(0))
	writeIntegrationAgentsConfig(t, root, claudePath)

	bin := buildIntegrationBinary(t)
	cmd := exec.Command(bin, "run", "--pr", "77")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		integrationTrustedPathEnvVar+"="+trustedPathWithFakeBin(binDir),
		"AUTO_IMPROVE_TEST_BASE_SHA="+shas.base,
		"AUTO_IMPROVE_TEST_TARGET_SHA="+shas.head,
		"AUTO_IMPROVE_TEST_MERGE_SHA="+shas.merge,
		"AUTO_IMPROVE_TEST_BEST_SHA="+shas.base,
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Run(), "stdout=%s stderr=%s", stdout.String(), stderr.String())

	runDirs := cliIntegrationEnv{runsBase: runsBase}.runDirs(t)
	require.Len(t, runDirs, 1)
	pkg, err := internalio.ReadJSON[contracts.TaskPackage](filepath.Join(runDirs[0], "task-package.json"))
	require.NoError(t, err)
	require.NotEmpty(t, pkg.Worktrees)
	for _, worktree := range pkg.Worktrees {
		assert.NoDirExists(t, worktree.Path, "step70 cleanup should remove real git worktrees")
		if worktree.Pass != 1 {
			continue
		}
		manifest := readIntegrationManifest(t, runDirs[0], worktree.Pass, worktree.Agent)
		assert.Equal(t, "commit", strings.TrimSpace(runIntegrationGit(t, repoRoot, "cat-file", "-t", manifest.BaseSHA)))
		runIntegrationGit(t, repoRoot, "merge-base", "--is-ancestor", shas.base, manifest.BaseSHA)
		runIntegrationGit(t, repoRoot, "merge-base", "--is-ancestor", manifest.BaseSHA, manifest.HeadSHA)
		assert.Equal(t, "commit", strings.TrimSpace(runIntegrationGit(t, repoRoot, "cat-file", "-t", manifest.HeadSHA)))
	}
	worktreeList := runIntegrationGit(t, repoRoot, "worktree", "list", "--porcelain")
	for _, worktree := range pkg.Worktrees {
		assert.NotContains(t, worktreeList, worktree.Path)
	}
	assert.FileExists(t, filepath.Join(runDirs[0], "70", "decision.json"))
}

func TestIntegrationRunAdoptsWithRealGitWorktreesAndFakeCLIs(t *testing.T) {
	requireIntegrationEnv(t)

	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	repoRoot := filepath.Join(root, "repo")
	binDir := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	require.NoError(t, os.MkdirAll(repoRoot, 0o755))
	require.NoError(t, os.MkdirAll(binDir, 0o755))

	shas := seedRealGitIntegrationRepo(t, repoRoot)
	seedIntegrationPolicyBranch(t, repoRoot, "auto-improve/policy")
	writeIntegrationConfigForRepo(t, root, repoRoot, runsBase, worktreeBase)
	ensureIntegrationRepoGitHubConfig(t, filepath.Join(root, "config.yaml"), "owner/repo")
	appendPolicyBranchConfig(t, filepath.Join(root, "config.yaml"), "auto-improve/policy")
	copyExecutable(t, filepath.Join(mustRepoRoot(t), "internal", "orchestrator", "testdata", "bin", "gh"), filepath.Join(binDir, "gh"))
	installPreflightRuntimeToolsWithoutGit(t, binDir)
	implementerPath := filepath.Join(binDir, "fake-implementer")
	judgePath := filepath.Join(binDir, "fake-judge")
	writeExecutable(t, implementerPath, fakeAdoptImplementerScript())
	writeExecutable(t, judgePath, fakeAdoptJudgeScript())
	writeIntegrationAdoptAgentsConfig(t, root, implementerPath, judgePath)

	bin := buildIntegrationBinary(t)
	cmd := exec.Command(bin, "run", "--pr", "77", "--with-preflight")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		integrationTrustedPathEnvVar+"="+trustedPathWithFakeBin(binDir),
		"AUTO_IMPROVE_TEST_BASE_SHA="+shas.base,
		"AUTO_IMPROVE_TEST_TARGET_SHA="+shas.head,
		"AUTO_IMPROVE_TEST_MERGE_SHA="+shas.merge,
		"AUTO_IMPROVE_TEST_BEST_SHA="+shas.base,
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Run(), "stdout=%s stderr=%s", stdout.String(), stderr.String())

	effectiveRunsBase := filepath.Join(root, "owner__repo", "runs")
	effectiveWorktreeBase := filepath.Join(root, "owner__repo", "worktrees")
	runDirs := cliIntegrationEnv{runsBase: effectiveRunsBase}.runDirs(t)
	require.Len(t, runDirs, 1)
	runDir := runDirs[0]
	pkg, err := internalio.ReadJSON[contracts.TaskPackage](filepath.Join(runDir, "task-package.json"))
	require.NoError(t, err)
	candidates, err := internalio.ReadJSON[contracts.Candidates](filepath.Join(runDir, "40", "candidates.json"))
	require.NoError(t, err)
	require.NotEmpty(t, candidates.Candidates)
	decision, err := internalio.ReadJSON[contracts.Decision](filepath.Join(runDir, "70", "decision.json"))
	require.NoError(t, err)
	require.Equal(t, contracts.DecisionActionAdopt, decision.Action)
	adopt, ok := decision.Value.(contracts.DecisionAdopt)
	require.True(t, ok)
	require.True(t, adopt.PolicyOnly)

	winnerManifest := readIntegrationManifest(t, runDir, 2, "a1")
	assert.NotEqual(t, winnerManifest.HeadSHA, adopt.TargetSha)
	assert.Equal(t, shas.base, adopt.TargetSha)
	remoteBestHead := strings.Fields(runIntegrationGit(t, repoRoot, "ls-remote", "origin", "auto-improve/best"))[0]
	assert.Equal(t, shas.base, remoteBestHead)

	lines, err := internalio.RegistryLines(filepath.Join(effectiveRunsBase, "rules-registry.jsonl"))
	require.NoError(t, err)
	require.Len(t, lines, 1)
	added, ok := lines[0].Entry.Value.(contracts.RuleRegistryAdded)
	require.True(t, ok)
	assert.Equal(t, pkg.RunID, added.ByRunID)
	assert.NotEmpty(t, added.IdempotencyKey)
	assert.Equal(t, adopt.RegistryAppendResult.Sha256, lines[0].Sha256)
	assert.FileExists(t, filepath.Join(effectiveRunsBase, added.RulePath))
	runIntegrationGit(t, repoRoot, "fetch", "origin", "+refs/heads/auto-improve/policy:refs/remotes/origin/auto-improve/policy")
	policyRegistry := runIntegrationGit(t, repoRoot, "show", "origin/auto-improve/policy:auto-improve/rules-registry.jsonl")
	assert.Equal(t, string(mustReadFile(t, filepath.Join(effectiveRunsBase, "rules-registry.jsonl"))), policyRegistry)
	policyTree := runIntegrationGit(t, repoRoot, "ls-tree", "-r", "--name-only", "origin/auto-improve/policy")
	assert.Contains(t, policyTree, "auto-improve/rules-registry.jsonl")
	assert.Contains(t, policyTree, "auto-improve/"+added.RulePath)
	assert.NotContains(t, policyTree, "app/message.txt")
	firstPublishedRegistry := string(mustReadFile(t, filepath.Join(effectiveRunsBase, "rules-registry.jsonl")))

	events, err := state.ScanEventsForRun(mustNewRunCtx(t, pkg.RunID, effectiveRunsBase, effectiveWorktreeBase), pkg.RunID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindPromoted, events[len(events)-1].Kind)
	for _, worktree := range pkg.Worktrees {
		assert.NoDirExists(t, worktree.Path, "step70 cleanup should remove adopted real git worktrees")
	}

	cmd2 := exec.Command(bin, "run", "--pr", "78", "--with-preflight")
	cmd2.Dir = root
	cmd2.Env = cmd.Env
	stdout.Reset()
	stderr.Reset()
	cmd2.Stdout = &stdout
	cmd2.Stderr = &stderr
	require.NoError(t, cmd2.Run(), "stdout=%s stderr=%s", stdout.String(), stderr.String())

	secondRunDir := findIntegrationRunDir(t, effectiveRunsBase, "PR78")
	secondSnapshotRegistry := string(mustReadFile(t, filepath.Join(secondRunDir, "policy", "rules-registry.jsonl")))
	assert.Equal(t, firstPublishedRegistry, secondSnapshotRegistry)
	secondDecision, err := internalio.ReadJSON[contracts.Decision](filepath.Join(secondRunDir, "70", "decision.json"))
	require.NoError(t, err)
	assert.Equal(t, contracts.DecisionActionNoop, secondDecision.Action)
	secondLines, err := internalio.RegistryLines(filepath.Join(effectiveRunsBase, "rules-registry.jsonl"))
	require.NoError(t, err)
	require.Len(t, secondLines, 1)
	runIntegrationGit(t, repoRoot, "fetch", "origin", "+refs/heads/auto-improve/policy:refs/remotes/origin/auto-improve/policy")
	secondPolicyRegistry := runIntegrationGit(t, repoRoot, "show", "origin/auto-improve/policy:auto-improve/rules-registry.jsonl")
	assert.Equal(t, string(mustReadFile(t, filepath.Join(effectiveRunsBase, "rules-registry.jsonl"))), secondPolicyRegistry)
	remoteBestHead = strings.Fields(runIntegrationGit(t, repoRoot, "ls-remote", "origin", "auto-improve/best"))[0]
	assert.Equal(t, shas.base, remoteBestHead)
}
