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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
)

const registryMandatoryIndexAt = 1800

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
			return writeNoop(runCtx, pkg, deps)
		}
		return writeReject(runCtx, pkg, "below_threshold", deps)
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
	if err := abortOnOtherRunSentinel(runCtx, store); err != nil {
		return err
	}
	if err := pushBranch(ctx, target, deps); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return handleRollback(ctx, pr, runCtx, pkg, target, intention, store, writer, deps, classifyPushErr(err))
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
		return handleRollback(ctx, pr, runCtx, pkg, targetFromIntention(pkg, intention), intention, store, writer, deps, reason)
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
	if err := promoteStagedRuleSidecars(runCtx, &intention); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return markManualRecoveryWithDetail(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure, classifyRulePublishFailureDetail(err))
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
	return finalizeAfterDecision(ctx, pr, runCtx, pkg, store, writer, deps)
}

func finalizeAfterDecision(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, store IntentionWriter, writer state.Writer, deps Deps) error {
	if err := blockOnOtherRunSentinel(runCtx); err != nil {
		return err
	}
	intention, err := store.Load()
	if err != nil {
		return err
	}
	if err := promoteStagedRuleSidecars(runCtx, intention); err != nil {
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
		return finalizeAfterDecision(ctx, pr, runCtx, pkg, store, writer, deps)
	case *contracts.DecisionAdopt:
		if v == nil {
			return nil
		}
		return finalizeAfterDecision(ctx, pr, runCtx, pkg, store, writer, deps)
	case contracts.DecisionRollback:
		if err := appendStateOnce(runCtx, writer, contracts.StateKindRollback, rollbackEvent(pr, runCtx.RunID, v.RollbackReason, v.FailedStep, deps.Now())); err != nil {
			return err
		}
		_ = store.Delete()
		return nil
	case *contracts.DecisionRollback:
		if v == nil {
			return nil
		}
		if err := appendStateOnce(runCtx, writer, contracts.StateKindRollback, rollbackEvent(pr, runCtx.RunID, v.RollbackReason, v.FailedStep, deps.Now())); err != nil {
			return err
		}
		_ = store.Delete()
		return nil
	default:
		return cleanupWorktrees(ctx, runCtx, pkg, deps.Git)
	}
}

// ---- Rollback ----

func handleRollback(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, target Target, intention contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps, reason contracts.RollbackReason) error {
	intention.Stage = contracts.IntentionStageRollingBackBranchReverted
	intention.RecoveryReason = reason
	intention.FailedStep = contracts.FailedStep70
	if err := store.Save(intention); err != nil {
		return err
	}

	remoteHead, err := deps.Git.RemoteHead(ctx, target.BestBranch)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return markManualRecovery(pr, runCtx, intention, store, writer, deps, reason)
	}
	switch {
	case remoteHead == target.TargetSHA:
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

	decision := contracts.Decision{
		Action: contracts.DecisionActionRollback,
		Value: contracts.DecisionRollback{
			Action:         contracts.DecisionActionRollback,
			SchemaVersion:  "1",
			RunID:          runCtx.RunID,
			IdempotencyKey: intention.IdempotencyKey,
			RollbackReason: reason,
			FailedStep:     contracts.FailedStep70,
			BestShaBefore:  intention.BestShaBefore,
			TargetSha:      intention.TargetSha,
			DecidedAt:      deps.Now(),
		},
	}
	if err := writeDecision(runCtx, decision); err != nil {
		return err
	}
	intention.Stage = contracts.IntentionStageRollingBackDecisionWritten
	if err := store.Save(intention); err != nil {
		return err
	}
	if err := appendStateOnce(runCtx, writer, contracts.StateKindRollback, rollbackEvent(pr, runCtx.RunID, reason, contracts.FailedStep70, deps.Now())); err != nil {
		return err
	}
	if err := cleanupStagedRuleSidecars(runCtx); err != nil {
		return err
	}
	return store.Delete()
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
		decision := contracts.Decision{
			Action: contracts.DecisionActionRollback,
			Value: contracts.DecisionRollback{
				Action:         contracts.DecisionActionRollback,
				SchemaVersion:  "1",
				RunID:          runCtx.RunID,
				IdempotencyKey: intention.IdempotencyKey,
				RollbackReason: reason,
				FailedStep:     contracts.FailedStep70,
				BestShaBefore:  intention.BestShaBefore,
				TargetSha:      intention.TargetSha,
				DecidedAt:      deps.Now(),
			},
		}
		if err := writeDecision(runCtx, decision); err != nil {
			return err
		}
		intention.Stage = contracts.IntentionStageRollingBackDecisionWritten
		if err := store.Save(intention); err != nil {
			return err
		}
	}

	if err := appendStateOnce(runCtx, writer, contracts.StateKindRollback, rollbackEvent(pr, runCtx.RunID, reason, contracts.FailedStep70, deps.Now())); err != nil {
		return err
	}
	if err := cleanupStagedRuleSidecars(runCtx); err != nil {
		return err
	}
	return store.Delete()
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
	// F9: persist the intention transition to needs_manual_recovery BEFORE
	// writing the durable sentinel. If the sentinel write later fails, the
	// intention is already parked at a stage that resume() treats as
	// terminal-but-persisted (returns ErrNeedsManualRecovery and refuses to
	// reopen transaction paths), so the operator-gate barrier holds even
	// without the sentinel. The inverse ordering (sentinel-first) would leave
	// a durable needs_manual_recovery file with an intention still at
	// branch_pushed / registry_appended; once the operator cleared the
	// sentinel, a subsequent tick would resume the old transaction path
	// instead of re-blocking — a silent bypass of the manual-recovery barrier.
	if err := store.Save(intention); err != nil {
		return err
	}
	if err := writeSentinelFn(runCtx.RunsBase, runCtx.RunID, pr, reason, contracts.FailedStep70, deps.Now()); err != nil {
		return err
	}
	if err := appendStateOnce(runCtx, writer, contracts.StateKindNeedsManualRecovery, needsManualRecoveryEvent(pr, runCtx.RunID, reason, contracts.FailedStep70, detail, deps.Now())); err != nil {
		return err
	}
	return ErrNeedsManualRecovery
}

// ---- Noop / reject / decision helpers ----

func writeNoop(runCtx internalio.RunContext, pkg *contracts.TaskPackage, deps Deps) error {
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
	return cleanupWorktrees(context.Background(), runCtx, pkg, deps.Git)
}

func writeReject(runCtx internalio.RunContext, pkg *contracts.TaskPackage, reason string, deps Deps) error {
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
	return cleanupWorktrees(context.Background(), runCtx, pkg, deps.Git)
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

// ---- Registry append / idempotency ----

type registryLine = internalio.RegistryLine

type plannedRegistryMatch struct {
	EntryIndex int
	OpID       string
	Result     contracts.RegistryAppendResult
}

func findPlannedRegistryMatches(runCtx internalio.RunContext, intention contracts.IntentionRecord) ([]*plannedRegistryMatch, error) {
	if intention.PlannedAdoption == nil {
		return nil, contracts.ErrIntentionMissingPlannedAdoption
	}
	if err := intention.PlannedAdoption.Validate(intention.IdempotencyKey); err != nil {
		return nil, err
	}
	lines, err := registryLookupLines(runCtx)
	if err != nil {
		return nil, err
	}
	matches := make([]*plannedRegistryMatch, len(intention.PlannedAdoption.Entries))
	wanted := make(map[string]int, len(intention.PlannedAdoption.Entries))
	for idx, entry := range intention.PlannedAdoption.Entries {
		wanted[entry.OpID] = idx
	}
	for i := len(lines) - 1; i >= 0; i-- {
		switch v := lines[i].Entry.Value.(type) {
		case contracts.RuleRegistryAdded:
			if idx, ok := wanted[v.IdempotencyKey]; ok {
				if err := plannedRegistryEntryMatches(intention.PlannedAdoption.Entries[idx], lines[i].Entry); err != nil {
					return nil, err
				}
				if matches[idx] != nil {
					continue
				}
				matches[idx] = &plannedRegistryMatch{
					EntryIndex: idx,
					OpID:       v.IdempotencyKey,
					Result:     contracts.RegistryAppendResult{Offset: lines[i].Offset, Sha256: lines[i].Sha256},
				}
			}
		case contracts.RuleRegistryUpdated:
			if idx, ok := wanted[v.IdempotencyKey]; ok {
				if err := plannedRegistryEntryMatches(intention.PlannedAdoption.Entries[idx], lines[i].Entry); err != nil {
					return nil, err
				}
				if matches[idx] != nil {
					continue
				}
				matches[idx] = &plannedRegistryMatch{
					EntryIndex: idx,
					OpID:       v.IdempotencyKey,
					Result:     contracts.RegistryAppendResult{Offset: lines[i].Offset, Sha256: lines[i].Sha256},
				}
			}
		}
	}
	return matches, nil
}

func plannedRegistryEntryMatches(planned contracts.PlannedAdoptionEntry, entry contracts.RuleRegistryEntry) error {
	switch v := entry.Value.(type) {
	case contracts.RuleRegistryAdded:
		if planned.Kind != contracts.RegistryKindAdded || planned.RuleID != v.RuleID || planned.RulePath != v.RulePath || planned.Sha256 != v.Sha256 || planned.PrevSha256 != "" {
			return ErrRegistryDivergence
		}
	case contracts.RuleRegistryUpdated:
		if planned.Kind != contracts.RegistryKindUpdated || planned.RuleID != v.RuleID || planned.RulePath != v.RulePath || planned.Sha256 != v.Sha256 || planned.PrevSha256 != v.PrevSha256 {
			return ErrRegistryDivergence
		}
	default:
		return ErrRegistryDivergence
	}
	return nil
}

func completePlannedRegistryMatches(matches []*plannedRegistryMatch) (contracts.RegistryAppendResult, bool) {
	if len(matches) == 0 {
		return contracts.RegistryAppendResult{}, false
	}
	var last contracts.RegistryAppendResult
	for _, match := range matches {
		if match == nil {
			return contracts.RegistryAppendResult{}, false
		}
		last = match.Result
	}
	return last, true
}

func resumeIndexForPlannedRegistryEntries(intention contracts.IntentionRecord, matches []*plannedRegistryMatch, currentHead string) (int, error) {
	prefixLen, contiguous := plannedRegistryPrefixLen(matches)
	if !contiguous {
		return 0, ErrRegistryDivergence
	}
	if prefixLen == 0 {
		if currentHead != intention.RegistryHeadBefore {
			return 0, ErrRegistryDivergence
		}
		return 0, nil
	}
	if currentHead != matches[prefixLen-1].Result.Sha256 {
		return 0, ErrRegistryDivergence
	}
	return prefixLen, nil
}

func plannedRegistryPrefixLen(matches []*plannedRegistryMatch) (int, bool) {
	prefixLen := 0
	for prefixLen < len(matches) && matches[prefixLen] != nil {
		prefixLen++
	}
	for i := prefixLen; i < len(matches); i++ {
		if matches[i] != nil {
			return prefixLen, false
		}
	}
	return prefixLen, true
}

func committedPromotionEntries(runCtx internalio.RunContext, intention contracts.IntentionRecord) ([]plannedRegistryMatch, error) {
	matches, err := findPlannedRegistryMatches(runCtx, intention)
	if err != nil {
		return nil, err
	}
	committed := make([]plannedRegistryMatch, 0, len(matches))
	for _, match := range matches {
		if match != nil {
			committed = append(committed, *match)
		}
	}
	return committed, nil
}

func appendRegistryEntries(ctx context.Context, runCtx internalio.RunContext, pkg *contracts.TaskPackage, intention *contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps, pr int) (contracts.RegistryAppendResult, error) {
	matches, err := findPlannedRegistryMatches(runCtx, *intention)
	if err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	if existing, ok := completePlannedRegistryMatches(matches); ok {
		if err := emitRegistrySizeWarnings(runCtx, writer, deps, pr); err != nil {
			return contracts.RegistryAppendResult{}, err
		}
		return existing, nil
	}

	currentHead, err := currentRegistryHead(runCtx.RulesRegistryPath())
	if err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	startIndex, err := resumeIndexForPlannedRegistryEntries(*intention, matches, currentHead)
	if err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	return appendPlannedRegistryEntries(ctx, runCtx, pkg, intention, store, writer, deps, pr, startIndex)
}

func appendRegistryRollbacks(runCtx internalio.RunContext, intention contracts.IntentionRecord, reason contracts.RollbackReason, at time.Time) (*contracts.RegistryAppendResult, error) {
	committed, err := committedPromotionEntries(runCtx, intention)
	if err != nil {
		return nil, err
	}
	if len(committed) == 0 {
		if len(intention.AppendedEntryOpIDs) > 0 || intention.RegistryAppendResult != nil {
			return nil, ErrRegistryDivergence
		}
		return nil, nil
	}

	var result *contracts.RegistryAppendResult
	for _, committedEntry := range committed {
		if existing, ok, err := findRollbackByTarget(runCtx, committedEntry.OpID, committedEntry.Result); err != nil {
			return nil, err
		} else if ok {
			existingCopy := existing
			result = &existingCopy
			continue
		}

		entry := contracts.RuleRegistryRolledBack{
			Kind:           contracts.RegistryKindRolledBack,
			SchemaVersion:  "1",
			TargetOpID:     committedEntry.OpID,
			TargetOffset:   committedEntry.Result.Offset,
			TargetSha256:   committedEntry.Result.Sha256,
			ByRunID:        intention.RunID,
			RollbackReason: reason,
			FailedStep:     contracts.FailedStep70,
			VersionSeq:     1,
			PrevHash:       "",
			At:             at,
		}
		wrapper, err := deriveRegistryChain(contracts.RuleRegistryEntry{Kind: entry.Kind, Value: entry}, runCtx.RulesRegistryPath())
		if err != nil {
			return nil, err
		}
		appended, err := appendRegistryEntry(runCtx.RulesRegistryPath(), wrapper)
		if err != nil {
			return nil, err
		}
		syncRegistryIndex(runCtx, wrapper, appended)
		appendedCopy := appended
		result = &appendedCopy
	}
	return result, nil
}

func deriveRegistryChain(entry contracts.RuleRegistryEntry, path string) (contracts.RuleRegistryEntry, error) {
	lines, err := readRegistryLines(path)
	if err != nil {
		return contracts.RuleRegistryEntry{}, err
	}
	prevHash := ""
	if len(lines) > 0 {
		prevHash = lines[len(lines)-1].Sha256
	}

	switch v := entry.Value.(type) {
	case contracts.RuleRegistryAdded:
		v.VersionSeq = nextRegistryVersionForRule(lines, v.RuleID)
		v.PrevHash = registryPrevHashForVersion(v.VersionSeq, prevHash)
		return contracts.RuleRegistryEntry{Kind: entry.Kind, Value: v}, nil
	case contracts.RuleRegistryUpdated:
		v.VersionSeq = nextRegistryVersionForRule(lines, v.RuleID)
		v.PrevHash = registryPrevHashForVersion(v.VersionSeq, prevHash)
		return contracts.RuleRegistryEntry{Kind: entry.Kind, Value: v}, nil
	case contracts.RuleRegistryRolledBack:
		v.VersionSeq = nextRegistryVersionForRollback(lines, v.TargetOpID)
		v.PrevHash = registryPrevHashForVersion(v.VersionSeq, prevHash)
		return contracts.RuleRegistryEntry{Kind: entry.Kind, Value: v}, nil
	default:
		return entry, nil
	}
}

func registryPrevHashForVersion(versionSeq int64, prevHash string) string {
	if versionSeq == 1 {
		return ""
	}
	return prevHash
}

func plannedAdoptionFromRegistryEntries(intentionIdempotencyKey string, entries []contracts.RuleRegistryEntry) (*contracts.PlannedAdoption, error) {
	if len(entries) == 0 {
		return nil, errors.New("step70: adopt target must include at least one registry entry")
	}
	planned := &contracts.PlannedAdoption{
		IdempotencyKey: intentionIdempotencyKey,
		Entries:        make([]contracts.PlannedAdoptionEntry, 0, len(entries)),
	}
	for i, entry := range entries {
		switch v := entry.Value.(type) {
		case contracts.RuleRegistryAdded:
			planned.Entries = append(planned.Entries, contracts.PlannedAdoptionEntry{
				OpID:     contracts.ComputePlannedAdoptionEntryOpID(intentionIdempotencyKey, i, v.RuleID),
				Kind:     contracts.RegistryKindAdded,
				RuleID:   v.RuleID,
				RulePath: v.RulePath,
				Sha256:   v.Sha256,
			})
		case contracts.RuleRegistryUpdated:
			planned.Entries = append(planned.Entries, contracts.PlannedAdoptionEntry{
				OpID:       contracts.ComputePlannedAdoptionEntryOpID(intentionIdempotencyKey, i, v.RuleID),
				Kind:       contracts.RegistryKindUpdated,
				RuleID:     v.RuleID,
				RulePath:   v.RulePath,
				Sha256:     v.Sha256,
				PrevSha256: v.PrevSha256,
			})
		default:
			return nil, fmt.Errorf("step70: unsupported planned adoption registry kind=%q", entry.Kind)
		}
	}
	if err := planned.Validate(intentionIdempotencyKey); err != nil {
		return nil, err
	}
	return planned, nil
}

func registryEntriesFromPlannedAdoption(intention contracts.IntentionRecord, at time.Time) ([]contracts.RuleRegistryEntry, error) {
	if intention.PlannedAdoption == nil {
		return nil, contracts.ErrIntentionMissingPlannedAdoption
	}
	if err := intention.PlannedAdoption.Validate(intention.IdempotencyKey); err != nil {
		return nil, err
	}
	entries := make([]contracts.RuleRegistryEntry, 0, len(intention.PlannedAdoption.Entries))
	for _, planned := range intention.PlannedAdoption.Entries {
		switch planned.Kind {
		case contracts.RegistryKindAdded:
			entry := contracts.RuleRegistryAdded{
				Kind:           contracts.RegistryKindAdded,
				SchemaVersion:  "1",
				RuleID:         planned.RuleID,
				RulePath:       planned.RulePath,
				Sha256:         planned.Sha256,
				IdempotencyKey: planned.OpID,
				VersionSeq:     1,
				PrevHash:       "",
				ByRunID:        intention.RunID,
				At:             at,
			}
			entries = append(entries, contracts.RuleRegistryEntry{Kind: entry.Kind, Value: entry})
		case contracts.RegistryKindUpdated:
			entry := contracts.RuleRegistryUpdated{
				Kind:           contracts.RegistryKindUpdated,
				SchemaVersion:  "1",
				RuleID:         planned.RuleID,
				RulePath:       planned.RulePath,
				Sha256:         planned.Sha256,
				PrevSha256:     planned.PrevSha256,
				IdempotencyKey: planned.OpID,
				VersionSeq:     1,
				PrevHash:       "",
				ByRunID:        intention.RunID,
				At:             at,
			}
			entries = append(entries, contracts.RuleRegistryEntry{Kind: entry.Kind, Value: entry})
		default:
			return nil, fmt.Errorf("step70: unsupported planned adoption kind=%q", planned.Kind)
		}
	}
	return entries, nil
}

func appendPlannedRegistryEntries(ctx context.Context, runCtx internalio.RunContext, pkg *contracts.TaskPackage, intention *contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps, pr int, startIndex int) (contracts.RegistryAppendResult, error) {
	rawEntries, err := registryEntriesFromPlannedAdoption(*intention, deps.Now())
	if err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	if startIndex < 0 || startIndex > len(rawEntries) {
		return contracts.RegistryAppendResult{}, fmt.Errorf("step70: invalid registry append start index=%d", startIndex)
	}

	var result contracts.RegistryAppendResult
	for idx := startIndex; idx < len(rawEntries); idx++ {
		if handled, err := rollbackOnOtherRunSentinel(ctx, pr, runCtx, pkg, *intention, store, writer, deps); err != nil {
			return contracts.RegistryAppendResult{}, err
		} else if handled {
			return contracts.RegistryAppendResult{}, errSentinelRollbackHandled
		}
		rawEntry := rawEntries[idx]
		entry, err := deriveRegistryChain(rawEntry, runCtx.RulesRegistryPath())
		if err != nil {
			return contracts.RegistryAppendResult{}, err
		}

		appended, err := appendRegistryEntry(runCtx.RulesRegistryPath(), entry)
		if err != nil {
			if !errors.Is(err, internalio.ErrRegistryCASMismatch) {
				return contracts.RegistryAppendResult{}, err
			}
			entry, err = deriveRegistryChain(rawEntry, runCtx.RulesRegistryPath())
			if err != nil {
				return contracts.RegistryAppendResult{}, err
			}
			appended, err = appendRegistryEntry(runCtx.RulesRegistryPath(), entry)
			if err != nil {
				return contracts.RegistryAppendResult{}, err
			}
		}

		syncRegistryIndex(runCtx, entry, appended)
		result = appended
		intention.AppendedEntryOpIDs = appendIfMissing(intention.AppendedEntryOpIDs, intention.PlannedAdoption.Entries[idx].OpID)
		if handled, err := rollbackOnOtherRunSentinel(ctx, pr, runCtx, pkg, *intention, store, writer, deps); err != nil {
			return contracts.RegistryAppendResult{}, err
		} else if handled {
			return contracts.RegistryAppendResult{}, errSentinelRollbackHandled
		}
		if err := store.Save(*intention); err != nil {
			return contracts.RegistryAppendResult{}, err
		}
	}

	if err := emitRegistrySizeWarnings(runCtx, writer, deps, pr); err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	return result, nil
}

func targetFromIntention(pkg *contracts.TaskPackage, intention contracts.IntentionRecord) Target {
	bestBranch := ""
	if pkg != nil {
		bestBranch = pkg.BestBranch
	}
	return Target{
		BestBranch:    bestBranch,
		BestShaBefore: intention.BestShaBefore,
		TargetSHA:     intention.TargetSha,
	}
}

func promoteStagedRuleSidecars(runCtx internalio.RunContext, intention *contracts.IntentionRecord) error {
	stagingDir, err := runCtx.ResolveRunRelative("staging")
	if err != nil {
		return err
	}
	if _, err := os.Stat(stagingDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if intention == nil || intention.PlannedAdoption == nil {
		return errMissingPlannedAdoptionForStaging
	}
	for _, entry := range intention.PlannedAdoption.Entries {
		stagedPath, err := stagedRuleSidecarPath(runCtx, entry.RulePath)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(runCtx.RunsBase, filepath.FromSlash(entry.RulePath))
		if err := promoteRuleSidecarFn(stagedPath, dstPath, entry.Sha256); err != nil {
			return err
		}
	}
	return cleanupStagedRuleSidecars(runCtx)
}

func promoteRuleSidecar(stagedPath, dstPath, wantSHA string) error {
	data, err := internalio.ReadValidatedRegularFile(stagedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: path=%s", errRulePublishStagedMissing, stagedPath)
		}
		return fmt.Errorf("%w: read staged=%s: %v", errRulePublishIntegrity, stagedPath, err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != wantSHA {
		return fmt.Errorf("%w: path=%s", errRulePublishIntegrity, stagedPath)
	}

	if info, err := os.Lstat(dstPath); err == nil {
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("%w: path=%s", errRulePublishDestinationType, dstPath)
		case info.Mode().IsRegular():
			promoteRuleSidecarBeforeDestinationRead(dstPath)
			dstData, err := internalio.ReadValidatedRegularFile(dstPath)
			if err != nil {
				return fmt.Errorf("%w: read destination=%s: %v", errRulePublishDestinationType, dstPath, err)
			}
			sum := sha256.Sum256(dstData)
			if hex.EncodeToString(sum[:]) == wantSHA {
				if err := removePathAndSyncParent(stagedPath); err != nil && !os.IsNotExist(err) {
					return err
				}
				return nil
			}
			return fmt.Errorf("%w: path=%s", errRulePublishConflict, dstPath)
		default:
			return fmt.Errorf("%w: path=%s", errRulePublishDestinationType, dstPath)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}
	if err := internalio.WriteAtomic(dstPath, data); err != nil {
		return err
	}
	if err := removePathAndSyncParent(stagedPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func cleanupStagedRuleSidecars(runCtx internalio.RunContext) error {
	stagingDir, err := runCtx.ResolveRunRelative("staging")
	if err != nil {
		return err
	}
	return removeAllAndSyncParent(stagingDir)
}

func nextRegistryVersionForRule(lines []registryLine, _ string) int64 {
	if len(lines) == 0 {
		return 1
	}
	return registryVersionSeq(lines[len(lines)-1].Entry) + 1
}

func appendIfMissing(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func nextRegistryVersionForRollback(lines []registryLine, targetOpID string) int64 {
	if len(lines) == 0 {
		return 1
	}
	return registryVersionSeq(lines[len(lines)-1].Entry) + 1
}

func registryVersionSeq(entry contracts.RuleRegistryEntry) int64 {
	switch v := entry.Value.(type) {
	case contracts.RuleRegistryAdded:
		return v.VersionSeq
	case contracts.RuleRegistryUpdated:
		return v.VersionSeq
	case contracts.RuleRegistryRolledBack:
		return v.VersionSeq
	case contracts.RuleRegistryStatusChanged:
		return v.VersionSeq
	case contracts.RuleRegistryArchived:
		return v.VersionSeq
	case contracts.RuleRegistryRestored:
		return v.VersionSeq
	default:
		return 0
	}
}

func findRegistryByIdempotencyKey(runCtx internalio.RunContext, key string) (contracts.RegistryAppendResult, bool, error) {
	if key == "" {
		return contracts.RegistryAppendResult{}, false, nil
	}
	lines, err := registryLookupLines(runCtx)
	if err != nil {
		return contracts.RegistryAppendResult{}, false, err
	}
	for i := len(lines) - 1; i >= 0; i-- {
		match, offset, sha := matchesIdempotency(lines[i], key)
		if match {
			return contracts.RegistryAppendResult{Offset: offset, Sha256: sha}, true, nil
		}
	}
	return contracts.RegistryAppendResult{}, false, nil
}

func findRollbackByTarget(runCtx internalio.RunContext, targetOpID string, target contracts.RegistryAppendResult) (contracts.RegistryAppendResult, bool, error) {
	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	if err != nil {
		return contracts.RegistryAppendResult{}, false, err
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if v, ok := lines[i].Entry.Value.(contracts.RuleRegistryRolledBack); ok &&
			v.TargetOpID == targetOpID &&
			v.TargetOffset == target.Offset &&
			v.TargetSha256 == target.Sha256 {
			return contracts.RegistryAppendResult{Offset: lines[i].Offset, Sha256: lines[i].Sha256}, true, nil
		}
	}
	return contracts.RegistryAppendResult{}, false, nil
}

func matchesIdempotency(line registryLine, key string) (bool, int64, string) {
	switch v := line.Entry.Value.(type) {
	case contracts.RuleRegistryAdded:
		if v.IdempotencyKey == key {
			return true, line.Offset, line.Sha256
		}
	case contracts.RuleRegistryUpdated:
		if v.IdempotencyKey == key {
			return true, line.Offset, line.Sha256
		}
	case contracts.RuleRegistryRolledBack:
		if v.TargetOpID == key {
			return true, line.Offset, line.Sha256
		}
	case contracts.RuleRegistryStatusChanged:
		if v.OpID == key {
			return true, line.Offset, line.Sha256
		}
	case contracts.RuleRegistryArchived:
		if v.OpID == key {
			return true, line.Offset, line.Sha256
		}
	case contracts.RuleRegistryRestored:
		if v.OpID == key {
			return true, line.Offset, line.Sha256
		}
	}
	return false, 0, ""
}

func planningResumeNeedsRefresh(intention contracts.IntentionRecord, currentCandidatesHash string, fresh Target, hasTarget bool) (bool, error) {
	if !hasTarget {
		return true, nil
	}
	if currentCandidatesHash != intention.CandidatesHash {
		return true, nil
	}
	if fresh.TargetSHA != intention.TargetSha {
		return true, nil
	}
	if intention.PlannedAdoption == nil {
		return false, contracts.ErrIntentionMissingPlannedAdoption
	}
	if len(fresh.RulesToAppend) != len(intention.PlannedAdoption.Entries) {
		return true, nil
	}
	for idx, entry := range fresh.RulesToAppend {
		planned := intention.PlannedAdoption.Entries[idx]
		switch v := entry.Value.(type) {
		case contracts.RuleRegistryAdded:
			if planned.Kind != contracts.RegistryKindAdded || planned.RuleID != v.RuleID || planned.RulePath != v.RulePath || planned.Sha256 != v.Sha256 {
				return true, nil
			}
		case contracts.RuleRegistryUpdated:
			if planned.Kind != contracts.RegistryKindUpdated || planned.RuleID != v.RuleID || planned.RulePath != v.RulePath || planned.Sha256 != v.Sha256 || planned.PrevSha256 != v.PrevSha256 {
				return true, nil
			}
		default:
			return false, fmt.Errorf("step70: unsupported planned adoption registry kind=%q", entry.Kind)
		}
	}
	return false, nil
}

func registryLookupLines(runCtx internalio.RunContext) ([]registryLine, error) {
	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	if err != nil {
		return nil, err
	}
	if len(lines) < registryMandatoryIndexAt {
		start := 0
		if len(lines) > internalio.RegistryTailScanN {
			start = len(lines) - internalio.RegistryTailScanN
		}
		return lines[start:], nil
	}
	indexEntries, err := ensureRegistryIndex(runCtx)
	if err != nil {
		slog.Warn("step70: idempotency index unavailable; falling back to tail scan", slog.String("error", err.Error()))
		start := 0
		if len(lines) > internalio.RegistryTailScanN {
			start = len(lines) - internalio.RegistryTailScanN
		}
		return lines[start:], nil
	}
	matches := make(map[int64]string, len(indexEntries))
	for _, entry := range indexEntries {
		matches[entry.RegistryOffset] = entry.RegistrySha256
	}
	filtered := make([]registryLine, 0, len(lines))
	for _, line := range lines {
		if sha, ok := matches[line.Offset]; ok && sha == line.Sha256 {
			filtered = append(filtered, line)
		}
	}
	return filtered, nil
}

func ensureRegistryIndex(runCtx internalio.RunContext) ([]contracts.RuleIdempotencyIndexEntry, error) {
	count, err := registryLineCount(runCtx.RulesRegistryPath())
	if err != nil {
		return nil, err
	}
	if count < 1500 {
		return nil, nil
	}
	indexEntries, _, err := internalio.EnsureVerifiedIdempotencyIndex(runCtx.RulesRegistryPath(), runCtx.RulesIdempotencyIndexPath())
	return indexEntries, err
}

func syncRegistryIndex(runCtx internalio.RunContext, entry contracts.RuleRegistryEntry, result contracts.RegistryAppendResult) {
	count, err := registryLineCount(runCtx.RulesRegistryPath())
	if err != nil {
		slog.Warn("step70: failed to inspect registry size for index sync", slog.String("error", err.Error()))
		return
	}
	if count < 1500 {
		return
	}
	if err := internalio.SyncIdempotencyIndex(runCtx.RulesRegistryPath(), runCtx.RulesIdempotencyIndexPath(), entry, result); err != nil {
		slog.Warn("step70: idempotency index sync failed; registry append remains committed", slog.String("error", err.Error()))
	}
}

func readRegistryLines(path string) ([]registryLine, error) {
	return internalio.RegistryLines(path)
}

func currentRegistryHead(path string) (string, error) {
	lines, err := readRegistryLines(path)
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", nil
	}
	return lines[len(lines)-1].Sha256, nil
}

func registryLineCount(path string) (int, error) {
	lines, err := readRegistryLines(path)
	if err != nil {
		return 0, err
	}
	return len(lines), nil
}

func emitRegistrySizeWarnings(runCtx internalio.RunContext, writer state.Writer, deps Deps, pr int) error {
	count, err := registryLineCount(runCtx.RulesRegistryPath())
	if err != nil {
		return err
	}
	source := contracts.WarningSourceStep70
	step := contracts.FailedStep70
	prVal := pr
	runID := runCtx.RunID
	cnt := int64(count)
	if count >= deps.RegistryCritAt {
		w := contracts.StateEntryWarning{
			Kind:   contracts.StateKindWarningRegistrySizeCritical,
			PR:     &prVal,
			RunID:  &runID,
			Source: &source,
			Step:   &step,
			Count:  &cnt,
			At:     deps.Now(),
		}
		return appendStateOnce(runCtx, writer, contracts.StateKindWarningRegistrySizeCritical, contracts.StateEntry{Kind: w.Kind, Value: w})
	}
	if count >= deps.RegistryHighAt {
		w := contracts.StateEntryWarning{
			Kind:   contracts.StateKindWarningRegistrySizeHigh,
			PR:     &prVal,
			RunID:  &runID,
			Source: &source,
			Step:   &step,
			Count:  &cnt,
			At:     deps.Now(),
		}
		return appendStateOnce(runCtx, writer, contracts.StateKindWarningRegistrySizeHigh, contracts.StateEntry{Kind: w.Kind, Value: w})
	}
	return nil
}

// ---- Resume from persisted intention ----

func resume(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, candidates *contracts.Candidates, intention *contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps) error {
	switch intention.Stage {
	case contracts.IntentionStagePlanning:
		target, hasTarget, err := deps.Resolver.Resolve(runCtx, pkg, candidates)
		if err != nil {
			return err
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
		return planningDecision(ctx, pr, runCtx, pkg, candidates, persistedTarget, *intention, store, writer, deps)
	case contracts.IntentionStageBranchPushed:
		return resumeBranchPushed(ctx, pr, runCtx, pkg, *intention, store, writer, deps)
	case contracts.IntentionStageRegistryAppended:
		return driveDecision(ctx, pr, runCtx, pkg, *intention, store, writer, deps)
	case contracts.IntentionStageDecisionWritten:
		return finalizeAfterDecision(ctx, pr, runCtx, pkg, store, writer, deps)
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
			return handleRollback(ctx, pr, runCtx, pkg, targetFromIntention(pkg, intention), intention, store, writer, deps, contracts.RollbackReasonRegistryDivergence)
		}
		return handleRollback(ctx, pr, runCtx, pkg, targetFromIntention(pkg, intention), intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure)
	}
	intention.RegistryAppendResult = &appendResult
	intention.Stage = contracts.IntentionStageRegistryAppended
	if err := store.Save(intention); err != nil {
		return err
	}
	return driveDecision(ctx, pr, runCtx, pkg, intention, store, writer, deps)
}

func planningDecision(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, candidates *contracts.Candidates, target Target, intention contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps) error {
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
		intention.Stage = contracts.IntentionStageBranchPushed
		if err := store.Save(intention); err != nil {
			return err
		}
		return driveRegistry(ctx, pr, runCtx, pkg, intention, store, writer, deps)
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

// ---- Cleanup ----

func cleanupWorktrees(ctx context.Context, runCtx internalio.RunContext, pkg *contracts.TaskPackage, git GitOps) error {
	if pkg == nil {
		return nil
	}
	for _, wt := range pkg.Worktrees {
		if err := runCtx.ValidateWorktreeAllocation(wt); err != nil {
			return err
		}
		if git != nil {
			if err := git.RemoveWorktree(ctx, wt.Path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		if _, err := os.Stat(wt.Path); err == nil {
			if err := os.RemoveAll(filepath.Clean(wt.Path)); err != nil {
				return err
			}
		}
	}
	return nil
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
	if target.BestBranch == "" || target.BestShaBefore != "" {
		return target, nil
	}
	bestShaBefore, err := deps.Git.RemoteHead(ctx, target.BestBranch)
	if err != nil {
		return Target{}, err
	}
	target.BestShaBefore = bestShaBefore
	return target, nil
}

func remoteHeadMatchesRollbackBase(remoteHead, bestShaBefore string) bool {
	return remoteHead == bestShaBefore || (remoteHead == "" && bestShaBefore == "")
}

func persistedDecisionCanOverride(stage contracts.IntentionStage) bool {
	switch stage {
	case contracts.IntentionStageRegistryAppended,
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

func abortOnOtherRunSentinel(runCtx internalio.RunContext, _ IntentionWriter) error {
	return errOrNilFromBlockedSentinel(runCtx)
}

func errOrNilFromBlockedSentinel(runCtx internalio.RunContext) error {
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
		return true, handleRollback(ctx, pr, runCtx, pkg, targetFromIntention(pkg, intention), intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure)
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
