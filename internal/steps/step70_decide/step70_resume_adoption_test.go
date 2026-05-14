package step70_decide

import (
	"context"
	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"path/filepath"
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
