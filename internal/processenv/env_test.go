package processenv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setSanitizeTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", "/tmp/home")
	t.Setenv("PATH", "/tmp/bin")
	t.Setenv("USER", "tester")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("LC_ALL", "C.UTF-8")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/ssh.sock")
	t.Setenv("TZ", "UTC")
	t.Setenv("TMPDIR", "/tmp/runtime")
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("GITHUB_TOKEN", "gh-pat")
	t.Setenv("GH_HOST", "github.example.com")
	t.Setenv("GH_REPO", "owner/repo")
	t.Setenv("GIT_ASKPASS", "/usr/local/bin/gh-askpass")
	t.Setenv("GIT_DIR", "/tmp/other/.git")
	t.Setenv("GIT_WORK_TREE", "/tmp/other")
	t.Setenv("GIT_EXTERNAL_DIFF", "/tmp/ext-diff")
	t.Setenv("GIT_CONFIG_GLOBAL", "/tmp/gitconfig")
	t.Setenv("GIT_SSH_COMMAND", "ssh -F /tmp/config")
	t.Setenv("BASH_ENV", "/tmp/bash_env")
	t.Setenv("LD_PRELOAD", "/tmp/preload.so")
}

func TestSanitize_UsesStrictAllowlistForBaseAndExtraEnv(t *testing.T) {
	setSanitizeTestEnv(t)

	env := Sanitize("AUTO_IMPROVE_STEP=20", "GH_TOKEN=override", "BASH_ENV=/tmp/extra", "PATH=/override/bin")

	assert.Contains(t, env, "HOME=/tmp/home")
	assert.Contains(t, env, "PATH="+trustedPATH)
	assert.Contains(t, env, "USER=tester")
	assert.Contains(t, env, "LANG=en_US.UTF-8")
	assert.Contains(t, env, "LC_ALL=C.UTF-8")
	assert.Contains(t, env, "TZ=UTC")
	assert.Contains(t, env, "TMPDIR=/tmp/runtime")
	assert.Contains(t, env, "AUTO_IMPROVE_STEP=20")
	assert.NotContains(t, env, "PATH=/tmp/bin")
	assert.NotContains(t, env, "PATH=/override/bin")
	assert.NotContains(t, env, "SSH_AUTH_SOCK=/tmp/ssh.sock")
	assert.NotContains(t, env, "GH_TOKEN=token")
	assert.NotContains(t, env, "GH_TOKEN=override")
	assert.NotContains(t, env, "GITHUB_TOKEN=gh-pat")
	assert.NotContains(t, env, "GH_HOST=github.example.com")
	assert.NotContains(t, env, "GH_REPO=owner/repo")
	assert.NotContains(t, env, "GIT_ASKPASS=/usr/local/bin/gh-askpass")
	assert.NotContains(t, env, "GIT_DIR=/tmp/other/.git")
	assert.NotContains(t, env, "GIT_WORK_TREE=/tmp/other")
	assert.NotContains(t, env, "GIT_EXTERNAL_DIFF=/tmp/ext-diff")
	assert.NotContains(t, env, "GIT_CONFIG_GLOBAL=/tmp/gitconfig")
	assert.NotContains(t, env, "GIT_SSH_COMMAND=ssh -F /tmp/config")
	assert.NotContains(t, env, "BASH_ENV=/tmp/bash_env")
	assert.NotContains(t, env, "BASH_ENV=/tmp/extra")
	assert.NotContains(t, env, "LD_PRELOAD=/tmp/preload.so")
}

func TestSanitizeForLocalExec_IsSameAsSanitize(t *testing.T) {
	setSanitizeTestEnv(t)

	local := SanitizeForLocalExec("AUTO_IMPROVE_STEP=20")
	legacy := Sanitize("AUTO_IMPROVE_STEP=20")

	assert.Equal(t, legacy, local)
}

func TestSanitizeForNetworkExec_PreservesAuthEnvButBlocksShellInit(t *testing.T) {
	setSanitizeTestEnv(t)

	env := SanitizeForNetworkExec("AUTO_IMPROVE_STEP=10", "GH_TOKEN=override")

	// Baseline allowlist still applied.
	assert.Contains(t, env, "HOME=/tmp/home")
	assert.Contains(t, env, "PATH="+trustedPATH)
	assert.Contains(t, env, "USER=tester")
	assert.Contains(t, env, "AUTO_IMPROVE_STEP=10")

	// Auth env required by gh / git over the network must survive.
	assert.Contains(t, env, "SSH_AUTH_SOCK=/tmp/ssh.sock")
	assert.Contains(t, env, "GH_HOST=github.example.com")
	assert.Contains(t, env, "GITHUB_TOKEN=gh-pat")
	assert.Contains(t, env, "GIT_ASKPASS=/usr/local/bin/gh-askpass")
	// Caller-provided override wins for auth tokens.
	assert.Contains(t, env, "GH_TOKEN=override")
	assert.NotContains(t, env, "GH_TOKEN=token")

	// Fixed PATH, never the caller's.
	assert.NotContains(t, env, "PATH=/tmp/bin")

	// Shell-init / git-plumbing injection vectors must still be blocked.
	assert.NotContains(t, env, "BASH_ENV=/tmp/bash_env")
	assert.NotContains(t, env, "LD_PRELOAD=/tmp/preload.so")
	assert.NotContains(t, env, "GIT_DIR=/tmp/other/.git")
	assert.NotContains(t, env, "GIT_WORK_TREE=/tmp/other")
	assert.NotContains(t, env, "GIT_EXTERNAL_DIFF=/tmp/ext-diff")
	assert.NotContains(t, env, "GIT_CONFIG_GLOBAL=/tmp/gitconfig")
	assert.NotContains(t, env, "GIT_SSH_COMMAND=ssh -F /tmp/config")
	// GH_REPO is not in the allowlist — callers pass --repo explicitly.
	assert.NotContains(t, env, "GH_REPO=owner/repo")
}

func TestTrustedLookPath_IgnoresParentPathShadow(t *testing.T) {
	shadowDir := t.TempDir()
	shadowPath := filepath.Join(shadowDir, "sh")
	require.NoError(t, os.WriteFile(shadowPath, []byte("#!/bin/sh\nexit 99\n"), 0o755))
	t.Setenv("PATH", shadowDir)

	resolved, err := TrustedLookPath("sh")
	require.NoError(t, err)
	assert.NotEqual(t, shadowPath, resolved)
	assert.Contains(t, filepath.SplitList(trustedPATH), filepath.Dir(resolved))
}

func TestTrustedLookPath_RejectsRelativePathWithSeparator(t *testing.T) {
	_, err := TrustedLookPath("./agent")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "relative executable path")
}
