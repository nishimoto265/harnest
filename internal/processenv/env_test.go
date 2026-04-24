package processenv

import (
	"encoding/base64"
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
	t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", "/tmp/hooks")
	t.Setenv("GIT_SSH_COMMAND", "ssh -F /tmp/config")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("SSH_ASKPASS", "/tmp/ssh-askpass")
	t.Setenv("BASH_ENV", "/tmp/bash_env")
	t.Setenv("LD_PRELOAD", "/tmp/preload.so")
}

func TestSanitizeForLocalExec_UsesStrictAllowlistForBaseAndExtraEnv(t *testing.T) {
	setSanitizeTestEnv(t)

	env := SanitizeForLocalExec("AUTO_IMPROVE_STEP=20", "GH_TOKEN=override", "BASH_ENV=/tmp/extra", "PATH=/override/bin")

	assert.Contains(t, env, "HOME=/tmp/home")
	assert.Contains(t, env, "PATH="+defaultTrustedPATH)
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
	assert.NotContains(t, env, "GIT_CONFIG_NOSYSTEM=0")
	assert.NotContains(t, env, "GIT_CONFIG_COUNT=1")
	assert.NotContains(t, env, "GIT_CONFIG_KEY_0=core.hooksPath")
	assert.NotContains(t, env, "GIT_CONFIG_VALUE_0=/tmp/hooks")
	assert.NotContains(t, env, "GIT_SSH_COMMAND=ssh -F /tmp/config")
	assert.NotContains(t, env, "GIT_TERMINAL_PROMPT=1")
	assert.NotContains(t, env, "SSH_ASKPASS=/tmp/ssh-askpass")
	assert.NotContains(t, env, "BASH_ENV=/tmp/bash_env")
	assert.NotContains(t, env, "BASH_ENV=/tmp/extra")
	assert.NotContains(t, env, "LD_PRELOAD=/tmp/preload.so")
}

func TestSanitizeForNetworkExec_PreservesAuthEnvButBlocksHooks(t *testing.T) {
	setSanitizeTestEnv(t)

	env := SanitizeForNetworkExec("AUTO_IMPROVE_STEP=10", "GH_TOKEN=override")

	// Baseline allowlist still applied.
	assert.Contains(t, env, "HOME=/tmp/home")
	assert.Contains(t, env, "PATH="+defaultTrustedPATH)
	assert.Contains(t, env, "USER=tester")
	assert.Contains(t, env, "AUTO_IMPROVE_STEP=10")

	// Auth env required by gh / git over the network must survive.
	assert.Contains(t, env, "SSH_AUTH_SOCK=/tmp/ssh.sock")
	assert.Contains(t, env, "GH_HOST=github.example.com")
	assert.Contains(t, env, "GITHUB_TOKEN=gh-pat")
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
	assert.NotContains(t, env, "GIT_ASKPASS=/usr/local/bin/gh-askpass")
	// GH_REPO is not in the allowlist — callers pass --repo explicitly.
	assert.NotContains(t, env, "GH_REPO=owner/repo")
}

func TestGitLocalEnv_AppendsSafeGitProfile(t *testing.T) {
	setSanitizeTestEnv(t)

	env := GitLocalEnv("AUTO_IMPROVE_STEP=20")
	falsePath := trustedFalsePath()

	assert.Contains(t, env, "AUTO_IMPROVE_STEP=20")
	assert.Contains(t, env, "PATH="+defaultTrustedPATH)
	assert.Contains(t, env, "GIT_CONFIG_NOSYSTEM=1")
	assert.Contains(t, env, "GIT_CONFIG_GLOBAL="+os.DevNull)
	assert.Contains(t, env, "GIT_CONFIG_COUNT=4")
	assert.Contains(t, env, "GIT_CONFIG_KEY_0=credential.helper")
	assert.Contains(t, env, "GIT_CONFIG_VALUE_0=")
	assert.Contains(t, env, "GIT_CONFIG_KEY_1=core.hooksPath")
	assert.Contains(t, env, "GIT_CONFIG_VALUE_1="+os.DevNull)
	assert.Contains(t, env, "GIT_CONFIG_KEY_2=core.fsmonitor")
	assert.Contains(t, env, "GIT_CONFIG_VALUE_2=false")
	assert.Contains(t, env, "GIT_CONFIG_KEY_3=core.sshCommand")
	assert.Contains(t, env, "GIT_CONFIG_VALUE_3=ssh -F "+os.DevNull)
	assert.Contains(t, env, "GIT_SSH_COMMAND=ssh -F "+os.DevNull)
	assert.Contains(t, env, "GIT_ASKPASS="+falsePath)
	assert.Contains(t, env, "SSH_ASKPASS="+falsePath)
	assert.Contains(t, env, "GIT_TERMINAL_PROMPT=0")

	assert.NotContains(t, env, "SSH_AUTH_SOCK=/tmp/ssh.sock")
	assert.NotContains(t, env, "GH_TOKEN=token")
	assert.NotContains(t, env, "GIT_CONFIG_GLOBAL=/tmp/gitconfig")
	assert.NotContains(t, env, "GIT_CONFIG_NOSYSTEM=0")
	assert.NotContains(t, env, "GIT_CONFIG_COUNT=1")
	assert.NotContains(t, env, "GIT_SSH_COMMAND=ssh -F /tmp/config")
	assert.NotContains(t, env, "GIT_ASKPASS=/usr/local/bin/gh-askpass")
	assert.NotContains(t, env, "SSH_ASKPASS=/tmp/ssh-askpass")
}

func TestGitNetworkEnv_PreservesNetworkAuthAndAppendsSafeGitProfile(t *testing.T) {
	setSanitizeTestEnv(t)

	env := GitNetworkEnv("GH_TOKEN=override")
	falsePath := trustedFalsePath()

	assert.Contains(t, env, "SSH_AUTH_SOCK=/tmp/ssh.sock")
	assert.Contains(t, env, "GH_TOKEN=override")
	assert.Contains(t, env, "GITHUB_TOKEN=gh-pat")
	assert.Contains(t, env, "GH_HOST=github.example.com")
	assert.Contains(t, env, "GIT_CONFIG_NOSYSTEM=1")
	assert.Contains(t, env, "GIT_CONFIG_GLOBAL="+os.DevNull)
	assert.Contains(t, env, "GIT_CONFIG_KEY_0=credential.helper")
	assert.Contains(t, env, "GIT_CONFIG_VALUE_0=")
	assert.Contains(t, env, "GIT_SSH_COMMAND=ssh -F "+os.DevNull)
	assert.Contains(t, env, "GIT_ASKPASS="+falsePath)

	assert.NotContains(t, env, "GH_TOKEN=token")
	assert.NotContains(t, env, "GIT_CONFIG_GLOBAL=/tmp/gitconfig")
	assert.NotContains(t, env, "GIT_SSH_COMMAND=ssh -F /tmp/config")
	assert.NotContains(t, env, "GIT_ASKPASS=/usr/local/bin/gh-askpass")
}

func TestGitNetworkEnvForRemoteURL_AddsScopedHTTPSGitHubTokenHeader(t *testing.T) {
	setSanitizeTestEnv(t)

	env := GitNetworkEnvForRemoteURL("https://github.com/owner/repo.git", "GH_TOKEN=override")
	header := "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:override"))

	assert.Contains(t, env, "GIT_CONFIG_COUNT=5")
	assert.Contains(t, env, "GIT_CONFIG_KEY_4=http.https://github.com/.extraheader")
	assert.Contains(t, env, "GIT_CONFIG_VALUE_4="+header)
}

func TestGitNetworkEnvForRemoteURL_DoesNotScopeTokenToWrongHost(t *testing.T) {
	setSanitizeTestEnv(t)

	env := GitNetworkEnvForRemoteURL("https://evil.example.com/owner/repo.git", "GH_TOKEN=override")

	assert.Contains(t, env, "GIT_CONFIG_COUNT=4")
	for _, item := range env {
		assert.NotContains(t, item, ".extraheader")
		assert.NotContains(t, item, "x-access-token")
	}
}

func TestGitNetworkEnvForRemoteURL_AllowsConfiguredGHHost(t *testing.T) {
	setSanitizeTestEnv(t)

	env := GitNetworkEnvForRemoteURL("https://github.example.com/owner/repo.git", "GH_TOKEN=override")
	header := "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:override"))

	assert.Contains(t, env, "GIT_CONFIG_KEY_4=http.https://github.example.com/.extraheader")
	assert.Contains(t, env, "GIT_CONFIG_VALUE_4="+header)
}

func TestGitSafeProfile_UsesPortableFalseFromTrustedPath(t *testing.T) {
	dir := t.TempDir()
	falsePath := filepath.Join(dir, "false")
	require.NoError(t, os.WriteFile(falsePath, []byte("#!/bin/sh\nexit 1\n"), 0o755))
	restore := SetTrustedPathForTest(dir)
	t.Cleanup(restore)

	env := GitLocalEnv()

	assert.Contains(t, env, "PATH="+dir)
	assert.Contains(t, env, "GIT_ASKPASS="+falsePath)
	assert.Contains(t, env, "SSH_ASKPASS="+falsePath)
}

func TestGitSafeProfile_FallsBackToSystemFalse(t *testing.T) {
	restore := SetTrustedPathForTest(t.TempDir())
	t.Cleanup(restore)

	falsePath := trustedFalsePath()
	env := GitLocalEnv()

	assert.Contains(t, []string{"/usr/bin/false", "/bin/false"}, falsePath)
	assert.Contains(t, env, "GIT_ASKPASS="+falsePath)
	assert.Contains(t, env, "SSH_ASKPASS="+falsePath)
}

func TestTrustedLookPath_IgnoresParentPathShadow(t *testing.T) {
	shadowDir := t.TempDir()
	shadowPath := filepath.Join(shadowDir, "sh")
	require.NoError(t, os.WriteFile(shadowPath, []byte("#!/bin/sh\nexit 99\n"), 0o755))
	t.Setenv("PATH", shadowDir)

	resolved, err := TrustedLookPath("sh")
	require.NoError(t, err)
	assert.NotEqual(t, shadowPath, resolved)
	assert.Contains(t, filepath.SplitList(defaultTrustedPATH), filepath.Dir(resolved))
}

func TestTrustedLookPath_RejectsRelativePathWithSeparator(t *testing.T) {
	_, err := TrustedLookPath("./agent")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "relative executable path")
}

func TestSetTrustedPathForTest_ControlsLookupAndSanitizedPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "tool")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	restore := SetTrustedPathForTest(dir)
	t.Cleanup(restore)

	resolved, err := TrustedLookPath("tool")
	require.NoError(t, err)
	assert.Equal(t, bin, resolved)
	assert.Contains(t, SanitizeForLocalExec(), "PATH="+dir)
}
