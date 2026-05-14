package step70_decide

import (
	"context"
	"fmt"
	"strings"

	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/policyrepo"
)

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
