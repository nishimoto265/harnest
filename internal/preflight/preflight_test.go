package preflight

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckReportsMissingRequiredTools(t *testing.T) {
	requiredTools := []string{"git", "gh", "curl", "jq", "yq", "lsof", "claude"}
	for _, missing := range requiredTools {
		t.Run(missing, func(t *testing.T) {
			cfg := testConfig(t)
			deps := fakeDependencies(t, missing)

			result := NewWithDependencies(deps).Check(context.Background(), cfg)

			require.False(t, result.OK)
			assert.Contains(t, failureNames(result.Failures), missing)
		})
	}
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	runsBase := filepath.Join(t.TempDir(), "runs")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")
	return config.Config{
		Repo: config.RepoConfig{
			GitHub:        "owner/repo",
			Root:          t.TempDir(),
			DefaultBranch: "main",
			BestBranch:    "auto-improve/best",
		},
		Worktree: config.WorktreeConfig{
			Base: worktreeBase,
		},
		Agents: config.AgentsConfig{
			Implementer:    "claude",
			JudgeSecondary: "codex",
		},
		Paths: config.PathsConfig{
			Runs: runsBase,
		},
	}
}

func TestCheckReportsMissingOperationalRepoFields(t *testing.T) {
	cfg := testConfig(t)
	cfg.Repo.GitHub = ""
	cfg.Repo.DefaultBranch = ""
	cfg.Repo.BestBranch = ""

	result := NewWithDependencies(fakeDependencies(t, "")).Check(context.Background(), cfg)

	require.False(t, result.OK)
	assert.Contains(t, failureNames(result.Failures), "repo.github")
	assert.Contains(t, failureNames(result.Failures), "repo.default_branch")
	assert.Contains(t, failureNames(result.Failures), "repo.best_branch")
}

func fakeDependencies(t *testing.T, missing string) Dependencies {
	t.Helper()

	toolPaths := map[string]string{
		"git":    "/usr/local/bin/git",
		"gh":     "/usr/local/bin/gh",
		"jq":     "/usr/local/bin/jq",
		"yq":     "/usr/local/bin/yq",
		"lsof":   "/usr/local/bin/lsof",
		"claude": "/usr/local/bin/claude",
		"codex":  "/usr/local/bin/codex",
	}
	outputs := map[string]string{
		"/usr/local/bin/git --version":                                         "git version 2.35.1",
		"/usr/local/bin/gh --version":                                          "gh version 2.40.1 (2024-01-01)",
		"/usr/local/bin/jq --version":                                          "jq-1.6",
		"/usr/local/bin/yq --version":                                          "yq (https://github.com/mikefarah/yq/) version v4.40.5",
		"/usr/local/bin/git -C " + toolPaths["git"] + " remote get-url origin": "",
		"/usr/local/bin/gh auth status":                                        "github.com\n  Logged in to github.com as test-user",
	}

	return Dependencies{
		LookPath: func(file string) (string, error) {
			if file == missing {
				return "", fmt.Errorf("%s not found", file)
			}
			path, ok := toolPaths[file]
			if !ok {
				return "", fmt.Errorf("unexpected lookup: %s", file)
			}
			return path, nil
		},
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == toolPaths["git"] && len(args) >= 5 && args[0] == "-C" && args[2] == "ls-remote" {
				return []byte(strings.Repeat("a", 40) + "\trefs/heads/auto-improve/best"), nil
			}
			if name == toolPaths["git"] && len(args) >= 4 && args[0] == "-C" && args[2] == "remote" && args[3] == "get-url" {
				return []byte("https://github.com/owner/repo.git"), nil
			}
			key := name
			for _, arg := range args {
				key += " " + arg
			}
			output, ok := outputs[key]
			if !ok {
				return nil, fmt.Errorf("unexpected command: %s", key)
			}
			return []byte(output), nil
		},
	}
}

func TestCheckReportsMissingBestBranchOnOrigin(t *testing.T) {
	cfg := testConfig(t)
	deps := fakeDependencies(t, "")
	originalRun := deps.Run
	deps.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) >= 6 && args[0] == "-C" && args[2] == "ls-remote" {
			return []byte{}, nil
		}
		return originalRun(ctx, name, args...)
	}

	result := NewWithDependencies(deps).Check(context.Background(), cfg)

	require.False(t, result.OK)
	assert.Contains(t, failureNames(result.Failures), "repo.best_branch")
}

func TestCheckReportsRepoGitHubMismatch(t *testing.T) {
	cfg := testConfig(t)
	cfg.Repo.GitHub = "owner/other"

	result := NewWithDependencies(fakeDependencies(t, "")).Check(context.Background(), cfg)

	require.False(t, result.OK)
	assert.Contains(t, failureNames(result.Failures), "repo.github")
}

func failureNames(failures []Failure) []string {
	names := make([]string, 0, len(failures))
	for _, failure := range failures {
		names = append(names, failure.Name)
	}
	return names
}
