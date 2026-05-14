package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/nishimoto265/harnest/internal/config"
	"github.com/nishimoto265/harnest/internal/detect"
	"github.com/nishimoto265/harnest/internal/orchestrator"
	"github.com/nishimoto265/harnest/internal/preflight"
	"github.com/nishimoto265/harnest/internal/state"
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

func newRunCmd(outputOptions *cliOutputOptions) *cobra.Command {
	var pr int
	var detectLoop bool
	var withPreflight bool
	var fromScratch bool

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the pipeline for one PR or the detect loop",
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case detectLoop && pr > 0:
				return commandExitError{code: 2, msg: "run: --pr and --detect-loop are mutually exclusive"}
			case detectLoop && fromScratch:
				return commandExitError{code: 2, msg: "run: --from-scratch and --detect-loop are mutually exclusive"}
			case fromScratch && pr <= 0:
				return commandExitError{code: 2, msg: "run: --from-scratch requires --pr <n>"}
			case !detectLoop && pr <= 0:
				return commandExitError{code: 2, msg: "run: either --pr <n> or --detect-loop is required"}
			}
			if outputOptions == nil {
				outputOptions = &cliOutputOptions{}
			}
			if err := validateOutputOptions(*outputOptions); err != nil {
				return err
			}
			reporter := newCLIProgressReporter(cmd, *outputOptions)
			defer reporter.Close()

			ctx, stopSignals := signalAwareContext(cmd.Context())
			defer stopSignals()

			cfg, err := config.LoadDefault()
			if err != nil {
				return commandExitError{code: 2, msg: err.Error()}
			}
			if detectLoop {
				if err := checkDetectLoopRecoveryGate(ctx, cfg); err != nil {
					return err
				}
			} else if err := checkCLIRecoveryGate(cfg); err != nil {
				return err
			}
			if withPreflight {
				checkCtx, cancel := withPreflightTimeout(ctx, cfg)
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
			attachProgressReporter(runner, reporter)
			if detectLoop {
				return runDetectLoop(ctx, cfg, runner)
			}
			return runner.Run(ctx, pr, orchestrator.RunOptions{FromScratch: fromScratch})
		},
	}
	cmd.Flags().IntVar(&pr, "pr", 0, "PR number to process")
	cmd.Flags().BoolVar(&detectLoop, "detect-loop", false, "Run the detect loop instead of a single PR")
	cmd.Flags().BoolVar(&withPreflight, "with-preflight", false, "Run preflight checks before starting")
	cmd.Flags().BoolVar(&fromScratch, "from-scratch", false, "Supersede any non-terminal run for --pr and start a fresh run")
	return cmd
}

type signalCancelCause struct {
	signal os.Signal
}

func (e signalCancelCause) Error() string {
	return "signal: " + e.signal.String()
}

func signalAwareContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case sig := <-signals:
			cancel(signalCancelCause{signal: sig})
		case <-ctx.Done():
		}
	}()
	return ctx, func() {
		signal.Stop(signals)
		cancel(nil)
	}
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
	hadResumeTargets := false
	resumedPRs := make(map[int]struct{})
	for {
		resumeTargets, err := state.ResumeTargetPath(processedPath)
		if err != nil {
			return err
		}
		if len(resumeTargets) == 0 {
			break
		}
		hadResumeTargets = true
		item := resumeTargets[0]
		resumedPRs[item.PR] = struct{}{}
		if err := runner.Run(ctx, item.PR, orchestrator.RunOptions{RunID: item.RunID}); err != nil {
			if commandErr := recoveryGateExitError(err); commandErr != nil {
				return commandErr
			}
			return err
		}
		if err := checkDetectLoopRecoveryGateForRunsBase(ctx, runsBase); err != nil {
			return err
		}
		remainingResumeTargets, err := state.ResumeTargetPath(processedPath)
		if err != nil {
			return err
		}
		if resumeTargetStillPending(remainingResumeTargets, item) {
			return nil
		}
	}
	prs, err := detectMergedPRs(ctx, cfg, processedPath)
	if err != nil {
		return err
	}
	prs = filterFreshPRsResumedThisTick(prs, resumedPRs)
	if !hadResumeTargets && len(prs) == 0 {
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

func resumeTargetStillPending(targets []state.ResumeRequest, current state.ResumeRequest) bool {
	for _, target := range targets {
		if target.PR == current.PR && target.RunID == current.RunID {
			return true
		}
	}
	return false
}

func filterFreshPRsResumedThisTick(prs []detect.MergedPR, resumed map[int]struct{}) []detect.MergedPR {
	if len(prs) == 0 || len(resumed) == 0 {
		return prs
	}
	filtered := prs[:0]
	for _, pr := range prs {
		if _, ok := resumed[pr.Number]; ok {
			continue
		}
		filtered = append(filtered, pr)
	}
	return filtered
}
