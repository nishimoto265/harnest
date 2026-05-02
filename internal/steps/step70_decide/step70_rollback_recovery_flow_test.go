package step70_decide

import (
	"context"
	"errors"
	"fmt"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRun_RollbackTerminalCleansGitWorktrees(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR201")
	git := &fakeGit{head: resolver.target.BestShaBefore, pushErr: ErrLeaseFailure}

	require.NoError(t, Run(context.Background(), 201, runCtx, pkg, candidates, store, Deps{
		Git:      git,
		Resolver: resolver,
		Now:      fixedNow(),
	}))

	assert.Equal(t, contracts.DecisionActionRollback, readDecision(t, runCtx).Action)
	require.Len(t, git.removeWorktreeCalls, len(pkg.Worktrees))
	for i, wt := range pkg.Worktrees {
		assert.Equal(t, wt.Path, git.removeWorktreeCalls[i])
	}
}
func TestRun_MultiEntryAppendFailure_RollbackAppendsMarkersForCommittedRows(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR440")
	resolver.target.RulesToAppend = adoptAddedEntriesWithTarget(runCtx.RunID, candidates.CandidatesHash, resolver.target.TargetSHA, "rule-a", "rule-b", "rule-c")
	stageFixtureRuleSidecars(t, runCtx, resolver.target)
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	plannedOpIDs := []string{
		intention.PlannedAdoption.Entries[0].OpID,
		intention.PlannedAdoption.Entries[1].OpID,
	}

	original := appendRegistryEntry
	appendCount := 0
	appendRegistryEntry = func(path string, entry contracts.RuleRegistryEntry) (contracts.RegistryAppendResult, error) {
		if path == runCtx.RulesRegistryPath() {
			switch entry.Kind {
			case contracts.RegistryKindAdded, contracts.RegistryKindUpdated:
				appendCount++
				if appendCount == 3 {
					return contracts.RegistryAppendResult{}, errors.New("boom")
				}
			}
		}
		return original(path, entry)
	}
	t.Cleanup(func() {
		appendRegistryEntry = original
	})

	deps := Deps{Git: &fakeGit{head: resolver.target.BestShaBefore}, Resolver: resolver, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 440, runCtx, pkg, candidates, store, deps))

	rollback := mustDecisionRollback(t, readDecision(t, runCtx))
	assert.Equal(t, contracts.RollbackReasonTransactionalFailure, rollback.RollbackReason)

	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	require.NoError(t, err)
	require.Len(t, lines, 4)
	rolledBack := make([]contracts.RuleRegistryRolledBack, 0, 2)
	for _, line := range lines {
		if v, ok := line.Entry.Value.(contracts.RuleRegistryRolledBack); ok {
			rolledBack = append(rolledBack, v)
		}
	}
	require.Len(t, rolledBack, 2)
	assert.ElementsMatch(t, plannedOpIDs, []string{rolledBack[0].TargetOpID, rolledBack[1].TargetOpID})
}
func TestRun_ResumeFromBranchPushed_RegistryDivergedRollsBack(t *testing.T) {
	runCtx, pkg, candidates, _, resolver := newFixtureWithResolver(t, "PR45")
	store := newTrackingStore(intentionPath(t, runCtx))
	firstResult, _ := seedRegistryUniqueAdd(t, runCtx.RulesRegistryPath(), "seed-before", fmt.Sprintf("%064x", 7001), "2026-04-21-PR80-abcdef0")

	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageBranchPushed
	intention.RegistryHeadBefore = firstResult.Sha256
	require.NoError(t, store.Save(intention))

	_, _ = seedRegistryUniqueAdd(t, runCtx.RulesRegistryPath(), "seed-after", fmt.Sprintf("%064x", 7002), "2026-04-21-PR81-abcdef0")

	git := &fakeGit{head: resolver.target.TargetSHA}
	deps := Deps{Git: git, Resolver: unexpectedResolver{t: t}, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 45, runCtx, pkg, candidates, store, deps))

	decision := readDecision(t, runCtx)
	rollback := mustDecisionRollback(t, decision)
	assert.Equal(t, contracts.RollbackReasonRegistryDivergence, rollback.RollbackReason)
	assert.NoFileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, string(runCtx.RunID)+".json"))
	require.Len(t, git.pushCalls, 1)
}
func TestMarkManualRecoveryWithDetail_ParksIntentionAtNeedsManualRecoveryWhenSentinelWriteFails(t *testing.T) {
	runCtx, _, candidates, _, resolver := newFixtureWithResolver(t, "PR416")
	store := newTrackingStore(intentionPath(t, runCtx))
	writer := state.NewWriter(runCtx)
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageBranchPushed

	originalWriteSentinel := writeSentinelFn
	writeSentinelFn = func(string, contracts.RunID, int, contracts.RollbackReason, contracts.FailedStep, time.Time) error {
		return errors.New("disk full")
	}
	t.Cleanup(func() {
		writeSentinelFn = originalWriteSentinel
	})

	err := markManualRecoveryWithDetail(416, runCtx, intention, store, writer, Deps{Now: fixedNow()}, contracts.RollbackReasonTransactionalFailure, "")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrNeedsManualRecovery)
	assert.Contains(t, err.Error(), "disk full")

	loaded, loadErr := store.Load()
	require.NoError(t, loadErr)
	require.NotNil(t, loaded, "intention must persist at needs_manual_recovery so resume() self-blocks even if the sentinel never lands")
	assert.Equal(t, contracts.IntentionStageNeedsManualRecovery, loaded.Stage)
	assert.Equal(t, contracts.RollbackReasonTransactionalFailure, loaded.RecoveryReason)
	assert.Equal(t, contracts.FailedStep70, loaded.FailedStep)

	events := readStateEvents(t, runCtx)
	require.NotEmpty(t, events)
	recovery := mustNeedsManualRecoveryEvent(t, events[len(events)-1])
	assert.Equal(t, contracts.RollbackReasonTransactionalFailure, recovery.Reason)

	// No sentinel landed, but the processed state event lets the global gate
	// block/reconstruct while the parked intention self-blocks this run.
	blocked, blockErr := SentinelExists(runCtx.RunsBase)
	require.NoError(t, blockErr)
	assert.False(t, blocked)
}

// F11: planning resume with remoteHead==target_sha must require ownership
// proof. When no committed registry entry exists for this intention's planned
// op_ids AND the registry head has advanced since planning, another run may
// have pushed the same SHA. Proceeding into driveRegistry would trigger
// rollback and force-push the branch back to best_sha_before — silently
// undoing the other run's promotion. Instead, the intention must be dropped
// and startFresh invoked so the planner observes the true state.
func TestRun_PrePushRollbackRefusesBranchRevertWithoutOwnershipProofWhenRegistryAdvanced(t *testing.T) {
	runCtx, pkg, candidates, _, resolver := newFixtureWithResolver(t, "PR4113")
	store := newTrackingStore(intentionPath(t, runCtx))
	// Seed registry with an unrelated promotion by another run.
	_, _ = seedRegistryUniqueAdd(t, runCtx.RulesRegistryPath(), "other-run", fmt.Sprintf("%064x", 7113), "2026-04-21-PR80-abcdef0")

	// Push fails, but remote happens to sit at target_sha (another run).
	git := &fakeGit{head: resolver.target.TargetSHA, pushErr: ErrLeaseFailure}
	err := Run(context.Background(), 4113, runCtx, pkg, candidates, store, Deps{
		Git:      git,
		Resolver: resolver,
		Now:      fixedNow(),
	})
	require.ErrorIs(t, err, ErrNeedsManualRecovery)
	assert.FileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, string(runCtx.RunID)+".json"))

	// Exactly one push attempt (the original adopt push). No follow-up
	// force-push to best_sha_before.
	require.Len(t, git.pushCalls, 1)
	assert.Equal(t, resolver.target.TargetSHA, git.pushCalls[0].target)
	for _, saved := range store.saved {
		assert.NotEqual(t, contracts.IntentionStageRollingBackBranchReverted, saved.Stage)
	}
	require.NotEmpty(t, store.saved)
	assert.Equal(t, contracts.IntentionStageNeedsManualRecovery, store.saved[len(store.saved)-1].Stage)
}

// F9: even with no sentinel on disk, a run whose intention is parked at
// needs_manual_recovery must self-block on resume instead of reopening the
// old staged transaction path.
func TestRun_ResumeFromParkedNeedsManualRecoveryIntentionWithoutSentinelReturnsErrNeedsManualRecovery(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR4161")
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageNeedsManualRecovery
	intention.RecoveryReason = contracts.RollbackReasonTransactionalFailure
	intention.FailedStep = contracts.FailedStep70
	require.NoError(t, store.Save(intention))

	deps := Deps{Git: &fakeGit{head: resolver.target.BestShaBefore}, Resolver: unexpectedResolver{t: t}, Now: fixedNow()}
	err := Run(context.Background(), 4161, runCtx, pkg, candidates, store, deps)
	require.ErrorIs(t, err, ErrNeedsManualRecovery)
	assert.NoFileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, string(runCtx.RunID)+".json"))
}
func TestRun_RollbackOnPushFailure(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR6")
	// Push fails, remote still at best_sha_before so rollback is safe.
	git := &fakeGit{head: resolver.target.BestShaBefore, pushErr: ErrLeaseFailure}
	deps := Deps{Git: git, Resolver: resolver, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 6, runCtx, pkg, candidates, store, deps))

	decision := readDecision(t, runCtx)
	assert.Equal(t, contracts.DecisionActionRollback, decision.Action)
	rb, ok := decision.Value.(contracts.DecisionRollback)
	require.True(t, ok)
	assert.Equal(t, contracts.RollbackReasonLeaseFailure, rb.RollbackReason)
}
func TestRun_RollbackTreatsMissingRemoteHeadAsManualRecoveryWhenBestShaBeforeKnown(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR601")
	git := &fakeGit{head: resolver.target.BestShaBefore, pushErr: ErrLeaseFailure}
	git.onPush = func(fakePushCall) {
		git.head = ""
	}

	err := Run(context.Background(), 601, runCtx, pkg, candidates, store, Deps{Git: git, Resolver: resolver, Now: fixedNow()})
	require.ErrorIs(t, err, ErrNeedsManualRecovery)
	assert.FileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, string(runCtx.RunID)+".json"))
}
func TestRun_CanceledPushReturnsContextErrorWithoutManualRecovery(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR423")
	ctx, cancel := context.WithCancel(context.Background())
	git := &fakeGit{
		head: resolver.target.BestShaBefore,
		onPush: func(call fakePushCall) {
			_ = call
			cancel()
		},
		pushErr: context.Canceled,
	}

	err := Run(ctx, 423, runCtx, pkg, candidates, store, Deps{Git: git, Resolver: resolver, Now: fixedNow()})
	require.ErrorIs(t, err, context.Canceled)
	assert.NoFileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, string(runCtx.RunID)+".json"))
}
func TestRun_NeedsManualRecoveryOnRemoteDivergence(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR7")
	git := &fakeGit{
		head:    resolver.target.BestShaBefore,
		pushErr: ErrRemoteDivergence,
	}
	git.onPush = func(fakePushCall) {
		git.head = strings.Repeat("9", 40)
	}
	deps := Deps{Git: git, Resolver: resolver, Now: fixedNow()}
	err := Run(context.Background(), 7, runCtx, pkg, candidates, store, deps)
	require.ErrorIs(t, err, ErrNeedsManualRecovery)

	// sentinel written.
	sentinelPath := filepath.Join(runCtx.RunsBase, "needs-recovery", string(runCtx.RunID)+".json")
	assert.FileExists(t, sentinelPath)

	events := readStateEvents(t, runCtx)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindNeedsManualRecovery, events[len(events)-1].Kind)
}
func TestRun_RollbackRequiresEmptyBestShaBeforeForEmptyRemoteHead(t *testing.T) {
	t.Run("fresh rollback blocks on empty remote head when best sha before exists", func(t *testing.T) {
		runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR701")
		git := &fakeGit{head: resolver.target.BestShaBefore, pushErr: ErrLeaseFailure}
		git.onPush = func(fakePushCall) {
			git.head = ""
		}

		err := Run(context.Background(), 701, runCtx, pkg, candidates, store, Deps{Git: git, Resolver: resolver, Now: fixedNow()})
		require.ErrorIs(t, err, ErrNeedsManualRecovery)
		assert.FileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, string(runCtx.RunID)+".json"))
	})

	t.Run("fresh rollback allows empty remote head when best sha before is empty", func(t *testing.T) {
		runCtx, pkg, candidates, _, resolver := newFixtureWithResolver(t, "PR702")
		resolver.target.BestShaBefore = ""
		intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
		writer := state.NewWriter(runCtx)

		require.NoError(t, handleRollback(context.Background(), 702, runCtx, pkg, resolver.target, intention, noopStore{}, writer, Deps{
			Git:      &fakeGit{head: ""},
			Resolver: resolver,
			Now:      fixedNow(),
		}, contracts.RollbackReasonLeaseFailure, pushUnknown))
		assert.Equal(t, contracts.DecisionActionRollback, readDecision(t, runCtx).Action)
		assert.NoFileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, string(runCtx.RunID)+".json"))
	})

	t.Run("resume rollback blocks on empty remote head when best sha before exists", func(t *testing.T) {
		runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR703")
		intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
		intention.Stage = contracts.IntentionStageRollingBackBranchReverted
		intention.RecoveryReason = contracts.RollbackReasonTransactionalFailure
		intention.FailedStep = contracts.FailedStep70
		require.NoError(t, store.Save(intention))

		err := Run(context.Background(), 703, runCtx, pkg, candidates, store, Deps{
			Git:      &fakeGit{head: ""},
			Resolver: unexpectedResolver{t: t},
			Now:      fixedNow(),
		})
		require.ErrorIs(t, err, ErrNeedsManualRecovery)
		assert.FileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, string(runCtx.RunID)+".json"))
	})

	t.Run("resume rollback allows empty remote head when best sha before is empty", func(t *testing.T) {
		runCtx, pkg, candidates, _, resolver := newFixtureWithResolver(t, "PR704")
		resolver.target.BestShaBefore = ""
		intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
		intention.Stage = contracts.IntentionStageRollingBackBranchReverted
		intention.RecoveryReason = contracts.RollbackReasonTransactionalFailure
		intention.FailedStep = contracts.FailedStep70
		writer := state.NewWriter(runCtx)

		require.NoError(t, ensureRollbackBranchState(context.Background(), 704, runCtx, pkg, intention, noopStore{}, writer, Deps{
			Git:      &fakeGit{head: ""},
			Resolver: unexpectedResolver{t: t},
			Now:      fixedNow(),
		}))
	})
}
func TestRun_PlanningRecoveryPrePushCrash(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR10")
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	require.NoError(t, store.Save(intention))

	git := &fakeGit{head: resolver.target.BestShaBefore}
	deps := Deps{Git: git, Resolver: resolver, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 10, runCtx, pkg, candidates, store, deps))

	events := readStateEvents(t, runCtx)
	kinds := make([]contracts.StateKind, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	assert.Contains(t, kinds, contracts.StateKindInterrupted)
	assert.Contains(t, kinds, contracts.StateKindPromoted)
}
func TestRun_RollbackWithoutRegistryAppendSkipsRollbackRegistryStage(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR11")
	git := &fakeGit{head: resolver.target.BestShaBefore, pushErr: ErrLeaseFailure}
	deps := Deps{Git: git, Resolver: resolver, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 11, runCtx, pkg, candidates, store, deps))

	assert.NoFileExists(t, intentionPath(t, runCtx))
	decision := readDecision(t, runCtx)
	assert.Equal(t, contracts.DecisionActionRollback, decision.Action)
	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	require.NoError(t, err)
	assert.Len(t, lines, 0)
}
func TestRun_ResumeFromRollingBackRegistryAppended(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR12")
	appendResult, _ := seedRegistryAdd(t, runCtx.RulesRegistryPath(), resolver, runCtx.RunID, candidates.CandidatesHash)
	rollbackResult, err := appendRegistryRollbacks(runCtx, contracts.IntentionRecord{
		SchemaVersion:        "1",
		Stage:                contracts.IntentionStageRollingBackBranchReverted,
		IdempotencyKey:       contracts.ComputeAdoptIdempotencyKey(string(runCtx.RunID), resolver.target.TargetSHA, resolver.target.BestShaBefore, candidates.CandidatesHash),
		RunID:                runCtx.RunID,
		BestShaBefore:        resolver.target.BestShaBefore,
		TargetSha:            resolver.target.TargetSHA,
		CandidatesHash:       candidates.CandidatesHash,
		RegistryHeadBefore:   "",
		PlannedAdoption:      mustPlannedAdoption(t, contracts.ComputeAdoptIdempotencyKey(string(runCtx.RunID), resolver.target.TargetSHA, resolver.target.BestShaBefore, candidates.CandidatesHash), resolver.target.RulesToAppend),
		StartedAt:            time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
		RegistryAppendResult: &appendResult,
	}, contracts.RollbackReasonTransactionalFailure, time.Date(2026, 4, 21, 10, 0, 1, 0, time.UTC))
	require.NoError(t, err)
	require.NotNil(t, rollbackResult)

	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageRollingBackRegistryAppended
	intention.RecoveryReason = contracts.RollbackReasonTransactionalFailure
	intention.FailedStep = contracts.FailedStep70
	intention.RegistryAppendResult = rollbackResult
	require.NoError(t, store.Save(intention))

	deps := Deps{Git: &fakeGit{head: resolver.target.BestShaBefore}, Resolver: unexpectedResolver{t: t}, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 12, runCtx, pkg, candidates, store, deps))

	events := readStateEvents(t, runCtx)
	assert.Equal(t, contracts.StateKindRollback, events[len(events)-1].Kind)
	assert.NoFileExists(t, intentionPath(t, runCtx))
}
func TestRun_ResumeFromRollingBackBranchReverted(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR121")
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageRollingBackBranchReverted
	intention.RecoveryReason = contracts.RollbackReasonTransactionalFailure
	intention.FailedStep = contracts.FailedStep70
	require.NoError(t, store.Save(intention))

	deps := Deps{Git: &fakeGit{head: resolver.target.BestShaBefore}, Resolver: unexpectedResolver{t: t}, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 121, runCtx, pkg, candidates, store, deps))

	decision := readDecision(t, runCtx)
	assert.Equal(t, contracts.DecisionActionRollback, decision.Action)
	events := readStateEvents(t, runCtx)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindRollback, events[len(events)-1].Kind)
	assert.NoFileExists(t, intentionPath(t, runCtx))
}
func TestRun_ResumeFromRollingBackDecisionWritten(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR122")
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageRollingBackDecisionWritten
	intention.RecoveryReason = contracts.RollbackReasonTransactionalFailure
	intention.FailedStep = contracts.FailedStep70
	intention.RegistryAppendResult = &contracts.RegistryAppendResult{Offset: 123, Sha256: strings.Repeat("d", 64)}
	require.NoError(t, store.Save(intention))

	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(decisionPath, contracts.Decision{
		Action: contracts.DecisionActionRollback,
		Value: contracts.DecisionRollback{
			Action:         contracts.DecisionActionRollback,
			SchemaVersion:  "1",
			RunID:          runCtx.RunID,
			IdempotencyKey: intention.IdempotencyKey,
			RollbackReason: contracts.RollbackReasonTransactionalFailure,
			FailedStep:     contracts.FailedStep70,
			BestShaBefore:  intention.BestShaBefore,
			TargetSha:      intention.TargetSha,
			DecidedAt:      fixedNow()(),
		},
	}))

	deps := Deps{Git: &fakeGit{head: resolver.target.BestShaBefore}, Resolver: unexpectedResolver{t: t}, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 122, runCtx, pkg, candidates, store, deps))

	events := readStateEvents(t, runCtx)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindRollback, events[len(events)-1].Kind)
	assert.NoFileExists(t, intentionPath(t, runCtx))
}
func TestRun_RollbackDeleteFailureDoesNotAppendRollback(t *testing.T) {
	runCtx, pkg, candidates, baseStore, resolver := newFixtureWithResolver(t, "PR125")
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageRollingBackDecisionWritten
	intention.RecoveryReason = contracts.RollbackReasonTransactionalFailure
	intention.FailedStep = contracts.FailedStep70
	intention.RegistryAppendResult = &contracts.RegistryAppendResult{Offset: 123, Sha256: strings.Repeat("d", 64)}
	require.NoError(t, baseStore.Save(intention))

	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(decisionPath, contracts.Decision{
		Action: contracts.DecisionActionRollback,
		Value: contracts.DecisionRollback{
			Action:         contracts.DecisionActionRollback,
			SchemaVersion:  "1",
			RunID:          runCtx.RunID,
			IdempotencyKey: intention.IdempotencyKey,
			RollbackReason: contracts.RollbackReasonTransactionalFailure,
			FailedStep:     contracts.FailedStep70,
			BestShaBefore:  intention.BestShaBefore,
			TargetSha:      intention.TargetSha,
			DecidedAt:      fixedNow()(),
		},
	}))

	store := deleteFailStore{IntentionWriter: baseStore, deleteErr: errors.New("delete intention")}
	err = Run(context.Background(), 125, runCtx, pkg, candidates, store, Deps{
		Git:      &fakeGit{head: resolver.target.BestShaBefore},
		Resolver: unexpectedResolver{t: t},
		Now:      fixedNow(),
	})
	require.ErrorContains(t, err, "delete intention")
	assert.NotContains(t, readStateKinds(t, runCtx), contracts.StateKindRollback)
	assert.FileExists(t, intentionPath(t, runCtx))
}
func TestRun_NeedsManualRecoveryStageRequiresExplicitCleanup(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR123")
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageNeedsManualRecovery
	intention.RecoveryReason = contracts.RollbackReasonTransactionalFailure
	intention.FailedStep = contracts.FailedStep70
	require.NoError(t, store.Save(intention))
	require.NoError(t, writeSentinel(runCtx.RunsBase, runCtx.RunID, 123, contracts.RollbackReasonTransactionalFailure, contracts.FailedStep70, fixedNow()()))

	deps := Deps{Git: &fakeGit{head: resolver.target.BestShaBefore}, Resolver: unexpectedResolver{t: t}, Now: fixedNow()}
	require.ErrorIs(t, Run(context.Background(), 123, runCtx, pkg, candidates, store, deps), ErrBlockedBySentinel)
	assert.FileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, string(runCtx.RunID)+".json"))

	require.NoError(t, FinalizeCleanup(runCtx, store))
	events := readStateEvents(t, runCtx)
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	require.Equal(t, contracts.StateKindCompleted, last.Kind)
	completed, ok := last.Value.(contracts.StateEntryCompleted)
	require.True(t, ok)
	assert.Equal(t, "manual_cleanup_finalized", completed.Detail)
	assert.NoFileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, string(runCtx.RunID)+".json"))
	assert.FileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, contracts.NeedsRecoverySentinelClearedFilename(runCtx.RunID)))
	assert.NoFileExists(t, intentionPath(t, runCtx))

	deps = Deps{Git: &fakeGit{head: resolver.target.BestShaBefore}, Resolver: resolver, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 123, runCtx, pkg, candidates, store, deps))
	assert.Equal(t, contracts.DecisionActionAdopt, readDecision(t, runCtx).Action)
}
func TestFinalizeCleanupKeepsSentinelAndIntentionWhenCompletedAppendFails(t *testing.T) {
	runCtx, _, candidates, store, resolver := newFixtureWithResolver(t, "PR124")
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageNeedsManualRecovery
	intention.RecoveryReason = contracts.RollbackReasonManualAbortPendingCleanup
	intention.FailedStep = contracts.FailedStep70
	require.NoError(t, store.Save(intention))
	require.NoError(t, writeAbortedSentinel(runCtx.RunsBase, runCtx.RunID, 124, contracts.RollbackReasonManualAbortPendingCleanup, contracts.FailedStep70, fixedNow()()))
	require.NoError(t, os.Mkdir(filepath.Join(runCtx.RunsBase, "state.lock"), 0o755))

	err := FinalizeCleanup(runCtx, store)
	require.Error(t, err)
	assert.FileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, contracts.NeedsRecoverySentinelAbortedFilename(runCtx.RunID)))
	assert.NoFileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, contracts.NeedsRecoverySentinelClearedFilename(runCtx.RunID)))
	assert.FileExists(t, intentionPath(t, runCtx))
}
func TestRun_PlanningRecoveryPrePushCrash_RegistryAdvancedRestartsFresh(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR13")
	initialTarget := resolver.target
	intention := planningIntention(runCtx.RunID, initialTarget, candidates.CandidatesHash)
	firstResult, _ := seedRegistryUniqueAdd(t, runCtx.RulesRegistryPath(), "seed-0", strings.Repeat("0", 64), "2026-04-21-PR90-abcdef0")
	intention.RegistryHeadBefore = firstResult.Sha256
	require.NoError(t, store.Save(intention))
	_, _ = seedRegistryUniqueAdd(t, runCtx.RulesRegistryPath(), "seed-1", strings.Repeat("1", 64), "2026-04-21-PR91-bcdef01")

	secondTarget := Target{
		BestBranch:    initialTarget.BestBranch,
		BestShaBefore: initialTarget.BestShaBefore,
		TargetSHA:     strings.Repeat("3", 40),
		RulesToAppend: []contracts.RuleRegistryEntry{adoptAddedEntryWithTarget(runCtx.RunID, candidates.CandidatesHash, strings.Repeat("3", 40), "seed-1")},
	}
	seqResolver := &sequenceResolver{targets: []Target{initialTarget, secondTarget}}

	deps := Deps{Git: &fakeGit{head: initialTarget.BestShaBefore}, Resolver: seqResolver, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 13, runCtx, pkg, candidates, store, deps))

	events := readStateEvents(t, runCtx)
	kinds := make([]contracts.StateKind, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	assert.Contains(t, kinds, contracts.StateKindInterrupted)
	assert.Contains(t, kinds, contracts.StateKindPromoted)

	decision := readDecision(t, runCtx)
	adopt, ok := decision.Value.(contracts.DecisionAdopt)
	require.True(t, ok)
	assert.Equal(t, secondTarget.TargetSHA, adopt.TargetSha)
	assert.Equal(t, contracts.ComputeAdoptIdempotencyKey(string(runCtx.RunID), secondTarget.TargetSHA, secondTarget.BestShaBefore, candidates.CandidatesHash), adopt.IdempotencyKey)
	assert.NotEqual(t, intention.IdempotencyKey, adopt.IdempotencyKey)
	assert.NoFileExists(t, intentionPath(t, runCtx))
}
func TestRun_CanceledPushReturnsContextWithoutRollback(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR706")
	ctx, cancel := context.WithCancel(context.Background())
	git := cancelOnPushGit{
		head:   resolver.target.BestShaBefore,
		cancel: cancel,
	}

	err := Run(ctx, 706, runCtx, pkg, candidates, store, Deps{Git: git, Resolver: resolver, Now: fixedNow()})
	require.ErrorIs(t, err, context.Canceled)

	decisionPath, pathErr := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, pathErr)
	assert.NoFileExists(t, decisionPath)
	assert.NoFileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, string(runCtx.RunID)+".json"))
}
func TestRun_PersistedRollbackDecisionCleansGitWorktrees(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR202")
	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(decisionPath, contracts.Decision{
		Action: contracts.DecisionActionRollback,
		Value: contracts.DecisionRollback{
			Action:         contracts.DecisionActionRollback,
			SchemaVersion:  "1",
			RunID:          runCtx.RunID,
			IdempotencyKey: contracts.ComputeAdoptIdempotencyKey(string(runCtx.RunID), resolver.target.TargetSHA, resolver.target.BestShaBefore, candidates.CandidatesHash),
			RollbackReason: contracts.RollbackReasonLeaseFailure,
			FailedStep:     contracts.FailedStep70,
			BestShaBefore:  resolver.target.BestShaBefore,
			TargetSha:      resolver.target.TargetSHA,
			DecidedAt:      fixedNow()(),
		},
	}))
	git := &fakeGit{head: resolver.target.BestShaBefore}

	require.NoError(t, Run(context.Background(), 202, runCtx, pkg, candidates, store, Deps{Git: git, Resolver: unexpectedResolver{t: t}, Now: fixedNow()}))

	require.Len(t, git.removeWorktreeCalls, len(pkg.Worktrees))
	for i, wt := range pkg.Worktrees {
		assert.Equal(t, wt.Path, git.removeWorktreeCalls[i])
	}
}
