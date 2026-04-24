package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/agents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig_RejectsUnknownField(t *testing.T) {
	path := writeConfigFixture(t, `
runs_base: "/tmp/auto-improve/runs"
worktree_base: "/tmp/auto-improve/worktrees"
claude_cli_path: "claude"
codex_cli_path: "codex"
preflight_timeout_sec: 30
rescue_max_retries: 3
step_timeouts:
  step10: 300
  step20: 1800
  step30: 1800
  step40: 900
  step50: 1800
  step60: 1800
  step70: 300
unexpected: true
`)

	_, err := LoadConfig(path)
	require.Error(t, err)
	assert.ErrorContains(t, err, "field unexpected not found")
}

func TestLoadConfig_RejectsInvalidType(t *testing.T) {
	path := writeConfigFixture(t, `
runs_base: "/tmp/auto-improve/runs"
worktree_base: "/tmp/auto-improve/worktrees"
claude_cli_path: "claude"
codex_cli_path: "codex"
preflight_timeout_sec: "thirty"
rescue_max_retries: 3
step_timeouts:
  step10: 300
  step20: 1800
  step30: 1800
  step40: 900
  step50: 1800
  step60: 1800
  step70: 300
`)

	_, err := LoadConfig(path)
	require.Error(t, err)
	assert.ErrorContains(t, err, "cannot unmarshal")
}

func TestLoadConfig_RejectsMissingRequiredField(t *testing.T) {
	path := writeConfigFixture(t, `
worktree_base: "/tmp/auto-improve/worktrees"
claude_cli_path: "claude"
codex_cli_path: "codex"
preflight_timeout_sec: 30
rescue_max_retries: 3
step_timeouts:
  step10: 300
  step20: 1800
  step30: 1800
  step40: 900
  step50: 1800
  step60: 1800
  step70: 300
`)

	_, err := LoadConfig(path)
	require.Error(t, err)
	assert.ErrorContains(t, err, "RunsBase")
	assert.ErrorContains(t, err, "required")
}

func TestLoadConfig_RejectsYAMLSyntaxError(t *testing.T) {
	path := writeConfigFixture(t, `
runs_base: "/tmp/auto-improve/runs"
worktree_base: "/tmp/auto-improve/worktrees"
claude_cli_path: "claude"
codex_cli_path: "codex"
preflight_timeout_sec: 30
rescue_max_retries: 3
step_timeouts: [
`)

	_, err := LoadConfig(path)
	require.Error(t, err)
	assert.ErrorContains(t, err, "line")
}

func TestLoadConfig_AppliesDefaultThresholds(t *testing.T) {
	path := writeConfigFixture(t, `
runs_base: "/tmp/auto-improve/runs"
worktree_base: "/tmp/auto-improve/worktrees"
claude_cli_path: "claude"
codex_cli_path: "codex"
preflight_timeout_sec: 30
rescue_max_retries: 3
step_timeouts:
  step10: 300
  step20: 1800
  step30: 1800
  step40: 900
  step50: 1800
  step60: 1800
  step70: 300
`)

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, DefaultRegistryHighThreshold, cfg.RegistryHighThreshold)
	assert.Equal(t, DefaultRegistryCriticalThreshold, cfg.RegistryCriticalThreshold)
}

func TestLoadConfig_RejectsRelativeRepoRoot(t *testing.T) {
	path := writeConfigFixture(t, `
repo:
  github: "owner/repo"
  root: "."
  default_branch: "main"
  best_branch: "auto-improve/best"
paths:
  runs: "/tmp/auto-improve/runs"
worktree:
  base: "/tmp/auto-improve/worktrees"
`)

	_, err := LoadConfig(path)
	require.Error(t, err)
	assert.ErrorContains(t, err, "absolute")
}

func TestLoadConfig_RejectsStateFileOverride(t *testing.T) {
	path := writeConfigFixture(t, `
repo:
  github: "owner/repo"
  root: "/tmp/auto-improve"
  default_branch: "main"
  best_branch: "auto-improve/best"
paths:
  runs: "/tmp/auto-improve/runs"
  state_file: "/tmp/auto-improve/custom-processed.jsonl"
worktree:
  base: "/tmp/auto-improve/worktrees"
`)

	_, err := LoadConfig(path)
	require.Error(t, err)
	assert.ErrorContains(t, err, "paths.state_file override is not supported")
}

func TestLoadConfig_RejectsMissingDefaultBranchWhenRepoGitHubSet(t *testing.T) {
	path := writeConfigFixture(t, `
repo:
  github: "owner/repo"
  root: "/tmp/auto-improve"
  best_branch: "auto-improve/best"
paths:
  runs: "/tmp/auto-improve/runs"
worktree:
  base: "/tmp/auto-improve/worktrees"
`)

	_, err := LoadConfig(path)
	require.Error(t, err)
	assert.ErrorContains(t, err, "repo.default_branch is required")
}

func TestLoadConfig_LoadsAgentsFileWhenPresent(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	agentsPath := filepath.Join(dir, "agents.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
repo:
  github: "owner/repo"
  root: "/tmp/auto-improve"
  default_branch: "main"
  best_branch: "auto-improve/best"
paths:
  runs: "/tmp/auto-improve/runs"
worktree:
  base: "/tmp/auto-improve/worktrees"
agent_config_path: "./agents.yaml"
`), 0o644))
	require.NoError(t, os.WriteFile(agentsPath, []byte(`
profiles:
  codex:
    provider: codex
    binary: codex
  stub:
    provider: stub
roles:
  implementer: codex
  judge_primary: stub
  judge_secondary: stub
  judge_arbiter: stub
`), 0o644))

	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)
	profile, err := cfg.AgentProfile(agents.RoleImplementer)
	require.NoError(t, err)
	assert.Equal(t, "codex", string(profile.Provider))
	assert.Equal(t, "codex", profile.Binary)
}

func TestLoadConfig_AgentFileSnapshotOverridesAgentConfigPath(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.snapshot.yaml")
	agentsPath := filepath.Join(dir, "agents.yaml")
	require.NoError(t, os.WriteFile(agentsPath, []byte("not: agents\n"), 0o644))
	require.NoError(t, os.WriteFile(configPath, []byte(`
repo:
  github: "owner/repo"
  root: "/tmp/auto-improve"
  default_branch: "main"
  best_branch: "auto-improve/best"
paths:
  runs: "/tmp/auto-improve/runs"
worktree:
  base: "/tmp/auto-improve/worktrees"
agent_config_path: "./agents.yaml"
agent_file_snapshot:
  profiles:
    codex_impl:
      provider: codex
      binary: codex
    stub:
      provider: stub
  roles:
    implementer: codex_impl
    judge_primary: stub
    judge_secondary: stub
    judge_arbiter: stub
`), 0o644))

	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)
	profile, err := cfg.AgentProfile(agents.RoleImplementer)
	require.NoError(t, err)
	assert.Equal(t, agents.ProviderCodex, profile.Provider)
	assert.Equal(t, "codex", profile.Binary)
}

func TestLoadConfig_DefaultsTaskPromptSourceToAuto(t *testing.T) {
	path := writeConfigFixture(t, `
repo:
  github: "owner/repo"
  root: "/tmp/auto-improve"
  default_branch: "main"
  best_branch: "auto-improve/best"
paths:
  runs: "/tmp/auto-improve/runs"
worktree:
  base: "/tmp/auto-improve/worktrees"
`)

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "auto", cfg.TaskPromptSource())
}

func TestLoadConfig_RejectsInvalidTaskPromptSource(t *testing.T) {
	path := writeConfigFixture(t, `
repo:
  github: "owner/repo"
  root: "/tmp/auto-improve"
  default_branch: "main"
  best_branch: "auto-improve/best"
paths:
  runs: "/tmp/auto-improve/runs"
worktree:
  base: "/tmp/auto-improve/worktrees"
task_prompt:
  source: "title_only"
`)

	_, err := LoadConfig(path)
	require.Error(t, err)
	assert.ErrorContains(t, err, "TaskPromptSource")
}

func TestRunsBaseAndWorktreeBase_AreNamespacedByRepoSlug(t *testing.T) {
	cfg := Config{
		Repo: RepoConfig{
			GitHub: "Owner/Repo",
			Root:   "/tmp/project",
		},
		Paths: PathsConfig{
			Runs: "/var/lib/auto-improve/runs",
		},
		Worktree: WorktreeConfig{
			Base: "/var/lib/auto-improve/worktrees",
		},
	}

	runsBase, err := cfg.RunsBase()
	require.NoError(t, err)
	worktreeBase, err := cfg.WorktreeBase()
	require.NoError(t, err)

	assert.Equal(t, filepath.Clean("/var/lib/auto-improve/owner__repo/runs"), runsBase)
	assert.Equal(t, filepath.Clean("/var/lib/auto-improve/owner__repo/worktrees"), worktreeBase)
}

func TestRunsBaseAndWorktreeBase_PreserveExplicitRepoScopedPaths(t *testing.T) {
	cfg := Config{
		Repo: RepoConfig{
			GitHub: "owner/repo",
			Root:   "/tmp/project",
		},
		Paths: PathsConfig{
			Runs: "/var/lib/auto-improve/owner__repo/runs",
		},
		Worktree: WorktreeConfig{
			Base: "/var/lib/auto-improve/owner__repo/worktrees",
		},
	}

	runsBase, err := cfg.RunsBase()
	require.NoError(t, err)
	worktreeBase, err := cfg.WorktreeBase()
	require.NoError(t, err)

	assert.Equal(t, filepath.Clean("/var/lib/auto-improve/owner__repo/runs"), runsBase)
	assert.Equal(t, filepath.Clean("/var/lib/auto-improve/owner__repo/worktrees"), worktreeBase)
}

func TestRunsBaseAndWorktreeBase_NamespaceCustomLeafPaths(t *testing.T) {
	cfg := Config{
		Repo: RepoConfig{
			GitHub: "owner/repo",
			Root:   "/tmp/project",
		},
		Paths: PathsConfig{
			Runs: "/var/lib/auto-improve/repo-a-state",
		},
		Worktree: WorktreeConfig{
			Base: "/var/lib/auto-improve/repo-a-wt",
		},
	}

	runsBase, err := cfg.RunsBase()
	require.NoError(t, err)
	worktreeBase, err := cfg.WorktreeBase()
	require.NoError(t, err)

	assert.Equal(t, filepath.Clean("/var/lib/auto-improve/owner__repo/repo-a-state"), runsBase)
	assert.Equal(t, filepath.Clean("/var/lib/auto-improve/owner__repo/repo-a-wt"), worktreeBase)
}

func TestRunsBaseAndWorktreeBase_NamespaceWhenNamespaceOnlyAppearsInAncestor(t *testing.T) {
	cfg := Config{
		Repo: RepoConfig{
			GitHub: "owner/repo",
			Root:   "/tmp/project",
		},
		Paths: PathsConfig{
			Runs: "/var/lib/owner__repo/shared/runs",
		},
		Worktree: WorktreeConfig{
			Base: "/var/lib/owner__repo/shared/worktrees",
		},
	}

	runsBase, err := cfg.RunsBase()
	require.NoError(t, err)
	worktreeBase, err := cfg.WorktreeBase()
	require.NoError(t, err)

	assert.Equal(t, filepath.Clean("/var/lib/owner__repo/shared/owner__repo/runs"), runsBase)
	assert.Equal(t, filepath.Clean("/var/lib/owner__repo/shared/owner__repo/worktrees"), worktreeBase)
}

func TestLoadConfig_RejectsConflictingPathAliases(t *testing.T) {
	path := writeConfigFixture(t, `
runs_base: "/tmp/auto-improve/legacy-runs"
worktree_base: "/tmp/auto-improve/worktrees"
paths:
  runs: "/tmp/auto-improve/runs"
worktree:
  base: "/tmp/auto-improve/worktrees"
`)

	_, err := LoadConfig(path)
	require.Error(t, err)
	assert.ErrorContains(t, err, "paths.runs and runs_base both set different paths")
}

func TestLoadConfig_LegacyCLIPathsOverrideDefaultAgentNames(t *testing.T) {
	path := writeConfigFixture(t, `
runs_base: "/tmp/auto-improve/runs"
worktree_base: "/tmp/auto-improve/worktrees"
claude_cli_path: "/opt/bin/claude"
codex_cli_path: "/opt/bin/codex"
`)

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	profile, err := cfg.AgentProfile(agents.RoleImplementer)
	require.NoError(t, err)
	assert.Equal(t, "/opt/bin/claude", profile.Binary)
	assert.Equal(t, "/opt/bin/codex", cfg.CodexBinary())
}

func writeConfigFixture(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}
