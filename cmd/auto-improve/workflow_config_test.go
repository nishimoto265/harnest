package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/nishimoto265/harnest/internal/agents"
	"github.com/nishimoto265/harnest/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowRenderConfigSeparatesClaudeImplementerAndJudgeProfiles(t *testing.T) {
	repoRoot := mustRepoRoot(t)
	outputDir := t.TempDir()
	homeDir := filepath.Join(outputDir, "home")
	runsBase := filepath.Join(outputDir, "runs")
	worktreeBase := filepath.Join(outputDir, "worktrees")

	cmd := exec.Command("bash", "scripts/render-workflow-config.sh")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(),
		"HOME="+homeDir,
		"AUTO_IMPROVE_WORKFLOW_OUTPUT_DIR="+outputDir,
		"GITHUB_REPOSITORY=owner/repo",
		"GITHUB_WORKSPACE="+repoRoot,
		"REPOSITORY_DEFAULT_BRANCH=main",
		"INPUT_REPO_GITHUB=",
		"INPUT_DEFAULT_BRANCH=",
		"INPUT_BEST_BRANCH=auto-improve/best",
		"INPUT_POLICY_BRANCH=auto-improve/policy",
		"INPUT_RUNS_BASE="+runsBase,
		"INPUT_WORKTREE_BASE="+worktreeBase,
		"INPUT_CLAUDE_CLI_PATH=/tmp/fake claude",
		"INPUT_CODEX_CLI_PATH=/tmp/fake-codex",
		"INPUT_IMPLEMENTER_PROVIDER=claude",
		"INPUT_JUDGE_PRIMARY_PROVIDER=claude",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	generatedAgents, err := agents.Load(filepath.Join(outputDir, "agents.yaml"))
	require.NoError(t, err)
	generatedConfig, err := config.Load(filepath.Join(outputDir, "config.yaml"))
	require.NoError(t, err)

	assert.Equal(t, "claude-implementer", generatedAgents.Roles[agents.RoleImplementer])
	assert.Equal(t, []string{"-p"}, generatedAgents.Profiles["claude-implementer"].Args)
	assert.Equal(t, "claude-judge", generatedAgents.Roles[agents.RoleJudgePrimary])
	assert.Empty(t, generatedAgents.Profiles["claude-judge"].Args)
	assert.NotContains(t, generatedAgents.Roles, agents.Role("judge_secondary"))
	assert.NotContains(t, generatedAgents.Roles, agents.Role("judge_arbiter"))
	assert.Equal(t, "owner/repo", generatedConfig.Repo.GitHub)
	assert.Equal(t, repoRoot, generatedConfig.Repo.Root)
	assert.Equal(t, "main", generatedConfig.Repo.DefaultBranch)
	assert.Equal(t, "auto-improve/best", generatedConfig.Repo.BestBranch)
	assert.Equal(t, "auto-improve/policy", generatedConfig.Repo.PolicyBranch)
	assert.Equal(t, runsBase, generatedConfig.Paths.Runs)
	assert.Equal(t, worktreeBase, generatedConfig.Worktree.Base)

	primary, err := generatedConfig.AgentProfile(agents.RoleJudgePrimary)
	require.NoError(t, err)
	assert.Equal(t, agents.ProviderClaude, primary.Provider)
	assert.Empty(t, primary.Args)
}
