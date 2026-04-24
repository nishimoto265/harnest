package step70_decide

import (
	"context"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/policyrepo"
	"github.com/nishimoto265/auto-improve/internal/state"
)

var preparePolicyPublish = policyrepo.PrepareSnapshotPublish
var branchSnapshotMatchesLocal = policyrepo.BranchSnapshotMatchesLocal

func drivePolicyPublish(ctx context.Context, pr int, runCtx internalio.RunContext, intention contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps) (contracts.IntentionRecord, error) {
	configuredBranch := strings.TrimSpace(deps.PolicyBranch)
	if configuredBranch == "" {
		if intention.Stage == contracts.IntentionStagePolicyPublishing || intention.Stage == contracts.IntentionStagePolicyPublished {
			return intention, markManualRecoveryWithDetail(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, "policy_branch_config_missing")
		}
		return intention, nil
	}
	if strings.TrimSpace(deps.RepoRoot) == "" {
		return intention, markManualRecoveryWithDetail(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, "policy_repo_root_missing")
	}
	if strings.TrimSpace(intention.PolicyBranch) != "" && strings.TrimSpace(intention.PolicyBranch) != configuredBranch {
		return intention, markManualRecoveryWithDetail(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, "policy_branch_config_mismatch")
	}
	if intention.Stage == contracts.IntentionStagePolicyPublished {
		if intention.PolicyHeadAfter == "" {
			return intention, markManualRecoveryWithDetail(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, "policy_head_after_missing")
		}
		currentHead, err := deps.Git.RemoteHead(ctx, configuredBranch)
		if err != nil {
			if ctx.Err() != nil {
				return intention, ctx.Err()
			}
			return intention, markManualRecoveryWithDetail(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, "policy_remote_head_failure")
		}
		if currentHead != intention.PolicyHeadAfter {
			return intention, markManualRecoveryWithDetail(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, "policy_branch_post_publish_stale")
		}
		return intention, nil
	}
	if intention.PolicyHeadBefore == "" {
		meta, ok, err := policySnapshotMetadataForBranch(runCtx, configuredBranch)
		if err != nil {
			return intention, markManualRecoveryWithDetail(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, "policy_snapshot_metadata_failure")
		}
		if !ok {
			return intention, markManualRecoveryWithDetail(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, "policy_snapshot_missing")
		}
		intention.PolicyBranch = configuredBranch
		intention.PolicyHeadBefore = meta.PolicyHead
	}
	if intention.Stage != contracts.IntentionStagePolicyPublishing {
		intention.Stage = contracts.IntentionStagePolicyPublishing
		if err := store.Save(intention); err != nil {
			return intention, err
		}
	}
	policyHeadBefore, err := deps.Git.RemoteHead(ctx, configuredBranch)
	if err != nil {
		if ctx.Err() != nil {
			return intention, ctx.Err()
		}
		return intention, markManualRecoveryWithDetail(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, "policy_remote_head_failure")
	}
	if intention.PolicyHeadAfter != "" {
		switch policyHeadBefore {
		case intention.PolicyHeadAfter:
			intention.Stage = contracts.IntentionStagePolicyPublished
			if err := store.Save(intention); err != nil {
				return intention, err
			}
			return intention, nil
		case intention.PolicyHeadBefore:
			plan, err := preparePolicyPublish(ctx, deps.RepoRoot, configuredBranch, intention.PolicyHeadBefore, runCtx.RunsBase, string(runCtx.RunID))
			if err != nil {
				if ctx.Err() != nil {
					return intention, ctx.Err()
				}
				return intention, markManualRecoveryWithDetail(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, "policy_publish_failure")
			}
			defer func() {
				_ = plan.Cleanup()
			}()
			if plan.Head != intention.PolicyHeadAfter {
				return intention, markManualRecoveryWithDetail(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, "policy_publish_plan_mismatch")
			}
			if err := plan.Push(ctx); err != nil {
				if ctx.Err() != nil {
					return intention, ctx.Err()
				}
				return intention, markManualRecoveryWithDetail(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, "policy_publish_failure")
			}
			intention.Stage = contracts.IntentionStagePolicyPublished
			if err := store.Save(intention); err != nil {
				return intention, err
			}
			return intention, nil
		default:
			return intention, markManualRecoveryWithDetail(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, "policy_branch_post_publish_stale")
		}
	}
	if policyHeadBefore != intention.PolicyHeadBefore {
		matches, err := branchSnapshotMatchesLocal(ctx, deps.RepoRoot, configuredBranch, runCtx.RunsBase)
		if err != nil {
			if ctx.Err() != nil {
				return intention, ctx.Err()
			}
			return intention, markManualRecoveryWithDetail(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, "policy_publish_probe_failure")
		}
		if matches {
			intention.PolicyHeadAfter = policyHeadBefore
			intention.Stage = contracts.IntentionStagePolicyPublished
			if err := store.Save(intention); err != nil {
				return intention, err
			}
			return intention, nil
		}
		return intention, markManualRecoveryWithDetail(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, "policy_branch_stale_or_untracked_publish")
	}
	plan, err := preparePolicyPublish(ctx, deps.RepoRoot, configuredBranch, intention.PolicyHeadBefore, runCtx.RunsBase, string(runCtx.RunID))
	if err != nil {
		if ctx.Err() != nil {
			return intention, ctx.Err()
		}
		return intention, markManualRecoveryWithDetail(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, "policy_publish_failure")
	}
	defer func() {
		_ = plan.Cleanup()
	}()
	intention.PolicyHeadAfter = plan.Head
	if err := store.Save(intention); err != nil {
		return intention, err
	}
	if err := plan.Push(ctx); err != nil {
		if ctx.Err() != nil {
			return intention, ctx.Err()
		}
		return intention, markManualRecoveryWithDetail(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, "policy_publish_failure")
	}
	intention.Stage = contracts.IntentionStagePolicyPublished
	if err := store.Save(intention); err != nil {
		return intention, err
	}
	return intention, nil
}
