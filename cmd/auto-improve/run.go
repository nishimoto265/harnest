package main

import (
	"context"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/detect"
	"github.com/nishimoto265/auto-improve/internal/orchestrator"
	"github.com/nishimoto265/auto-improve/internal/preflight"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/spf13/cobra"
)

type pipelineRunner interface {
	Run(context.Context, int, orchestrator.RunOptions) error
}

var newPipelineRunner = func(cfg *config.Config) (pipelineRunner, error) {
	return orchestrator.NewOrchestrator(cfg)
}

var runPreflightCheck = func(ctx context.Context, cfg config.Config) preflight.PreflightResult {
	return preflight.New().Check(ctx, cfg)
}

var detectMergedPRs = func(ctx context.Context, cfg config.Config, processedPath string) ([]detect.MergedPR, error) {
	return detect.New(processedPath).DetectMergedPRs(ctx, cfg.Repo.GitHub, cfg.Repo.DefaultBranch)
}

func newRunCmd() *cobra.Command {
	var pr int
	var detectLoop bool
	var withPreflight bool

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the pipeline for one PR or the detect loop",
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case detectLoop && pr > 0:
				return commandExitError{code: 2, msg: "run: --pr and --detect-loop are mutually exclusive"}
			case !detectLoop && pr <= 0:
				return commandExitError{code: 2, msg: "run: either --pr <n> or --detect-loop is required"}
			}

			cfg, err := config.LoadDefault()
			if err != nil {
				return commandExitError{code: 2, msg: err.Error()}
			}
			if detectLoop {
				if err := checkDetectLoopRecoveryGate(cmd.Context(), cfg); err != nil {
					return err
				}
			} else if err := checkCLIRecoveryGate(cfg); err != nil {
				return err
			}
			if withPreflight {
				checkCtx, cancel := withPreflightTimeout(cmd.Context(), cfg)
				defer cancel()
				result := runPreflightCheck(checkCtx, cfg)
				if !result.OK {
					return commandExitError{code: 10, msg: "run: preflight failed; run `auto-improve preflight` for details"}
				}
			}

			runner, err := newPipelineRunner(&cfg)
			if err != nil {
				return err
			}
			if detectLoop {
				return runDetectLoop(cmd.Context(), cfg, runner)
			}
			return runner.Run(cmd.Context(), pr, orchestrator.RunOptions{})
		},
	}
	cmd.Flags().IntVar(&pr, "pr", 0, "PR number to process")
	cmd.Flags().BoolVar(&detectLoop, "detect-loop", false, "Run the detect loop instead of a single PR")
	cmd.Flags().BoolVar(&withPreflight, "with-preflight", false, "Run preflight checks before starting")
	return cmd
}

func runDetectLoop(ctx context.Context, cfg config.Config, runner pipelineRunner) error {
	processedPath, err := cfg.ProcessedPath()
	if err != nil {
		return commandExitError{code: 2, msg: err.Error()}
	}
	runsBase, err := cfg.RunsBase()
	if err != nil {
		return commandExitError{code: 2, msg: err.Error()}
	}
	if err := checkDetectLoopRecoveryGateForRunsBase(ctx, runsBase); err != nil {
		return err
	}
	resumeTargets, err := state.ResumeTargetPath(processedPath)
	if err != nil {
		return err
	}
	for _, item := range resumeTargets {
		if err := runner.Run(ctx, item.PR, orchestrator.RunOptions{RunID: item.RunID}); err != nil {
			if commandErr := recoveryGateExitError(err); commandErr != nil {
				return commandErr
			}
			return err
		}
		if err := checkDetectLoopRecoveryGateForRunsBase(ctx, runsBase); err != nil {
			return err
		}
	}
	prs, err := detectMergedPRs(ctx, cfg, processedPath)
	if err != nil {
		return err
	}
	if len(resumeTargets) == 0 && len(prs) == 0 {
		if err := checkDetectLoopRecoveryGateForRunsBase(ctx, runsBase); err != nil {
			return err
		}
	}
	for _, pr := range prs {
		if err := runner.Run(ctx, pr.Number, orchestrator.RunOptions{}); err != nil {
			if commandErr := recoveryGateExitError(err); commandErr != nil {
				return commandErr
			}
			return err
		}
		if err := checkDetectLoopRecoveryGateForRunsBase(ctx, runsBase); err != nil {
			return err
		}
	}
	return nil
}
