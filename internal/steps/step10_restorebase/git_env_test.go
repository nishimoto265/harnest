package step10restorebase

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/processenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGitClient_RepoSlugParsesOriginURL verifies that repo discovery uses the
// local git remote and returns the owner/name slug step10 should pass to gh.
func TestGitClient_RepoSlugParsesOriginURL(t *testing.T) {
	repoRoot := "/tmp/auto-improve-r17-fake-repo"

	git := gitCLI{
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			if name != "git" {
				return nil, nil, fmt.Errorf("unexpected binary: %s", name)
			}
			if len(args) == 5 && args[0] == "-C" && args[1] == repoRoot && args[2] == "remote" && args[3] == "get-url" && args[4] == "origin" {
				return []byte("git@github.com:owner/repo.git\n"), nil, nil
			}
			return nil, nil, fmt.Errorf("unexpected git args: %v", args)
		},
	}

	slug, err := git.RepoSlug(context.Background(), repoRoot)
	require.NoError(t, err)
	assert.Equal(t, "owner/repo", slug)
}

func TestGitClient_RepoSlugRejectsSameSlugOnWrongHost(t *testing.T) {
	git := gitCLI{
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			if len(args) == 5 && args[2] == "remote" && args[3] == "get-url" && args[4] == "origin" {
				return []byte("https://evil.example.com/owner/repo.git\n"), nil, nil
			}
			return nil, nil, fmt.Errorf("unexpected git args: %v", args)
		},
	}

	_, err := git.RepoSlug(context.Background(), "/tmp/repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an allowed GitHub host")
}

func TestGitClient_ProductionRunnerUsesLocalAndNetworkGitEnvs(t *testing.T) {
	toolsDir := t.TempDir()
	envPath := filepath.Join(t.TempDir(), "git-env.txt")
	gitScript := []byte(`#!/bin/sh
{
  printf 'ARGS:%s\n' "$*"
  /usr/bin/env
  printf '%s\n' '---'
} >> "$FAKE_GIT_ENV_OUT"
if [ "$3" = "remote" ] && [ "$4" = "get-url" ]; then
  printf 'https://github.com/owner/repo.git\n'
  exit 0
fi
if [ "$3" = "fetch" ]; then
  exit 0
fi
exit 1
`)
	require.NoError(t, os.WriteFile(filepath.Join(toolsDir, "git"), gitScript, 0o755))
	restore := processenv.SetTrustedPathForTest(toolsDir)
	t.Cleanup(restore)
	t.Setenv("FAKE_GIT_ENV_OUT", envPath)
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("GIT_ASKPASS", "/tmp/malicious-askpass")

	err := NewGitClient().FetchCommit(context.Background(), "/tmp/repo", strings.Repeat("a", 40))

	require.NoError(t, err)
	envBytes, err := os.ReadFile(envPath)
	require.NoError(t, err)
	env := string(envBytes)
	header := "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:token"))
	assert.Contains(t, env, "ARGS:-C /tmp/repo remote get-url origin")
	assert.Contains(t, env, "ARGS:-C /tmp/repo fetch --no-tags origin "+strings.Repeat("a", 40))
	assert.Contains(t, env, "GIT_CONFIG_KEY_5=http.https://github.com/.extraheader")
	assert.Contains(t, env, "GIT_CONFIG_VALUE_5="+header)
	assert.NotContains(t, env, "GIT_ASKPASS=/tmp/malicious-askpass")
}
