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
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
)

const registryMandatoryIndexAt = 1800

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
	deps = applyDepDefaults(deps)

	if blocked, err := SentinelExists(runCtx.RunsBase); err != nil {
		return fmt.Errorf("step70: sentinel scan: %w", err)
	} else if blocked {
		return nil
	}

	lock, err := internalio.AcquirePromotionLock(runCtx)
	if err != nil {
		return fmt.Errorf("step70: acquire promotion.lock: %w", err)
	}
	defer func() {
		_ = lock.Unlock()
	}()

	writer := state.NewWriter(runCtx)

	if blocked, err := SentinelExists(runCtx.RunsBase); err != nil {
		return err
	} else if blocked {
		return nil
	}

	intention, err := store.Load()
	if err != nil {
		return err
	}
	if intention != nil {
		return resume(ctx, pr, runCtx, pkg, candidates, intention, store, writer, deps)
	}

	decision, ok, err := loadDecisionIfExists(runCtx)
	if err != nil {
		return err
	}
	if ok {
		return finalizePersistedDecision(pr, runCtx, pkg, decision, store, writer, deps)
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
		if len(candidates.Candidates) == 0 {
			return writeNoop(runCtx, pkg, deps)
		}
		return writeReject(runCtx, pkg, "below_threshold", deps)
	}

	registryHead, err := currentRegistryHead(runCtx.RulesRegistryPath())
	if err != nil {
		return err
	}
	now := deps.Now()
	intention := contracts.IntentionRecord{
		SchemaVersion:      "1",
		Stage:              contracts.IntentionStagePlanning,
		IdempotencyKey:     contracts.ComputeAdoptIdempotencyKey(string(runCtx.RunID), target.TargetSHA, target.BestShaBefore, candidates.CandidatesHash),
		RunID:              runCtx.RunID,
		BestShaBefore:      target.BestShaBefore,
		TargetSha:          target.TargetSHA,
		CandidatesHash:     candidates.CandidatesHash,
		RegistryHeadBefore: registryHead,
		StartedAt:          now,
	}
	if err := store.Save(intention); err != nil {
		return err
	}
	if err := appendStateOnce(runCtx, writer, contracts.StateKindPromoting, promotingEvent(pr, runCtx.RunID, now)); err != nil {
		return err
	}
	return driveAdopt(ctx, pr, runCtx, pkg, candidates, target, intention, store, writer, deps)
}

func driveAdopt(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, candidates *contracts.Candidates, target Target, intention contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := pushBranch(target, deps); err != nil {
		return handleRollback(pr, runCtx, pkg, target, intention, store, writer, deps, classifyPushErr(err))
	}
	intention.Stage = contracts.IntentionStageBranchPushed
	if err := store.Save(intention); err != nil {
		return err
	}
	return driveRegistry(ctx, pr, runCtx, pkg, candidates, target, intention, store, writer, deps)
}

func driveRegistry(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, candidates *contracts.Candidates, target Target, intention contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	appendResult, err := appendRegistryEntries(runCtx, intention, target, writer, deps, pr)
	if err != nil {
		return handleRollback(pr, runCtx, pkg, target, intention, store, writer, deps, contracts.RollbackReasonRegistryDivergence)
	}
	intention.RegistryAppendResult = &appendResult
	intention.Stage = contracts.IntentionStageRegistryAppended
	if err := store.Save(intention); err != nil {
		return err
	}
	return driveDecision(pr, runCtx, pkg, intention, store, writer, deps)
}

func driveDecision(pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, intention contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps) error {
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
	return finalizeAfterDecision(pr, runCtx, pkg, store, writer, deps)
}

func finalizeAfterDecision(pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, store IntentionWriter, writer state.Writer, deps Deps) error {
	if err := store.Delete(); err != nil {
		return err
	}
	if err := appendStateOnce(runCtx, writer, contracts.StateKindPromoted, promotedEvent(pr, runCtx.RunID, deps.Now())); err != nil {
		return err
	}
	return cleanupWorktrees(pkg)
}

func finalizePersistedDecision(pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, decision contracts.Decision, store IntentionWriter, writer state.Writer, deps Deps) error {
	switch v := decision.Value.(type) {
	case contracts.DecisionAdopt:
		if err := appendStateOnce(runCtx, writer, contracts.StateKindPromoted, promotedEvent(pr, runCtx.RunID, deps.Now())); err != nil {
			return err
		}
		_ = store.Delete()
		return cleanupWorktrees(pkg)
	case *contracts.DecisionAdopt:
		if v == nil {
			return nil
		}
		if err := appendStateOnce(runCtx, writer, contracts.StateKindPromoted, promotedEvent(pr, runCtx.RunID, deps.Now())); err != nil {
			return err
		}
		_ = store.Delete()
		return cleanupWorktrees(pkg)
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
		return cleanupWorktrees(pkg)
	}
}

// ---- Rollback ----

func handleRollback(pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, target Target, intention contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps, reason contracts.RollbackReason) error {
	remoteHead, err := deps.Git.RemoteHead(target.BestBranch)
	if err != nil {
		return markManualRecovery(pr, runCtx, intention, store, writer, deps, reason)
	}
	switch remoteHead {
	case target.TargetSHA:
		if err := deps.Git.PushForceWithLease(target.BestBranch, target.BestShaBefore, target.TargetSHA); err != nil {
			if errors.Is(err, ErrLeaseFailure) {
				return markManualRecovery(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonLeaseFailure)
			}
			return markManualRecovery(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure)
		}
	case target.BestShaBefore, "":
		// Push never landed; no branch mutation needed.
	default:
		return markManualRecovery(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonRemoteDivergence)
	}

	intention.Stage = contracts.IntentionStageRollingBackBranchReverted
	intention.RecoveryReason = reason
	intention.FailedStep = contracts.FailedStep70
	if err := store.Save(intention); err != nil {
		return err
	}

	if intention.RegistryAppendResult != nil {
		result, err := appendRegistryRollback(runCtx, intention, reason, deps.Now())
		if err != nil {
			return markManualRecovery(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure)
		}
		intention.RegistryAppendResult = &result
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
	return store.Delete()
}

func resumeRollback(pr int, runCtx internalio.RunContext, intention contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps) error {
	reason := intention.RecoveryReason
	if reason == "" {
		reason = contracts.RollbackReasonTransactionalFailure
	}

	if intention.Stage == contracts.IntentionStageRollingBackBranchReverted && intention.RegistryAppendResult != nil {
		result, err := appendRegistryRollback(runCtx, intention, reason, deps.Now())
		if err != nil {
			return markManualRecovery(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure)
		}
		intention.RegistryAppendResult = &result
		intention.Stage = contracts.IntentionStageRollingBackRegistryAppended
		if err := store.Save(intention); err != nil {
			return err
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
	return store.Delete()
}

func markManualRecovery(pr int, runCtx internalio.RunContext, intention contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps, reason contracts.RollbackReason) error {
	intention.Stage = contracts.IntentionStageNeedsManualRecovery
	intention.RecoveryReason = reason
	intention.FailedStep = contracts.FailedStep70
	if err := store.Save(intention); err != nil {
		return err
	}
	if err := writeSentinel(runCtx.RunsBase, runCtx.RunID, pr, reason, contracts.FailedStep70, deps.Now()); err != nil {
		return err
	}
	if err := appendStateOnce(runCtx, writer, contracts.StateKindNeedsManualRecovery, needsManualRecoveryEvent(pr, runCtx.RunID, reason, contracts.FailedStep70, deps.Now())); err != nil {
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
	return cleanupWorktrees(pkg)
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
	return cleanupWorktrees(pkg)
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

func pushBranch(target Target, deps Deps) error {
	return deps.Git.PushForceWithLease(target.BestBranch, target.TargetSHA, target.BestShaBefore)
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

type registryLine struct {
	Offset int64
	Sha256 string
	Entry  contracts.RuleRegistryEntry
}

func appendRegistryEntries(runCtx internalio.RunContext, intention contracts.IntentionRecord, target Target, writer state.Writer, deps Deps, pr int) (contracts.RegistryAppendResult, error) {
	if existing, ok, err := findRegistryByIdempotencyKey(runCtx, intention.IdempotencyKey); err != nil {
		return contracts.RegistryAppendResult{}, err
	} else if ok {
		if err := emitRegistrySizeWarnings(runCtx, writer, deps, pr); err != nil {
			return contracts.RegistryAppendResult{}, err
		}
		return existing, nil
	}

	currentHead, err := currentRegistryHead(runCtx.RulesRegistryPath())
	if err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	if currentHead != intention.RegistryHeadBefore {
		return contracts.RegistryAppendResult{}, ErrRegistryDivergence
	}
	if len(target.RulesToAppend) == 0 {
		return contracts.RegistryAppendResult{}, errors.New("step70: adopt target must include at least one registry entry")
	}

	var result contracts.RegistryAppendResult
	for _, rawEntry := range target.RulesToAppend {
		entry, err := deriveRegistryChain(rawEntry, runCtx.RulesRegistryPath())
		if err != nil {
			return contracts.RegistryAppendResult{}, err
		}

		appended, err := internalio.AppendRegistryEntry(runCtx.RulesRegistryPath(), entry)
		if err != nil {
			if !errors.Is(err, internalio.ErrRegistryCASMismatch) {
				return contracts.RegistryAppendResult{}, err
			}
			entry, err = deriveRegistryChain(rawEntry, runCtx.RulesRegistryPath())
			if err != nil {
				return contracts.RegistryAppendResult{}, err
			}
			appended, err = internalio.AppendRegistryEntry(runCtx.RulesRegistryPath(), entry)
			if err != nil {
				return contracts.RegistryAppendResult{}, err
			}
		}

		if err := syncRegistryIndex(runCtx, entry, appended); err != nil {
			return contracts.RegistryAppendResult{}, err
		}
		result = appended
	}

	if err := emitRegistrySizeWarnings(runCtx, writer, deps, pr); err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	return result, nil
}

func appendRegistryRollback(runCtx internalio.RunContext, intention contracts.IntentionRecord, reason contracts.RollbackReason, at time.Time) (contracts.RegistryAppendResult, error) {
	if intention.RegistryAppendResult == nil {
		return contracts.RegistryAppendResult{}, nil
	}
	if existing, ok, err := findRollbackByTargetOpID(runCtx, intention.IdempotencyKey); err != nil {
		return contracts.RegistryAppendResult{}, err
	} else if ok {
		return existing, nil
	}

	target := *intention.RegistryAppendResult
	entry := contracts.RuleRegistryRolledBack{
		Kind:           contracts.RegistryKindRolledBack,
		SchemaVersion:  "1",
		TargetOpID:     intention.IdempotencyKey,
		TargetOffset:   target.Offset,
		TargetSha256:   target.Sha256,
		ByRunID:        intention.RunID,
		RollbackReason: reason,
		FailedStep:     contracts.FailedStep70,
		VersionSeq:     1,
		PrevHash:       "",
		At:             at,
	}
	wrapper, err := deriveRegistryChain(contracts.RuleRegistryEntry{Kind: entry.Kind, Value: entry}, runCtx.RulesRegistryPath())
	if err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	result, err := internalio.AppendRegistryEntry(runCtx.RulesRegistryPath(), wrapper)
	if err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	if err := syncRegistryIndex(runCtx, wrapper, result); err != nil {
		return contracts.RegistryAppendResult{}, err
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
		v.PrevHash = prevHash
		return contracts.RuleRegistryEntry{Kind: entry.Kind, Value: v}, nil
	case contracts.RuleRegistryUpdated:
		v.VersionSeq = nextRegistryVersionForRule(lines, v.RuleID)
		v.PrevHash = prevHash
		return contracts.RuleRegistryEntry{Kind: entry.Kind, Value: v}, nil
	case contracts.RuleRegistryRolledBack:
		v.VersionSeq = nextRegistryVersionForRollback(lines, v.TargetOpID)
		v.PrevHash = prevHash
		return contracts.RuleRegistryEntry{Kind: entry.Kind, Value: v}, nil
	default:
		return entry, nil
	}
}

func nextRegistryVersionForRule(lines []registryLine, ruleID string) int64 {
	var seq int64
	for _, line := range lines {
		switch v := line.Entry.Value.(type) {
		case contracts.RuleRegistryAdded:
			if v.RuleID == ruleID && v.VersionSeq > seq {
				seq = v.VersionSeq
			}
		case contracts.RuleRegistryUpdated:
			if v.RuleID == ruleID && v.VersionSeq > seq {
				seq = v.VersionSeq
			}
		case contracts.RuleRegistryStatusChanged:
			if v.RuleID == ruleID && v.VersionSeq > seq {
				seq = v.VersionSeq
			}
		case contracts.RuleRegistryArchived:
			if v.RuleID == ruleID && v.VersionSeq > seq {
				seq = v.VersionSeq
			}
		case contracts.RuleRegistryRestored:
			if v.RuleID == ruleID && v.VersionSeq > seq {
				seq = v.VersionSeq
			}
		}
	}
	return seq + 1
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

func findRollbackByTargetOpID(runCtx internalio.RunContext, targetOpID string) (contracts.RegistryAppendResult, bool, error) {
	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	if err != nil {
		return contracts.RegistryAppendResult{}, false, err
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if v, ok := lines[i].Entry.Value.(contracts.RuleRegistryRolledBack); ok && v.TargetOpID == targetOpID {
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
	if err := ensureRegistryIndex(runCtx); err != nil {
		return nil, err
	}
	indexEntries, err := internalio.ReadJSONL[contracts.RuleIdempotencyIndexEntry](runCtx.RulesIdempotencyIndexPath())
	if err != nil {
		return nil, err
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

func ensureRegistryIndex(runCtx internalio.RunContext) error {
	count, err := registryLineCount(runCtx.RulesRegistryPath())
	if err != nil {
		return err
	}
	if count < 1500 {
		return nil
	}
	if _, err := os.Stat(runCtx.RulesIdempotencyIndexPath()); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		_, err = internalio.RebuildIdempotencyIndex(runCtx.RulesRegistryPath(), runCtx.RulesIdempotencyIndexPath())
		return err
	}
	return nil
}

func syncRegistryIndex(runCtx internalio.RunContext, entry contracts.RuleRegistryEntry, result contracts.RegistryAppendResult) error {
	count, err := registryLineCount(runCtx.RulesRegistryPath())
	if err != nil {
		return err
	}
	if count < 1500 {
		return nil
	}
	if _, err := os.Stat(runCtx.RulesIdempotencyIndexPath()); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		_, err = internalio.RebuildIdempotencyIndex(runCtx.RulesRegistryPath(), runCtx.RulesIdempotencyIndexPath())
		return err
	}
	indexEntry, err := internalio.BuildRuleIdempotencyIndexEntry(entry, result)
	if err != nil {
		return err
	}
	return internalio.AppendIdempotencyIndexEntry(runCtx.RulesIdempotencyIndexPath(), indexEntry)
}

func readRegistryLines(path string) ([]registryLine, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	lines := make([]registryLine, 0, 8)
	var offset int64
	for {
		raw, err := reader.ReadBytes('\n')
		if len(raw) == 0 && err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		line := strings.TrimRight(string(raw), "\n")
		if line == "" {
			if errors.Is(err, io.EOF) {
				break
			}
			offset += int64(len(raw))
			continue
		}
		var entry contracts.RuleRegistryEntry
		if decodeErr := contracts.DecodeStrictJSON([]byte(line), &entry); decodeErr != nil {
			return nil, decodeErr
		}
		lines = append(lines, registryLine{
			Offset: offset,
			Sha256: hashOfRegistryLine([]byte(line)),
			Entry:  entry,
		})
		offset += int64(len(line) + 1)
		if errors.Is(err, io.EOF) {
			break
		}
	}
	return lines, nil
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
	target, hasTarget, err := deps.Resolver.Resolve(runCtx, pkg, candidates)
	if err != nil {
		return err
	}
	if !hasTarget {
		return markManualRecovery(pr, runCtx, *intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure)
	}

	switch intention.Stage {
	case contracts.IntentionStagePlanning:
		return planningDecision(ctx, pr, runCtx, pkg, candidates, target, *intention, store, writer, deps)
	case contracts.IntentionStageBranchPushed:
		return driveRegistry(ctx, pr, runCtx, pkg, candidates, target, *intention, store, writer, deps)
	case contracts.IntentionStageRegistryAppended:
		return driveDecision(pr, runCtx, pkg, *intention, store, writer, deps)
	case contracts.IntentionStageDecisionWritten:
		return finalizeAfterDecision(pr, runCtx, pkg, store, writer, deps)
	case contracts.IntentionStageRollingBackBranchReverted,
		contracts.IntentionStageRollingBackRegistryAppended,
		contracts.IntentionStageRollingBackDecisionWritten:
		return resumeRollback(pr, runCtx, *intention, store, writer, deps)
	case contracts.IntentionStageNeedsManualRecovery:
		return ErrNeedsManualRecovery
	default:
		return fmt.Errorf("step70: unknown intention stage=%q", intention.Stage)
	}
}

func planningDecision(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, candidates *contracts.Candidates, target Target, intention contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps) error {
	remoteHead, err := deps.Git.RemoteHead(target.BestBranch)
	if err != nil {
		return markManualRecovery(pr, runCtx, intention, store, writer, deps, contracts.RollbackReasonTransactionalFailure)
	}
	switch remoteHead {
	case target.TargetSHA:
		intention.Stage = contracts.IntentionStageBranchPushed
		if err := store.Save(intention); err != nil {
			return err
		}
		return driveRegistry(ctx, pr, runCtx, pkg, candidates, target, intention, store, writer, deps)
	case target.BestShaBefore, "":
		currentHead, err := currentRegistryHead(runCtx.RulesRegistryPath())
		if err != nil {
			return err
		}
		if currentHead == intention.RegistryHeadBefore {
			if err := appendStateOnce(runCtx, writer, contracts.StateKindInterrupted, interruptedEvent(pr, runCtx.RunID, contracts.InterruptedReasonPrePushCrash, "", deps.Now())); err != nil {
				return err
			}
			return driveAdopt(ctx, pr, runCtx, pkg, candidates, target, intention, store, writer, deps)
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

func cleanupWorktrees(pkg *contracts.TaskPackage) error {
	if pkg == nil {
		return nil
	}
	for _, wt := range pkg.Worktrees {
		cmd := exec.Command("git", "worktree", "remove", "--force", wt.Path)
		_ = cmd.Run()
		if _, err := os.Stat(wt.Path); err == nil {
			if err := os.RemoveAll(filepath.Clean(wt.Path)); err != nil {
				return err
			}
		}
	}
	return nil
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

func needsManualRecoveryEvent(pr int, runID contracts.RunID, reason contracts.RollbackReason, failed contracts.FailedStep, at time.Time) contracts.StateEntry {
	v := contracts.StateEntryNeedsManualRecovery{
		Kind:       contracts.StateKindNeedsManualRecovery,
		PR:         pr,
		RunID:      runID,
		Step:       contracts.FailedStep70,
		Reason:     reason,
		FailedStep: failed,
		At:         at,
	}
	return contracts.StateEntry{Kind: v.Kind, Value: v}
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

// ---- stepio helpers for callers that operate against request/response ----

// BuildResponse assembles a request-bound Step70Response from the Decision
// written to disk.
func BuildResponse(runID contracts.RunID, decision contracts.Decision, promoted bool, req stepio.Step70Request) (stepio.Step70Response, error) {
	return stepio.NewStep70Response(string(runID), decision, promoted, req)
}
