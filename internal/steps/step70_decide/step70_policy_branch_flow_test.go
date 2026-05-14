package step70_decide

import (
	"context"
	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/policyrepo"
	"github.com/nishimoto265/harnest/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun_RejectsWhenPolicyBranchAdvancedSinceRunSnapshot(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR102")
	policyDir := filepath.Join(runCtx.RunDir(), "policy")
	require.NoError(t, os.MkdirAll(policyDir, 0o755))
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(policyDir, "snapshot.json"), policyrepo.SnapshotMetadata{
		SchemaVersion: "1",
		PolicyBranch:  "auto-improve/policy",
		PolicyHead:    strings.Repeat("1", 40),
		RegistryHead:  "",
	}))
	git := &fakeGit{head: strings.Repeat("9", 40)}

	err := Run(context.Background(), 102, runCtx, pkg, candidates, store, Deps{
		Git:          git,
		Resolver:     resolver,
		Now:          fixedNow(),
		PolicyBranch: "auto-improve/policy",
		RepoRoot:     runCtx.RunsBase,
	})

	var stale *PolicySnapshotStaleError
	require.ErrorAs(t, err, &stale)
	assert.Equal(t, "policy_branch_stale", stale.Reason)
	assert.Empty(t, git.pushCalls)
	assert.NoFileExists(t, mustRunPath(t, runCtx, "70/decision.json"))
}
func TestRun_PlanningResumeRejectsWhenPolicyBranchAdvancedSinceRunSnapshot(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR1021")
	policyDir := filepath.Join(runCtx.RunDir(), "policy")
	require.NoError(t, os.MkdirAll(policyDir, 0o755))
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(policyDir, "snapshot.json"), policyrepo.SnapshotMetadata{
		SchemaVersion: "1",
		PolicyBranch:  "auto-improve/policy",
		PolicyHead:    strings.Repeat("1", 40),
		RegistryHead:  "",
	}))
	require.NoError(t, store.Save(planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)))
	git := &fakeGit{head: strings.Repeat("9", 40)}

	err := Run(context.Background(), 1021, runCtx, pkg, candidates, store, Deps{
		Git:          git,
		Resolver:     resolver,
		Now:          fixedNow(),
		PolicyBranch: "auto-improve/policy",
		RepoRoot:     runCtx.RunsBase,
	})

	var stale *PolicySnapshotStaleError
	require.ErrorAs(t, err, &stale)
	assert.Equal(t, "policy_branch_stale", stale.Reason)
	assert.Empty(t, git.pushCalls)
	assert.NoFileExists(t, mustRunPath(t, runCtx, "70/decision.json"))
}
func TestRun_RejectsWhenLocalPolicyRegistryAdvancedSinceRunSnapshot(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR104")
	policyDir := filepath.Join(runCtx.RunDir(), "policy")
	require.NoError(t, os.MkdirAll(policyDir, 0o755))
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(policyDir, "snapshot.json"), policyrepo.SnapshotMetadata{
		SchemaVersion: "1",
		RegistryHead:  "",
	}))
	_, _ = seedRegistryUniqueAdd(t, runCtx.RulesRegistryPath(), "rule-other", strings.Repeat("9", 64), string(runCtx.RunID))

	err := Run(context.Background(), 104, runCtx, pkg, candidates, store, Deps{
		Git:      &fakeGit{head: strings.Repeat("1", 40)},
		Resolver: resolver,
		Now:      fixedNow(),
	})

	var stale *PolicySnapshotStaleError
	require.ErrorAs(t, err, &stale)
	assert.Equal(t, "policy_registry_stale", stale.Reason)
	assert.NoFileExists(t, mustRunPath(t, runCtx, "70/decision.json"))
}
func TestDrivePolicyPublish_PolicyPublishingEmptyAfterAdoptsMatchingRemoteSnapshot(t *testing.T) {
	runCtx, _, candidates, _, resolver := newFixtureWithResolver(t, "PR103")
	store := newMemStore(intentionPath(t, runCtx))
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStagePolicyPublishing
	intention.RegistryAppendResult = &contracts.RegistryAppendResult{Offset: 12, Sha256: strings.Repeat("a", 64)}
	intention.PolicyBranch = "auto-improve/policy"
	intention.PolicyHeadBefore = strings.Repeat("1", 40)
	require.NoError(t, store.Save(intention))

	originalMatches := branchSnapshotMatchesLocal
	branchSnapshotMatchesLocal = func(ctx context.Context, repoRoot, branch, runsBase string) (bool, error) {
		assert.Equal(t, "repo-root", repoRoot)
		assert.Equal(t, "auto-improve/policy", branch)
		assert.Equal(t, runCtx.RunsBase, runsBase)
		return true, nil
	}
	t.Cleanup(func() {
		branchSnapshotMatchesLocal = originalMatches
	})

	publishedHead := strings.Repeat("2", 40)
	updated, err := drivePolicyPublish(context.Background(), 103, runCtx, intention, store, state.NewWriter(runCtx), Deps{
		Git:          &fakeGit{head: publishedHead},
		Now:          fixedNow(),
		RepoRoot:     "repo-root",
		PolicyBranch: "auto-improve/policy",
	})
	require.NoError(t, err)

	assert.Equal(t, contracts.IntentionStagePolicyPublished, updated.Stage)
	assert.Equal(t, publishedHead, updated.PolicyHeadAfter)
	loaded, err := store.Load()
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, contracts.IntentionStagePolicyPublished, loaded.Stage)
	assert.Equal(t, publishedHead, loaded.PolicyHeadAfter)
	assert.NoFileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, string(runCtx.RunID)+".json"))
}
func TestFilesystemResolver_PolicyOnlyAdoptDoesNotPushImplementationCommit(t *testing.T) {
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

	assert.Empty(t, git.pushCalls)
	decision := readDecision(t, runCtx).Value.(contracts.DecisionAdopt)
	assert.Equal(t, strings.Repeat("1", 40), decision.BestShaBefore)
	assert.Equal(t, strings.Repeat("1", 40), decision.TargetSha)
}

func TestFilesystemResolver_PolicyOnlyAdoptAllowsMissingBestBranch(t *testing.T) {
	runCtx, pkg, candidates := seedFilesystemResolverFixture(t)
	resolver := FilesystemResolver{
		RepoDir: runCtx.RunsBase,
		Now:     fixedNow(),
	}
	target, ok, err := resolver.Resolve(runCtx, pkg, candidates)
	require.NoError(t, err)
	require.True(t, ok)

	store := newMemStore(intentionPath(t, runCtx))
	git := &fakeGit{head: ""}
	require.NoError(t, Run(context.Background(), 42, runCtx, pkg, candidates, store, Deps{
		Git:      git,
		Resolver: &fixtureResolver{target: target},
		Now:      fixedNow(),
	}))

	assert.Empty(t, git.pushCalls)
	decision := mustDecisionAdopt(t, readDecision(t, runCtx))
	assert.True(t, decision.PolicyOnly)
	assert.Empty(t, decision.BestShaBefore)
	assert.Empty(t, decision.TargetSha)
}

func TestHandleRollback_PolicyOnlyPreRegistryDoesNotRequirePushOwnership(t *testing.T) {
	runCtx, pkg, candidates, _, resolver := newFixtureWithResolver(t, "PR421")
	resolver.target.PolicyOnly = true
	resolver.target.TargetSHA = resolver.target.BestShaBefore
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.PolicyOnly = true
	writer := state.NewWriter(runCtx)

	git := &fakeGit{head: resolver.target.BestShaBefore}
	require.NoError(t, handleRollback(context.Background(), 421, runCtx, pkg, resolver.target, intention, noopStore{}, writer, Deps{
		Git:      git,
		Resolver: resolver,
		Now:      fixedNow(),
	}, contracts.RollbackReasonTransactionalFailure, pushUnknown))

	assert.Empty(t, git.pushCalls)
	assert.Equal(t, contracts.DecisionActionRollback, readDecision(t, runCtx).Action)
	assert.NoFileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, string(runCtx.RunID)+".json"))
}
func TestHandleRollback_PolicyOnlyIgnoresUnrelatedBestBranchAdvancement(t *testing.T) {
	runCtx, pkg, candidates, _, resolver := newFixtureWithResolver(t, "PR425")
	resolver.target.PolicyOnly = true
	resolver.target.TargetSHA = resolver.target.BestShaBefore
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.PolicyOnly = true
	writer := state.NewWriter(runCtx)

	git := &fakeGit{head: strings.Repeat("9", 40)}
	require.NoError(t, handleRollback(context.Background(), 425, runCtx, pkg, resolver.target, intention, noopStore{}, writer, Deps{
		Git:      git,
		Resolver: resolver,
		Now:      fixedNow(),
	}, contracts.RollbackReasonTransactionalFailure, pushUnknown))

	assert.Empty(t, git.pushCalls)
	assert.Zero(t, git.remoteHeadCalls)
	assert.Equal(t, contracts.DecisionActionRollback, readDecision(t, runCtx).Action)
	assert.NoFileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, string(runCtx.RunID)+".json"))
}
func TestRun_ResumeFromDecisionWritten_PolicyBranchStaleNeedsManualRecovery(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR5011")
	registryPath := runCtx.RulesRegistryPath()
	appendResult, _ := seedRegistryAdd(t, registryPath, resolver, runCtx.RunID, candidates.CandidatesHash)

	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageDecisionWritten
	intention.RegistryAppendResult = &appendResult
	intention.PolicyBranch = "auto-improve/policy"
	intention.PolicyHeadBefore = strings.Repeat("3", 40)
	intention.PolicyHeadAfter = strings.Repeat("4", 40)
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

	err = Run(context.Background(), 5011, runCtx, pkg, candidates, store, Deps{
		Git: &fakeGit{heads: map[string]string{
			resolver.target.BestBranch: intention.TargetSha,
			"auto-improve/policy":      strings.Repeat("5", 40),
		}},
		Resolver: unexpectedResolver{t: t},
		Now:      fixedNow(),
	})
	require.ErrorIs(t, err, ErrNeedsManualRecovery)

	events := readStateEvents(t, runCtx)
	require.NotEmpty(t, events)
	recovery := mustNeedsManualRecoveryEvent(t, events[len(events)-1])
	assert.Equal(t, contracts.RollbackReasonTransactionalFailure, recovery.Reason)
	assert.Equal(t, "decision_written_policy_branch_stale", recovery.Detail)
	assert.NotContains(t, readStateKinds(t, runCtx), contracts.StateKindPromoted)
}
func TestRecoverAdoptAnywayPolicyOnlyIgnoresBestBranchAdvancement(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR506")
	resolver.target.PolicyOnly = true
	resolver.target.TargetSHA = resolver.target.BestShaBefore
	resolver.target.RulesToAppend = adoptAddedEntriesWithTarget(runCtx.RunID, candidates.CandidatesHash, resolver.target.TargetSHA, "policy-only-rule")
	stageFixtureRuleSidecars(t, runCtx, resolver.target)
	registryPath := runCtx.RulesRegistryPath()
	appendResult, _ := seedRegistryAdd(t, registryPath, resolver, runCtx.RunID, candidates.CandidatesHash)

	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.PolicyOnly = true
	intention.Stage = contracts.IntentionStageNeedsManualRecovery
	intention.RegistryAppendResult = &appendResult
	intention.RecoveryReason = contracts.RollbackReasonTransactionalFailure
	intention.FailedStep = contracts.FailedStep70
	require.NoError(t, store.Save(intention))

	err := RecoverAdoptAnyway(context.Background(), 506, runCtx, pkg, candidates, store, Deps{
		Git: &fakeGit{head: strings.Repeat("9", 40)},
		Now: fixedNow(),
	})
	require.NoError(t, err)

	decision := readDecision(t, runCtx)
	adopt := mustDecisionAdopt(t, decision)
	assert.True(t, adopt.PolicyOnly)
	assert.Contains(t, readStateKinds(t, runCtx), contracts.StateKindPromoted)
	assert.NoFileExists(t, intentionPath(t, runCtx))
}
func TestRecoverRollbackPolicyOnlyIgnoresBestBranchAdvancement(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR507")
	resolver.target.PolicyOnly = true
	resolver.target.TargetSHA = resolver.target.BestShaBefore

	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.PolicyOnly = true
	intention.Stage = contracts.IntentionStageNeedsManualRecovery
	intention.RecoveryReason = contracts.RollbackReasonTransactionalFailure
	intention.FailedStep = contracts.FailedStep70
	require.NoError(t, store.Save(intention))

	git := &fakeGit{head: strings.Repeat("9", 40)}
	err := RecoverRollback(context.Background(), 507, runCtx, pkg, store, Deps{
		Git: git,
		Now: fixedNow(),
	})
	require.NoError(t, err)

	assert.Zero(t, git.remoteHeadCalls)
	assert.Empty(t, git.pushCalls)
	assert.Equal(t, contracts.DecisionActionRollback, readDecision(t, runCtx).Action)
	assert.NoFileExists(t, intentionPath(t, runCtx))
}
