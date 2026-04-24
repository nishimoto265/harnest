package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	integrationEnvVar            = "AUTO_IMPROVE_INTEGRATION"
	integrationTrustedPathEnvVar = "AUTO_IMPROVE_INTEGRATION_TRUSTED_PATH"
	testTrustedPathSuffix        = "/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/bin:/opt/homebrew/bin"
)

func TestIntegrationConcurrentRunsDifferentPRsSucceed(t *testing.T) {
	requireIntegrationEnv(t)

	env := newCLIIntegrationEnv(t, 1*time.Second)
	bin := buildIntegrationBinary(t)

	cmd41, stdout41, stderr41 := env.newRunCommand(bin, 41)
	cmd42, stdout42, stderr42 := env.newRunCommand(bin, 42)

	require.NoError(t, cmd41.Start())
	require.NoError(t, cmd42.Start())

	require.NoError(t, cmd41.Wait(), "stdout=%s stderr=%s", stdout41.String(), stderr41.String())
	require.NoError(t, cmd42.Wait(), "stdout=%s stderr=%s", stdout42.String(), stderr42.String())

	runDirs := env.runDirs(t)
	require.Len(t, runDirs, 2)
	for _, runDir := range runDirs {
		assert.FileExists(t, filepath.Join(runDir, "70", "decision.json"))
	}
	assert.NoDirExists(t, filepath.Join(env.runsBase, "needs-recovery"))
}

func TestIntegrationConcurrentSamePRSecondFailsWithPRLock(t *testing.T) {
	requireIntegrationEnv(t)

	env := newCLIIntegrationEnv(t, 5*time.Second)
	bin := buildIntegrationBinary(t)

	cmd1, stdout1, stderr1 := env.newRunCommand(bin, 42)
	require.NoError(t, cmd1.Start())
	t.Cleanup(func() {
		if cmd1.ProcessState == nil && cmd1.Process != nil {
			_ = cmd1.Process.Kill()
		}
	})

	waitForPath(t, filepath.Join(env.runsBase, "pr-locks", "pr-42.lock"), 5*time.Second)

	cmd2, stdout2, stderr2 := env.newRunCommand(bin, 42)
	err := cmd2.Run()
	require.Error(t, err)
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Contains(t, stdout2.String()+stderr2.String(), "another process is already running this PR")

	require.NoError(t, cmd1.Wait(), "stdout=%s stderr=%s", stdout1.String(), stderr1.String())
	assert.NotEmpty(t, stdout2.String()+stderr2.String())
}

func TestIntegrationRecoverAdoptAnywaySubprocess(t *testing.T) {
	requireIntegrationEnv(t)

	root, runsBase, worktreeBase, runID := seedRecoverActionRun(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	runDir := filepath.Join(runsBase, string(runID))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))
	candidatesDoc, err := internalio.ReadJSON[contracts.Candidates](filepath.Join(runDir, "40", "candidates.json"))
	require.NoError(t, err)
	candidatesHash := candidatesDoc.CandidatesHash
	intention := seedRecoverIntention(runID, contracts.IntentionStageNeedsManualRecovery, strings.Repeat("a", 40), strings.Repeat("b", 40), candidatesHash)
	intention.RegistryAppendResult = nil
	intention.RecoveryReason = contracts.RollbackReasonTransactionalFailure
	intention.FailedStep = contracts.FailedStep70
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "70", "intention.json"), intention))
	_ = appendRecoverRegistryEntry(t, runsBase, runID, intention)
	seedRecoverPublishedRule(t, runsBase)
	writeTestConfig(t, root, runsBase, worktreeBase)

	bin := buildIntegrationBinary(t)
	binDir := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	writeExecutable(t, filepath.Join(binDir, "git"), recoverAdoptAnywayGitScript())

	pkg, err := internalio.ReadJSON[contracts.TaskPackage](filepath.Join(runDir, "task-package.json"))
	require.NoError(t, err)
	var worktreesList strings.Builder
	for _, wt := range pkg.Worktrees {
		worktreesList.WriteString(wt.Path)
		worktreesList.WriteByte('\n')
	}
	gitStateDir := filepath.Join(root, "git-state")
	require.NoError(t, os.MkdirAll(gitStateDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(gitStateDir, "worktrees.list"), []byte(worktreesList.String()), 0o644))

	cmd := exec.Command(bin, "recover", "--run", string(runID), "--adopt-anyway")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		integrationTrustedPathEnvVar+"="+trustedPathWithFakeBin(binDir),
		"AUTO_IMPROVE_GIT_STATE_DIR="+gitStateDir,
		"AUTO_IMPROVE_TEST_REMOTE_SHA="+strings.Repeat("b", 40),
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Run(), "stdout=%s stderr=%s", stdout.String(), stderr.String())

	events, err := state.ScanEventsForRun(mustNewRunCtx(t, runID, runsBase, worktreeBase), runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindPromoted, events[len(events)-1].Kind)
	assert.NoFileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)))
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
		assert.Equal(t, shas.base, manifest.BaseSHA)
		assert.Equal(t, "commit", strings.TrimSpace(runIntegrationGit(t, repoRoot, "cat-file", "-t", manifest.HeadSHA)))
	}
	worktreeList := runIntegrationGit(t, repoRoot, "worktree", "list", "--porcelain")
	for _, worktree := range pkg.Worktrees {
		assert.NotContains(t, worktreeList, worktree.Path)
	}
	assert.FileExists(t, filepath.Join(runDirs[0], "70", "decision.json"))
}

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

type cliIntegrationEnv struct {
	root      string
	runsBase  string
	worktrees string
	binDir    string
	env       []string
}

func newCLIIntegrationEnv(t *testing.T, agentSleep time.Duration) cliIntegrationEnv {
	t.Helper()

	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktrees := filepath.Join(root, "worktrees")
	binDir := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktrees, 0o755))
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))

	writeTestConfig(t, root, runsBase, worktrees)

	repoRoot := mustRepoRoot(t)
	copyExecutable(t, filepath.Join(repoRoot, "internal", "orchestrator", "testdata", "bin", "git"), filepath.Join(binDir, "git"))
	copyExecutable(t, filepath.Join(repoRoot, "internal", "orchestrator", "testdata", "bin", "gh"), filepath.Join(binDir, "gh"))
	claudePath := filepath.Join(binDir, "claude")
	writeExecutable(t, claudePath, fakeClaudeScript(agentSleep))
	writeIntegrationAgentsConfig(t, root, claudePath)

	baseEnv := os.Environ()
	baseEnv = append(baseEnv,
		integrationTrustedPathEnvVar+"="+trustedPathWithFakeBin(binDir),
		"AUTO_IMPROVE_GIT_STATE_DIR="+filepath.Join(root, "git-state"),
		"AUTO_IMPROVE_TEST_BASE_SHA="+strings.Repeat("a", 40),
		"AUTO_IMPROVE_TEST_TARGET_SHA="+strings.Repeat("b", 40),
		"AUTO_IMPROVE_TEST_MERGE_SHA="+strings.Repeat("c", 40),
		"AUTO_IMPROVE_TEST_BEST_SHA="+strings.Repeat("d", 40),
	)

	return cliIntegrationEnv{
		root:      root,
		runsBase:  runsBase,
		worktrees: worktrees,
		binDir:    binDir,
		env:       baseEnv,
	}
}

func (e cliIntegrationEnv) newRunCommand(bin string, pr int) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	cmd := exec.Command(bin, "run", "--pr", strconv.Itoa(pr))
	cmd.Dir = e.root
	cmd.Env = e.env
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	return cmd, &stdout, &stderr
}

func (e cliIntegrationEnv) runDirs(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(e.runsBase)
	require.NoError(t, err)
	dirs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == "pr-locks" || name == "rules" || name == "needs-recovery" {
			continue
		}
		dirs = append(dirs, filepath.Join(e.runsBase, name))
	}
	sort.Strings(dirs)
	return dirs
}

func buildIntegrationBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "auto-improve")
	cmd := exec.Command("go", "build", "-tags", "integrationtest", "-o", bin, ".")
	cmd.Dir = mustPackageDir(t)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return bin
}

func trustedPathWithFakeBin(binDir string) string {
	return binDir + string(os.PathListSeparator) + testTrustedPathSuffix
}

func writeIntegrationAgentsConfig(t *testing.T, root, claudePath string) {
	t.Helper()
	content := "profiles:\n" +
		"  fake-claude:\n" +
		"    provider: claude\n" +
		"    binary: " + yamlDoubleQuote(claudePath) + "\n" +
		"    args: [\"-p\"]\n" +
		"  judge-primary:\n" +
		"    provider: stub\n" +
		"  judge-secondary:\n" +
		"    provider: stub\n" +
		"  judge-arbiter:\n" +
		"    provider: stub\n" +
		"roles:\n" +
		"  implementer: fake-claude\n" +
		"  judge_primary: judge-primary\n" +
		"  judge_secondary: judge-secondary\n" +
		"  judge_arbiter: judge-arbiter\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "agents.yaml"), []byte(content), 0o644))

	configPath := filepath.Join(root, "config.yaml")
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	if strings.Contains(string(data), "agent_config_path:") {
		return
	}
	require.NoError(t, os.WriteFile(configPath, append(data, []byte("agent_config_path: \"./agents.yaml\"\n")...), 0o644))
}

type realGitIntegrationSHAs struct {
	base  string
	head  string
	merge string
}

func seedRealGitIntegrationRepo(t *testing.T, repoRoot string) realGitIntegrationSHAs {
	t.Helper()
	runIntegrationGit(t, repoRoot, "init", "-b", "main")
	runIntegrationGit(t, repoRoot, "config", "user.name", "Test User")
	runIntegrationGit(t, repoRoot, "config", "user.email", "test@example.com")
	relativeRemote := filepath.Join("github.com", "owner", "repo")
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, "github.com", "owner"), 0o755))
	runIntegrationGit(t, repoRoot, "init", "--bare", relativeRemote)
	runIntegrationGit(t, repoRoot, "remote", "add", "origin", relativeRemote)

	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, "app"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "app", "message.txt"), []byte("base\n"), 0o644))
	runIntegrationGit(t, repoRoot, "add", "app/message.txt")
	runIntegrationGit(t, repoRoot, "commit", "-m", "base")
	baseSHA := strings.TrimSpace(runIntegrationGit(t, repoRoot, "rev-parse", "HEAD"))
	runIntegrationGit(t, repoRoot, "push", "origin", "HEAD:refs/heads/main")
	runIntegrationGit(t, repoRoot, "push", "origin", baseSHA+":refs/heads/auto-improve/best")

	runIntegrationGit(t, repoRoot, "checkout", "-b", "feature/pr-77")
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "app", "message.txt"), []byte("merged change\n"), 0o644))
	runIntegrationGit(t, repoRoot, "commit", "-am", "change message")
	headSHA := strings.TrimSpace(runIntegrationGit(t, repoRoot, "rev-parse", "HEAD"))

	runIntegrationGit(t, repoRoot, "checkout", "main")
	runIntegrationGit(t, repoRoot, "merge", "--no-ff", "feature/pr-77", "-m", "merge pr 77")
	mergeSHA := strings.TrimSpace(runIntegrationGit(t, repoRoot, "rev-parse", "HEAD"))
	runIntegrationGit(t, repoRoot, "push", "origin", "HEAD:refs/heads/main")

	return realGitIntegrationSHAs{base: baseSHA, head: headSHA, merge: mergeSHA}
}

func writeIntegrationConfigForRepo(t *testing.T, root, repoRoot, runsBase, worktreeBase string) {
	t.Helper()
	content := "repo:\n" +
		"  root: " + repoRoot + "\n" +
		"  default_branch: main\n" +
		"  best_branch: auto-improve/best\n" +
		"paths:\n" +
		"  runs: " + runsBase + "\n" +
		"worktree:\n" +
		"  base: " + worktreeBase + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "config.yaml"), []byte(content), 0o644))
}

func seedIntegrationDeprecatedRule(t *testing.T, registryPath, ruleID string) {
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

func readIntegrationManifest(t *testing.T, runDir string, pass int, agent contracts.AgentID) contracts.ManifestSuccess {
	t.Helper()
	prefix := fmt.Sprintf("20-pass%d", pass)
	if pass == 2 {
		prefix = "50-pass2"
	}
	manifest, err := internalio.ReadJSON[contracts.Manifest](filepath.Join(runDir, prefix, string(agent), "manifest.json"))
	require.NoError(t, err, "run files:\n%s", listIntegrationRunFiles(t, runDir))
	require.Equal(t, contracts.ManifestKindSuccess, manifest.Kind)
	success, ok := manifest.Value.(contracts.ManifestSuccess)
	require.True(t, ok)
	return success
}

func listIntegrationRunFiles(t *testing.T, runDir string) string {
	t.Helper()
	var out strings.Builder
	err := filepath.WalkDir(runDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(runDir, path)
		if err != nil {
			return err
		}
		out.WriteString(rel)
		out.WriteByte('\n')
		return nil
	})
	require.NoError(t, err)
	return out.String()
}

func runIntegrationGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return string(out)
}

func yamlDoubleQuote(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	return "\"" + value + "\""
}

func mustRepoRoot(t *testing.T) string {
	t.Helper()
	wd := mustPackageDir(t)
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func mustPackageDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return wd
}

func requireIntegrationEnv(t *testing.T) {
	t.Helper()
	if os.Getenv(integrationEnvVar) != "1" {
		t.Skip("set AUTO_IMPROVE_INTEGRATION=1 to run integration tests")
	}
}

func copyExecutable(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dst, data, 0o755))
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(body), 0o755))
}

func fakeClaudeScript(delay time.Duration) string {
	return "#!/bin/sh\n" +
		"set -eu\n" +
		"sleep " + formatSleep(delay) + "\n" +
		"cat > checklist-result.json <<EOF\n" +
		"{\"schema_version\":\"1\",\"run_id\":\"${AUTO_IMPROVE_RUN_ID}\",\"pass\":${AUTO_IMPROVE_PASS},\"agent\":\"${AUTO_IMPROVE_AGENT}\",\"items\":[]}\n" +
		"EOF\n" +
		"printf 'generated change\\n' > \"generated-${AUTO_IMPROVE_PASS}-${AUTO_IMPROVE_AGENT}.txt\"\n" +
		"printf '{\"event\":\"ok\"}\\n'\n"
}

func formatSleep(delay time.Duration) string {
	if delay <= 0 {
		return "0"
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", delay.Seconds()), "0"), ".")
}

func waitForPath(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func sha256String(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func recoverAdoptAnywayGitScript() string {
	return `#!/bin/sh
set -eu

state_dir="${AUTO_IMPROVE_GIT_STATE_DIR}"
mkdir -p "$state_dir"

if [ "${1:-}" = "-C" ]; then
  repo_dir="$2"
  shift 2
else
  repo_dir="$(pwd)"
fi

subcmd="$1"
shift

case "$subcmd" in
  ls-remote)
    branch="${4:-best}"
    printf '%s\trefs/heads/%s\n' "${AUTO_IMPROVE_TEST_REMOTE_SHA}" "$branch"
    ;;
  worktree)
    case "${1:-}" in
      list)
        if [ -f "$state_dir/worktrees.list" ]; then
          while IFS= read -r path; do
            [ -n "$path" ] || continue
            printf 'worktree %s\n\n' "$path"
          done < "$state_dir/worktrees.list"
        fi
        ;;
      remove)
        rm -rf "${3:-}"
        ;;
    esac
    ;;
  remote)
    case "${1:-} ${2:-}" in
      "get-url origin")
        printf '%s\n' "git@github.com:owner/repo.git"
        ;;
      *)
        exit 1
        ;;
    esac
    ;;
esac

exit 0
`
}

func appendPolicyBranchConfig(t *testing.T, path, policyBranch string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	content := string(data)
	replacement := "  best_branch: auto-improve/best\n"
	if policyBranch != "" {
		replacement += "  policy_branch: " + policyBranch + "\n"
	}
	content = strings.Replace(content, "  best_branch: auto-improve/best\n", replacement, 1)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func brokenPolicyGitScript() string {
	return `#!/bin/sh
set -eu

if [ "${1:-}" = "-C" ]; then
  repo_dir="$2"
  shift 2
else
  repo_dir="$(pwd)"
fi

subcmd="$1"
shift

case "$subcmd" in
  rev-parse)
    if [ "${1:-}" = "--verify" ]; then
      echo "${AUTO_IMPROVE_TEST_BEST_SHA}"
      exit 0
    fi
    case "${1:-}" in
      *^1) echo "${AUTO_IMPROVE_TEST_BASE_SHA}" ;;
      HEAD) echo "${AUTO_IMPROVE_TEST_TARGET_SHA}" ;;
      refs/heads/*) echo "${AUTO_IMPROVE_TEST_TARGET_SHA}" ;;
      *) echo "${AUTO_IMPROVE_TEST_BEST_SHA}" ;;
    esac
    ;;
  fetch)
    exit 0
    ;;
  remote)
    case "${1:-} ${2:-}" in
      "get-url origin")
        printf '%s\n' "git@github.com:owner/repo.git"
        ;;
      *)
        exit 1
        ;;
    esac
    ;;
  ls-tree)
    printf '%s\n' "auto-improve/rules-registry.jsonl"
    printf '%s\n' "auto-improve/rules/r-bad.md"
    ;;
  show)
    case "${1:-}" in
      origin/auto-improve/policy:auto-improve/rules-registry.jsonl)
        printf '%s\n' '{"kind":"added","schema_version":"1","rule_id":"r-bad","rule_path":"rules/r-bad.md","sha256":"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff","idempotency_key":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","version_seq":1,"prev_hash":"","by_run_id":"2026-04-23-PR1-feedbee","at":"2026-04-23T08:00:00Z"}'
        ;;
      origin/auto-improve/policy:auto-improve/rules/r-bad.md)
        printf '%s\n' '# broken policy'
        ;;
      *)
        exit 1
        ;;
    esac
    ;;
  merge-base)
    if [ "${1:-}" = "--is-ancestor" ]; then
      exit 0
    fi
    ;;
  worktree)
    case "${1:-}" in
      add)
        if [ "${2:-}" = "-b" ]; then
          mkdir -p "$4"
        else
          mkdir -p "$2"
        fi
        ;;
      remove)
        rm -rf "${3:-}"
        ;;
      list)
        exit 0
        ;;
    esac
    ;;
  diff|ls-files|status|branch|ls-remote|push)
    exit 0
    ;;
  *)
    exit 1
    ;;
esac

exit 0
`
}
