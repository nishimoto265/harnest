package step70_decide

import (
	"context"
	"errors"
	"fmt"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
)

func resume(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, candidates *contracts.Candidates, intention *contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps) error {
	switch intention.Stage {
	case contracts.IntentionStagePlanning:
		target, hasTarget, err := deps.Resolver.Resolve(runCtx, pkg, candidates)
		if err != nil {
			return err
		}
		if hasTarget {
			target, err = resolveBestShaBefore(ctx, pkg, target, deps)
			if err != nil {
				return err
			}
		}
		if restart, err := planningResumeNeedsRefresh(*intention, candidates.CandidatesHash, target, hasTarget); err != nil {
			return err
		} else if restart {
			if err := cleanupStagedRuleSidecars(runCtx); err != nil {
				return err
			}
			if err := store.Delete(); err != nil {
				return err
			}
			if err := appendStateOnce(runCtx, writer, contracts.StateKindInterrupted, interruptedEvent(pr, runCtx.RunID, contracts.InterruptedReasonPrePushCrash, "planning target changed during resume, snapshot refresh required", deps.Now())); err != nil {
				return err
			}
			return startFresh(ctx, pr, runCtx, pkg, candidates, store, writer, deps)
		}
		persistedTarget := targetFromIntention(pkg, *intention)
		if target.BestBranch != "" {
			persistedTarget.BestBranch = target.BestBranch
		}
		if reason, err := policySnapshotPreAdoptBlockReason(ctx, runCtx, deps); err != nil {
			return err
		} else if reason != "" {
			return newPolicySnapshotStaleError(reason)
		}
		return planningDecision(ctx, pr, runCtx, pkg, candidates, persistedTarget, *intention, store, writer, deps)
	case contracts.IntentionStageBranchPushed:
		return resumeBranchPushed(ctx, pr, runCtx, pkg, *intention, store, writer, deps)
	case contracts.IntentionStageRegistryAppended,
		contracts.IntentionStagePolicyPublishing,
		contracts.IntentionStagePolicyPublished:
		return driveDecision(ctx, pr, runCtx, pkg, *intention, store, writer, deps)
	case contracts.IntentionStageDecisionWritten:
		decision, ok, err := loadDecisionIfExists(runCtx)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("step70: decision_written stage requires persisted decision.json")
		}
		return finalizePersistedDecision(ctx, pr, runCtx, pkg, candidates, decision, store, writer, deps)
	case contracts.IntentionStageRollingBackBranchReverted,
		contracts.IntentionStageRollingBackRegistryAppended,
		contracts.IntentionStageRollingBackDecisionWritten:
		return resumeRollback(ctx, pr, runCtx, pkg, *intention, store, writer, deps)
	case contracts.IntentionStageNeedsManualRecovery:
		return ErrNeedsManualRecovery
	default:
		return fmt.Errorf("step70: unknown intention stage=%q", intention.Stage)
	}
}

func resumeBranchPushed(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, intention contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps) error {
	if handled, err := rollbackOnOtherRunSentinel(ctx, pr, runCtx, pkg, intention, store, writer, deps); err != nil {
		return err
	} else if handled {
		return nil
	}
	appendResult, err := appendRegistryEntries(ctx, runCtx, pkg, &intention, store, writer, deps, pr)
	if err != nil {
		if errors.Is(err, errSentinelRollbackHandled) {
			return nil
		}
		if errors.Is(err, ErrRegistryDivergence) {
			return handleRollback(ctx, pr, runCtx, pkg, targetFromIntention(pkg, intention), intention, store, writer, deps, contracts.RollbackReasonRegistryDivergence, pushOwned)
		}
		return handleRollback(ctx, pr, runCtx, pkg, targetFromIntention(pkg, intention), intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, pushOwned)
	}
	intention.RegistryAppendResult = &appendResult
	intention.Stage = contracts.IntentionStageRegistryAppended
	if err := store.Save(intention); err != nil {
		return err
	}
	return driveDecision(ctx, pr, runCtx, pkg, intention, store, writer, deps)
}

func planningDecision(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, candidates *contracts.Candidates, target Target, intention contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps) error {
	if target.PolicyOnly {
		currentHead, err := currentRegistryHead(runCtx.RulesRegistryPath())
		if err != nil {
			return err
		}
		if currentHead == intention.RegistryHeadBefore {
			if err := appendStateOnce(runCtx, writer, contracts.StateKindInterrupted, interruptedEvent(pr, runCtx.RunID, contracts.InterruptedReasonPrePushCrash, "", deps.Now())); err != nil {
				return err
			}
			return driveAdopt(ctx, pr, runCtx, pkg, target, intention, store, writer, deps)
		}
		if err := store.Delete(); err != nil {
			return err
		}
		if err := appendStateOnce(runCtx, writer, contracts.StateKindInterrupted, interruptedEvent(pr, runCtx.RunID, contracts.InterruptedReasonPrePushCrash, "registry advanced during policy-only planning crash, snapshot refresh required", deps.Now())); err != nil {
			return err
		}
		return startFresh(ctx, pr, runCtx, pkg, candidates, store, writer, deps)
	}
	if target.BestBranch == "" && pkg != nil {
		target.BestBranch = pkg.BestBranch
	}
	remoteHead, err := deps.Git.RemoteHead(ctx, target.BestBranch)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return markManualRecovery(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure)
	}
	switch {
	case remoteHead == target.TargetSHA:
		// F11: require proof that this intention owns the current remote SHA
		// before treating the resume as "branch_pushed by us". Without proof,
		// the same SHA may have been produced by a concurrent run that
		// already completed promotion — proceeding here would later drive
		// driveRegistry's rollback path and force-push the branch back to
		// best_sha_before, silently undoing the other run.
		owns, err := intentionOwnsRemotePush(runCtx, intention)
		if err != nil {
			return err
		}
		if owns {
			intention.Stage = contracts.IntentionStageBranchPushed
			if err := store.Save(intention); err != nil {
				return err
			}
			return driveRegistry(ctx, pr, runCtx, pkg, intention, store, writer, deps)
		}
		// No committed op-id match. If the registry head still matches the
		// planning snapshot, our push is the one that landed pre-registry-
		// append (legit crash between push and registry). Otherwise the
		// snapshot is stale: another run advanced registry AND the branch
		// sits at target_sha only because they happened to pick the same
		// commit. Do NOT rollback the branch — refresh and re-plan.
		currentHead, err := currentRegistryHead(runCtx.RulesRegistryPath())
		if err != nil {
			return err
		}
		if currentHead == intention.RegistryHeadBefore {
			intention.Stage = contracts.IntentionStageBranchPushed
			if err := store.Save(intention); err != nil {
				return err
			}
			return driveRegistry(ctx, pr, runCtx, pkg, intention, store, writer, deps)
		}
		if err := store.Delete(); err != nil {
			return err
		}
		if err := appendStateOnce(runCtx, writer, contracts.StateKindInterrupted, interruptedEvent(pr, runCtx.RunID, contracts.InterruptedReasonPrePushCrash, "remote matches target_sha without committed op-id and registry advanced; snapshot refresh required", deps.Now())); err != nil {
			return err
		}
		return startFresh(ctx, pr, runCtx, pkg, candidates, store, writer, deps)
	case remoteHeadMatchesRollbackBase(remoteHead, target.BestShaBefore):
		currentHead, err := currentRegistryHead(runCtx.RulesRegistryPath())
		if err != nil {
			return err
		}
		if currentHead == intention.RegistryHeadBefore {
			if err := appendStateOnce(runCtx, writer, contracts.StateKindInterrupted, interruptedEvent(pr, runCtx.RunID, contracts.InterruptedReasonPrePushCrash, "", deps.Now())); err != nil {
				return err
			}
			return driveAdopt(ctx, pr, runCtx, pkg, target, intention, store, writer, deps)
		}
		if err := store.Delete(); err != nil {
			return err
		}
		if err := appendStateOnce(runCtx, writer, contracts.StateKindInterrupted, interruptedEvent(pr, runCtx.RunID, contracts.InterruptedReasonPrePushCrash, "registry advanced during planning crash, snapshot refresh required", deps.Now())); err != nil {
			return err
		}
		return startFresh(ctx, pr, runCtx, pkg, candidates, store, writer, deps)
	default:
		return markManualRecovery(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonRemoteDivergence)
	}
}
