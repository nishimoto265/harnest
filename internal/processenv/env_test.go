package processenv

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitize_StripsGitAndUnsafeGHVariables(t *testing.T) {
	t.Setenv("HOME", "/tmp/home")
	t.Setenv("PATH", "/tmp/bin")
	t.Setenv("USER", "tester")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/ssh.sock")
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("GH_REPO", "owner/repo")
	t.Setenv("GIT_DIR", "/tmp/other/.git")
	t.Setenv("GIT_WORK_TREE", "/tmp/other")
	t.Setenv("GIT_EXTERNAL_DIFF", "/tmp/ext-diff")
	t.Setenv("GIT_CONFIG_GLOBAL", "/tmp/gitconfig")
	t.Setenv("GIT_SSH_COMMAND", "ssh -F /tmp/config")

	env := Sanitize("AUTO_IMPROVE_STEP=20")

	assert.Contains(t, env, "HOME=/tmp/home")
	assert.Contains(t, env, "PATH=/tmp/bin")
	assert.Contains(t, env, "USER=tester")
	assert.Contains(t, env, "LANG=en_US.UTF-8")
	assert.Contains(t, env, "SSH_AUTH_SOCK=/tmp/ssh.sock")
	assert.Contains(t, env, "GH_TOKEN=token")
	assert.Contains(t, env, "AUTO_IMPROVE_STEP=20")
	assert.NotContains(t, env, "GH_REPO=owner/repo")
	assert.NotContains(t, env, "GIT_DIR=/tmp/other/.git")
	assert.NotContains(t, env, "GIT_WORK_TREE=/tmp/other")
	assert.NotContains(t, env, "GIT_EXTERNAL_DIFF=/tmp/ext-diff")
	assert.NotContains(t, env, "GIT_CONFIG_GLOBAL=/tmp/gitconfig")
	assert.NotContains(t, env, "GIT_SSH_COMMAND=ssh -F /tmp/config")
}
