package preflight

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/agents"
	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/processenv"
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

func TestCheckRejectsGatedTestStubProvidersByDefault(t *testing.T) {
	t.Setenv(agents.AllowTestStubProvidersEnv, "")

	for _, provider := range []agents.Provider{agents.ProviderStubViolation, agents.ProviderStubAdopt} {
		t.Run(string(provider), func(t *testing.T) {
			cfg := loadPreflightConfigWithJudgeProvider(t, provider)

			result := NewWithDependencies(fakeDependencies(t, "")).Check(context.Background(), cfg)

			require.False(t, result.OK)
			failure, ok := failureByName(result.Failures, string(agents.RoleJudgePrimary))
			require.True(t, ok)
			assert.Contains(t, failure.Detail, agents.AllowTestStubProvidersEnv)
		})
	}
}

func TestCheckAcceptsGatedTestStubProvidersWithEnvGate(t *testing.T) {
	t.Setenv(agents.AllowTestStubProvidersEnv, "1")

	for _, provider := range []agents.Provider{agents.ProviderStubViolation, agents.ProviderStubAdopt} {
		t.Run(string(provider), func(t *testing.T) {
			cfg := loadPreflightConfigWithJudgeProvider(t, provider)

			result := NewWithDependencies(fakeDependencies(t, "")).Check(context.Background(), cfg)

			assert.True(t, result.OK, "failures: %+v", result.Failures)
			assert.NotContains(t, failureNames(result.Failures), string(agents.RoleJudgePrimary))
		})
	}
}

func TestDefaultRunnerUsesSanitizedNetworkEnv(t *testing.T) {
	toolsDir := t.TempDir()
	envPath := filepath.Join(t.TempDir(), "preflight-env.txt")
	toolScript := []byte(`#!/bin/sh
/usr/bin/env >> "$FAKE_PREFLIGHT_ENV_OUT"
printf "\n---\n" >> "$FAKE_PREFLIGHT_ENV_OUT"
name="${0##*/}"
if [ "$name" = "git" ] && [ "$1" = "--version" ]; then
  printf "git version 2.35.1\n"
  exit 0
fi
if [ "$name" = "git" ] && [ "$1" = "-C" ] && [ "$3" = "remote" ]; then
  printf "https://github.com/owner/repo.git\n"
  exit 0
fi
if [ "$name" = "git" ] && [ "$1" = "-C" ] && [ "$3" = "ls-remote" ]; then
  printf "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\trefs/heads/auto-improve/best\n"
  exit 0
fi
if [ "$name" = "gh" ] && [ "$1" = "--version" ]; then
  printf "gh version 2.40.1 (2024-01-01)\n"
  exit 0
fi
if [ "$name" = "gh" ] && [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  printf "github.com\n  Logged in to github.com as test-user\n"
  exit 0
fi
if [ "$name" = "jq" ] && [ "$1" = "--version" ]; then
  printf "jq-1.6\n"
  exit 0
fi
if [ "$name" = "yq" ] && [ "$1" = "--version" ]; then
  printf "yq (https://github.com/mikefarah/yq/) version v4.40.5\n"
  exit 0
fi
exit 0
`)
	for _, name := range []string{"git", "gh", "curl", "jq", "yq", "lsof", "claude", "codex"} {
		require.NoError(t, os.WriteFile(filepath.Join(toolsDir, name), toolScript, 0o755))
	}
	restore := processenv.SetTrustedPathForTest(toolsDir)
	t.Cleanup(restore)
	t.Setenv("FAKE_PREFLIGHT_ENV_OUT", envPath)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("GIT_ASKPASS", "/tmp/malicious-askpass")
	t.Setenv("BASH_ENV", "/tmp/bash-env")
	t.Setenv("GIT_CONFIG_GLOBAL", "/tmp/gitconfig")

	result := New().Check(context.Background(), testConfig(t))

	require.True(t, result.OK, "failures: %+v", result.Failures)
	envBytes, err := os.ReadFile(envPath)
	require.NoError(t, err)
	env := string(envBytes)
	header := "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:token"))
	assert.Contains(t, env, "PATH="+toolsDir)
	assert.Contains(t, env, "GH_TOKEN=token")
	assert.Contains(t, env, "GIT_CONFIG_KEY_4=http.https://github.com/.extraheader")
	assert.Contains(t, env, "GIT_CONFIG_VALUE_4="+header)
	assert.Contains(t, env, "GIT_ASKPASS=")
	assert.NotContains(t, env, "GIT_ASKPASS=/tmp/malicious-askpass")
	assert.NotContains(t, env, "BASH_ENV=")
	assert.NotContains(t, env, "GIT_CONFIG_GLOBAL=/tmp/gitconfig")
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

func loadPreflightConfigWithJudgeProvider(t *testing.T, provider agents.Provider) config.Config {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(fmt.Sprintf(`
repo:
  github: owner/repo
  root: %q
  default_branch: main
  best_branch: auto-improve/best
paths:
  runs: %q
worktree:
  base: %q
`, filepath.Join(dir, "repo"), filepath.Join(dir, "runs"), filepath.Join(dir, "worktrees"))), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents.yaml"), []byte(fmt.Sprintf(`
profiles:
  claude:
    provider: claude
    binary: claude
  judge-primary:
    provider: %s
  judge-secondary:
    provider: stub
  judge-arbiter:
    provider: stub
roles:
  implementer: claude
  judge_primary: judge-primary
  judge_secondary: judge-secondary
  judge_arbiter: judge-arbiter
`, provider)), 0o644))

	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	return cfg
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
		"curl":   "/usr/local/bin/curl",
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
		PrepareProviderBinary: func(_ agents.Provider, binary string) (string, []string, error) {
			if binary == missing {
				return "", nil, fmt.Errorf("%s not found", binary)
			}
			path, ok := toolPaths[binary]
			if !ok {
				return "", nil, fmt.Errorf("unexpected provider binary: %s", binary)
			}
			return path, nil, nil
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

func TestCheckRejectsSameSlugOriginOnWrongHost(t *testing.T) {
	cfg := testConfig(t)
	deps := fakeDependencies(t, "")
	originalRun := deps.Run
	deps.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) >= 5 && args[0] == "-C" && args[2] == "remote" && args[3] == "get-url" {
			return []byte("https://evil.example.com/owner/repo.git"), nil
		}
		return originalRun(ctx, name, args...)
	}

	result := NewWithDependencies(deps).Check(context.Background(), cfg)

	require.False(t, result.OK)
	failure, ok := failureByName(result.Failures, "repo.github")
	require.True(t, ok)
	assert.Contains(t, failure.Detail, "not an allowed GitHub host")
}

func failureNames(failures []Failure) []string {
	names := make([]string, 0, len(failures))
	for _, failure := range failures {
		names = append(names, failure.Name)
	}
	return names
}

func failureByName(failures []Failure, name string) (Failure, bool) {
	for _, failure := range failures {
		if failure.Name == name {
			return failure, true
		}
	}
	return Failure{}, false
}
