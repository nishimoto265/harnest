package step70_decide

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_NoopWhenNoTarget(t *testing.T) {
	runCtx, pkg, candidates, store, _ := newFixture(t, "PR1")
	require.NoError(t, Run(context.Background(), 1, runCtx, pkg, candidates, store, Deps{Now: fixedNow()}))

	decision := readDecision(t, runCtx)
	assert.Equal(t, contracts.DecisionActionNoop, decision.Action)
	assert.NoFileExists(t, intentionPath(t, runCtx))
}

func TestRun_DuplicateOnlyCandidatesEmitNoop(t *testing.T) {
	runCtx, pkg, _, store, _ := newFixture(t, "PR101")
	body := "# Duplicate rule\n\n- source_rule_id: rule-existing\n- classification: duplicate\n"
	candidate := contracts.Candidate{
		CandidateID:        "cand-dup",
		Kind:               contracts.CandidateKindDuplicate,
		TargetRuleID:       "rule-existing",
		Title:              "Duplicate rule",
		Problem:            "problem",
		Rationale:          "rationale",
		ProposedBodyPath:   "40/candidates/cand-dup.md",
		ProposedBodySha256: sha256String(body),
	}
	candidates := &contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          runCtx.RunID,
		Candidates:     []contracts.Candidate{candidate},
		CandidatesHash: contracts.CanonicalCandidatesHash([]contracts.Candidate{candidate}),
		CreatedAt:      time.Now().UTC(),
	}

	require.NoError(t, Run(context.Background(), 1, runCtx, pkg, candidates, store, Deps{Now: fixedNow()}))
	assert.Equal(t, contracts.DecisionActionNoop, readDecision(t, runCtx).Action)
}

func TestFilesystemResolver_LeaseFailureLeavesCanonicalRuleUntouched(t *testing.T) {
	runCtx, pkg, candidates := seedFilesystemResolverFixture(t)
	rulePath := filepath.Join(runCtx.RunsBase, "rules", "r-cand-1.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(rulePath), 0o755))
	require.NoError(t, os.WriteFile(rulePath, []byte("canonical-before\n"), 0o644))

	resolver := FilesystemResolver{
		RepoDir: runCtx.RunsBase,
		Now:     fixedNow(),
	}
	target, ok, err := resolver.Resolve(runCtx, pkg, candidates)
	require.NoError(t, err)
	require.True(t, ok)

	before, err := os.ReadFile(rulePath)
	require.NoError(t, err)
	assert.Equal(t, "canonical-before\n", string(before))

	store := newMemStore(intentionPath(t, runCtx))
	git := &fakeGit{head: strings.Repeat("1", 40), pushErr: ErrLeaseFailure}
	require.NoError(t, Run(context.Background(), 42, runCtx, pkg, candidates, store, Deps{
		Git:      git,
		Resolver: &fixtureResolver{target: target},
		Now:      fixedNow(),
	}))

	after, err := os.ReadFile(rulePath)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.NoFileExists(t, mustStagedRulePath(t, runCtx, "rules/r-cand-1.md"))
}

func TestFilesystemResolver_AdoptPromotesExactSidecarBytes(t *testing.T) {
	runCtx, pkg, candidates := seedFilesystemResolverFixture(t)
	resolver := FilesystemResolver{
		RepoDir: runCtx.RunsBase,
		Now:     fixedNow(),
	}
	target, ok, err := resolver.Resolve(runCtx, pkg, candidates)
	require.NoError(t, err)
	require.True(t, ok)

	store := newMemStore(intentionPath(t, runCtx))
	git := &fakeGit{head: strings.Repeat("1", 40)}
	require.NoError(t, Run(context.Background(), 42, runCtx, pkg, candidates, store, Deps{
		Git:      git,
		Resolver: &fixtureResolver{target: target},
		Now:      fixedNow(),
	}))

	rulePath := filepath.Join(runCtx.RunsBase, "rules", "r-cand-1.md")
	ruleBytes, err := os.ReadFile(rulePath)
	require.NoError(t, err)
	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	require.NoError(t, err)
	require.Len(t, lines, 1)
	added := lines[0].Entry.Value.(contracts.RuleRegistryAdded)
	assert.Equal(t, added.Sha256, sha256String(string(ruleBytes)))
	assert.NoFileExists(t, mustStagedRulePath(t, runCtx, "rules/r-cand-1.md"))
}

func TestRun_AdoptHappyPath(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR2")
	git := &fakeGit{head: resolver.target.BestShaBefore}
	deps := Deps{Git: git, Resolver: resolver, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 2, runCtx, pkg, candidates, store, deps))

	decision := readDecision(t, runCtx)
	assert.Equal(t, contracts.DecisionActionAdopt, decision.Action)

	// promoting + promoted events persisted by step70 itself.
	events := readStateEvents(t, runCtx)
	assert.Equal(t, contracts.StateKindPromoting, events[0].Kind)
	assert.Equal(t, contracts.StateKindPromoted, events[len(events)-1].Kind)

	// Intention deleted on finalize.
	assert.NoFileExists(t, intentionPath(t, runCtx))

	// Exactly one lease push (target_sha) landed.
	require.Len(t, git.pushCalls, 1)
	assert.Equal(t, resolver.target.TargetSHA, git.pushCalls[0].target)
}

func TestRun_SentinelBlocksExecution(t *testing.T) {
	runCtx, pkg, candidates, store, _ := newFixture(t, "PR3")
	require.NoError(t, os.MkdirAll(filepath.Join(runCtx.RunsBase, "needs-recovery"), 0o755))
	blockPath := filepath.Join(runCtx.RunsBase, "needs-recovery", "other-run.json")
	require.NoError(t, os.WriteFile(blockPath, []byte("{}"), 0o644))

	require.ErrorIs(t, Run(context.Background(), 3, runCtx, pkg, candidates, store, Deps{Now: fixedNow()}), ErrBlockedBySentinel)

	// No decision written.
	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	assert.NoFileExists(t, decisionPath)
}

func TestRun_SunsetMarkerBlocksExecution(t *testing.T) {
	runCtx, pkg, candidates, store, _ := newFixture(t, "PR303")
	require.NoError(t, os.WriteFile(filepath.Join(runCtx.RunsBase, sunsetMarkerFile), []byte("{}"), 0o644))

	require.ErrorIs(t, Run(context.Background(), 3, runCtx, pkg, candidates, store, Deps{Now: fixedNow()}), ErrBlockedBySentinel)

	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	assert.NoFileExists(t, decisionPath)
}

func TestNextRegistryVersionForRule_IsChainScoped(t *testing.T) {
	lines := []registryLine{
		{
			Entry: contracts.RuleRegistryEntry{
				Kind: contracts.RegistryKindAdded,
				Value: contracts.RuleRegistryAdded{
					Kind:       contracts.RegistryKindAdded,
					RuleID:     "rule-a",
					VersionSeq: 2,
				},
			},
		},
		{
			Entry: contracts.RuleRegistryEntry{
				Kind: contracts.RegistryKindUpdated,
				Value: contracts.RuleRegistryUpdated{
					Kind:       contracts.RegistryKindUpdated,
					RuleID:     "rule-b",
					VersionSeq: 5,
				},
			},
		},
		{
			Entry: contracts.RuleRegistryEntry{
				Kind: contracts.RegistryKindStatusChanged,
				Value: contracts.RuleRegistryStatusChanged{
					Kind:       contracts.RegistryKindStatusChanged,
					RuleID:     "rule-a",
					VersionSeq: 4,
				},
			},
		},
		{
			Entry: contracts.RuleRegistryEntry{
				Kind: contracts.RegistryKindArchived,
				Value: contracts.RuleRegistryArchived{
					Kind:       contracts.RegistryKindArchived,
					RuleID:     "rule-c",
					VersionSeq: 7,
				},
			},
		},
	}

	assert.EqualValues(t, 8, nextRegistryVersionForRule(lines, "rule-a"))
	assert.EqualValues(t, 8, nextRegistryVersionForRule(lines, "rule-b"))
	assert.EqualValues(t, 8, nextRegistryVersionForRule(lines, "rule-c"))
	assert.EqualValues(t, 8, nextRegistryVersionForRule(lines, "rule-d"))
}

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
	require.Len(t, store.saved, 3)
	assert.Equal(t, contracts.IntentionStageRegistryAppended, store.saved[1].Stage)
	require.NotNil(t, store.saved[1].RegistryAppendResult)
	assert.Equal(t, appendResult, *store.saved[1].RegistryAppendResult)
	// No additional push from the resume path (branch already pushed).
	assert.Empty(t, git.pushCalls)
}

func TestRun_ResumeFromBranchPushed_MultiEntryIdempotencyHit(t *testing.T) {
	runCtx, pkg, candidates, _, resolver := newFixtureWithResolver(t, "PR401")
	resolver.target.RulesToAppend = adoptAddedEntriesWithTarget(runCtx.RunID, candidates.CandidatesHash, resolver.target.TargetSHA, "rule-a", "rule-b")
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
	require.Len(t, store.saved, 4)
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

func TestRun_ResumeFromBranchPushed_RollsBackWhenSentinelAppearsBeforeResumeStep(t *testing.T) {
	runCtx, pkg, candidates, _, resolver := newFixtureWithResolver(t, "PR422")
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageBranchPushed

	store := newLoadHookStore(intentionPath(t, runCtx), func() {
		require.NoError(t, writeSentinel(runCtx.RunsBase, "2026-04-21-PR99-feedbee", 99, contracts.RollbackReasonTransactionalFailure, contracts.FailedStep70, fixedNow()()))
	})
	require.NoError(t, store.Save(intention))

	git := &fakeGit{head: resolver.target.TargetSHA}
	require.NoError(t, Run(context.Background(), 422, runCtx, pkg, candidates, store, Deps{Git: git, Resolver: unexpectedResolver{t: t}, Now: fixedNow()}))

	rollback := mustDecisionRollback(t, readDecision(t, runCtx))
	assert.Equal(t, contracts.RollbackReasonTransactionalFailure, rollback.RollbackReason)
	assert.Len(t, git.pushCalls, 1)
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

func TestRun_MultiEntryAppendFailure_RollbackAppendsMarkersForCommittedRows(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR440")
	resolver.target.RulesToAppend = adoptAddedEntriesWithTarget(runCtx.RunID, candidates.CandidatesHash, resolver.target.TargetSHA, "rule-a", "rule-b", "rule-c")
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

	deps := Deps{Git: &fakeGit{head: resolver.target.TargetSHA}, Resolver: resolver, Now: fixedNow()}
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
	git := &fakeGit{head: "", pushErr: ErrLeaseFailure}

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
	// Push succeeds, but later rollback path reads an unrelated head.
	git := &fakeGit{head: strings.Repeat("9", 40), pushErr: ErrRemoteDivergence}
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
		git := &fakeGit{head: "", pushErr: ErrLeaseFailure}

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
		}, contracts.RollbackReasonLeaseFailure))
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

func TestFlockContention_ChildCannotAcquire(t *testing.T) {
	runCtx, _, _, _, _ := newFixture(t, "PR8")
	lock, err := internalio.AcquirePromotionLock(runCtx)
	require.NoError(t, err)
	defer lock.Unlock()

	cmd := exec.Command(os.Args[0], "-test.run=TestTryNonBlockingFlockHelper")
	cmd.Env = append(os.Environ(),
		"GO_WANT_FLOCK_HELPER=1",
		"FLOCK_PATH="+runCtx.PromotionLockPath(),
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err)
	assert.Equal(t, "wouldblock\n", string(out))

	require.NoError(t, lock.Unlock())

	cmd = exec.Command(os.Args[0], "-test.run=TestTryNonBlockingFlockHelper")
	cmd.Env = append(os.Environ(),
		"GO_WANT_FLOCK_HELPER=1",
		"FLOCK_PATH="+runCtx.PromotionLockPath(),
	)
	out, err = cmd.CombinedOutput()
	require.NoError(t, err)
	assert.Equal(t, "ok\n", string(out))
}

func TestTryNonBlockingFlockHelper(t *testing.T) {
	if os.Getenv("GO_WANT_FLOCK_HELPER") != "1" {
		return
	}
	path := os.Getenv("FLOCK_PATH")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		fmt.Fprint(os.Stdout, err)
		os.Exit(1)
	}
	defer f.Close()
	if err := tryNonBlockingFlock(int(f.Fd())); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			fmt.Fprintln(os.Stdout, "wouldblock")
			os.Exit(0)
		}
		fmt.Fprint(os.Stdout, err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, "ok")
	os.Exit(0)
}

func TestRun_AdoptIdempotencyDuplicatePlanning(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR9")
	git := &fakeGit{head: resolver.target.BestShaBefore}
	deps := Deps{Git: git, Resolver: resolver, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 9, runCtx, pkg, candidates, store, deps))
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
	assert.NoFileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, string(runCtx.RunID)+".json"))
	assert.NoFileExists(t, intentionPath(t, runCtx))

	deps = Deps{Git: &fakeGit{head: resolver.target.BestShaBefore}, Resolver: resolver, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 123, runCtx, pkg, candidates, store, deps))
	assert.Equal(t, contracts.DecisionActionAdopt, readDecision(t, runCtx).Action)
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

func TestFindRegistryByIdempotencyKey_RebuildsMandatoryIndexBeforeLookup(t *testing.T) {
	runCtx, _, candidates, _, _ := newFixture(t, "PR14")
	targetKey := contracts.ComputeAdoptIdempotencyKey(string(runCtx.RunID), strings.Repeat("2", 40), strings.Repeat("1", 40), candidates.CandidatesHash)
	specs := make([]seedRegistrySpec, 0, registryMandatoryIndexAt)
	for i := 0; i < registryMandatoryIndexAt; i++ {
		key := fmt.Sprintf("%064x", i+1)
		if i == registryMandatoryIndexAt/2 {
			key = targetKey
		}
		specs = append(specs, seedRegistrySpec{
			RuleID:         fmt.Sprintf("seed-%04d", i),
			IdempotencyKey: key,
			ByRunID:        contracts.RunID(fmt.Sprintf("2026-04-21-PR%02d-abcdef0", (i%90)+10)),
		})
	}
	results := writeSeedRegistryAdds(t, runCtx.RulesRegistryPath(), specs)
	targetResult := results[targetKey]

	require.NoError(t, internalio.WriteAtomic(runCtx.RulesIdempotencyIndexPath(), []byte("{\"idempotency_key\":\""+strings.Repeat("f", 64)+"\",\"registry_offset\":0,\"registry_sha256\":\""+strings.Repeat("e", 64)+"\",\"kind\":\"added\",\"at\":\"2026-04-21T00:01:40Z\"}\n")))

	found, ok, err := findRegistryByIdempotencyKey(runCtx, targetKey)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, targetResult, found)
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

func TestRun_AbortsBeforePushWhenOtherRunSentinelAppears(t *testing.T) {
	runCtx, pkg, candidates, _, resolver := newFixtureWithResolver(t, "PR16")
	store := newHookStore(intentionPath(t, runCtx), func(record contracts.IntentionRecord) {
		if record.Stage == contracts.IntentionStagePlanning {
			require.NoError(t, writeSentinel(runCtx.RunsBase, "2026-04-21-PR99-deadbee", 99, contracts.RollbackReasonTransactionalFailure, contracts.FailedStep70, fixedNow()()))
		}
	})

	git := &fakeGit{head: resolver.target.BestShaBefore}
	err := Run(context.Background(), 16, runCtx, pkg, candidates, store, Deps{Git: git, Resolver: resolver, Now: fixedNow()})
	require.ErrorIs(t, err, ErrBlockedBySentinel)
	assert.Empty(t, git.pushCalls)
	assert.NoFileExists(t, intentionPath(t, runCtx))

	decisionPath, pathErr := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, pathErr)
	assert.NoFileExists(t, decisionPath)
}

func TestRun_IgnoresSelfOwnedSentinelAtStageBoundary(t *testing.T) {
	runCtx, pkg, candidates, _, resolver := newFixtureWithResolver(t, "PR17")
	store := newHookStore(intentionPath(t, runCtx), func(record contracts.IntentionRecord) {
		if record.Stage == contracts.IntentionStagePlanning {
			require.NoError(t, writeSentinel(runCtx.RunsBase, runCtx.RunID, 17, contracts.RollbackReasonTransactionalFailure, contracts.FailedStep70, fixedNow()()))
		}
	})

	git := &fakeGit{head: resolver.target.BestShaBefore}
	require.NoError(t, Run(context.Background(), 17, runCtx, pkg, candidates, store, Deps{Git: git, Resolver: resolver, Now: fixedNow()}))
	assert.Equal(t, contracts.DecisionActionAdopt, readDecision(t, runCtx).Action)
	require.Len(t, git.pushCalls, 1)
}

func TestRun_RollsBackWhenOtherRunSentinelAppearsAfterPush(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR18")
	git := &fakeGit{
		head: resolver.target.TargetSHA,
		onPush: func(call fakePushCall) {
			if call.target == resolver.target.TargetSHA {
				require.NoError(t, writeSentinel(runCtx.RunsBase, "2026-04-21-PR98-cafef00", 98, contracts.RollbackReasonTransactionalFailure, contracts.FailedStep70, fixedNow()()))
			}
		},
	}

	require.NoError(t, Run(context.Background(), 18, runCtx, pkg, candidates, store, Deps{Git: git, Resolver: resolver, Now: fixedNow()}))

	rollback := mustDecisionRollback(t, readDecision(t, runCtx))
	assert.Equal(t, contracts.RollbackReasonTransactionalFailure, rollback.RollbackReason)
	require.Len(t, git.pushCalls, 2)
}

func TestRun_ResumeFromBranchPushed_RollsBackWhenOtherRunSentinelAppearsBeforeResumeRegistry(t *testing.T) {
	runCtx, pkg, candidates, _, resolver := newFixtureWithResolver(t, "PR705")
	store := newLoadHookStore(intentionPath(t, runCtx), func() {
		require.NoError(t, writeSentinel(runCtx.RunsBase, "2026-04-21-PR99-feedbee", 99, contracts.RollbackReasonTransactionalFailure, contracts.FailedStep70, fixedNow()()))
	})
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageBranchPushed
	require.NoError(t, store.Save(intention))

	require.NoError(t, Run(context.Background(), 705, runCtx, pkg, candidates, store, Deps{
		Git:      &fakeGit{head: resolver.target.TargetSHA},
		Resolver: unexpectedResolver{t: t},
		Now:      fixedNow(),
	}))

	rollback := mustDecisionRollback(t, readDecision(t, runCtx))
	assert.Equal(t, contracts.RollbackReasonTransactionalFailure, rollback.RollbackReason)
}

func TestRun_RollsBackWhenOtherRunSentinelAppearsAfterRegistryAppend(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR19")
	original := appendRegistryEntry
	appendRegistryEntry = func(path string, entry contracts.RuleRegistryEntry) (contracts.RegistryAppendResult, error) {
		result, err := original(path, entry)
		if err == nil && path == runCtx.RulesRegistryPath() {
			switch entry.Kind {
			case contracts.RegistryKindAdded, contracts.RegistryKindUpdated:
				require.NoError(t, writeSentinel(runCtx.RunsBase, "2026-04-21-PR97-feedbee", 97, contracts.RollbackReasonTransactionalFailure, contracts.FailedStep70, fixedNow()()))
			}
		}
		return result, err
	}
	t.Cleanup(func() {
		appendRegistryEntry = original
	})

	git := &fakeGit{head: resolver.target.TargetSHA}
	require.NoError(t, Run(context.Background(), 19, runCtx, pkg, candidates, store, Deps{Git: git, Resolver: resolver, Now: fixedNow()}))

	rollback := mustDecisionRollback(t, readDecision(t, runCtx))
	assert.Equal(t, contracts.RollbackReasonTransactionalFailure, rollback.RollbackReason)
	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	require.NoError(t, err)
	require.Len(t, lines, 2)
}

func TestRun_RollsBackBeforeSecondRegistryAppendWhenSentinelAppearsMidLoop(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR191")
	resolver.target.RulesToAppend = adoptAddedEntriesWithTarget(runCtx.RunID, candidates.CandidatesHash, resolver.target.TargetSHA, "rule-a", "rule-b")

	original := appendRegistryEntry
	appendCount := 0
	appendRegistryEntry = func(path string, entry contracts.RuleRegistryEntry) (contracts.RegistryAppendResult, error) {
		result, err := original(path, entry)
		if err == nil && path == runCtx.RulesRegistryPath() && entry.Kind == contracts.RegistryKindAdded {
			appendCount++
			if appendCount == 1 {
				require.NoError(t, writeSentinel(runCtx.RunsBase, "2026-04-21-PR97-feedbee", 97, contracts.RollbackReasonTransactionalFailure, contracts.FailedStep70, fixedNow()()))
			}
		}
		return result, err
	}
	t.Cleanup(func() {
		appendRegistryEntry = original
	})

	git := &fakeGit{head: resolver.target.TargetSHA}
	require.NoError(t, Run(context.Background(), 19, runCtx, pkg, candidates, store, Deps{Git: git, Resolver: resolver, Now: fixedNow()}))

	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	require.NoError(t, err)
	require.Len(t, lines, 2)
	assert.Equal(t, contracts.RegistryKindAdded, lines[0].Entry.Kind)
	assert.Equal(t, contracts.RegistryKindRolledBack, lines[1].Entry.Kind)
}

func TestRun_FillsBestShaBeforeFromRemoteHeadAfterLock(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR20")
	resolver.target.BestShaBefore = ""
	git := &fakeGit{head: strings.Repeat("4", 40)}

	require.NoError(t, Run(context.Background(), 20, runCtx, pkg, candidates, store, Deps{Git: git, Resolver: resolver, Now: fixedNow()}))

	adopt := mustDecisionAdopt(t, readDecision(t, runCtx))
	assert.Equal(t, strings.Repeat("4", 40), adopt.BestShaBefore)
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

func TestRun_RejectsPersistedDecisionThatFailsRequestBoundValidation(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR21")
	appendResult, _ := seedRegistryAdd(t, runCtx.RulesRegistryPath(), resolver, runCtx.RunID, candidates.CandidatesHash)
	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(decisionPath, contracts.Decision{
		Action: contracts.DecisionActionAdopt,
		Value: contracts.DecisionAdopt{
			Action:               contracts.DecisionActionAdopt,
			SchemaVersion:        "1",
			RunID:                runCtx.RunID,
			IdempotencyKey:       contracts.ComputeAdoptIdempotencyKey(string(runCtx.RunID), resolver.target.TargetSHA, resolver.target.BestShaBefore, strings.Repeat("f", 64)),
			BestShaBefore:        resolver.target.BestShaBefore,
			TargetSha:            resolver.target.TargetSHA,
			CandidatesHash:       strings.Repeat("f", 64),
			RegistryAppendResult: appendResult,
			DecidedAt:            fixedNow()(),
		},
	}))

	err = Run(context.Background(), 21, runCtx, pkg, candidates, store, Deps{Git: &fakeGit{head: resolver.target.TargetSHA}, Resolver: unexpectedResolver{t: t}, Now: fixedNow()})
	require.Error(t, err)
	assert.ErrorIs(t, err, stepio.ErrStep70AdoptCandidatesHashMismatch)
}

func TestCleanupWorktrees_RejectsPathOutsideWorktreeBase(t *testing.T) {
	runCtx, err := internalio.NewRunContext("2026-04-21-PR999-abcdef0", t.TempDir(), t.TempDir())
	require.NoError(t, err)
	pkg := &contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runCtx.RunID,
		PR:                      999,
		Title:                   "cleanup guard",
		BaseSHA:                 strings.Repeat("a", 40),
		BestBranch:              "best",
		ReconstructedTaskPrompt: "cleanup guard",
		CreatedAt:               time.Now().UTC(),
		Worktrees: []contracts.WorktreeAllocation{
			{Agent: "a1", Pass: 1, Path: filepath.Join(t.TempDir(), "escape"), Branch: "stub/pass1/a1", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("a", 40)},
			{Agent: "a2", Pass: 1, Path: filepath.Join(runCtx.WorktreeBase, "pass1-a2"), Branch: "stub/pass1/a2", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("a", 40)},
			{Agent: "a3", Pass: 1, Path: filepath.Join(runCtx.WorktreeBase, "pass1-a3"), Branch: "stub/pass1/a3", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("a", 40)},
			{Agent: "a1", Pass: 2, Path: filepath.Join(runCtx.WorktreeBase, "pass2-a1"), Branch: "stub/pass2/a1", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("a", 40)},
			{Agent: "a2", Pass: 2, Path: filepath.Join(runCtx.WorktreeBase, "pass2-a2"), Branch: "stub/pass2/a2", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("a", 40)},
			{Agent: "a3", Pass: 2, Path: filepath.Join(runCtx.WorktreeBase, "pass2-a3"), Branch: "stub/pass2/a3", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("a", 40)},
		},
	}

	err = cleanupWorktrees(context.Background(), runCtx, pkg, NoopGitOps{})
	require.Error(t, err)
	assert.ErrorIs(t, err, internalio.ErrWorktreePathEscapesBase)
}

// ---- helpers ----

type fixtureResolver struct {
	target Target
}

type seedRegistrySpec struct {
	RuleID         string
	IdempotencyKey string
	ByRunID        contracts.RunID
}

func (r *fixtureResolver) Resolve(internalio.RunContext, *contracts.TaskPackage, *contracts.Candidates) (Target, bool, error) {
	return r.target, true, nil
}

type unexpectedResolver struct {
	t *testing.T
}

func (r unexpectedResolver) Resolve(internalio.RunContext, *contracts.TaskPackage, *contracts.Candidates) (Target, bool, error) {
	r.t.Helper()
	r.t.Fatalf("unexpected resolver call")
	return Target{}, false, nil
}

type sequenceResolver struct {
	targets []Target
	index   int
}

func (r *sequenceResolver) Resolve(internalio.RunContext, *contracts.TaskPackage, *contracts.Candidates) (Target, bool, error) {
	if len(r.targets) == 0 {
		return Target{}, false, nil
	}
	if r.index >= len(r.targets) {
		return r.targets[len(r.targets)-1], true, nil
	}
	target := r.targets[r.index]
	r.index++
	return target, true, nil
}

type fakePushCall struct {
	branch   string
	target   string
	expected string
}

type fakeGit struct {
	head      string
	pushErr   error
	pushCalls []fakePushCall
	onPush    func(fakePushCall)
}

type cancelOnPushGit struct {
	head   string
	cancel context.CancelFunc
}

func (g *fakeGit) RemoteHead(_ context.Context, _ string) (string, error) {
	return g.head, nil
}

func (g *fakeGit) PushForceWithLease(_ context.Context, branch, target, expected string) error {
	call := fakePushCall{branch: branch, target: target, expected: expected}
	g.pushCalls = append(g.pushCalls, call)
	if g.onPush != nil {
		g.onPush(call)
	}
	if g.pushErr != nil && len(g.pushCalls) == 1 {
		return g.pushErr
	}
	// Subsequent calls (rollback revert) succeed so the rollback path can
	// reach terminal state.
	return nil
}

func (g *fakeGit) RemoveWorktree(_ context.Context, _ string) error {
	return nil
}

func (g cancelOnPushGit) RemoteHead(_ context.Context, _ string) (string, error) {
	return g.head, nil
}

func (g cancelOnPushGit) PushForceWithLease(ctx context.Context, branch, target, expected string) error {
	_ = branch
	_ = target
	_ = expected
	if g.cancel != nil {
		g.cancel()
	}
	return ctx.Err()
}

func (g cancelOnPushGit) RemoveWorktree(_ context.Context, _ string) error {
	return nil
}

func fixedNow() func() time.Time {
	t := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	return func() time.Time {
		t = t.Add(time.Second)
		return t
	}
}

func newFixture(t *testing.T, prLabel string) (internalio.RunContext, *contracts.TaskPackage, *contracts.Candidates, IntentionWriter, *fixtureResolver) {
	t.Helper()
	tempRuns := t.TempDir()
	worktreeBase := t.TempDir()
	runID := contracts.RunID("2026-04-21-" + prLabel + "-abcdef0")
	runCtx, err := internalio.NewRunContext(runID, tempRuns, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunDir(), 0o755))

	pkg := validTaskPackage(runID, worktreeBase)
	require.NoError(t, internalio.WriteJSONAtomic(runCtx.TaskPackagePath(), pkg))
	// Rebuild runCtx with worktrees populated.
	runCtx, err = internalio.RunContextFromTaskPackage(pkg, tempRuns, worktreeBase)
	require.NoError(t, err)

	candidates := emptyCandidates(runID)

	store := newMemStore(intentionPath(t, runCtx))
	return runCtx, &pkg, &candidates, store, &fixtureResolver{}
}

func seedFilesystemResolverFixture(t *testing.T) (internalio.RunContext, *contracts.TaskPackage, *contracts.Candidates) {
	t.Helper()
	runCtx, pkg, _, _, _ := newFixture(t, "PR420")
	runID := runCtx.RunID
	manifestPath, err := runCtx.ManifestPath(2, "a1")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(manifestPath, contracts.Manifest{
		Kind: contracts.ManifestKindSuccess,
		Value: contracts.ManifestSuccess{
			Kind:          contracts.ManifestKindSuccess,
			SchemaVersion: "1",
			RunID:         runID,
			Pass:          2,
			Agent:         "a1",
			BranchName:    pkg.Worktrees[3].Branch,
			HeadSHA:       strings.Repeat("2", 40),
			BaseSHA:       strings.Repeat("a", 40),
			DiffPath:      "50-pass2/a1/diff.patch",
			SessionPath:   "50-pass2/a1/session.jsonl",
			ChecklistPath: "50-pass2/a1/checklist-result.json",
			PromptVersion: "stub",
			StartedAt:     time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
			FinishedAt:    time.Date(2026, 4, 21, 10, 1, 0, 0, time.UTC),
		},
	}))

	pairwisePath, err := runCtx.ResolveRunRelative("60/pairwise.jsonl")
	require.NoError(t, err)
	require.NoError(t, internalio.AppendJSONL(pairwisePath, contracts.PairwiseEntry{
		SchemaVersion: "1",
		RunID:         runID,
		AgentA:        "a1",
		AgentB:        "a1",
		Winner:        contracts.PairwiseWinnerB,
		Margin:        contracts.PairwiseMarginClear,
		Justification: "resolver fixture",
		VerdictPath:   contracts.VerdictPathAgreement,
		RubricVersion: "default",
		PromptVersion: "phase0-stub",
		ResolvedAt:    time.Date(2026, 4, 21, 10, 2, 0, 0, time.UTC),
	}))
	scorePath, err := runCtx.ResolveRunRelative("60/scores-B.jsonl")
	require.NoError(t, err)
	require.NoError(t, internalio.AppendJSONL(scorePath, contracts.ScoreEntry{
		SchemaVersion: "1",
		RunID:         runID,
		Pass:          2,
		Agent:         "a1",
		Dimension:     contracts.DimensionFidelity,
		Score:         90,
		Reasons:       "resolver fixture",
		VerdictPath:   contracts.VerdictPathAgreement,
		RubricVersion: "default",
		PromptVersion: "phase0-stub",
		ResolvedAt:    time.Date(2026, 4, 21, 10, 2, 0, 0, time.UTC),
	}))

	body := "# Candidate body\n- exact bytes only\n"
	candidate := contracts.Candidate{
		CandidateID:        "cand-1",
		Kind:               contracts.CandidateKindNew,
		Title:              "Candidate body",
		Problem:            "problem",
		Rationale:          "rationale",
		ProposedBodyPath:   "40/candidates/cand-1.md",
		ProposedBodySha256: sha256String(body),
	}
	bodyPath, err := runCtx.ResolveRunRelative(candidate.ProposedBodyPath)
	require.NoError(t, err)
	require.NoError(t, internalio.WriteAtomic(bodyPath, []byte(body)))

	candidates := &contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          runID,
		Candidates:     []contracts.Candidate{candidate},
		CandidatesHash: contracts.CanonicalCandidatesHash([]contracts.Candidate{candidate}),
		CreatedAt:      time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}
	return runCtx, pkg, candidates
}

func mustStagedRulePath(t *testing.T, runCtx internalio.RunContext, rulePath string) string {
	t.Helper()
	path, err := stagedRuleSidecarPath(runCtx, rulePath)
	require.NoError(t, err)
	return path
}

func newFixtureWithResolver(t *testing.T, prLabel string) (internalio.RunContext, *contracts.TaskPackage, *contracts.Candidates, IntentionWriter, *fixtureResolver) {
	runCtx, pkg, candidates, store, resolver := newFixture(t, prLabel)
	resolver.target = Target{
		BestBranch:    "best",
		BestShaBefore: strings.Repeat("1", 40),
		TargetSHA:     strings.Repeat("2", 40),
		RulesToAppend: []contracts.RuleRegistryEntry{adoptAddedEntry(runCtx.RunID, candidates.CandidatesHash)},
	}
	// Idempotency key requires the target.TargetSHA to be known; re-derive
	// against the empty candidates hash used by newFixture.
	return runCtx, pkg, candidates, store, resolver
}

func adoptAddedEntry(runID contracts.RunID, candidatesHash string) contracts.RuleRegistryEntry {
	return adoptAddedEntryWithTarget(runID, candidatesHash, strings.Repeat("2", 40), "rule-seed")
}

func adoptAddedEntriesWithTarget(runID contracts.RunID, candidatesHash, targetSHA string, ruleIDs ...string) []contracts.RuleRegistryEntry {
	entries := make([]contracts.RuleRegistryEntry, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		entries = append(entries, adoptAddedEntryWithTarget(runID, candidatesHash, targetSHA, ruleID))
	}
	return entries
}

func adoptAddedEntryWithTarget(runID contracts.RunID, candidatesHash, targetSHA, ruleID string) contracts.RuleRegistryEntry {
	key := contracts.ComputeAdoptIdempotencyKey(string(runID), strings.Repeat("2", 40), strings.Repeat("1", 40), candidatesHash)
	v := contracts.RuleRegistryAdded{
		Kind:           contracts.RegistryKindAdded,
		SchemaVersion:  "1",
		RuleID:         ruleID,
		RulePath:       "rules/" + ruleID + ".md",
		Sha256:         strings.Repeat("a", 64),
		IdempotencyKey: key,
		VersionSeq:     1,
		PrevHash:       "",
		ByRunID:        runID,
		At:             time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}
	v.IdempotencyKey = contracts.ComputeAdoptIdempotencyKey(string(runID), targetSHA, strings.Repeat("1", 40), candidatesHash)
	return contracts.RuleRegistryEntry{Kind: v.Kind, Value: v}
}

func planningIntention(runID contracts.RunID, target Target, candidatesHash string) contracts.IntentionRecord {
	idempotencyKey := contracts.ComputeAdoptIdempotencyKey(string(runID), target.TargetSHA, target.BestShaBefore, candidatesHash)
	return contracts.IntentionRecord{
		SchemaVersion:      "1",
		Stage:              contracts.IntentionStagePlanning,
		IdempotencyKey:     idempotencyKey,
		RunID:              runID,
		BestShaBefore:      target.BestShaBefore,
		TargetSha:          target.TargetSHA,
		CandidatesHash:     candidatesHash,
		RegistryHeadBefore: "",
		PlannedAdoption:    mustPlannedAdoption(nil, idempotencyKey, target.RulesToAppend),
		StartedAt:          time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}
}

func mustPlannedAdoption(t *testing.T, intentionIdempotencyKey string, entries []contracts.RuleRegistryEntry) *contracts.PlannedAdoption {
	if t != nil {
		t.Helper()
	}
	planned, err := plannedAdoptionFromRegistryEntries(intentionIdempotencyKey, entries)
	if t != nil {
		require.NoError(t, err)
	} else if err != nil {
		panic(err)
	}
	return planned
}

func seedRegistryAdd(t *testing.T, path string, resolver *fixtureResolver, runID contracts.RunID, candidatesHash string) (contracts.RegistryAppendResult, contracts.RuleRegistryEntry) {
	t.Helper()
	intention := planningIntention(runID, resolver.target, candidatesHash)
	entries, err := registryEntriesFromPlannedAdoption(intention, time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	entry := entries[0]
	result, err := internalio.AppendRegistryEntry(path, entry)
	require.NoError(t, err)
	return result, entry
}

func seedRegistryUniqueAdd(t *testing.T, path, ruleID, idemKey, byRunID string) (contracts.RegistryAppendResult, contracts.RuleRegistryEntry) {
	t.Helper()
	prevHash, err := currentRegistryHead(path)
	require.NoError(t, err)
	versionSeq := int64(1)
	lines, err := readRegistryLines(path)
	require.NoError(t, err)
	if len(lines) > 0 {
		versionSeq = int64(len(lines) + 1)
	}
	entry := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         ruleID,
			RulePath:       "rules/" + ruleID + ".md",
			Sha256:         strings.Repeat("b", 64),
			IdempotencyKey: idemKey,
			VersionSeq:     versionSeq,
			PrevHash:       prevHash,
			ByRunID:        contracts.RunID(byRunID),
			At:             time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
		},
	}
	result, err := internalio.AppendRegistryEntry(path, entry)
	require.NoError(t, err)
	return result, entry
}

func writeSeedRegistryAdds(t *testing.T, path string, specs []seedRegistrySpec) map[string]contracts.RegistryAppendResult {
	t.Helper()

	var (
		buffer   bytes.Buffer
		offset   int64
		prevHash string
	)
	results := make(map[string]contracts.RegistryAppendResult, len(specs))
	for i, spec := range specs {
		entry := contracts.RuleRegistryEntry{
			Kind: contracts.RegistryKindAdded,
			Value: contracts.RuleRegistryAdded{
				Kind:           contracts.RegistryKindAdded,
				SchemaVersion:  "1",
				RuleID:         spec.RuleID,
				RulePath:       "rules/" + spec.RuleID + ".md",
				Sha256:         fmt.Sprintf("%064x", i+10000),
				IdempotencyKey: spec.IdempotencyKey,
				VersionSeq:     int64(i + 1),
				PrevHash:       prevHash,
				ByRunID:        spec.ByRunID,
				At:             time.Unix(100, 0).UTC(),
			},
		}
		var line bytes.Buffer
		require.NoError(t, contracts.EncodeStrict(&line, entry))
		payload := bytes.TrimSuffix(line.Bytes(), []byte{'\n'})
		_, err := buffer.Write(payload)
		require.NoError(t, err)
		require.NoError(t, buffer.WriteByte('\n'))

		sum := sha256.Sum256(payload)
		result := contracts.RegistryAppendResult{
			Offset: offset,
			Sha256: hex.EncodeToString(sum[:]),
		}
		results[spec.IdempotencyKey] = result
		prevHash = result.Sha256
		offset += int64(len(payload) + 1)
	}
	require.NoError(t, internalio.WriteAtomic(path, buffer.Bytes()))
	return results
}

func validTaskPackage(runID contracts.RunID, worktreeBase string) contracts.TaskPackage {
	base := strings.Repeat("a", 40)
	worktrees := make([]contracts.WorktreeAllocation, 0, 6)
	for pass := 1; pass <= 2; pass++ {
		for _, agent := range []contracts.AgentID{"a1", "a2", "a3"} {
			worktrees = append(worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    filepath.Join(worktreeBase, fmt.Sprintf("%s-pass%d-%s", runID, pass, agent)),
				Branch:  "auto-improve/" + string(runID) + "/pass" + pad(pass) + "/" + string(agent),
				BaseSHA: base,
				HeadSHA: base,
			})
		}
	}
	return contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runID,
		PR:                      1,
		Title:                   "test",
		BaseSHA:                 base,
		BestBranch:              "best",
		ReconstructedTaskPrompt: "p",
		Worktrees:               worktrees,
		CreatedAt:               time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}
}

func pad(pass int) string {
	if pass == 1 {
		return "1"
	}
	return "2"
}

func emptyCandidates(runID contracts.RunID) contracts.Candidates {
	return contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          runID,
		Candidates:     []contracts.Candidate{},
		CandidatesHash: contracts.CanonicalCandidatesHash(nil),
		CreatedAt:      time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}
}

func sha256String(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func installAppendCASMismatchHook(t *testing.T, runCtx internalio.RunContext, mismatches int) {
	t.Helper()
	original := appendRegistryEntry
	count := 0
	appendRegistryEntry = func(path string, entry contracts.RuleRegistryEntry) (contracts.RegistryAppendResult, error) {
		if path == runCtx.RulesRegistryPath() && count < mismatches {
			switch entry.Kind {
			case contracts.RegistryKindAdded, contracts.RegistryKindUpdated:
				count++
				_, _ = seedRegistryUniqueAdd(
					t,
					path,
					fmt.Sprintf("race-%d", count),
					fmt.Sprintf("%064x", 9000+count),
					fmt.Sprintf("2026-04-21-PR9%d-abcdef0", count),
				)
			}
		}
		return original(path, entry)
	}
	t.Cleanup(func() {
		appendRegistryEntry = original
	})
}

// memStore is a minimal in-memory IntentionWriter replacement for tests that
// exercise stage transitions. Saves are also persisted to disk so that a
// subsequent Run() call (resume) sees them.
type memStore struct {
	path string
}

func newMemStore(path string) *memStore { return &memStore{path: path} }

type trackingStore struct {
	*memStore
	saved []contracts.IntentionRecord
}

type hookStore struct {
	*memStore
	hook func(contracts.IntentionRecord)
}

type loadHookStore struct {
	*memStore
	hook func()
}

type noopStore struct{}

func newTrackingStore(path string) *trackingStore {
	return &trackingStore{memStore: newMemStore(path)}
}

func newHookStore(path string, hook func(contracts.IntentionRecord)) *hookStore {
	return &hookStore{memStore: newMemStore(path), hook: hook}
}

func newLoadHookStore(path string, hook func()) *loadHookStore {
	return &loadHookStore{memStore: newMemStore(path), hook: hook}
}

func (m *memStore) Load() (*contracts.IntentionRecord, error) {
	if m.path == "" {
		return nil, nil
	}
	_, err := os.Stat(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	rec, err := internalio.ReadJSON[contracts.IntentionRecord](m.path)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func (s *loadHookStore) Load() (*contracts.IntentionRecord, error) {
	if s.hook != nil {
		s.hook()
		s.hook = nil
	}
	return s.memStore.Load()
}

func (s *loadHookStore) Save(r contracts.IntentionRecord) error {
	return s.memStore.Save(r)
}

func (s *loadHookStore) Delete() error {
	return s.memStore.Delete()
}

func (noopStore) Load() (*contracts.IntentionRecord, error) { return nil, nil }
func (noopStore) Save(contracts.IntentionRecord) error      { return nil }
func (noopStore) Delete() error                             { return nil }

func (m *memStore) Save(r contracts.IntentionRecord) error {
	if m.path == "" {
		return nil
	}
	return internalio.WriteJSONAtomic(m.path, r)
}

func (m *memStore) Delete() error {
	if m.path == "" {
		return nil
	}
	if err := os.Remove(m.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *trackingStore) Save(r contracts.IntentionRecord) error {
	s.saved = append(s.saved, r)
	return s.memStore.Save(r)
}

func (s *hookStore) Save(r contracts.IntentionRecord) error {
	if err := s.memStore.Save(r); err != nil {
		return err
	}
	if s.hook != nil {
		s.hook(r)
	}
	return nil
}

func readDecision(t *testing.T, runCtx internalio.RunContext) contracts.Decision {
	t.Helper()
	path, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	d, err := internalio.ReadJSON[contracts.Decision](path)
	require.NoError(t, err)
	return d
}

func readStateEvents(t *testing.T, runCtx internalio.RunContext) []contracts.StateEntry {
	t.Helper()
	events, err := state.ScanEventsForRun(runCtx, runCtx.RunID)
	require.NoError(t, err)
	return events
}

func mustDecisionAdopt(t *testing.T, decision contracts.Decision) contracts.DecisionAdopt {
	t.Helper()
	switch v := decision.Value.(type) {
	case contracts.DecisionAdopt:
		return v
	case *contracts.DecisionAdopt:
		require.NotNil(t, v)
		return *v
	default:
		t.Fatalf("expected adopt decision, got action=%s type=%T", decision.Action, decision.Value)
		return contracts.DecisionAdopt{}
	}
}

func mustDecisionRollback(t *testing.T, decision contracts.Decision) contracts.DecisionRollback {
	t.Helper()
	switch v := decision.Value.(type) {
	case contracts.DecisionRollback:
		return v
	case *contracts.DecisionRollback:
		require.NotNil(t, v)
		return *v
	default:
		t.Fatalf("expected rollback decision, got action=%s type=%T", decision.Action, decision.Value)
		return contracts.DecisionRollback{}
	}
}

func intentionPath(t *testing.T, runCtx internalio.RunContext) string {
	t.Helper()
	p, err := runCtx.ResolveRunRelative("70/intention.json")
	require.NoError(t, err)
	return p
}

func tryNonBlockingFlock(fd int) error {
	return syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
}
