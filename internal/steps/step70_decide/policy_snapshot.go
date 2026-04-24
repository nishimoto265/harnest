package step70_decide

import (
	"context"
	"fmt"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/policyrepo"
	"github.com/nishimoto265/auto-improve/internal/state"
)

var preparePolicyPublish = policyrepo.PrepareSnapshotPublish
var branchSnapshotMatchesLocal = policyrepo.BranchSnapshotMatchesLocal

func policySnapshotPreAdoptBlockReason(ctx context.Context, runCtx internalio.RunContext, deps Deps) (string, error) {
	branch := strings.TrimSpace(deps.PolicyBranch)
	if branch == "" {
		return localPolicySnapshotPreAdoptBlockReason(runCtx)
	}
	meta, ok, err := policySnapshotMetadataForBranch(runCtx, branch)
	if err != nil {
		return "", err
	}
	if !ok {
		return "policy_snapshot_missing", nil
	}
	globalRegistryHead, err := currentRegistryHead(runCtx.RulesRegistryPath())
	if err != nil {
		return "", err
	}
	if globalRegistryHead != meta.RegistryHead {
		return "policy_registry_stale", nil
	}
	policyHead, err := deps.Git.RemoteHead(ctx, branch)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", err
	}
	if policyHead != meta.PolicyHead {
		return "policy_branch_stale", nil
	}
	return "", nil
}

func localPolicySnapshotPreAdoptBlockReason(runCtx internalio.RunContext) (string, error) {
	meta, ok, err := policyrepo.LoadSnapshotMetadata(runCtx)
	if err != nil {
		return "", err
	}
	globalRegistryHead, err := currentRegistryHead(runCtx.RulesRegistryPath())
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	if strings.TrimSpace(meta.PolicyBranch) != "" {
		return "", fmt.Errorf("step70: local policy snapshot has branch metadata: snapshot=%s", meta.PolicyBranch)
	}
	if globalRegistryHead != meta.RegistryHead {
		return "policy_registry_stale", nil
	}
	return "", nil
}

func policySnapshotMetadataForBranch(runCtx internalio.RunContext, branch string) (policyrepo.SnapshotMetadata, bool, error) {
	meta, ok, err := policyrepo.LoadSnapshotMetadata(runCtx)
	if err != nil || !ok {
		return meta, ok, err
	}
	if strings.TrimSpace(meta.PolicyBranch) != strings.TrimSpace(branch) {
		return policyrepo.SnapshotMetadata{}, false, fmt.Errorf("step70: policy snapshot branch mismatch: snapshot=%s config=%s", meta.PolicyBranch, branch)
	}
	return meta, true, nil
}

type PolicySnapshotStaleError struct {
	Reason string
}

func newPolicySnapshotStaleError(reason string) *PolicySnapshotStaleError {
	return &PolicySnapshotStaleError{Reason: reason}
}

func (e *PolicySnapshotStaleError) Error() string {
	if e == nil || e.Reason == "" {
		return "step70: policy snapshot stale"
	}
	return "step70: policy snapshot stale: " + e.Reason
}

func (e *PolicySnapshotStaleError) InterruptedDetail() string {
	if e == nil || e.Reason == "" {
		return "policy_snapshot_stale"
	}
	return "policy_snapshot_stale: " + e.Reason
}

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
