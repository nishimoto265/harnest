package step70_decide

import (
	"context"
	"fmt"
	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
