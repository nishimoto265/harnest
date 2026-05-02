package step70_decide

import (
	"context"
	"errors"
	"fmt"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun_UsesPersistedDecisionBeforeRegistryAppendedIntention(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR410")
	appendResult, _ := seedRegistryAdd(t, runCtx.RulesRegistryPath(), resolver, runCtx.RunID, candidates.CandidatesHash)

	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageRegistryAppended
	intention.RegistryAppendResult = &appendResult
	require.NoError(t, store.Save(intention))

	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(decisionPath, contracts.Decision{
		Action: contracts.DecisionActionAdopt,
		Value: contracts.DecisionAdopt{
			Action:               contracts.DecisionActionAdopt,
			SchemaVersion:        "1",
			RunID:                runCtx.RunID,
			IdempotencyKey:       intention.IdempotencyKey,
			BestShaBefore:        intention.BestShaBefore,
			TargetSha:            intention.TargetSha,
			CandidatesHash:       intention.CandidatesHash,
			RegistryAppendResult: appendResult,
			DecidedAt:            fixedNow()(),
		},
	}))

	require.NoError(t, Run(context.Background(), 410, runCtx, pkg, candidates, store, Deps{
		Git:      &fakeGit{head: resolver.target.TargetSHA},
		Resolver: unexpectedResolver{t: t},
		Now:      fixedNow(),
	}))

	events := readStateEvents(t, runCtx)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindPromoted, events[len(events)-1].Kind)
	assert.NoFileExists(t, intentionPath(t, runCtx))
}

func TestRun_ResumeFromPersistedDecisionRepublishesMissingRuleBodies(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR411")
	resolver.target.RulesToAppend = []contracts.RuleRegistryEntry{
		adoptAddedEntryWithBody(runCtx.RunID, "rule-a", "rule-a body\n"),
		adoptAddedEntryWithBody(runCtx.RunID, "rule-b", "rule-b body\n"),
	}

	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageRegistryAppended
	entries, err := registryEntriesFromPlannedAdoption(intention, fixedNow()())
	require.NoError(t, err)
	var appendResult contracts.RegistryAppendResult
	for _, entry := range entries {
		entry, err = deriveRegistryChain(entry, runCtx.RulesRegistryPath())
		require.NoError(t, err)
		appendResult, err = internalio.AppendRegistryEntry(runCtx.RulesRegistryPath(), entry)
		require.NoError(t, err)
	}
	intention.RegistryAppendResult = &appendResult
	require.NoError(t, store.Save(intention))

	require.NoError(t, internalio.WriteAtomic(mustStagedRulePath(t, runCtx, "rules/rule-a.md"), []byte("rule-a body\n")))
	require.NoError(t, internalio.WriteAtomic(mustStagedRulePath(t, runCtx, "rules/rule-b.md"), []byte("rule-b body\n")))

	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(decisionPath, contracts.Decision{
		Action: contracts.DecisionActionAdopt,
		Value: contracts.DecisionAdopt{
			Action:               contracts.DecisionActionAdopt,
			SchemaVersion:        "1",
			RunID:                runCtx.RunID,
			IdempotencyKey:       intention.IdempotencyKey,
			BestShaBefore:        intention.BestShaBefore,
			TargetSha:            intention.TargetSha,
			CandidatesHash:       intention.CandidatesHash,
			RegistryAppendResult: appendResult,
			DecidedAt:            fixedNow()(),
		},
	}))

	originalPromote := promoteRuleSidecarFn
	callCount := 0
	promoteRuleSidecarFn = func(stagedPath, dstPath, wantSHA, prevSHA string) error {
		callCount++
		if callCount == 2 {
			return errors.New("synthetic crash during rule publish")
		}
		return originalPromote(stagedPath, dstPath, wantSHA, prevSHA)
	}
	err = Run(context.Background(), 411, runCtx, pkg, candidates, store, Deps{
		Git:      &fakeGit{head: resolver.target.TargetSHA},
		Resolver: unexpectedResolver{t: t},
		Now:      fixedNow(),
	})
	require.ErrorIs(t, err, ErrNeedsManualRecovery)
	events := readStateEvents(t, runCtx)
	require.NotEmpty(t, events)
	recovery := mustNeedsManualRecoveryEvent(t, events[len(events)-1])
	assert.Equal(t, "rule_publish_failure", recovery.Detail)

	promoteRuleSidecarFn = originalPromote
	t.Cleanup(func() {
		promoteRuleSidecarFn = originalPromote
	})
	assert.FileExists(t, filepath.Join(runCtx.RunsBase, "rules", "rule-a.md"))
	assert.NoFileExists(t, filepath.Join(runCtx.RunsBase, "rules", "rule-b.md"))
	assert.FileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, string(runCtx.RunID)+".json"))
	assert.FileExists(t, intentionPath(t, runCtx))
}

// F11: pre-push rollback (driveAdopt push-failure path) with remoteHead==
// target_sha and no ownership proof must mark needs_manual_recovery instead
// of force-pushing the branch back to best_sha_before — another run may have
// pushed the same SHA and promoted it.
func TestRun_PersistedAdoptDecisionWithStagedRulesButNoIntentionFailsClosed(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR418")
	resolver.target.RulesToAppend = []contracts.RuleRegistryEntry{
		adoptAddedEntryWithBody(runCtx.RunID, "rule-a", "rule-a body\n"),
	}

	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	entries, err := registryEntriesFromPlannedAdoption(intention, fixedNow()())
	require.NoError(t, err)
	entry, err := deriveRegistryChain(entries[0], runCtx.RulesRegistryPath())
	require.NoError(t, err)
	appendResult, err := internalio.AppendRegistryEntry(runCtx.RulesRegistryPath(), entry)
	require.NoError(t, err)

	require.NoError(t, internalio.WriteAtomic(mustStagedRulePath(t, runCtx, "rules/rule-a.md"), []byte("rule-a body\n")))
	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(decisionPath, contracts.Decision{
		Action: contracts.DecisionActionAdopt,
		Value: contracts.DecisionAdopt{
			Action:               contracts.DecisionActionAdopt,
			SchemaVersion:        "1",
			RunID:                runCtx.RunID,
			IdempotencyKey:       intention.IdempotencyKey,
			BestShaBefore:        intention.BestShaBefore,
			TargetSha:            intention.TargetSha,
			CandidatesHash:       intention.CandidatesHash,
			RegistryAppendResult: appendResult,
			DecidedAt:            fixedNow()(),
		},
	}))

	err = Run(context.Background(), 418, runCtx, pkg, candidates, store, Deps{
		Git:      &fakeGit{head: resolver.target.TargetSHA},
		Resolver: unexpectedResolver{t: t},
		Now:      fixedNow(),
	})
	require.ErrorIs(t, err, errMissingPlannedAdoptionForStaging)
	assert.FileExists(t, mustStagedRulePath(t, runCtx, "rules/rule-a.md"))
	assert.NoFileExists(t, filepath.Join(runCtx.RunsBase, "rules", "rule-a.md"))
}

func TestRun_ResumeFromDecisionWritten(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR5")
	registryPath := runCtx.RulesRegistryPath()
	appendResult, _ := seedRegistryAdd(t, registryPath, resolver, runCtx.RunID, candidates.CandidatesHash)

	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageDecisionWritten
	intention.RegistryAppendResult = &appendResult
	require.NoError(t, store.Save(intention))

	// decision.json pre-existing to simulate crash after stage 5.
	d := contracts.Decision{
		Action: contracts.DecisionActionAdopt,
		Value: contracts.DecisionAdopt{
			Action:               contracts.DecisionActionAdopt,
			SchemaVersion:        "1",
			RunID:                runCtx.RunID,
			IdempotencyKey:       intention.IdempotencyKey,
			BestShaBefore:        intention.BestShaBefore,
			TargetSha:            intention.TargetSha,
			CandidatesHash:       intention.CandidatesHash,
			RegistryAppendResult: appendResult,
			DecidedAt:            fixedNow()(),
		},
	}
	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(decisionPath, d))

	git := &fakeGit{head: resolver.target.TargetSHA}
	deps := Deps{Git: git, Resolver: unexpectedResolver{t: t}, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 5, runCtx, pkg, candidates, store, deps))

	events := readStateEvents(t, runCtx)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindPromoted, events[len(events)-1].Kind)
	assert.NoFileExists(t, intentionPath(t, runCtx))
}

func TestRun_AdoptDeleteFailureDoesNotAppendPromoted(t *testing.T) {
	runCtx, pkg, candidates, baseStore, resolver := newFixtureWithResolver(t, "PR506")
	registryPath := runCtx.RulesRegistryPath()
	appendResult, _ := seedRegistryAdd(t, registryPath, resolver, runCtx.RunID, candidates.CandidatesHash)

	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageDecisionWritten
	intention.RegistryAppendResult = &appendResult
	require.NoError(t, baseStore.Save(intention))

	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(decisionPath, contracts.Decision{
		Action: contracts.DecisionActionAdopt,
		Value: contracts.DecisionAdopt{
			Action:               contracts.DecisionActionAdopt,
			SchemaVersion:        "1",
			RunID:                runCtx.RunID,
			IdempotencyKey:       intention.IdempotencyKey,
			BestShaBefore:        intention.BestShaBefore,
			TargetSha:            intention.TargetSha,
			CandidatesHash:       intention.CandidatesHash,
			RegistryAppendResult: appendResult,
			DecidedAt:            fixedNow()(),
		},
	}))

	store := deleteFailStore{IntentionWriter: baseStore, deleteErr: errors.New("delete intention")}
	err = Run(context.Background(), 506, runCtx, pkg, candidates, store, Deps{
		Git:      &fakeGit{head: resolver.target.TargetSHA},
		Resolver: unexpectedResolver{t: t},
		Now:      fixedNow(),
	})
	require.ErrorContains(t, err, "delete intention")
	assert.NotContains(t, readStateKinds(t, runCtx), contracts.StateKindPromoted)
	assert.FileExists(t, intentionPath(t, runCtx))
}

func TestRun_ResumeFromDecisionWritten_RemoteMismatchNeedsManualRecovery(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR501")
	registryPath := runCtx.RulesRegistryPath()
	appendResult, _ := seedRegistryAdd(t, registryPath, resolver, runCtx.RunID, candidates.CandidatesHash)

	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageDecisionWritten
	intention.RegistryAppendResult = &appendResult
	require.NoError(t, store.Save(intention))

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
			RegistryAppendResult: appendResult,
			DecidedAt:            fixedNow()(),
		},
	}
	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(decisionPath, decision))

	err = Run(context.Background(), 501, runCtx, pkg, candidates, store, Deps{
		Git:      &fakeGit{head: strings.Repeat("9", 40)},
		Resolver: unexpectedResolver{t: t},
		Now:      fixedNow(),
	})
	require.ErrorIs(t, err, ErrNeedsManualRecovery)

	events := readStateEvents(t, runCtx)
	require.NotEmpty(t, events)
	recovery := mustNeedsManualRecoveryEvent(t, events[len(events)-1])
	assert.Equal(t, contracts.RollbackReasonRemoteDivergence, recovery.Reason)
	assert.Equal(t, "decision_written_remote_mismatch", recovery.Detail)
}

func TestRun_OrphanPersistedAdoptDecisionRemoteMismatchNeedsManualRecovery(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR502")
	registryPath := runCtx.RulesRegistryPath()
	appendResult, _ := seedRegistryAdd(t, registryPath, resolver, runCtx.RunID, candidates.CandidatesHash)
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	require.NoError(t, cleanupStagedRuleSidecars(runCtx))

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
			RegistryAppendResult: appendResult,
			DecidedAt:            fixedNow()(),
		},
	}
	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(decisionPath, decision))

	err = Run(context.Background(), 502, runCtx, pkg, candidates, store, Deps{
		Git:      &fakeGit{head: strings.Repeat("9", 40)},
		Resolver: unexpectedResolver{t: t},
		Now:      fixedNow(),
	})
	require.ErrorIs(t, err, ErrNeedsManualRecovery)

	events := readStateEvents(t, runCtx)
	require.NotEmpty(t, events)
	recovery := mustNeedsManualRecoveryEvent(t, events[len(events)-1])
	assert.Equal(t, contracts.RollbackReasonRemoteDivergence, recovery.Reason)
	assert.Equal(t, "decision_written_remote_mismatch", recovery.Detail)
	assert.NotContains(t, readStateKinds(t, runCtx), contracts.StateKindPromoted)
}

func TestRun_OrphanPersistedAdoptDecisionAllowsLaterRegistryProgress(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR503")
	registryPath := runCtx.RulesRegistryPath()
	appendResult, _ := seedRegistryAdd(t, registryPath, resolver, runCtx.RunID, candidates.CandidatesHash)
	_, _ = seedRegistryUniqueAdd(t, registryPath, "other-rule", fmt.Sprintf("%064x", 7503), "2026-04-21-PR75-abcdef0")
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	require.NoError(t, cleanupStagedRuleSidecars(runCtx))

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
			RegistryAppendResult: appendResult,
			DecidedAt:            fixedNow()(),
		},
	}
	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(decisionPath, decision))

	err = Run(context.Background(), 503, runCtx, pkg, candidates, store, Deps{
		Git:      &fakeGit{head: resolver.target.TargetSHA},
		Resolver: unexpectedResolver{t: t},
		Now:      fixedNow(),
	})
	require.NoError(t, err)

	events := readStateEvents(t, runCtx)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindPromoted, events[len(events)-1].Kind)
	assert.NoFileExists(t, intentionPath(t, runCtx))
}

func TestRun_OrphanPersistedAdoptDecisionMissingRegistryRowNeedsManualRecovery(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR506")
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	require.NoError(t, cleanupStagedRuleSidecars(runCtx))

	decision := contracts.Decision{
		Action: contracts.DecisionActionAdopt,
		Value: contracts.DecisionAdopt{
			Action:         contracts.DecisionActionAdopt,
			SchemaVersion:  "1",
			RunID:          runCtx.RunID,
			IdempotencyKey: intention.IdempotencyKey,
			BestShaBefore:  intention.BestShaBefore,
			TargetSha:      intention.TargetSha,
			CandidatesHash: intention.CandidatesHash,
			RegistryAppendResult: contracts.RegistryAppendResult{
				Offset: 0,
				Sha256: strings.Repeat("c", 64),
			},
			DecidedAt: fixedNow()(),
		},
	}
	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(decisionPath, decision))

	err = Run(context.Background(), 506, runCtx, pkg, candidates, store, Deps{
		Git:      &fakeGit{head: resolver.target.TargetSHA},
		Resolver: unexpectedResolver{t: t},
		Now:      fixedNow(),
	})
	require.ErrorIs(t, err, ErrNeedsManualRecovery)

	events := readStateEvents(t, runCtx)
	require.NotEmpty(t, events)
	recovery := mustNeedsManualRecoveryEvent(t, events[len(events)-1])
	assert.Equal(t, contracts.RollbackReasonRegistryDivergence, recovery.Reason)
	assert.Equal(t, "decision_written_registry_mismatch", recovery.Detail)
	assert.NotContains(t, readStateKinds(t, runCtx), contracts.StateKindPromoted)
}
