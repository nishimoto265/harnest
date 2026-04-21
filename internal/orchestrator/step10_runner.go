package orchestrator

import (
	"context"
	"errors"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	step10restorebase "github.com/nishimoto265/auto-improve/internal/steps/step10_restorebase"
)

type liveStep10 struct{}

func (liveStep10) Run(ctx context.Context, run *StepRunContext) error {
	if run == nil {
		return errors.New("orchestrator: step10 run context is required")
	}
	if run.Config == nil {
		return errors.New("orchestrator: step10 config is required")
	}

	repoRoot, err := run.Config.RepoRoot()
	if err != nil {
		return err
	}

	runner := &step10restorebase.Runner{
		GH:  step10restorebase.NewRealGHClient(run.Config.Repo.GitHub),
		Git: step10restorebase.NewRealGitClient(),
	}
	result, err := runner.Run(ctx, step10restorebase.Input{
		PR:            run.PR,
		BestBranch:    run.Config.Repo.BestBranch,
		HarnessFiles:  true,
		ExpectedRunID: run.IO.RunID,
		RepoRoot:      repoRoot,
		RunCtx:        run.IO,
		Agents:        append([]contracts.AgentID(nil), defaultAgents...),
		Now: func() time.Time {
			return time.Now().UTC()
		},
	})
	if err != nil {
		return err
	}
	run.TaskPackage = &result.Response.TaskPackage
	return nil
}
