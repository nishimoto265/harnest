package step70_decide

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
)

type RecoverRefusalError struct {
	Message string
}

func (e *RecoverRefusalError) Error() string {
	if e == nil {
		return "step70: recover refused"
	}
	return "step70: recover refused: " + e.Message
}

func RecoverRollback(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, store IntentionWriter, deps Deps) error {
	return withRecoverPromotionLock(ctx, runCtx, func() error {
		return recoverRollbackUnlocked(ctx, pr, runCtx, pkg, store, deps)
	})
}

func recoverRollbackUnlocked(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, store IntentionWriter, deps Deps) error {
	intention, err := loadRecoverIntention(store)
	if err != nil {
		return err
	}
	deps = applyDepDefaults(deps)
	writer := state.NewWriter(runCtx)
	target := targetFromIntention(pkg, *intention)
	remoteHead, err := deps.Git.RemoteHead(ctx, target.BestBranch)
	if err != nil {
		return err
	}
	registryHead, idempotencyHit, err := recoverRegistryStatus(runCtx, *intention)
	if err != nil {
		return err
	}
	if err := recoverPolicyRollbackSafe(ctx, *intention, deps); err != nil {
		return err
	}
	reason := intention.RecoveryReason
	if reason == "" {
		reason = contracts.RollbackReasonTransactionalFailure
	}

	switch {
	case !idempotencyHit &&
		registryHead == intention.RegistryHeadBefore &&
		(remoteHeadMatchesRollbackBase(remoteHead, target.BestShaBefore)) &&
		(intention.Stage == contracts.IntentionStagePlanning || intention.Stage == contracts.IntentionStageBranchPushed):
		if err := handleRollback(ctx, pr, runCtx, pkg, target, *intention, store, writer, deps, reason, pushUnknown); err != nil {
			return err
		}
	case !idempotencyHit &&
		registryHead == intention.RegistryHeadBefore &&
		remoteHead == target.TargetSHA &&
		intention.Stage == contracts.IntentionStageBranchPushed:
		if err := handleRollback(ctx, pr, runCtx, pkg, target, *intention, store, writer, deps, reason, pushOwned); err != nil {
			return err
		}
	default:
		return &RecoverRefusalError{
			Message: fmt.Sprintf(
				"rollback safe matrix mismatch: stage=%s remote_head=%s registry_head=%s idempotency_hit=%t",
				intention.Stage, remoteHead, registryHead, idempotencyHit,
			),
		}
	}
	return removeNeedsRecoverySentinels(runCtx)
}

func RecoverAdoptAnyway(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, candidates *contracts.Candidates, store IntentionWriter, deps Deps) error {
	return withRecoverPromotionLock(ctx, runCtx, func() error {
		return recoverAdoptAnywayUnlocked(ctx, pr, runCtx, pkg, candidates, store, deps)
	})
}

func recoverAdoptAnywayUnlocked(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, candidates *contracts.Candidates, store IntentionWriter, deps Deps) error {
	intention, err := loadRecoverIntention(store)
	if err != nil {
		return err
	}
	if candidates == nil {
		return errors.New("step70: recover adopt-anyway requires candidates")
	}
	deps = applyDepDefaults(deps)
	writer := state.NewWriter(runCtx)
	target := targetFromIntention(pkg, *intention)
	remoteHead, err := deps.Git.RemoteHead(ctx, target.BestBranch)
	if err != nil {
		return err
	}
	registryHead, idempotencyHit, err := recoverRegistryStatus(runCtx, *intention)
	if err != nil {
		return err
	}
	if idempotencyHit && intention.RegistryAppendResult == nil {
		matches, err := findPlannedRegistryMatches(runCtx, *intention)
		if err != nil {
			return err
		}
		if result, ok := completePlannedRegistryMatches(matches); ok {
			intention.RegistryAppendResult = &result
			if err := store.Save(*intention); err != nil {
				return err
			}
		}
	}
	if remoteHead != target.TargetSHA || !idempotencyHit {
		return &RecoverRefusalError{
			Message: fmt.Sprintf(
				"adopt-anyway safe matrix mismatch: stage=%s remote_head=%s registry_head=%s idempotency_hit=%t",
				intention.Stage, remoteHead, registryHead, idempotencyHit,
			),
		}
	}
	switch intention.Stage {
	case contracts.IntentionStageBranchPushed,
		contracts.IntentionStageRegistryAppended,
		contracts.IntentionStagePolicyPublishing,
		contracts.IntentionStagePolicyPublished:
		if err := resume(ctx, pr, runCtx, pkg, candidates, intention, store, writer, deps); err != nil {
			return err
		}
	case contracts.IntentionStageNeedsManualRecovery:
		if err := driveDecision(ctx, pr, runCtx, pkg, *intention, store, writer, deps); err != nil {
			return err
		}
	case contracts.IntentionStageDecisionWritten:
		decision, ok, err := loadDecisionIfExists(runCtx)
		if err != nil {
			return err
		}
		if !ok {
			return &RecoverRefusalError{Message: "decision_written stage requires persisted decision.json"}
		}
		if err := finalizePersistedDecision(ctx, pr, runCtx, pkg, candidates, decision, store, writer, deps); err != nil {
			return err
		}
	default:
		return &RecoverRefusalError{
			Message: fmt.Sprintf("adopt-anyway unsupported intention stage=%s", intention.Stage),
		}
	}
	return removeNeedsRecoverySentinels(runCtx)
}

func recoverPolicyRollbackSafe(ctx context.Context, intention contracts.IntentionRecord, deps Deps) error {
	branch := strings.TrimSpace(intention.PolicyBranch)
	if branch == "" {
		branch = strings.TrimSpace(deps.PolicyBranch)
	}
	if branch == "" || intention.PolicyHeadBefore == "" {
		return nil
	}
	current, err := deps.Git.RemoteHead(ctx, branch)
	if err != nil {
		return err
	}
	if intention.PolicyHeadAfter != "" && current == intention.PolicyHeadAfter {
		return &RecoverRefusalError{
			Message: fmt.Sprintf("rollback refused: policy_branch already published: branch=%s head=%s", branch, current),
		}
	}
	if current != intention.PolicyHeadBefore {
		return &RecoverRefusalError{
			Message: fmt.Sprintf("rollback refused: policy_branch head mismatch: branch=%s have=%s want=%s", branch, current, intention.PolicyHeadBefore),
		}
	}
	return nil
}

func RecoverMarkManualAbort(runCtx internalio.RunContext, pr int, store IntentionWriter, at time.Time) error {
	return withRecoverPromotionLock(context.Background(), runCtx, func() error {
		return recoverMarkManualAbortUnlocked(runCtx, pr, store, at)
	})
}

func recoverMarkManualAbortUnlocked(runCtx internalio.RunContext, pr int, store IntentionWriter, at time.Time) error {
	intention, err := loadRecoverIntention(store)
	if err != nil {
		return err
	}
	intention.Stage = contracts.IntentionStageNeedsManualRecovery
	intention.RecoveryReason = contracts.RollbackReasonManualAbortPendingCleanup
	intention.FailedStep = contracts.FailedStep70
	if err := store.Save(*intention); err != nil {
		return err
	}
	decision := contracts.Decision{
		Action: contracts.DecisionActionRollback,
		Value: contracts.DecisionRollback{
			Action:         contracts.DecisionActionRollback,
			SchemaVersion:  "1",
			RunID:          runCtx.RunID,
			IdempotencyKey: intention.IdempotencyKey,
			RollbackReason: contracts.RollbackReasonManualAbortPendingCleanup,
			FailedStep:     contracts.FailedStep70,
			BestShaBefore:  intention.BestShaBefore,
			TargetSha:      intention.TargetSha,
			DecidedAt:      at,
		},
	}
	if err := writeDecision(runCtx, decision); err != nil {
		return err
	}
	sentinel, _, err := readExistingSentinelPaths(runCtx)
	if err != nil {
		return err
	}
	if sentinel != nil {
		if err := renameNeedsRecoverySentinelToAborted(runCtx); err != nil {
			return err
		}
	} else if err := writeAbortedSentinel(runCtx.RunsBase, runCtx.RunID, pr, contracts.RollbackReasonManualAbortPendingCleanup, contracts.FailedStep70, at); err != nil {
		return err
	}
	return state.NewWriter(runCtx).Append(needsManualRecoveryEvent(pr, runCtx.RunID, contracts.RollbackReasonManualAbortPendingCleanup, contracts.FailedStep70, "", at))
}

func RecoverClearSentinel(runCtx internalio.RunContext) error {
	return withRecoverPromotionLock(context.Background(), runCtx, func() error {
		return recoverClearSentinelUnlocked(runCtx)
	})
}

func recoverClearSentinelUnlocked(runCtx internalio.RunContext) error {
	if err := removeNeedsRecoverySentinels(runCtx); err != nil {
		return err
	}
	return writeClearedSentinelMarker(runCtx)
}

func withRecoverPromotionLock(ctx context.Context, runCtx internalio.RunContext, fn func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	lockPath := runCtx.PromotionLockPath()
	if internalio.IsFileLockHeld(lockPath) {
		return fn()
	}
	lock, err := internalio.AcquireFileLockContext(ctx, lockPath)
	if err != nil {
		return fmt.Errorf("step70: acquire promotion.lock for recover: %w", err)
	}
	defer func() {
		_ = lock.Unlock()
	}()
	return fn()
}

func loadRecoverIntention(store IntentionWriter) (*contracts.IntentionRecord, error) {
	if store == nil {
		return nil, errors.New("step70: recover requires intention store")
	}
	intention, err := store.Load()
	if err != nil {
		return nil, err
	}
	if intention == nil {
		return nil, errors.New("step70: recover requires persisted intention")
	}
	return intention, nil
}

func recoverRegistryStatus(runCtx internalio.RunContext, intention contracts.IntentionRecord) (string, bool, error) {
	currentHead, err := currentRegistryHead(runCtx.RulesRegistryPath())
	if err != nil {
		return "", false, err
	}
	matches, err := findPlannedRegistryMatches(runCtx, intention)
	if err != nil {
		return "", false, err
	}
	_, hit := completePlannedRegistryMatches(matches)
	return currentHead, hit, nil
}

func readExistingSentinelPaths(runCtx internalio.RunContext) (*contracts.NeedsRecoverySentinel, string, error) {
	for _, name := range []string{
		contracts.NeedsRecoverySentinelAbortedFilename(runCtx.RunID),
		contracts.NeedsRecoverySentinelFilename(runCtx.RunID),
	} {
		path := filepath.Join(runCtx.RunsBase, needsRecoveryDir, name)
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, "", err
		}
		sentinel, err := internalio.ReadJSON[contracts.NeedsRecoverySentinel](path)
		if err != nil {
			return nil, "", err
		}
		return &sentinel, path, nil
	}
	return nil, "", nil
}
