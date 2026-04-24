package judges

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/agents"
	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewJudgeFromConfigRejectsGatedTestStubProvidersByDefault(t *testing.T) {
	t.Setenv(agents.AllowTestStubProvidersEnv, "")

	for _, provider := range []agents.Provider{agents.ProviderStubViolation, agents.ProviderStubAdopt} {
		t.Run(string(provider), func(t *testing.T) {
			cfg := loadJudgeProviderConfig(t, provider)

			judge, err := NewJudgeFromConfig(&cfg, contracts.JudgeRolePrimary)

			require.Error(t, err)
			assert.Nil(t, judge)
			assert.ErrorContains(t, err, agents.AllowTestStubProvidersEnv)
		})
	}
}

func TestNewJudgeFromConfigAcceptsGatedTestStubProvidersWithEnvGate(t *testing.T) {
	t.Setenv(agents.AllowTestStubProvidersEnv, "1")

	for _, provider := range []agents.Provider{agents.ProviderStubViolation, agents.ProviderStubAdopt} {
		t.Run(string(provider), func(t *testing.T) {
			cfg := loadJudgeProviderConfig(t, provider)

			judge, err := NewJudgeFromConfig(&cfg, contracts.JudgeRolePrimary)

			require.NoError(t, err)
			assert.NotNil(t, judge)
		})
	}
}

func TestNewJudgeFromConfigPreservesPlainStubWithoutEnvGate(t *testing.T) {
	t.Setenv(agents.AllowTestStubProvidersEnv, "")
	cfg := loadJudgeProviderConfig(t, agents.ProviderStub)

	judge, err := NewJudgeFromConfig(&cfg, contracts.JudgeRolePrimary)

	require.NoError(t, err)
	assert.NotNil(t, judge)
}

func loadJudgeProviderConfig(t *testing.T, provider agents.Provider) config.Config {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(fmt.Sprintf(`
paths:
  runs: %q
worktree:
  base: %q
`, filepath.Join(dir, "runs"), filepath.Join(dir, "worktrees"))), 0o644))
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
