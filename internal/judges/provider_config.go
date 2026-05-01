package judges

import (
	"fmt"

	"github.com/nishimoto265/auto-improve/internal/agents"
	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
)

func NewJudgeFromConfig(cfg *config.Config, role contracts.JudgeRole) (Judge, error) {
	if cfg == nil {
		return nil, fmt.Errorf("judges: config is required")
	}
	var agentRole agents.Role
	switch role {
	case contracts.JudgeRolePrimary:
		agentRole = agents.RoleJudgePrimary
	default:
		return nil, fmt.Errorf("judges: only primary judge role is supported by runtime config, got %q", role)
	}
	profile, err := cfg.AgentProfile(agentRole)
	if err != nil {
		return nil, err
	}
	if agents.IsGatedTestStubProvider(profile.Provider) && !agents.AllowTestStubProviders() {
		return nil, fmt.Errorf("judges: provider %q for role %q requires %s=1", profile.Provider, role, agents.AllowTestStubProvidersEnv)
	}
	switch profile.Provider {
	case agents.ProviderStub:
		return NewStub(Role(role))
	case agents.ProviderClaude:
		return NewCLIJudge(profile, Role(role)), nil
	case agents.ProviderCodex:
		return NewCLIJudge(profile, Role(role)), nil
	case agents.ProviderStubViolation:
		return NewViolationStub(Role(role))
	case agents.ProviderStubAdopt:
		return NewAdoptStub(Role(role))
	default:
		return nil, fmt.Errorf("judges: provider %q for role %q is not implemented yet", profile.Provider, role)
	}
}
