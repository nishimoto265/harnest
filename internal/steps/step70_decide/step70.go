// Package step70_decide implements the step70 staged transaction described in
// docs/design/io-contracts.md §step70.
//
// Invariants (load-bearing):
//   - Every mutation happens while the <runs_base>/promotion.lock flock is
//     held. state.lock is only acquired while promotion.lock is held — never
//     the reverse (lock order: promotion.lock -> state.lock).
//   - Needs-recovery sentinels are scanned before acquiring promotion.lock so
//     the gate wins races against tick-level retries that just cleared a
//     sentinel.
package step70_decide

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
)

var appendRegistryEntry = internalio.AppendRegistryEntry
var promoteRuleSidecarFn = promoteRuleSidecar
var syncStagingParentDir = syncDir
var writeSentinelFn = writeSentinel
var promoteRuleSidecarBeforeDestinationRead = func(string) {}

var errRulePublishConflict = errors.New("step70: canonical rule sidecar conflict")
var errRulePublishIntegrity = errors.New("step70: canonical rule sidecar integrity failure")
var errRulePublishDestinationType = errors.New("step70: canonical rule sidecar destination type mismatch")
var errRulePublishStagedMissing = errors.New("step70: canonical rule sidecar staged file missing")
var errMissingPlannedAdoptionForStaging = errors.New("step70: staged rule sidecars exist without persisted planned_adoption")

// TargetResolver is injected by the caller (orchestrator) to derive the
// promotion target (best candidate head) from candidates + manifests.
//
// Returning ok=false means "no adopt target". step70 emits noop when there are
// zero candidates and reject when candidates exist but the rubric gate rejects
// them.
type TargetResolver interface {
	Resolve(runCtx internalio.RunContext, pkg *contracts.TaskPackage, candidates *contracts.Candidates) (Target, bool, error)
}

// Target carries the resolver's pick for an adopt transaction.
type Target struct {
	BestBranch    string
	BestShaBefore string
	TargetSHA     string
	// RulesToAppend is the list of rules-registry.jsonl entries to append as
	// part of this adoption. Chain fields are derived at append time.
	RulesToAppend []contracts.RuleRegistryEntry
}

// NoopResolver always returns ok=false, forcing a noop or reject decision.
type NoopResolver struct{}

func (NoopResolver) Resolve(internalio.RunContext, *contracts.TaskPackage, *contracts.Candidates) (Target, bool, error) {
	return Target{}, false, nil
}

// Deps bundles the injectable collaborators used by Run. Fields left zero are
// replaced with safe defaults.
type Deps struct {
	Git            GitOps
	Resolver       TargetResolver
	Now            func() time.Time
	RegistryHighAt int
	RegistryCritAt int
	RepoRoot       string
	PolicyBranch   string
}

// IntentionWriter is the minimal subset of orchestrator.IntentionStore used by
// step70.
type IntentionWriter interface {
	Load() (*contracts.IntentionRecord, error)
	Save(contracts.IntentionRecord) error
	Delete() error
}

// Run executes the step70 staged transaction for a single PR run. It owns the
// promotion.lock for its full duration.
func Run(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, candidates *contracts.Candidates, store IntentionWriter, deps Deps) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if pkg == nil {
		return errors.New("step70: task_package is required")
	}
	if candidates == nil {
		return errors.New("step70: candidates are required")
	}
	if err := pkg.Validate(); err != nil {
		return fmt.Errorf("step70: task_package invalid: %w", err)
	}
	if pkg.RunID != runCtx.RunID {
		return fmt.Errorf("step70: task_package run_id mismatch: task_package=%s io=%s", pkg.RunID, runCtx.RunID)
	}
	if err := candidates.Validate(); err != nil {
		return fmt.Errorf("step70: candidates invalid: %w", err)
	}
	if candidates.RunID != runCtx.RunID {
		return fmt.Errorf("step70: candidates run_id mismatch: candidates=%s io=%s", candidates.RunID, runCtx.RunID)
	}
	deps = applyDepDefaults(deps)

	if blocked, reason, err := globalBlockReason(runCtx.RunsBase, ""); err != nil {
		return fmt.Errorf("step70: sentinel scan: %w", err)
	} else if blocked {
		return fmt.Errorf("%w: %s", ErrBlockedBySentinel, reason)
	}

	lock, err := internalio.AcquirePromotionLock(runCtx)
	if err != nil {
		return fmt.Errorf("step70: acquire promotion.lock: %w", err)
	}
	defer func() {
		_ = lock.Unlock()
	}()

	writer := state.NewWriter(runCtx)

	if blocked, reason, err := globalBlockReason(runCtx.RunsBase, ""); err != nil {
		return err
	} else if blocked {
		return fmt.Errorf("%w: %s", ErrBlockedBySentinel, reason)
	}

	intention, err := store.Load()
	if err != nil {
		return err
	}
	if intention != nil {
		if persistedDecisionCanOverride(intention.Stage) {
			decision, ok, err := loadDecisionIfExists(runCtx)
			if err != nil {
				return err
			}
			if ok {
				return finalizePersistedDecision(ctx, pr, runCtx, pkg, candidates, decision, store, writer, deps)
			}
		}
		return resume(ctx, pr, runCtx, pkg, candidates, intention, store, writer, deps)
	}

	decision, ok, err := loadDecisionIfExists(runCtx)
	if err != nil {
		return err
	}
	if ok {
		return finalizePersistedDecision(ctx, pr, runCtx, pkg, candidates, decision, store, writer, deps)
	}

	return startFresh(ctx, pr, runCtx, pkg, candidates, store, writer, deps)
}

func applyDepDefaults(d Deps) Deps {
	if d.Git == nil {
		d.Git = NoopGitOps{}
	}
	if d.Resolver == nil {
		d.Resolver = NoopResolver{}
	}
	if d.Now == nil {
		d.Now = func() time.Time { return time.Now().UTC() }
	}
	if d.RegistryHighAt == 0 {
		d.RegistryHighAt = 1500
	}
	if d.RegistryCritAt == 0 {
		d.RegistryCritAt = 2000
	}
	return d
}

// ---- Fresh planning ----

func startFresh(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, candidates *contracts.Candidates, store IntentionWriter, writer state.Writer, deps Deps) error {
	target, hasTarget, err := deps.Resolver.Resolve(runCtx, pkg, candidates)
	if err != nil {
		return err
	}
	if !hasTarget {
		if len(candidates.Candidates) == 0 || allCandidatesDuplicate(candidates) {
			return writeNoop(ctx, runCtx, pkg, deps)
		}
		return writeReject(ctx, runCtx, pkg, "below_threshold", deps)
	}
	if reason, err := policySnapshotPreAdoptBlockReason(ctx, runCtx, deps); err != nil {
		return err
	} else if reason != "" {
		return newPolicySnapshotStaleError(reason)
	}
	target, err = resolveBestShaBefore(ctx, pkg, target, deps)
	if err != nil {
		return err
	}

	registryHead, err := currentRegistryHead(runCtx.RulesRegistryPath())
	if err != nil {
		return err
	}
	idempotencyKey := contracts.ComputeAdoptIdempotencyKey(string(runCtx.RunID), target.TargetSHA, target.BestShaBefore, candidates.CandidatesHash)
	plannedAdoption, err := plannedAdoptionFromRegistryEntries(idempotencyKey, target.RulesToAppend)
	if err != nil {
		return err
	}
	now := deps.Now()
	intention := contracts.IntentionRecord{
		SchemaVersion:      "1",
		Stage:              contracts.IntentionStagePlanning,
		IdempotencyKey:     idempotencyKey,
		RunID:              runCtx.RunID,
		BestShaBefore:      target.BestShaBefore,
		TargetSha:          target.TargetSHA,
		CandidatesHash:     candidates.CandidatesHash,
		RegistryHeadBefore: registryHead,
		PlannedAdoption:    plannedAdoption,
		StartedAt:          now,
	}
	if err := store.Save(intention); err != nil {
		return err
	}
	if err := appendStateOnce(runCtx, writer, contracts.StateKindPromoting, promotingEvent(pr, runCtx.RunID, now)); err != nil {
		return err
	}
	return driveAdopt(ctx, pr, runCtx, pkg, target, intention, store, writer, deps)
}

func driveAdopt(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, target Target, intention contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := blockOnOtherRunSentinel(runCtx); err != nil {
		return err
	}
	if err := pushBranch(ctx, target, deps); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return handleRollback(ctx, pr, runCtx, pkg, target, intention, store, writer, deps, classifyPushErr(err), pushUnknown)
	}
	intention.Stage = contracts.IntentionStageBranchPushed
	if err := store.Save(intention); err != nil {
		return err
	}
	return driveRegistry(ctx, pr, runCtx, pkg, intention, store, writer, deps)
}

func driveRegistry(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, intention contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
		reason := contracts.RollbackReasonTransactionalFailure
		if errors.Is(err, ErrRegistryDivergence) {
			reason = contracts.RollbackReasonRegistryDivergence
		}
		return handleRollback(ctx, pr, runCtx, pkg, targetFromIntention(pkg, intention), intention, store, writer, deps, reason, pushOwned)
	}
	intention.RegistryAppendResult = &appendResult
	intention.Stage = contracts.IntentionStageRegistryAppended
	if err := store.Save(intention); err != nil {
		return err
	}
	return driveDecision(ctx, pr, runCtx, pkg, intention, store, writer, deps)
}

func driveDecision(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, intention contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps) error {
	if handled, err := rollbackOnOtherRunSentinel(ctx, pr, runCtx, pkg, intention, store, writer, deps); err != nil {
		return err
	} else if handled {
		return nil
	}
	if err := verifyPlannedRegistryAppendProof(runCtx, intention); err != nil {
		return markManualRecoveryWithDetail(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonRegistryDivergence, "decision_registry_mismatch")
	}
	if err := promoteStagedRuleSidecars(runCtx, &intention, store); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return markManualRecoveryWithDetail(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, classifyRulePublishFailureDetail(err))
	}
	var err error
	intention, err = drivePolicyPublish(ctx, pr, runCtx, intention, store, writer, deps)
	if err != nil {
		return err
	}
	now := deps.Now()
	decision := contracts.Decision{
		Action: contracts.DecisionActionAdopt,
		Value: contracts.DecisionAdopt{
			Action:               contracts.DecisionActionAdopt,
			SchemaVersion:        "1",
			RunID:                runCtx.RunID,
			IdempotencyKey:       intention.IdempotencyKey,
			BestShaBefore:        intention.BestShaBefore,
			TargetSha:            intention.TargetSha,
			CandidatesHash:       intention.CandidatesHash,
			RegistryAppendResult: *intention.RegistryAppendResult,
			DecidedAt:            now,
		},
	}
	if err := writeDecision(runCtx, decision); err != nil {
		return err
	}
	intention.Stage = contracts.IntentionStageDecisionWritten
	if err := store.Save(intention); err != nil {
		return err
	}
	return finalizeAfterDecision(ctx, pr, runCtx, pkg, store, writer, deps, nil)
}

func finalizeAfterDecision(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, store IntentionWriter, writer state.Writer, deps Deps, adopt *contracts.DecisionAdopt) error {
	if err := blockOnOtherRunSentinel(runCtx); err != nil {
		return err
	}
	intention, err := store.Load()
	if err != nil {
		return err
	}
	if adopt != nil {
		if err := verifyPersistedAdoptDecisionState(ctx, pr, runCtx, pkg, intention, *adopt, store, writer, deps); err != nil {
			return err
		}
	}
	if err := promoteStagedRuleSidecars(runCtx, intention, store); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if intention != nil {
			return markManualRecoveryWithDetail(pr, runCtx, *intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, classifyRulePublishFailureDetail(err))
		}
		return err
	}
	if err := cleanupWorktrees(ctx, runCtx, pkg, deps.Git); err != nil {
		return err
	}
	if err := store.Delete(); err != nil {
		return err
	}
	return appendStateOnce(runCtx, writer, contracts.StateKindPromoted, promotedEvent(pr, runCtx.RunID, deps.Now()))
}

func finalizePersistedDecision(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, candidates *contracts.Candidates, decision contracts.Decision, store IntentionWriter, writer state.Writer, deps Deps) error {
	if err := validatePersistedDecision(runCtx, pkg, candidates, decision); err != nil {
		return err
	}
	switch v := decision.Value.(type) {
	case contracts.DecisionAdopt:
		return finalizeAfterDecision(ctx, pr, runCtx, pkg, store, writer, deps, &v)
	case *contracts.DecisionAdopt:
		if v == nil {
			return nil
		}
		return finalizeAfterDecision(ctx, pr, runCtx, pkg, store, writer, deps, v)
	case contracts.DecisionRollback:
		return finalizeRollbackTerminal(ctx, pr, runCtx, pkg, v.RollbackReason, v.FailedStep, store, writer, deps)
	case *contracts.DecisionRollback:
		if v == nil {
			return nil
		}
		return finalizeRollbackTerminal(ctx, pr, runCtx, pkg, v.RollbackReason, v.FailedStep, store, writer, deps)
	default:
		return cleanupWorktrees(ctx, runCtx, pkg, deps.Git)
	}
}

func verifyPersistedAdoptDecisionState(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, intention *contracts.IntentionRecord, adopt contracts.DecisionAdopt, store IntentionWriter, writer state.Writer, deps Deps) error {
	bestBranch := ""
	if intention != nil {
		bestBranch = targetFromIntention(pkg, *intention).BestBranch
	}
	if bestBranch == "" && pkg != nil {
		bestBranch = pkg.BestBranch
	}
	if bestBranch == "" {
		return errors.New("step70: decision_written adopt requires best_branch")
	}
	recoveryIntention := persistedAdoptRecoveryIntention(runCtx.RunID, intention, adopt)
	remoteHead, err := deps.Git.RemoteHead(ctx, bestBranch)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return markManualRecoveryWithDetail(pr, runCtx, recoveryIntention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, "decision_written_remote_head_failure")
	}
	if remoteHead != adopt.TargetSha {
		return markManualRecoveryWithDetail(pr, runCtx, recoveryIntention, store, writer, deps, contracts.RollbackReasonRemoteDivergence, "decision_written_remote_mismatch")
	}
	exists, err := registryPromotionAppendResultExists(runCtx, adopt.RegistryAppendResult, adopt.RunID)
	if err != nil {
		return markManualRecoveryWithDetail(pr, runCtx, recoveryIntention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, "decision_written_registry_probe_failure")
	}
	if !exists {
		return markManualRecoveryWithDetail(pr, runCtx, recoveryIntention, store, writer, deps, contracts.RollbackReasonRegistryDivergence, "decision_written_registry_mismatch")
	}
	if intention != nil && strings.TrimSpace(intention.PolicyBranch) != "" && intention.PolicyHeadAfter != "" {
		policyBranch := strings.TrimSpace(intention.PolicyBranch)
		policyHead, err := deps.Git.RemoteHead(ctx, policyBranch)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return markManualRecoveryWithDetail(pr, runCtx, recoveryIntention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, "decision_written_policy_remote_head_failure")
		}
		if policyHead != intention.PolicyHeadAfter {
			return markManualRecoveryWithDetail(pr, runCtx, recoveryIntention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, "decision_written_policy_branch_stale")
		}
	}
	return nil
}

func persistedAdoptRecoveryIntention(runID contracts.RunID, intention *contracts.IntentionRecord, adopt contracts.DecisionAdopt) contracts.IntentionRecord {
	if intention != nil {
		return *intention
	}
	return contracts.IntentionRecord{
		SchemaVersion:        "1",
		Stage:                contracts.IntentionStageDecisionWritten,
		IdempotencyKey:       adopt.IdempotencyKey,
		RunID:                runID,
		BestShaBefore:        adopt.BestShaBefore,
		TargetSha:            adopt.TargetSha,
		CandidatesHash:       adopt.CandidatesHash,
		RegistryAppendResult: &adopt.RegistryAppendResult,
		StartedAt:            adopt.DecidedAt,
	}
}
