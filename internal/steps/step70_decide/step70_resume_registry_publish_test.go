package step70_decide

import (
	"context"
	"fmt"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

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
