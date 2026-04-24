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
		ImplementerBinary: "claude",
	})

	require.NoError(t, cfg.Validate())

	impl, err := cfg.ProfileForRole(RoleImplementer)
	require.NoError(t, err)
	assert.Equal(t, ProviderClaude, impl.Provider)
	assert.Equal(t, []string{"-p"}, impl.Args)

	arbiter, err := cfg.ProfileForRole(RoleJudgeArbiter)
	require.NoError(t, err)
	assert.Equal(t, ProviderStub, arbiter.Provider)
}

func TestLegacyInfersCodexImplementerProviderFromBinary(t *testing.T) {
	cfg := Legacy(LegacyDefaults{
		ImplementerBinary: "/opt/bin/codex",
	})

	impl, err := cfg.ProfileForRole(RoleImplementer)
	require.NoError(t, err)
	assert.Equal(t, ProviderCodex, impl.Provider)
	assert.Equal(t, "/opt/bin/codex", impl.Binary)
	assert.Empty(t, impl.Args)
}

func TestAllowTestStubProvidersRequiresExplicitEnvGate(t *testing.T) {
	t.Setenv(AllowTestStubProvidersEnv, "")
	assert.False(t, AllowTestStubProviders())

	t.Setenv(AllowTestStubProvidersEnv, "1")
	assert.True(t, AllowTestStubProviders())
}

func TestIsGatedTestStubProviderPreservesPlainStub(t *testing.T) {
	assert.False(t, IsGatedTestStubProvider(ProviderStub))
	assert.True(t, IsGatedTestStubProvider(ProviderStubViolation))
	assert.True(t, IsGatedTestStubProvider(ProviderStubAdopt))
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

func TestValidateRejectsStubImplementerProfiles(t *testing.T) {
	for _, provider := range []Provider{ProviderStub, ProviderStubViolation, ProviderStubAdopt} {
		t.Run(string(provider), func(t *testing.T) {
			cfg := File{
				Profiles: map[string]Profile{
					"impl": {Provider: provider},
					"stub": {Provider: ProviderStub},
				},
				Roles: map[Role]string{
					RoleImplementer:    "impl",
					RoleJudgePrimary:   "stub",
					RoleJudgeSecondary: "stub",
					RoleJudgeArbiter:   "stub",
				},
			}

			err := cfg.Validate()

			require.Error(t, err)
			assert.ErrorContains(t, err, "implementer")
			assert.ErrorContains(t, err, "claude")
			assert.ErrorContains(t, err, "codex")
		})
	}
}

func TestValidateAllowsStubJudgeProfiles(t *testing.T) {
	cfg := File{
		Profiles: map[string]Profile{
			"impl":           {Provider: ProviderCodex, Binary: "codex"},
			"stub":           {Provider: ProviderStub},
			"stub-violation": {Provider: ProviderStubViolation},
			"stub-adopt":     {Provider: ProviderStubAdopt},
		},
		Roles: map[Role]string{
			RoleImplementer:    "impl",
			RoleJudgePrimary:   "stub",
			RoleJudgeSecondary: "stub-violation",
			RoleJudgeArbiter:   "stub-adopt",
		},
	}

	require.NoError(t, cfg.Validate())
}
