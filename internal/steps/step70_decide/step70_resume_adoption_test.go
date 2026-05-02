package step70_decide

import (
	"context"
	"errors"
	"fmt"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRun_ResumeFromBranchPushed_IdempotencyHit(t *testing.T) {
	runCtx, pkg, candidates, _, resolver := newFixtureWithResolver(t, "PR4")
	store := newTrackingStore(intentionPath(t, runCtx))
	appendResult, _ := seedRegistryAdd(t, runCtx.RulesRegistryPath(), resolver, runCtx.RunID, candidates.CandidatesHash)
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageBranchPushed
	require.NoError(t, store.Save(intention))

	git := &fakeGit{head: resolver.target.TargetSHA}
	deps := Deps{Git: git, Resolver: unexpectedResolver{t: t}, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 4, runCtx, pkg, candidates, store, deps))

	decision := readDecision(t, runCtx)
	assert.Equal(t, contracts.DecisionActionAdopt, decision.Action)
	adopt, ok := decision.Value.(contracts.DecisionAdopt)
	require.True(t, ok)
	assert.Equal(t, appendResult, adopt.RegistryAppendResult)
	require.Len(t, store.saved, 4)
	assert.Equal(t, contracts.IntentionStageRegistryAppended, store.saved[1].Stage)
	require.NotNil(t, store.saved[1].RegistryAppendResult)
	assert.Equal(t, appendResult, *store.saved[1].RegistryAppendResult)
	assert.NotEmpty(t, store.saved[2].PublishedRuleOpIDs)
	// No additional push from the resume path (branch already pushed).
	assert.Empty(t, git.pushCalls)
}
func TestRun_ResumeFromBranchPushed_MultiEntryIdempotencyHit(t *testing.T) {
	runCtx, pkg, candidates, _, resolver := newFixtureWithResolver(t, "PR401")
	resolver.target.RulesToAppend = adoptAddedEntriesWithTarget(runCtx.RunID, candidates.CandidatesHash, resolver.target.TargetSHA, "rule-a", "rule-b")
	stageFixtureRuleSidecars(t, runCtx, resolver.target)
	store := newTrackingStore(intentionPath(t, runCtx))
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageBranchPushed
	require.NoError(t, store.Save(intention))

	entries, err := registryEntriesFromPlannedAdoption(intention, time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	var lastResult contracts.RegistryAppendResult
	for _, entry := range entries {
		derived, deriveErr := deriveRegistryChain(entry, runCtx.RulesRegistryPath())
		require.NoError(t, deriveErr)
		lastResult, err = internalio.AppendRegistryEntry(runCtx.RulesRegistryPath(), derived)
		require.NoError(t, err)
	}

	deps := Deps{Git: &fakeGit{head: resolver.target.TargetSHA}, Resolver: unexpectedResolver{t: t}, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 401, runCtx, pkg, candidates, store, deps))

	adopt := mustDecisionAdopt(t, readDecision(t, runCtx))
	assert.Equal(t, lastResult, adopt.RegistryAppendResult)
	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	require.NoError(t, err)
	assert.Len(t, lines, 2)
}
func TestRun_ResumeFromBranchPushed_CASAppendSucceeds(t *testing.T) {
	runCtx, pkg, candidates, _, resolver := newFixtureWithResolver(t, "PR42")
	store := newTrackingStore(intentionPath(t, runCtx))
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageBranchPushed
	require.NoError(t, store.Save(intention))

	deps := Deps{Git: &fakeGit{head: resolver.target.TargetSHA}, Resolver: unexpectedResolver{t: t}, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 42, runCtx, pkg, candidates, store, deps))

	decision := readDecision(t, runCtx)
	adopt, ok := decision.Value.(contracts.DecisionAdopt)
	require.True(t, ok)
	require.Len(t, store.saved, 5)
	assert.Equal(t, contracts.IntentionStageBranchPushed, store.saved[1].Stage)
	assert.NotEmpty(t, store.saved[1].AppendedEntryOpIDs)
	assert.Equal(t, contracts.IntentionStageRegistryAppended, store.saved[2].Stage)
	require.NotNil(t, store.saved[2].RegistryAppendResult)
	assert.Equal(t, *store.saved[2].RegistryAppendResult, adopt.RegistryAppendResult)

	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	require.NoError(t, err)
	require.Len(t, lines, 1)
	assert.Equal(t, lines[0].Offset, adopt.RegistryAppendResult.Offset)
	assert.Equal(t, lines[0].Sha256, adopt.RegistryAppendResult.Sha256)
}
func TestRun_ResumeFromBranchPushed_CASMismatchRetrySucceeds(t *testing.T) {
	runCtx, pkg, candidates, _, resolver := newFixtureWithResolver(t, "PR43")
	store := newTrackingStore(intentionPath(t, runCtx))
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageBranchPushed
	require.NoError(t, store.Save(intention))
	installAppendCASMismatchHook(t, runCtx, 1)

	deps := Deps{Git: &fakeGit{head: resolver.target.TargetSHA}, Resolver: unexpectedResolver{t: t}, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 43, runCtx, pkg, candidates, store, deps))

	decision := readDecision(t, runCtx)
	adopt := mustDecisionAdopt(t, decision)
	assert.Equal(t, contracts.IntentionStageRegistryAppended, store.saved[2].Stage)

	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	require.NoError(t, err)
	require.Len(t, lines, 2)
	assert.Equal(t, lines[len(lines)-1].Offset, adopt.RegistryAppendResult.Offset)
	assert.Equal(t, lines[len(lines)-1].Sha256, adopt.RegistryAppendResult.Sha256)
}
func TestRun_ResumeFromBranchPushed_MultiEntryResumePartialFromRegistry(t *testing.T) {
	runCtx, pkg, candidates, _, resolver := newFixtureWithResolver(t, "PR430")
	resolver.target.RulesToAppend = adoptAddedEntriesWithTarget(runCtx.RunID, candidates.CandidatesHash, resolver.target.TargetSHA, "rule-a", "rule-b")
	stageFixtureRuleSidecars(t, runCtx, resolver.target)
	store := newTrackingStore(intentionPath(t, runCtx))
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageBranchPushed
	require.NoError(t, store.Save(intention))

	entries, err := registryEntriesFromPlannedAdoption(intention, time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	_, err = internalio.AppendRegistryEntry(runCtx.RulesRegistryPath(), entries[0])
	require.NoError(t, err)

	deps := Deps{Git: &fakeGit{head: resolver.target.TargetSHA}, Resolver: unexpectedResolver{t: t}, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 430, runCtx, pkg, candidates, store, deps))

	adopt := mustDecisionAdopt(t, readDecision(t, runCtx))
	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	require.NoError(t, err)
	require.Len(t, lines, 2)
	assert.Equal(t, lines[1].Offset, adopt.RegistryAppendResult.Offset)
	assert.Equal(t, lines[1].Sha256, adopt.RegistryAppendResult.Sha256)
}
func TestRun_ResumeFromBranchPushed_MultiEntryCrashRecoveryFromPersistedProgress(t *testing.T) {
	runCtx, pkg, candidates, _, resolver := newFixtureWithResolver(t, "PR431")
	resolver.target.RulesToAppend = adoptAddedEntriesWithTarget(runCtx.RunID, candidates.CandidatesHash, resolver.target.TargetSHA, "rule-a", "rule-b")
	stageFixtureRuleSidecars(t, runCtx, resolver.target)
	store := newTrackingStore(intentionPath(t, runCtx))
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageBranchPushed

	entries, err := registryEntriesFromPlannedAdoption(intention, time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	_, err = internalio.AppendRegistryEntry(runCtx.RulesRegistryPath(), entries[0])
	require.NoError(t, err)
	intention.AppendedEntryOpIDs = []string{intention.PlannedAdoption.Entries[0].OpID}
	require.NoError(t, store.Save(intention))

	deps := Deps{Git: &fakeGit{head: resolver.target.TargetSHA}, Resolver: unexpectedResolver{t: t}, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 431, runCtx, pkg, candidates, store, deps))

	adopt := mustDecisionAdopt(t, readDecision(t, runCtx))
	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	require.NoError(t, err)
	require.Len(t, lines, 2)
	assert.Equal(t, lines[1].Sha256, adopt.RegistryAppendResult.Sha256)
}
func TestRun_ResumeFromBranchPushed_CASMismatchTwiceRollsBack(t *testing.T) {
	runCtx, pkg, candidates, _, resolver := newFixtureWithResolver(t, "PR44")
	store := newTrackingStore(intentionPath(t, runCtx))
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageBranchPushed
	require.NoError(t, store.Save(intention))
	installAppendCASMismatchHook(t, runCtx, 2)

	git := &fakeGit{head: resolver.target.TargetSHA}
	deps := Deps{Git: git, Resolver: unexpectedResolver{t: t}, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 44, runCtx, pkg, candidates, store, deps))

	decision := readDecision(t, runCtx)
	assert.Equal(t, contracts.DecisionActionRollback, decision.Action)
	assert.NoFileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, string(runCtx.RunID)+".json"))
	require.Len(t, git.pushCalls, 1)
	assert.Equal(t, resolver.target.BestShaBefore, git.pushCalls[0].target)
	assert.Equal(t, resolver.target.TargetSHA, git.pushCalls[0].expected)
}
func TestRun_ResumeFromRegistryAppended(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR41")
	appendResult, _ := seedRegistryAdd(t, runCtx.RulesRegistryPath(), resolver, runCtx.RunID, candidates.CandidatesHash)

	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageRegistryAppended
	intention.RegistryAppendResult = &appendResult
	require.NoError(t, store.Save(intention))

	deps := Deps{Git: &fakeGit{head: resolver.target.TargetSHA}, Resolver: unexpectedResolver{t: t}, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 41, runCtx, pkg, candidates, store, deps))

	decision := readDecision(t, runCtx)
	assert.Equal(t, contracts.DecisionActionAdopt, decision.Action)
	events := readStateEvents(t, runCtx)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindPromoted, events[len(events)-1].Kind)
	assert.NoFileExists(t, intentionPath(t, runCtx))
}
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
func TestRun_RulePublishConflictTransitionsToManualRecovery(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR412")
	resolver.target.RulesToAppend = []contracts.RuleRegistryEntry{
		adoptAddedEntryWithBody(runCtx.RunID, "rule-conflict", "new body\n"),
	}

	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageRegistryAppended
	entries, err := registryEntriesFromPlannedAdoption(intention, fixedNow()())
	require.NoError(t, err)
	entry, err := deriveRegistryChain(entries[0], runCtx.RulesRegistryPath())
	require.NoError(t, err)
	appendResult, err := internalio.AppendRegistryEntry(runCtx.RulesRegistryPath(), entry)
	require.NoError(t, err)
	intention.RegistryAppendResult = &appendResult
	require.NoError(t, store.Save(intention))

	require.NoError(t, internalio.WriteAtomic(mustStagedRulePath(t, runCtx, "rules/rule-conflict.md"), []byte("new body\n")))
	require.NoError(t, internalio.WriteAtomic(filepath.Join(runCtx.RunsBase, "rules", "rule-conflict.md"), []byte("old body\n")))

	err = Run(context.Background(), 412, runCtx, pkg, candidates, store, Deps{
		Git:      &fakeGit{head: resolver.target.TargetSHA},
		Resolver: unexpectedResolver{t: t},
		Now:      fixedNow(),
	})
	require.ErrorIs(t, err, ErrNeedsManualRecovery)
	assert.FileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, string(runCtx.RunID)+".json"))

	decisionPath, decisionErr := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, decisionErr)
	assert.NoFileExists(t, decisionPath)

	events := readStateEvents(t, runCtx)
	require.NotEmpty(t, events)
	recovery := mustNeedsManualRecoveryEvent(t, events[len(events)-1])
	assert.Equal(t, "rule_publish_conflict", recovery.Detail)
}
func TestRun_RulePublishUpdateMissingDestinationTransitionsToManualRecovery(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR4121")
	resolver.target.RulesToAppend = []contracts.RuleRegistryEntry{
		adoptUpdatedEntryWithBody(runCtx.RunID, "rule-missing-update", "old body\n", "new body\n"),
	}

	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageRegistryAppended
	entries, err := registryEntriesFromPlannedAdoption(intention, fixedNow()())
	require.NoError(t, err)
	entry, err := deriveRegistryChain(entries[0], runCtx.RulesRegistryPath())
	require.NoError(t, err)
	appendResult, err := internalio.AppendRegistryEntry(runCtx.RulesRegistryPath(), entry)
	require.NoError(t, err)
	intention.RegistryAppendResult = &appendResult
	require.NoError(t, store.Save(intention))

	stagedPath := mustStagedRulePath(t, runCtx, "rules/rule-missing-update.md")
	require.NoError(t, internalio.WriteAtomic(stagedPath, []byte("new body\n")))
	dstPath := filepath.Join(runCtx.RunsBase, "rules", "rule-missing-update.md")
	assert.NoFileExists(t, dstPath)

	err = Run(context.Background(), 4121, runCtx, pkg, candidates, store, Deps{
		Git:      &fakeGit{head: resolver.target.TargetSHA},
		Resolver: unexpectedResolver{t: t},
		Now:      fixedNow(),
	})
	require.ErrorIs(t, err, ErrNeedsManualRecovery)
	assert.NoFileExists(t, dstPath)
	assert.FileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, string(runCtx.RunID)+".json"))

	events := readStateEvents(t, runCtx)
	require.NotEmpty(t, events)
	recovery := mustNeedsManualRecoveryEvent(t, events[len(events)-1])
	assert.Equal(t, "rule_publish_conflict", recovery.Detail)
}
func TestRun_RulePublishIntegrityFailuresTransitionToManualRecovery(t *testing.T) {
	cases := []struct {
		name       string
		prLabel    string
		setup      func(t *testing.T, stagedPath, dstPath string)
		wantDetail string
	}{
		{
			name:    "staged_integrity",
			prLabel: "PR413",
			setup: func(t *testing.T, stagedPath, dstPath string) {
				require.NoError(t, internalio.WriteAtomic(stagedPath, []byte("corrupted body\n")))
			},
			wantDetail: "rule_publish_integrity",
		},
		{
			name:    "destination_type",
			prLabel: "PR414",
			setup: func(t *testing.T, stagedPath, dstPath string) {
				require.NoError(t, internalio.WriteAtomic(stagedPath, []byte("expected body\n")))
				require.NoError(t, os.MkdirAll(dstPath, 0o755))
			},
			wantDetail: "rule_publish_destination_type",
		},
		{
			name:    "staged_missing",
			prLabel: "PR415",
			setup: func(t *testing.T, stagedPath, dstPath string) {
				require.NoError(t, os.MkdirAll(filepath.Dir(stagedPath), 0o755))
			},
			wantDetail: "rule_publish_staged_missing",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, tc.prLabel)
			resolver.target.RulesToAppend = []contracts.RuleRegistryEntry{
				adoptAddedEntryWithBody(runCtx.RunID, "rule-publish", "expected body\n"),
			}

			intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
			intention.Stage = contracts.IntentionStageRegistryAppended
			entries, err := registryEntriesFromPlannedAdoption(intention, fixedNow()())
			require.NoError(t, err)
			entry, err := deriveRegistryChain(entries[0], runCtx.RulesRegistryPath())
			require.NoError(t, err)
			appendResult, err := internalio.AppendRegistryEntry(runCtx.RulesRegistryPath(), entry)
			require.NoError(t, err)
			intention.RegistryAppendResult = &appendResult
			require.NoError(t, store.Save(intention))

			stagedPath := mustStagedRulePath(t, runCtx, "rules/rule-publish.md")
			dstPath := filepath.Join(runCtx.RunsBase, "rules", "rule-publish.md")
			tc.setup(t, stagedPath, dstPath)

			err = Run(context.Background(), 412, runCtx, pkg, candidates, store, Deps{
				Git:      &fakeGit{head: resolver.target.TargetSHA},
				Resolver: unexpectedResolver{t: t},
				Now:      fixedNow(),
			})
			require.ErrorIs(t, err, ErrNeedsManualRecovery)

			events := readStateEvents(t, runCtx)
			require.NotEmpty(t, events)
			recovery := mustNeedsManualRecoveryEvent(t, events[len(events)-1])
			assert.Equal(t, tc.wantDetail, recovery.Detail)
		})
	}
}

// F9: when the durable sentinel write fails, the intention must already be
// parked at stage=needs_manual_recovery so a subsequent resume tick returns
// ErrNeedsManualRecovery from resume() and refuses to reopen the transaction
// path. Otherwise an intention left at branch_pushed / registry_appended could
// silently continue promotion once the sentinel is cleared out of band.
func TestRun_PlanningResumeRemoteMatchesTargetButAnotherRunAdvancedRegistryRestartsFresh(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR4111")
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)

	// Seed registry: planning snapshot points to empty head, but another run
	// has since appended a row unrelated to this intention's planned op-ids.
	_, _ = seedRegistryUniqueAdd(t, runCtx.RulesRegistryPath(), "other-run-seed", fmt.Sprintf("%064x", 7110), "2026-04-21-PR80-abcdef0")
	// intention.RegistryHeadBefore stays at "" (planningIntention default).
	require.NoError(t, store.Save(intention))

	git := &fakeGit{head: resolver.target.TargetSHA}
	secondTarget := Target{
		BestBranch:    resolver.target.BestBranch,
		BestShaBefore: resolver.target.BestShaBefore,
		TargetSHA:     strings.Repeat("3", 40),
		RulesToAppend: []contracts.RuleRegistryEntry{
			adoptAddedEntryWithTarget(runCtx.RunID, candidates.CandidatesHash, strings.Repeat("3", 40), "rule-refreshed"),
		},
	}
	seqResolver := &sequenceResolver{targets: []Target{resolver.target, secondTarget}}
	require.NoError(t, Run(context.Background(), 4111, runCtx, pkg, candidates, store, Deps{
		Git:      git,
		Resolver: seqResolver,
		Now:      fixedNow(),
	}))

	// No branch force-push back to best_sha_before — the other run is safe.
	for _, call := range git.pushCalls {
		assert.NotEqual(t, resolver.target.BestShaBefore, call.target, "must not force-push branch back to best_sha_before when ownership unproven")
	}

	// interrupted state event signals snapshot refresh.
	kinds := readStateKinds(t, runCtx)
	assert.Contains(t, kinds, contracts.StateKindInterrupted)
	assert.NoFileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, string(runCtx.RunID)+".json"))
}

// F11: planning resume with remoteHead==target_sha AND a committed registry
// entry under this intention's idempotency_key proves ownership, so resume
// proceeds into driveRegistry normally.
func TestRun_PlanningResumeRemoteMatchesTargetWithOwnershipProofProceeds(t *testing.T) {
	runCtx, pkg, candidates, _, resolver := newFixtureWithResolver(t, "PR4112")
	store := newTrackingStore(intentionPath(t, runCtx))
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	require.NoError(t, store.Save(intention))

	// Seed the registry with this intention's planned row — proof of
	// ownership even though registry_head has advanced past planning.
	seedRegistryAdd(t, runCtx.RulesRegistryPath(), resolver, runCtx.RunID, candidates.CandidatesHash)

	git := &fakeGit{head: resolver.target.TargetSHA}
	// Planning resume calls the resolver once to verify target hasn't changed
	// (planningResumeNeedsRefresh); a fixtureResolver returning the same
	// target keeps that path quiescent.
	require.NoError(t, Run(context.Background(), 4112, runCtx, pkg, candidates, store, Deps{
		Git:      git,
		Resolver: resolver,
		Now:      fixedNow(),
	}))

	assert.Equal(t, contracts.DecisionActionAdopt, readDecision(t, runCtx).Action)
	// Ownership proof allowed the adopt path — no rollback force-push.
	assert.Empty(t, git.pushCalls, "ownership-proof resume must not push anything")
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
func TestRun_ResumeFromRegistryAppendedAllowsLaterRegistryProgress(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR504")
	registryPath := runCtx.RulesRegistryPath()
	appendResult, _ := seedRegistryAdd(t, registryPath, resolver, runCtx.RunID, candidates.CandidatesHash)
	_, _ = seedRegistryUniqueAdd(t, registryPath, "other-rule", fmt.Sprintf("%064x", 7504), "2026-04-21-PR75-abcdef0")

	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageRegistryAppended
	intention.RegistryAppendResult = &appendResult
	require.NoError(t, store.Save(intention))

	err := Run(context.Background(), 504, runCtx, pkg, candidates, store, Deps{
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
func TestRecoverAdoptAnywayAllowsLaterRegistryProgress(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR505")
	registryPath := runCtx.RulesRegistryPath()
	appendResult, _ := seedRegistryAdd(t, registryPath, resolver, runCtx.RunID, candidates.CandidatesHash)
	_, _ = seedRegistryUniqueAdd(t, registryPath, "other-rule", fmt.Sprintf("%064x", 7505), "2026-04-21-PR75-abcdef0")

	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageNeedsManualRecovery
	intention.RegistryAppendResult = &appendResult
	intention.RecoveryReason = contracts.RollbackReasonTransactionalFailure
	intention.FailedStep = contracts.FailedStep70
	require.NoError(t, store.Save(intention))

	err := RecoverAdoptAnyway(context.Background(), 505, runCtx, pkg, candidates, store, Deps{
		Git: &fakeGit{head: resolver.target.TargetSHA},
		Now: fixedNow(),
	})
	require.NoError(t, err)

	decision := readDecision(t, runCtx)
	assert.Equal(t, contracts.DecisionActionAdopt, decision.Action)
	assert.Contains(t, readStateKinds(t, runCtx), contracts.StateKindPromoted)
	assert.NoFileExists(t, intentionPath(t, runCtx))
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
func TestRun_AdoptIdempotencyDuplicatePlanning(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR9")
	git := &fakeGit{head: resolver.target.BestShaBefore}
	deps := Deps{Git: git, Resolver: resolver, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 9, runCtx, pkg, candidates, store, deps))
	git.head = resolver.target.TargetSHA
	require.NoError(t, Run(context.Background(), 9, runCtx, pkg, candidates, store, deps))

	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	require.NoError(t, err)
	require.Len(t, lines, 1)

	events := readStateEvents(t, runCtx)
	promoted := 0
	for _, event := range events {
		if event.Kind == contracts.StateKindPromoted {
			promoted++
		}
	}
	assert.Equal(t, 1, promoted)
}
func TestRun_PlanningResumeRefreshesOnCandidatesHashMismatch(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR130")
	candidates.Candidates = []contracts.Candidate{
		{
			CandidateID:        "cand-1",
			Kind:               contracts.CandidateKindNew,
			Title:              "candidate title",
			Problem:            "problem",
			Rationale:          "rationale",
			ProposedBodyPath:   "40/candidates/cand-1.md",
			ProposedBodySha256: strings.Repeat("a", 64),
		},
	}
	candidates.CandidatesHash = contracts.CanonicalCandidatesHash(candidates.Candidates)
	require.NoError(t, candidates.Validate())

	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	require.NoError(t, store.Save(intention))

	candidates.Candidates[0].Title = "candidate title changed"
	candidates.CandidatesHash = contracts.CanonicalCandidatesHash(candidates.Candidates)
	require.NoError(t, candidates.Validate())

	require.NoError(t, Run(context.Background(), 130, runCtx, pkg, candidates, store, Deps{
		Git:      &fakeGit{head: resolver.target.BestShaBefore},
		Resolver: resolver,
		Now:      fixedNow(),
	}))

	decision := mustDecisionAdopt(t, readDecision(t, runCtx))
	assert.Equal(t, candidates.CandidatesHash, decision.CandidatesHash)
	assert.Contains(t, readStateKinds(t, runCtx), contracts.StateKindInterrupted)
}
func TestRun_ResumePlanningRestartsWhenResolverTargetChanges(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR160")
	originalTarget := resolver.target
	changedTarget := Target{
		BestBranch:    originalTarget.BestBranch,
		BestShaBefore: originalTarget.BestShaBefore,
		TargetSHA:     strings.Repeat("3", 40),
		RulesToAppend: []contracts.RuleRegistryEntry{
			adoptAddedEntryWithTarget(runCtx.RunID, candidates.CandidatesHash, strings.Repeat("3", 40), "rule-replanned"),
		},
	}
	intention := planningIntention(runCtx.RunID, originalTarget, candidates.CandidatesHash)
	require.NoError(t, store.Save(intention))

	git := &fakeGit{head: originalTarget.BestShaBefore}
	require.NoError(t, Run(context.Background(), 160, runCtx, pkg, candidates, store, Deps{
		Git:      git,
		Resolver: &fixtureResolver{target: changedTarget},
		Now:      fixedNow(),
	}))

	decision := mustDecisionAdopt(t, readDecision(t, runCtx))
	assert.Equal(t, changedTarget.TargetSHA, decision.TargetSha)
	assert.NoFileExists(t, intentionPath(t, runCtx))
}
func TestRun_AdoptIgnoresIndexSyncFailureAfterRegistryCommit(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR15")
	resolver.target.RulesToAppend = []contracts.RuleRegistryEntry{adoptAddedEntryWithTarget(runCtx.RunID, candidates.CandidatesHash, resolver.target.TargetSHA, "seed-sync-1498")}
	specs := make([]seedRegistrySpec, 0, 1499)
	for i := 0; i < 1499; i++ {
		specs = append(specs, seedRegistrySpec{
			RuleID:         fmt.Sprintf("seed-sync-%04d", i),
			IdempotencyKey: fmt.Sprintf("%064x", i+5000),
			ByRunID:        contracts.RunID(fmt.Sprintf("2026-04-21-PR%02d-abcdef0", (i%90)+10)),
		})
	}
	writeSeedRegistryAdds(t, runCtx.RulesRegistryPath(), specs)
	require.NoError(t, os.MkdirAll(runCtx.RulesIdempotencyIndexPath(), 0o755))

	deps := Deps{Git: &fakeGit{head: resolver.target.BestShaBefore}, Resolver: resolver, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 15, runCtx, pkg, candidates, store, deps))

	assert.Equal(t, contracts.DecisionActionAdopt, readDecision(t, runCtx).Action)
	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	require.NoError(t, err)
	assert.Len(t, lines, 1500)
}
