package step30_score

import (
	"github.com/nishimoto265/harnest/internal/config"
	"github.com/nishimoto265/harnest/internal/judges"
)

// defaultPanelProvider returns the default primary judge used for absolute
// scoring.
type defaultPanelProvider struct{}

// DefaultPanelProvider is the stub-backed provider used by the orchestrator
// when it wires Step via step30_score.New().
func DefaultPanelProvider() PanelProvider { return defaultPanelProvider{} }

func (defaultPanelProvider) Judge(_ judges.JudgeInput) (judges.Judge, error) {
	return judges.NewPrimaryStub(), nil
}

func (defaultPanelProvider) PanelPromptVersion(base string) string {
	primary, err := defaultPanelProvider{}.Judge(judges.JudgeInput{})
	if err != nil {
		return base
	}
	return judges.PanelPromptVersion(base, primary, nil, nil)
}

type configPanelProvider struct {
	cfg *config.Config
}

func ConfigPanelProvider(cfg *config.Config) PanelProvider {
	return configPanelProvider{cfg: cfg}
}

func (p configPanelProvider) Judge(_ judges.JudgeInput) (judges.Judge, error) {
	primary, err := judges.NewJudgeFromConfig(p.cfg, judges.RolePrimary)
	if err != nil {
		return nil, err
	}
	return primary, nil
}

func (p configPanelProvider) PanelPromptVersion(base string) string {
	primary, err := p.Judge(judges.JudgeInput{})
	if err != nil {
		return base
	}
	return judges.PanelPromptVersion(base, primary, nil, nil)
}
