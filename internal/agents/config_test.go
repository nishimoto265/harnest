package agents

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadParsesProfilesAndRoles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
profiles:
  claude_impl:
    provider: claude
    binary: claude
  codex_judge:
    provider: codex
    binary: codex
  stub:
    provider: stub
roles:
  implementer: claude_impl
  judge_primary: codex_judge
  judge_secondary: stub
  judge_arbiter: stub
`), 0o644))

	cfg, err := Load(path)
	require.NoError(t, err)

	impl, err := cfg.ProfileForRole(RoleImplementer)
	require.NoError(t, err)
	assert.Equal(t, ProviderClaude, impl.Provider)
	assert.Equal(t, "claude", impl.Binary)
}

func TestLegacyBuildsDefaultRoleMap(t *testing.T) {
	cfg := Legacy(LegacyDefaults{
		ImplementerBinary:    "claude",
		JudgePrimaryBinary:   "claude",
		JudgeSecondaryBinary: "codex",
	})

	require.NoError(t, cfg.Validate())

	impl, err := cfg.ProfileForRole(RoleImplementer)
	require.NoError(t, err)
	assert.Equal(t, ProviderClaude, impl.Provider)

	arbiter, err := cfg.ProfileForRole(RoleJudgeArbiter)
	require.NoError(t, err)
	assert.Equal(t, ProviderStub, arbiter.Provider)
}

func TestValidateRejectsUnknownProfileReference(t *testing.T) {
	cfg := File{
		Profiles: map[string]Profile{
			"claude": {Provider: ProviderClaude, Binary: "claude"},
		},
		Roles: map[Role]string{
			RoleImplementer:    "claude",
			RoleJudgePrimary:   "missing",
			RoleJudgeSecondary: "claude",
			RoleJudgeArbiter:   "claude",
		},
	}

	err := cfg.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "unknown profile")
}
