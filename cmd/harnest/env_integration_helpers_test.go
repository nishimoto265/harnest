package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

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
		"HARNEST_HOME="+filepath.Join(root, "home"),
		"HARNEST_GIT_STATE_DIR="+filepath.Join(root, "git-state"),
		"HARNEST_TEST_BASE_SHA="+strings.Repeat("a", 40),
		"HARNEST_TEST_TARGET_SHA="+strings.Repeat("b", 40),
		"HARNEST_TEST_MERGE_SHA="+strings.Repeat("c", 40),
		"HARNEST_TEST_BEST_SHA="+strings.Repeat("d", 40),
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
		if strings.HasPrefix(name, ".") || name == "pr-locks" || name == "rules" || name == "needs-recovery" {
			continue
		}
		dirs = append(dirs, filepath.Join(e.runsBase, name))
	}
	sort.Strings(dirs)
	return dirs
}

func findIntegrationRunDir(t *testing.T, runsBase, contains string) string {
	t.Helper()
	for _, runDir := range (cliIntegrationEnv{runsBase: runsBase}).runDirs(t) {
		if strings.Contains(filepath.Base(runDir), contains) {
			return runDir
		}
	}
	t.Fatalf("run dir containing %q not found under %s", contains, runsBase)
	return ""
}

func buildIntegrationBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "harnest")
	cmd := exec.Command("go", "build", "-tags", "integrationtest", "-o", bin, ".")
	cmd.Dir = mustPackageDir(t)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return bin
}

func trustedPathWithFakeBin(binDir string) string {
	return binDir + string(os.PathListSeparator) + testTrustedPathSuffix
}

func installPreflightRuntimeTools(t *testing.T, binDir string) {
	t.Helper()
	wrapIntegrationGitForPreflight(t, binDir)
	installPreflightRuntimeToolsWithoutGit(t, binDir)
}

func installPreflightRuntimeToolsWithoutGit(t *testing.T, binDir string) {
	t.Helper()
	wrapIntegrationGHForPreflight(t, binDir)
	writeExecutable(t, filepath.Join(binDir, "curl"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "jq"), "#!/bin/sh\nprintf 'jq-1.6\\n'\n")
	writeExecutable(t, filepath.Join(binDir, "yq"), "#!/bin/sh\nprintf 'yq (https://github.com/mikefarah/yq/) version v4.40.5\\n'\n")
	writeExecutable(t, filepath.Join(binDir, "lsof"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "codex"), "#!/bin/sh\nexit 0\n")
}

func wrapIntegrationGitForPreflight(t *testing.T, binDir string) {
	t.Helper()
	path := filepath.Join(binDir, "git")
	fixturePath := path + ".fixture"
	copyExecutable(t, path, fixturePath)
	writeExecutable(t, path, `#!/bin/sh
set -eu

if [ "${1:-}" = "--version" ]; then
  printf 'git version 2.45.0\n'
  exit 0
fi
if [ "${1:-}" = "-C" ] && [ "${3:-}" = "config" ] && [ "${4:-}" = "--get-all" ] && [ "${5:-}" = "remote.origin.pushurl" ]; then
  exit 1
fi

exec "$0.fixture" "$@"
`)
}

func wrapIntegrationGHForPreflight(t *testing.T, binDir string) {
	t.Helper()
	path := filepath.Join(binDir, "gh")
	fixturePath := path + ".fixture"
	copyExecutable(t, path, fixturePath)
	writeExecutable(t, path, `#!/bin/sh
set -eu

if [ "${1:-}" = "--version" ]; then
  printf 'gh version 2.40.1 (2024-01-01)\n'
  exit 0
fi
if [ "${1:-}" = "auth" ] && [ "${2:-}" = "status" ]; then
  printf 'github.com\n  Logged in to github.com as test-user\n'
  exit 0
fi

exec "$0.fixture" "$@"
`)
}

func ensureIntegrationRepoGitHubConfig(t *testing.T, configPath, slug string) {
	t.Helper()
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	content := string(data)
	if strings.Contains(content, "  github: ") {
		return
	}
	content = strings.Replace(content, "repo:\n", "repo:\n  github: "+slug+"\n", 1)
	require.NoError(t, os.WriteFile(configPath, []byte(content), 0o644))
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
		t.Skip("set HARNEST_INTEGRATION=1 to run integration tests")
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

func yamlDoubleQuote(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	return "\"" + value + "\""
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
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
