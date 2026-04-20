package config

import (
	"os"
	"path/filepath"
	"testing"

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

func writeConfigFixture(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}
