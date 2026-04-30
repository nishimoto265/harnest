package step70_decide

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/nishimoto265/auto-improve/internal/worktreecleanup"
)

// intentionOwnsRemotePush reports whether any of this intention's planned
// adoption entries are already committed to the registry by THIS run (or by
// any run under this intention's idempotency key). A positive proof means the
// remote's current target_sha was produced by this intention's push, so
// rollback / resume can proceed without risk of undoing another run's
// successful promotion (F11).
//
// The check is based on the planned op-ids (derived from intention's
// idempotency_key). Per io-contracts §idempotency_key, the adopt
// idempotency_key is sha256(run_id || target_sha || best_sha_before ||
// candidates_hash), so a match here uniquely implies this run authored the
// registry row.
func intentionOwnsRemotePush(runCtx internalio.RunContext, intention contracts.IntentionRecord) (bool, error) {
	if intention.PlannedAdoption == nil {
		return false, nil
	}
	matches, err := findPlannedRegistryMatches(runCtx, intention)
	if err != nil {
		// ErrRegistryDivergence here means a planned op-id is present with a
		// mismatched payload — treat as "not ours" and let the caller refresh
		// the snapshot.
		if errors.Is(err, ErrRegistryDivergence) {
			return false, nil
		}
		return false, err
	}
	for _, match := range matches {
		if match != nil {
			return true, nil
		}
	}
	return false, nil
}

// ---- Cleanup ----

func cleanupWorktrees(ctx context.Context, runCtx internalio.RunContext, pkg *contracts.TaskPackage, git GitOps) error {
	return worktreecleanup.Cleanup(ctx, runCtx, pkg, git)
}

func allCandidatesDuplicate(candidates *contracts.Candidates) bool {
	if candidates == nil || len(candidates.Candidates) == 0 {
		return false
	}
	for _, candidate := range candidates.Candidates {
		if candidate.Kind != contracts.CandidateKindDuplicate {
			return false
		}
	}
	return true
}

// ---- Event builders ----

func promotingEvent(pr int, runID contracts.RunID, at time.Time) contracts.StateEntry {
	v := contracts.StateEntryPromoting{Kind: contracts.StateKindPromoting, PR: pr, RunID: runID, Step: contracts.FailedStep70, At: at}
	return contracts.StateEntry{Kind: v.Kind, Value: v}
}

func promotedEvent(pr int, runID contracts.RunID, at time.Time) contracts.StateEntry {
	v := contracts.StateEntryPromoted{Kind: contracts.StateKindPromoted, PR: pr, RunID: runID, Step: contracts.FailedStep70, At: at}
	return contracts.StateEntry{Kind: v.Kind, Value: v}
}

func interruptedEvent(pr int, runID contracts.RunID, reason contracts.InterruptedReason, detail string, at time.Time) contracts.StateEntry {
	v := contracts.StateEntryInterrupted{
		Kind:   contracts.StateKindInterrupted,
		PR:     pr,
		RunID:  runID,
		Step:   contracts.FailedStep70,
		Reason: reason,
		Detail: detail,
		At:     at,
	}
	return contracts.StateEntry{Kind: v.Kind, Value: v}
}

func rollbackEvent(pr int, runID contracts.RunID, reason contracts.RollbackReason, failed contracts.FailedStep, at time.Time) contracts.StateEntry {
	v := contracts.StateEntryRollback{
		Kind:           contracts.StateKindRollback,
		PR:             pr,
		RunID:          runID,
		Step:           contracts.FailedStep70,
		RollbackReason: reason,
		FailedStep:     failed,
		At:             at,
	}
	return contracts.StateEntry{Kind: v.Kind, Value: v}
}

func needsManualRecoveryEvent(pr int, runID contracts.RunID, reason contracts.RollbackReason, failed contracts.FailedStep, detail string, at time.Time) contracts.StateEntry {
	v := contracts.StateEntryNeedsManualRecovery{
		Kind:       contracts.StateKindNeedsManualRecovery,
		PR:         pr,
		RunID:      runID,
		Step:       contracts.FailedStep70,
		Reason:     reason,
		FailedStep: failed,
		Detail:     detail,
		At:         at,
	}
	return contracts.StateEntry{Kind: v.Kind, Value: v}
}

func classifyRulePublishFailureDetail(err error) string {
	if errors.Is(err, errRulePublishConflict) {
		return "rule_publish_conflict"
	}
	if errors.Is(err, errRulePublishIntegrity) {
		return "rule_publish_integrity"
	}
	if errors.Is(err, errRulePublishDestinationType) {
		return "rule_publish_destination_type"
	}
	if errors.Is(err, errRulePublishStagedMissing) {
		return "rule_publish_staged_missing"
	}
	return "rule_publish_failure"
}

func removePathAndSyncParent(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncStagingParentDir(filepath.Dir(path))
}

func removeAllAndSyncParent(path string) error {
	if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(path)
	if _, err := os.Stat(parent); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return syncStagingParentDir(parent)
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func appendStateOnce(runCtx internalio.RunContext, writer state.Writer, kind contracts.StateKind, entry contracts.StateEntry) error {
	events, err := state.ScanEventsForRun(runCtx, runCtx.RunID)
	if err != nil {
		return err
	}
	for _, event := range events {
		if event.Kind == kind {
			return nil
		}
	}
	return writer.Append(entry)
}

func resolveBestShaBefore(ctx context.Context, pkg *contracts.TaskPackage, target Target, deps Deps) (Target, error) {
	if target.BestBranch == "" && pkg != nil {
		target.BestBranch = pkg.BestBranch
	}
	if target.BestBranch == "" {
		return target, nil
	}
	bestShaBefore, err := deps.Git.RemoteHead(ctx, target.BestBranch)
	if err != nil {
		return Target{}, err
	}
	if bestShaBefore == "" {
		return Target{}, fmt.Errorf("step70: best_branch %q has no remote HEAD", target.BestBranch)
	}
	target.BestShaBefore = bestShaBefore
	if target.PolicyOnly && target.TargetSHA == "" {
		target.TargetSHA = bestShaBefore
	}
	return target, nil
}

func remoteHeadMatchesRollbackBase(remoteHead, bestShaBefore string) bool {
	return remoteHead == bestShaBefore || (remoteHead == "" && bestShaBefore == "")
}

func persistedDecisionCanOverride(stage contracts.IntentionStage) bool {
	switch stage {
	case contracts.IntentionStageRegistryAppended,
		contracts.IntentionStagePolicyPublishing,
		contracts.IntentionStagePolicyPublished,
		contracts.IntentionStageDecisionWritten,
		contracts.IntentionStageRollingBackBranchReverted,
		contracts.IntentionStageRollingBackRegistryAppended,
		contracts.IntentionStageRollingBackDecisionWritten:
		return true
	default:
		return false
	}
}

func blockOnOtherRunSentinel(runCtx internalio.RunContext) error {
	return blockedSentinelErr(runCtx, runCtx.RunID)
}

func blockedSentinelErr(runCtx internalio.RunContext, ignoreRunID contracts.RunID) error {
	blocked, reason, err := globalBlockReason(runCtx.RunsBase, ignoreRunID)
	if err != nil {
		return fmt.Errorf("step70: sentinel scan: %w", err)
	}
	if blocked {
		return fmt.Errorf("%w: %s", ErrBlockedBySentinel, reason)
	}
	return nil
}

func rollbackOnOtherRunSentinel(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, intention contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps) (bool, error) {
	if err := blockOnOtherRunSentinel(runCtx); err != nil {
		if !errors.Is(err, ErrBlockedBySentinel) {
			return false, err
		}
		// All call sites are post-push (driveRegistry / driveDecision /
		// appendPlannedRegistryEntries / resumeBranchPushed), so ownership
		// is confirmed.
		return true, handleRollback(ctx, pr, runCtx, pkg, targetFromIntention(pkg, intention), intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, pushOwned)
	}
	return false, nil
}

func validatePersistedDecision(runCtx internalio.RunContext, pkg *contracts.TaskPackage, candidates *contracts.Candidates, decision contracts.Decision) error {
	if pkg == nil {
		return errors.New("step70: task_package is required")
	}
	if candidates == nil {
		return errors.New("step70: candidates are required")
	}
	req := stepio.Step70Request{
		TaskPackage:  *pkg,
		Candidates:   *candidates,
		RegistryPath: runCtx.RulesRegistryPath(),
	}
	promoted := decision.Action == contracts.DecisionActionAdopt
	resp, err := BuildResponse(runCtx.RunID, decision, promoted, req)
	if err != nil {
		return err
	}
	payload, err := contracts.MarshalStrict(resp)
	if err != nil {
		return err
	}
	_, err = stepio.DecodeAndValidateStep70Response(payload, req)
	return err
}

// ---- stepio helpers for callers that operate against request/response ----

// BuildResponse assembles a request-bound Step70Response from the Decision
// written to disk.
func BuildResponse(runID contracts.RunID, decision contracts.Decision, promoted bool, req stepio.Step70Request) (stepio.Step70Response, error) {
	return stepio.NewStep70Response(string(runID), decision, promoted, req)
}
