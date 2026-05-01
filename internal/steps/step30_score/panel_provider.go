package step30_score

import (
	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/judges"
)

// defaultPanelProvider returns the single default judge used for absolute
// scoring. Secondary/arbiter panel support remains in scorecore for tests and
// compatibility, but the normal harness path intentionally uses one judge.
type defaultPanelProvider struct{}

// DefaultPanelProvider is the stub-backed provider used by the orchestrator
// when it wires Step via step30_score.New().
func DefaultPanelProvider() PanelProvider { return defaultPanelProvider{} }

func (defaultPanelProvider) Judges(_ judges.JudgeInput) (judges.Judge, judges.Judge, judges.Judge, error) {
	return judges.NewPrimaryStub(), nil, nil, nil
}

func (defaultPanelProvider) PanelPromptVersion(base string) string {
	primary, secondary, arbiter, err := defaultPanelProvider{}.Judges(judges.JudgeInput{})
	if err != nil {
		return base
	}
	return judges.PanelPromptVersion(base, primary, secondary, arbiter)
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
	return primary, nil, nil, nil
}

func (p configPanelProvider) PanelPromptVersion(base string) string {
	primary, secondary, arbiter, err := p.Judges(judges.JudgeInput{})
	if err != nil {
		return base
	}
	return judges.PanelPromptVersion(base, primary, secondary, arbiter)
}

// FuncPanelProvider adapts a plain closure to the PanelProvider interface so
// tests can stub the panel without declaring a type.
type FuncPanelProvider func(judges.JudgeInput) (judges.Judge, judges.Judge, judges.Judge, error)

func (f FuncPanelProvider) Judges(in judges.JudgeInput) (judges.Judge, judges.Judge, judges.Judge, error) {
	return f(in)
}
