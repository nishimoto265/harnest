package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nishimoto265/harnest/internal/config"
	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/nishimoto265/harnest/internal/judges"
	"github.com/nishimoto265/harnest/internal/processenv"
	"github.com/stretchr/testify/require"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	installTestRubricCache(t)
	runsBase := filepath.Join(t.TempDir(), "runs")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")
	return &config.Config{
		Repo: config.RepoConfig{
			Root:          t.TempDir(),
			DefaultBranch: "main",
			BestBranch:    "best",
		},
		Worktree: config.WorktreeConfig{
			Base: worktreeBase,
		},
		Paths: config.PathsConfig{
			Runs: runsBase,
		},
		RegistryHighThreshold:     config.DefaultRegistryHighThreshold,
		RegistryCriticalThreshold: config.DefaultRegistryCriticalThreshold,
		StepTimeouts: map[string]int{
			"step10": 300,
			"step20": 300,
			"step30": 300,
			"step40": 300,
			"step50": 300,
			"step60": 300,
			"step70": 300,
		},
	}
}

func testConfigWithCLIJudge(t *testing.T) *config.Config {
	t.Helper()
	installTestRubricCache(t)
	dir := t.TempDir()
	runsBase := filepath.Join(dir, "runs")
	worktreeBase := filepath.Join(dir, "worktrees")
	repoRoot := filepath.Join(dir, "repo")
	require.NoError(t, os.MkdirAll(repoRoot, 0o755))
	codexPath := writeFakeCodexJudge(t, dir)
	implementerPath := writeFakeConfigImplementer(t, dir)
	configPath := filepath.Join(dir, "config.yaml")
	agentsPath := filepath.Join(dir, "agents.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(fmt.Sprintf(`
repo:
  root: %q
  default_branch: "main"
  best_branch: "best"
paths:
  runs: %q
worktree:
  base: %q
agent_config_path: %q
`, repoRoot, runsBase, worktreeBase, agentsPath)), 0o644))
	require.NoError(t, os.WriteFile(agentsPath, []byte(fmt.Sprintf(`
profiles:
  fake-implementer:
    provider: claude
    binary: %q
  codex-judge:
    provider: codex
    binary: %q
  stub:
    provider: stub
roles:
  implementer: fake-implementer
  judge_primary: codex-judge
`, implementerPath, codexPath)), 0o644))
	cfg, err := config.LoadConfig(configPath)
	require.NoError(t, err)
	return cfg
}

func installTestRubricCache(t *testing.T) {
	t.Helper()
	judges.SetDefaultRubricDirForTest(filepath.Join(t.TempDir(), "rubric-cache"))
	t.Cleanup(func() { judges.SetDefaultRubricDirForTest("") })
}

func writeFakeConfigImplementer(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-implementer")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	return path
}

func writeFakeCodexJudge(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "codex-judge")
	require.NoError(t, os.WriteFile(path, []byte(`#!/bin/sh
set -eu
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    out="$2"
    shift 2
    continue
  fi
  shift
done
prompt="$(mktemp)"
cat > "$prompt"
if grep -q "final decision judge for step60 pairwise" "$prompt"; then
cat > "$out" <<'EOF'
{"decision":"adopt","agent_decisions":[
  {"agent":"a1","winner":"B","margin":"clear","justification":"pass2 wins"},
  {"agent":"a2","winner":"B","margin":"clear","justification":"pass2 wins"},
  {"agent":"a3","winner":"B","margin":"clear","justification":"pass2 wins"}
],"justification":"fake pairwise decision"}
EOF
rm -f "$prompt"
exit 0
fi
if grep -q "step60 true pairwise judge" "$prompt"; then
cat > "$out" <<'EOF'
{"winner":"B","margin":"clear","dimension_votes":[
  {"dimension":"fidelity","winner":"B","reason":"better"},
  {"dimension":"correctness","winner":"B","reason":"better"},
  {"dimension":"maintainability","winner":"B","reason":"better"},
  {"dimension":"discipline","winner":"B","reason":"better"},
  {"dimension":"communication","winner":"B","reason":"better"}
],"fatal_issues":[],"justification":"fake pairwise comparison"}
EOF
rm -f "$prompt"
exit 0
fi
cat > "$out" <<'EOF'
{"scores":[
  {"dimension":"fidelity","score":80,"reason":"r1"},
  {"dimension":"correctness","score":81,"reason":"r2"},
  {"dimension":"maintainability","score":82,"reason":"r3"},
  {"dimension":"discipline","score":83,"reason":"r4"},
  {"dimension":"communication","score":84,"reason":"r5"}
],"compliance":[
  {"rule_id":"stub-rubric-rule","verdict":"compliant","rationale":"ok"}
]}
EOF
rm -f "$prompt"
`), 0o755))
	return path
}

func configJudgePanelPromptVersion(t *testing.T, cfg *config.Config) string {
	t.Helper()
	primary, err := judges.NewJudgeFromConfig(cfg, contracts.JudgeRolePrimary)
	require.NoError(t, err)
	return judges.PanelPromptVersion("phase0-stub", primary, nil, nil)
}

func repoRootFromTestFile(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func runGit(t *testing.T, repoRoot string, args ...string) string {
	t.Helper()
	cmdArgs := args
	if repoRoot != "" {
		cmdArgs = append([]string{"-C", repoRoot}, args...)
	}
	cmd, err := processenv.TrustedCommand("git", cmdArgs...)
	require.NoError(t, err)
	cmd.Env = processenv.SanitizeForLocalExec()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s\n%s", strings.Join(cmdArgs, " "), string(out))
	return string(out)
}

func installFakeCLI(t *testing.T) string {
	t.Helper()
	sourceDir := filepath.Join(repoRootFromTestFile(t), "internal", "orchestrator", "testdata", "bin")
	destDir := t.TempDir()
	t.Setenv("HARNEST_GIT_STATE_DIR", filepath.Join(t.TempDir(), "git-state"))
	for _, name := range []string{"gh", "git", "claude"} {
		src := filepath.Join(sourceDir, name)
		data, err := os.ReadFile(src)
		require.NoError(t, err)
		dst := filepath.Join(destDir, name)
		require.NoError(t, os.WriteFile(dst, data, 0o755))
	}
	restoreTrustedPath := processenv.SetTrustedPathForTest(destDir + string(os.PathListSeparator) + processenv.TrustedPath())
	t.Cleanup(restoreTrustedPath)
	return destDir
}
