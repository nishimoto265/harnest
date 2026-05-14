package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/nishimoto265/harnest/internal/orchestrator"
	"github.com/nishimoto265/harnest/internal/state"
	"github.com/spf13/cobra"
)

func outputRepoEntrypointDryRun(ctx context.Context, cmd *cobra.Command, runtime repoEntrypointRuntime, opts repoEntrypointOptions, prs []int) error {
	plan := repoEntrypointPlan{
		Event:        "repo_entrypoint_dry_run",
		Repo:         runtime.Repo,
		RepoURL:      runtime.RepoURL,
		RepoRoot:     runtime.RepoRoot,
		RunsBase:     runtime.RunsBase,
		WorktreeBase: runtime.WorktreeBase,
		PRs:          prs,
		DryRun:       true,
	}
	switch {
	case len(prs) > 0:
		plan.Mode = "pr"
		selected, skipped, err := resolveExplicitRepoEntrypointPRs(ctx, runtime.Config, prs)
		if err != nil {
			return err
		}
		plan.Selected = selected
		plan.Skipped = skipped
	case opts.Limit > 0:
		plan.Mode = "limit"
		candidates, skipped, err := repoEntrypointCandidates(ctx, runtime.Config, runtime.processedPath())
		if err != nil {
			return err
		}
		plan.Candidates = candidates
		plan.Skipped = skipped
		plan.Selected = limitCandidates(candidates, opts.Limit)
	default:
		plan.Mode = "watch"
		candidates, skipped, err := repoEntrypointCandidates(ctx, runtime.Config, runtime.processedPath())
		if err != nil {
			return err
		}
		plan.Candidates = candidates
		plan.Skipped = skipped
		plan.Selected = candidates
	}
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(plan)
}

func runRepoEntrypointPRs(ctx context.Context, runtime repoEntrypointRuntime, prs []int, reporter *cliProgressReporter) error {
	if err := registerRepoEntrypoint(runtime); err != nil {
		return err
	}
	if err := repoEntrypointEnsureClone(ctx, runtime); err != nil {
		return err
	}
	if err := checkCLIRecoveryGate(runtime.Config); err != nil {
		return err
	}
	selected, _, err := resolveExplicitRepoEntrypointPRs(ctx, runtime.Config, prs)
	if err != nil {
		return err
	}
	runner, err := newPipelineRunner(&runtime.Config)
	if err != nil {
		return err
	}
	attachProgressReporter(runner, reporter)
	for _, pr := range selected {
		if err := runner.Run(ctx, pr.Number, orchestrator.RunOptions{}); err != nil {
			if commandErr := recoveryGateExitError(err); commandErr != nil {
				return commandErr
			}
			return err
		}
	}
	return nil
}

func runRepoEntrypointBatch(ctx context.Context, runtime repoEntrypointRuntime, limit int, reporter *cliProgressReporter) error {
	if err := registerRepoEntrypoint(runtime); err != nil {
		return err
	}
	if err := repoEntrypointEnsureClone(ctx, runtime); err != nil {
		return err
	}
	if err := checkDetectLoopRecoveryGate(ctx, runtime.Config); err != nil {
		return err
	}
	runner, err := newPipelineRunner(&runtime.Config)
	if err != nil {
		return err
	}
	attachProgressReporter(runner, reporter)
	return runRepoEntrypointTick(ctx, runtime, runner, limit, false)
}

func runRepoEntrypointWatch(ctx context.Context, runtime repoEntrypointRuntime, reporter *cliProgressReporter) error {
	if err := registerRepoEntrypoint(runtime); err != nil {
		return err
	}
	if err := repoEntrypointEnsureClone(ctx, runtime); err != nil {
		return err
	}
	if err := checkDetectLoopRecoveryGate(ctx, runtime.Config); err != nil {
		return err
	}
	runner, err := newPipelineRunner(&runtime.Config)
	if err != nil {
		return err
	}
	attachProgressReporter(runner, reporter)
	for {
		if err := runRepoEntrypointTick(ctx, runtime, runner, 0, true); err != nil {
			return err
		}
		if err := repoEntrypointSleep(ctx, defaultRepoEntrypointInterval); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
	}
}

func runRepoEntrypointTick(ctx context.Context, runtime repoEntrypointRuntime, runner pipelineRunner, limit int, drainResume bool) error {
	if err := checkDetectLoopRecoveryGateForRunsBase(ctx, runtime.RunsBase); err != nil {
		return err
	}
	if drainResume {
		paused, err := drainRepoEntrypointResumeQueue(ctx, runtime, runner)
		if err != nil {
			return err
		}
		if paused {
			return nil
		}
	}
	candidates, _, err := repoEntrypointCandidates(ctx, runtime.Config, runtime.processedPath())
	if err != nil {
		return err
	}
	for _, pr := range limitCandidates(candidates, limit) {
		if err := runner.Run(ctx, pr.Number, orchestrator.RunOptions{}); err != nil {
			if commandErr := recoveryGateExitError(err); commandErr != nil {
				return commandErr
			}
			return err
		}
		if err := checkDetectLoopRecoveryGateForRunsBase(ctx, runtime.RunsBase); err != nil {
			return err
		}
	}
	return nil
}

func drainRepoEntrypointResumeQueue(ctx context.Context, runtime repoEntrypointRuntime, runner pipelineRunner) (bool, error) {
	for {
		resumeTargets, err := stateResumeTargets(runtime.processedPath())
		if err != nil {
			return false, err
		}
		if len(resumeTargets) == 0 {
			return false, nil
		}
		item := resumeTargets[0]
		if err := runner.Run(ctx, item.PR, orchestrator.RunOptions{RunID: item.RunID}); err != nil {
			if commandErr := recoveryGateExitError(err); commandErr != nil {
				return false, commandErr
			}
			return false, err
		}
		if err := checkDetectLoopRecoveryGateForRunsBase(ctx, runtime.RunsBase); err != nil {
			return false, err
		}
		remaining, err := stateResumeTargets(runtime.processedPath())
		if err != nil {
			return false, err
		}
		if resumeTargetStillPending(remaining, item) {
			return true, nil
		}
	}
}

var stateResumeTargets = func(processedPath string) ([]state.ResumeRequest, error) {
	return state.ResumeTargetPath(processedPath)
}
