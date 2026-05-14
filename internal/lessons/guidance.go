package lessons

import "github.com/nishimoto265/harnest/internal/harnessinstall"

type InstallGuidanceOptions struct {
	Root      string
	Providers []string
}

type InstallGuidanceResult struct {
	Files []string `json:"files"`
}

func InstallGuidance(opts InstallGuidanceOptions) (InstallGuidanceResult, error) {
	plan, err := harnessinstall.Plan(opts.Root, harnessinstall.InstallOptions{
		Providers: opts.Providers,
	}, harnessinstall.PlanOptions{})
	if err != nil {
		return InstallGuidanceResult{}, err
	}
	result, err := harnessinstall.Apply(plan)
	if err != nil {
		return InstallGuidanceResult{}, err
	}
	return InstallGuidanceResult{Files: result.Files}, nil
}
