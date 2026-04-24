package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	ilog "github.com/nishimoto265/auto-improve/internal/logger"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/nishimoto265/auto-improve/internal/steps/step60_scorepairwise"
	"github.com/nishimoto265/auto-improve/internal/steps/step70_decide"
)

func (o *Orchestrator) runCycle(ctx context.Context, pr int, opts RunOptions) error {
	if pr <= 0 {
		return fmt.Errorf("orchestrator: pr must be > 0: pr=%d", pr)
	}
	runsBase, err := o.cfg.RunsBase()
	if err != nil {
		return err
	}
	sentinel, blocked, err := firstNeedsRecoverySentinel(runsBase)
	if err != nil {
		return err
	}
	if blocked {
		return &GlobalNeedsRecoveryError{Sentinel: sentinel}
	}
	prLock, err := acquirePRRunLock(ctx, runsBase, pr)
	if err != nil {
		return err
	}
	defer func() {
		_ = prLock.Unlock()
	}()

	selection, err := o.selectRun(pr, opts)
	if err != nil {
		return err
	}

	o.runContext = selection.runContext
	o.stateWriter = state.NewWriter(selection.runContext)

	run := &StepRunContext{
		Config:        o.cfg,
		Logger:        o.logger.With(slog.String(ilog.FieldRunID, string(selection.runContext.RunID))),
		PR:            pr,
		IO:            selection.runContext,
		IntentionFile: NewIntentionStore(selection.runContext),
	}

	if selection.fresh {
		if err := beforeFreshRunGateHook(run); err != nil {
			return err
		}
	}
	if err := beforeRunScaffoldHook(run); err != nil {
		return err
	}
	if err := o.ensureNoGlobalSentinel(run.IO); err != nil {
		return err
	}
	if err := o.ensureRunScaffold(run); err != nil {
		return err
	}
	if selection.fresh {
		if err := beforeStartedAppendHook(run); err != nil {
			return err
		}
		if err := o.ensureNoGlobalSentinel(run.IO); err != nil {
			return err
		}
		if err := o.appendState(startedEntry(pr, selection.runContext.RunID, time.Now().UTC())); err != nil {
			return err
		}
	}

	if err := o.loadPersistedArtifacts(run); err != nil {
		return err
	}

	start, err := o.resolveStartStep(run)
	if err != nil {
		if errors.Is(err, errNoScorableAgentsResume) {
			if err := o.appendState(failedEntry(pr, run.IO.RunID, contracts.FailedStep30, "no_scorable_agents", "step30 resume selected without any scorable pass1 manifests", time.Now().UTC())); err != nil {
				return err
			}
			return nil
		}
		return err
	}

	preserveWorktrees := true
	defer func() {
		if preserveWorktrees {
			return
		}
		_ = cleanupWorktrees(run.IO, run.TaskPackage)
	}()

	for _, step := range pipelineFrom(start) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := o.ensureNoGlobalSentinel(run.IO); err != nil {
			return err
		}
		switch step {
		case contracts.FailedStep10:
			if err := o.runStep10(ctx, run); err != nil {
				return err
			}
		case contracts.FailedStep20:
			if err := o.runParallel(ctx, run, 1, contracts.FailedStep20, o.steps.Step20); err != nil {
				if errors.Is(err, errStopPipeline) {
					return nil
				}
				return err
			}
			if err := o.appendState(stepDoneEntry(pr, run.IO.RunID, contracts.FailedStep20, time.Now().UTC())); err != nil {
				return err
			}
			scorableAgents, err := scorableAgentsForPass(run.IO, run.TaskPackage, 1)
			if err != nil {
				return err
			}
			if len(scorableAgents) == 0 {
				if err := o.appendState(failedEntry(pr, run.IO.RunID, contracts.FailedStep20, "no_scorable_agents", "step20 completed without any scorable pass1 manifests", time.Now().UTC())); err != nil {
					return err
				}
				return nil
			}
		case contracts.FailedStep30:
			if err := o.runSingle(ctx, run, contracts.FailedStep30, o.steps.Step30); err != nil {
				return err
			}
			if err := o.appendState(stepDoneEntry(pr, run.IO.RunID, contracts.FailedStep30, time.Now().UTC())); err != nil {
				return err
			}
		case contracts.FailedStep40:
			if err := o.runSingle(ctx, run, contracts.FailedStep40, o.steps.Step40); err != nil {
				return err
			}
			if err := o.appendState(stepDoneEntry(pr, run.IO.RunID, contracts.FailedStep40, time.Now().UTC())); err != nil {
				return err
			}
		case contracts.FailedStep50:
			if noActionableCandidates(run.Candidates) {
				continue
			}
			if err := o.runParallel(ctx, run, 2, contracts.FailedStep50, o.steps.Step50); err != nil {
				if errors.Is(err, errStopPipeline) {
					return nil
				}
				return err
			}
			if err := o.appendState(stepDoneEntry(pr, run.IO.RunID, contracts.FailedStep50, time.Now().UTC())); err != nil {
				return err
			}
		case contracts.FailedStep60:
			if noActionableCandidates(run.Candidates) {
				continue
			}
			if err := o.runSingle(ctx, run, contracts.FailedStep60, o.steps.Step60); err != nil {
				if errors.Is(err, step60_scorepairwise.ErrNoScorablePass2Agents) {
					if appendErr := o.appendState(failedEntry(pr, run.IO.RunID, contracts.FailedStep60, "no_scorable_agents", "step60 completed without any scorable pass2 manifests", time.Now().UTC())); appendErr != nil {
						return appendErr
					}
					return nil
				}
				return err
			}
			if err := o.appendState(stepDoneEntry(pr, run.IO.RunID, contracts.FailedStep60, time.Now().UTC())); err != nil {
				return err
			}
		case contracts.FailedStep70:
			if err := o.runSingle(ctx, run, contracts.FailedStep70, o.steps.Step70); err != nil {
				switch {
				case errors.Is(err, step70_decide.ErrBlockedBySentinel):
					if appendErr := o.appendInterrupted(run.PR, run.IO.RunID, contracts.FailedStep70, contracts.InterruptedReasonUnknown, "step70 blocked by needs-recovery sentinel"); appendErr != nil {
						return appendErr
					}
					return nil
				case errors.Is(err, step70_decide.ErrNeedsManualRecovery):
					if appendErr := o.ensureStep70NeedsManualRecoveryState(run); appendErr != nil {
						return appendErr
					}
					return nil
				}
				return err
			}
			if err := ctx.Err(); err != nil {
				terminal, terminalErr := hasTerminalEvent(run.IO, run.IO.RunID)
				if terminalErr != nil {
					return terminalErr
				}
				if !terminal {
					if appendErr := o.appendInterrupted(run.PR, run.IO.RunID, contracts.FailedStep70, interruptedReasonFromContext(err), err.Error()); appendErr != nil {
						return appendErr
					}
				}
				return nil
			}
			terminal, err := hasTerminalEvent(run.IO, run.IO.RunID)
			if err != nil {
				return err
			}
			if !terminal {
				if err := o.appendState(stepDoneEntry(pr, run.IO.RunID, contracts.FailedStep70, time.Now().UTC())); err != nil {
					return err
				}
			}
			if err := o.appendTerminalDecision(run); err != nil {
				return err
			}
			if err := o.runSingle(ctx, run, contracts.FailedStep70, o.steps.Archive); err != nil {
				return err
			}
			preserveWorktrees = false
		}
		if err := o.loadPersistedArtifacts(run); err != nil {
			return err
		}
	}

	return nil
}
