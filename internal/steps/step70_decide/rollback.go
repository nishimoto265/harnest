package step70_decide

import (
	"context"
	"errors"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
)

type pushOwnership int

const (
	pushUnknown pushOwnership = iota
	pushOwned
)

func handleRollback(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, target Target, intention contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps, reason contracts.RollbackReason, ownership pushOwnership) error {
	intention.RecoveryReason = reason
	intention.FailedStep = contracts.FailedStep70

	remoteHead, err := deps.Git.RemoteHead(ctx, target.BestBranch)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return markManualRecovery(pr, runCtx, intention, store, writer, deps, reason)
	}
	switch {
	case remoteHead == target.TargetSHA:
		// F11: only revert the branch when we can prove this intention owns
		// the remote target_sha. Post-push callers (pushOwnership=pushOwned)
		// have already confirmed ownership via the successful push+stage
		// transition. Pre-push callers (pushOwnership=pushUnknown) must
		// produce proof via a committed registry row under this intention's
		// idempotency_key — without it, another run may have legitimately
		// pushed the same SHA and a force-push back to best_sha_before would
		// silently undo their success. No proof ⇒ manual recovery, never
		// branch rollback.
		if ownership != pushOwned {
			owns, ownErr := intentionOwnsRemotePush(runCtx, intention)
			if ownErr != nil {
				return markManualRecovery(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure)
			}
			if !owns {
				return markManualRecovery(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonRemoteDivergence)
			}
		}
		if err := deps.Git.PushForceWithLease(ctx, target.BestBranch, target.BestShaBefore, target.TargetSHA); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, ErrLeaseFailure) {
				return markManualRecovery(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonLeaseFailure)
			}
			return markManualRecovery(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure)
		}
	case remoteHeadMatchesRollbackBase(remoteHead, target.BestShaBefore):
		// Push never landed; no branch mutation needed.
	default:
		return markManualRecovery(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonRemoteDivergence)
	}

	intention.Stage = contracts.IntentionStageRollingBackBranchReverted
	if err := store.Save(intention); err != nil {
		return err
	}

	rollbackResult, err := appendRegistryRollbacks(runCtx, intention, reason, deps.Now())
	if err != nil {
		return markManualRecovery(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure)
	}
	if rollbackResult != nil {
		intention.RegistryAppendResult = rollbackResult
		intention.Stage = contracts.IntentionStageRollingBackRegistryAppended
		if err := store.Save(intention); err != nil {
			return err
		}
	}

	decision := newRollbackDecision(runCtx, intention, reason, contracts.FailedStep70, deps.Now())
	if err := writeDecision(runCtx, decision); err != nil {
		return err
	}
	intention.Stage = contracts.IntentionStageRollingBackDecisionWritten
	if err := store.Save(intention); err != nil {
		return err
	}
	return finalizeRollbackTerminal(ctx, pr, runCtx, pkg, reason, contracts.FailedStep70, store, writer, deps)
}

func resumeRollback(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, intention contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps) error {
	reason := intention.RecoveryReason
	if reason == "" {
		reason = contracts.RollbackReasonTransactionalFailure
	}

	if intention.Stage == contracts.IntentionStageRollingBackBranchReverted {
		if err := ensureRollbackBranchState(ctx, pr, runCtx, pkg, intention, store, writer, deps); err != nil {
			return err
		}
		result, err := appendRegistryRollbacks(runCtx, intention, reason, deps.Now())
		if err != nil {
			return markManualRecovery(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure)
		}
		if result != nil {
			intention.RegistryAppendResult = result
			intention.Stage = contracts.IntentionStageRollingBackRegistryAppended
			if err := store.Save(intention); err != nil {
				return err
			}
		}
	}

	if intention.Stage == contracts.IntentionStageRollingBackRegistryAppended ||
		intention.Stage == contracts.IntentionStageRollingBackBranchReverted {
		decision := newRollbackDecision(runCtx, intention, reason, contracts.FailedStep70, deps.Now())
		if err := writeDecision(runCtx, decision); err != nil {
			return err
		}
		intention.Stage = contracts.IntentionStageRollingBackDecisionWritten
		if err := store.Save(intention); err != nil {
			return err
		}
	}

	return finalizeRollbackTerminal(ctx, pr, runCtx, pkg, reason, contracts.FailedStep70, store, writer, deps)
}

func finalizeRollbackTerminal(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, reason contracts.RollbackReason, failed contracts.FailedStep, store IntentionWriter, writer state.Writer, deps Deps) error {
	if err := cleanupStagedRuleSidecars(runCtx); err != nil {
		return err
	}
	if err := cleanupWorktrees(ctx, runCtx, pkg, deps.Git); err != nil {
		return err
	}
	if err := store.Delete(); err != nil {
		return err
	}
	return appendStateOnce(runCtx, writer, contracts.StateKindRollback, rollbackEvent(pr, runCtx.RunID, reason, failed, deps.Now()))
}

func ensureRollbackBranchState(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, intention contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps) error {
	target := targetFromIntention(pkg, intention)
	remoteHead, err := deps.Git.RemoteHead(ctx, target.BestBranch)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return markManualRecovery(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure)
	}
	switch {
	case remoteHead == target.TargetSHA:
		// F11: ensureRollbackBranchState is only reached via
		// rolling_back_branch_reverted resume, which means a prior tick
		// already satisfied handleRollback's ownership check before
		// transitioning the intention into rolling_back_*. Ownership is
		// therefore already proven. Still, if concurrent activity advanced
		// registry past planning + we have no committed op-id, treat it as
		// registry divergence instead of blindly force-pushing.
		owns, ownErr := intentionOwnsRemotePush(runCtx, intention)
		if ownErr != nil {
			return markManualRecovery(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure)
		}
		if !owns {
			currentHead, headErr := currentRegistryHead(runCtx.RulesRegistryPath())
			if headErr != nil {
				return markManualRecovery(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure)
			}
			if currentHead != intention.RegistryHeadBefore {
				return markManualRecovery(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonRegistryDivergence)
			}
		}
		if err := deps.Git.PushForceWithLease(ctx, target.BestBranch, target.BestShaBefore, target.TargetSHA); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, ErrLeaseFailure) {
				return markManualRecovery(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonLeaseFailure)
			}
			return markManualRecovery(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure)
		}
	case remoteHeadMatchesRollbackBase(remoteHead, target.BestShaBefore):
		return nil
	default:
		return markManualRecovery(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonRemoteDivergence)
	}
	return nil
}

func markManualRecovery(pr int, runCtx internalio.RunContext, intention contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps, reason contracts.RollbackReason) error {
	return markManualRecoveryWithDetail(pr, runCtx, intention, store, writer, deps, reason, "")
}

func markManualRecoveryWithDetail(pr int, runCtx internalio.RunContext, intention contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps, reason contracts.RollbackReason, detail string) error {
	intention.Stage = contracts.IntentionStageNeedsManualRecovery
	intention.RecoveryReason = reason
	intention.FailedStep = contracts.FailedStep70
	// F9: persist the parked intention first, then record the state event and
	// sentinel as independent global barriers. If one global write fails, the
	// other can still block or reconstruct the barrier on the next tick; the
	// parked intention prevents this run from reopening transaction paths.
	if err := store.Save(intention); err != nil {
		return err
	}
	now := deps.Now()
	stateErr := appendStateOnce(runCtx, writer, contracts.StateKindNeedsManualRecovery, needsManualRecoveryEvent(pr, runCtx.RunID, reason, contracts.FailedStep70, detail, now))
	sentinelErr := writeSentinelFn(runCtx.RunsBase, runCtx.RunID, pr, reason, contracts.FailedStep70, now)
	if stateErr != nil || sentinelErr != nil {
		return errors.Join(ErrNeedsManualRecovery, stateErr, sentinelErr)
	}
	return ErrNeedsManualRecovery
}
