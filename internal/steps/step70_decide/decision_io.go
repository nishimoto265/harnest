package step70_decide

import (
	"context"
	"errors"
	"os"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

func writeNoop(ctx context.Context, runCtx internalio.RunContext, pkg *contracts.TaskPackage, deps Deps) error {
	decision := contracts.Decision{
		Action: contracts.DecisionActionNoop,
		Value: contracts.DecisionNoop{
			Action:        contracts.DecisionActionNoop,
			SchemaVersion: "1",
			RunID:         runCtx.RunID,
			Reason:        "no_candidates",
			DecidedAt:     deps.Now(),
		},
	}
	if err := writeDecision(runCtx, decision); err != nil {
		return err
	}
	return cleanupWorktrees(ctx, runCtx, pkg, deps.Git)
}

func writeReject(ctx context.Context, runCtx internalio.RunContext, pkg *contracts.TaskPackage, reason string, deps Deps) error {
	decision := contracts.Decision{
		Action: contracts.DecisionActionReject,
		Value: contracts.DecisionReject{
			Action:        contracts.DecisionActionReject,
			SchemaVersion: "1",
			RunID:         runCtx.RunID,
			Reason:        reason,
			DecidedAt:     deps.Now(),
		},
	}
	if err := writeDecision(runCtx, decision); err != nil {
		return err
	}
	return cleanupWorktrees(ctx, runCtx, pkg, deps.Git)
}

func writeDecision(runCtx internalio.RunContext, decision contracts.Decision) error {
	path, err := runCtx.ResolveRunRelative("70/decision.json")
	if err != nil {
		return err
	}
	return internalio.WriteJSONAtomic(path, decision)
}

func loadDecisionIfExists(runCtx internalio.RunContext) (contracts.Decision, bool, error) {
	path, err := runCtx.ResolveRunRelative("70/decision.json")
	if err != nil {
		return contracts.Decision{}, false, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return contracts.Decision{}, false, nil
		}
		return contracts.Decision{}, false, err
	}
	decision, err := internalio.ReadJSON[contracts.Decision](path)
	if err != nil {
		return contracts.Decision{}, false, err
	}
	return decision, true, nil
}

// ---- Git push staging ----

func pushBranch(ctx context.Context, target Target, deps Deps) error {
	if target.PolicyOnly && target.TargetSHA == target.BestShaBefore {
		return nil
	}
	return deps.Git.PushForceWithLease(ctx, target.BestBranch, target.TargetSHA, target.BestShaBefore)
}

func classifyPushErr(err error) contracts.RollbackReason {
	if errors.Is(err, ErrLeaseFailure) {
		return contracts.RollbackReasonLeaseFailure
	}
	if errors.Is(err, ErrRemoteDivergence) {
		return contracts.RollbackReasonRemoteDivergence
	}
	return contracts.RollbackReasonTransactionalFailure
}
