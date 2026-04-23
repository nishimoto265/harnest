package step30_score

import (
	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/judges"
)

// defaultPanelProvider returns the Phase 0 stub panel (primary + secondary +
// arbiter). With the default threshold of 5 the stubs resolve to "agreement"
// since primary and secondary differ by exactly 1 per dimension.
type defaultPanelProvider struct{}

// DefaultPanelProvider is the stub-backed provider used by the orchestrator
// when it wires Step via step30_score.New(). Phase 1 will swap this for a
// config-driven LLM panel.
func DefaultPanelProvider() PanelProvider { return defaultPanelProvider{} }

func (defaultPanelProvider) Judges(_ judges.JudgeInput) (judges.Judge, judges.Judge, judges.Judge, error) {
	return judges.NewPrimaryStub(), judges.NewSecondaryStub(), judges.NewArbiterStub(), nil
}

type configPanelProvider struct {
	cfg *config.Config
}

func ConfigPanelProvider(cfg *config.Config) PanelProvider {
	return configPanelProvider{cfg: cfg}
}

func (p configPanelProvider) Judges(_ judges.JudgeInput) (judges.Judge, judges.Judge, judges.Judge, error) {
	primary, err := judges.NewJudgeFromConfig(p.cfg, judges.RolePrimary)
	if err != nil {
		return nil, nil, nil, err
	}
	secondary, err := judges.NewJudgeFromConfig(p.cfg, judges.RoleSecondary)
	if err != nil {
		return nil, nil, nil, err
	}
	arbiter, err := judges.NewJudgeFromConfig(p.cfg, judges.RoleArbiter)
	if err != nil {
		return nil, nil, nil, err
	}
	return primary, secondary, arbiter, nil
}

// FuncPanelProvider adapts a plain closure to the PanelProvider interface so
// tests can stub the panel without declaring a type.
type FuncPanelProvider func(judges.JudgeInput) (judges.Judge, judges.Judge, judges.Judge, error)

func (f FuncPanelProvider) Judges(in judges.JudgeInput) (judges.Judge, judges.Judge, judges.Judge, error) {
	return f(in)
}
