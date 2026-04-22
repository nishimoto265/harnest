package processenv

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitize_UsesStrictAllowlistForBaseAndExtraEnv(t *testing.T) {
	t.Setenv("HOME", "/tmp/home")
	t.Setenv("PATH", "/tmp/bin")
	t.Setenv("USER", "tester")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("LC_ALL", "C.UTF-8")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/ssh.sock")
	t.Setenv("TZ", "UTC")
	t.Setenv("TMPDIR", "/tmp/runtime")
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("GH_REPO", "owner/repo")
	t.Setenv("GIT_DIR", "/tmp/other/.git")
	t.Setenv("GIT_WORK_TREE", "/tmp/other")
	t.Setenv("GIT_EXTERNAL_DIFF", "/tmp/ext-diff")
	t.Setenv("GIT_CONFIG_GLOBAL", "/tmp/gitconfig")
	t.Setenv("GIT_SSH_COMMAND", "ssh -F /tmp/config")
	t.Setenv("BASH_ENV", "/tmp/bash_env")
	t.Setenv("LD_PRELOAD", "/tmp/preload.so")

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
	assert.NotContains(t, env, "GH_REPO=owner/repo")
	assert.NotContains(t, env, "GIT_DIR=/tmp/other/.git")
	assert.NotContains(t, env, "GIT_WORK_TREE=/tmp/other")
	assert.NotContains(t, env, "GIT_EXTERNAL_DIFF=/tmp/ext-diff")
	assert.NotContains(t, env, "GIT_CONFIG_GLOBAL=/tmp/gitconfig")
	assert.NotContains(t, env, "GIT_SSH_COMMAND=ssh -F /tmp/config")
	assert.NotContains(t, env, "BASH_ENV=/tmp/bash_env")
	assert.NotContains(t, env, "BASH_ENV=/tmp/extra")
	assert.NotContains(t, env, "LD_PRELOAD=/tmp/preload.so")
}
