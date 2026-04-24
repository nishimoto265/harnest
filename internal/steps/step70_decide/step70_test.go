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
	"github.com/nishimoto265/auto-improve/internal/policyrepo"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func realTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return real
}

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

func TestFilesystemResolver_LeaseFailureLeavesCanonicalRuleUntouched(t *testing.T) {
	runCtx, pkg, candidates := seedFilesystemResolverFixture(t)
	ruleID := generatedRuleID("cand-1")
	rulePath := filepath.Join(runCtx.RunsBase, "rules", ruleID+".md")
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
	assert.NoFileExists(t, mustStagedRulePath(t, runCtx, filepath.Join("rules", ruleID+".md")))
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

	ruleID := generatedRuleID("cand-1")
	rulePath := filepath.Join(runCtx.RunsBase, "rules", ruleID+".md")
	ruleBytes, err := os.ReadFile(rulePath)
	require.NoError(t, err)
	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	require.NoError(t, err)
	require.Len(t, lines, 1)
	added := lines[0].Entry.Value.(contracts.RuleRegistryAdded)
	assert.Equal(t, added.Sha256, sha256String(string(ruleBytes)))
	assert.NoFileExists(t, mustStagedRulePath(t, runCtx, filepath.Join("rules", ruleID+".md")))
}

func TestFilesystemResolver_RejectsDuplicateUpdateTargetsBeforeBuildingRegistryEntries(t *testing.T) {
	runCtx, pkg, candidates := seedFilesystemResolverFixture(t)
	targetRuleID := "r-existing"
	existingBody := "# Existing rule\n"
	_, err := internalio.AppendRegistryEntry(runCtx.RulesRegistryPath(), contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         targetRuleID,
			RulePath:       "rules/" + targetRuleID + ".md",
			Sha256:         sha256String(existingBody),
			IdempotencyKey: strings.Repeat("d", 64),
			VersionSeq:     1,
			ByRunID:        "2026-04-21-PR99-abcdef0",
			At:             time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
		},
	})
	require.NoError(t, err)
	require.NoError(t, internalio.WriteAtomic(filepath.Join(runCtx.RunsBase, "rules", targetRuleID+".md"), []byte(existingBody)))

	firstBody := "# Updated body one\n"
	secondBody := "# Updated body two\n"
	first := contracts.Candidate{
		CandidateID:        "cand-update-1",
		Kind:               contracts.CandidateKindUpdate,
		TargetRuleID:       targetRuleID,
		Title:              "Update one",
		ProposedBodyPath:   "40/candidates/cand-update-1.md",
		ProposedBodySha256: sha256String(firstBody),
	}
	second := contracts.Candidate{
		CandidateID:        "cand-update-2",
		Kind:               contracts.CandidateKindUpdate,
		TargetRuleID:       targetRuleID,
		Title:              "Update two",
		ProposedBodyPath:   "40/candidates/cand-update-2.md",
		ProposedBodySha256: sha256String(secondBody),
	}
	require.NoError(t, internalio.WriteAtomic(mustRunPath(t, runCtx, first.ProposedBodyPath), []byte(firstBody)))
	require.NoError(t, internalio.WriteAtomic(mustRunPath(t, runCtx, second.ProposedBodyPath), []byte(secondBody)))

	candidates.Candidates = []contracts.Candidate{first, second}
	candidates.CandidatesHash = contracts.CanonicalCandidatesHash(candidates.Candidates)
	require.NoError(t, candidates.Validate())
	require.NoError(t, internalio.WriteJSONAtomic(mustRunPath(t, runCtx, "40/candidates.json"), candidates))
	seedResolverGateCompliance(t, runCtx, first.CandidateID, contracts.ComplianceVerdictCompliant)
	seedResolverGateCompliance(t, runCtx, second.CandidateID, contracts.ComplianceVerdictCompliant)
	writeStep60DoneMarkerForResolverFixture(t, runCtx)

	resolver := FilesystemResolver{RepoDir: runCtx.RunsBase, Now: fixedNow()}
	target, ok, err := resolver.Resolve(runCtx, pkg, candidates)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, target.RulesToAppend)
	assert.NoFileExists(t, mustStagedRulePath(t, runCtx, filepath.Join("rules", targetRuleID+".md")))
}

func TestFilesystemResolver_RequiresStep60DoneMarker(t *testing.T) {
	runCtx, pkg, candidates := seedFilesystemResolverFixture(t)
	markerPath, err := runCtx.ResolveRunRelative("60/done.marker")
	require.NoError(t, err)
	require.NoError(t, os.Remove(markerPath))

	resolver := FilesystemResolver{RepoDir: runCtx.RunsBase, Now: fixedNow()}
	_, ok, err := resolver.Resolve(runCtx, pkg, candidates)
	require.Error(t, err)
	assert.False(t, ok)
	assert.Contains(t, err.Error(), "step60 done marker")
	stagingRulesPath, err := runCtx.ResolveRunRelative("staging/rules")
	require.NoError(t, err)
	assert.NoDirExists(t, stagingRulesPath)
}

func TestFilesystemResolver_RejectsStep60ArtifactsThatDoNotMatchDoneMarker(t *testing.T) {
	runCtx, pkg, candidates := seedFilesystemResolverFixture(t)
	scorePath, err := runCtx.ResolveRunRelative("60/scores-B.jsonl")
	require.NoError(t, err)
	require.NoError(t, internalio.AppendJSONL(scorePath, contracts.ScoreEntry{
		SchemaVersion: "1",
		RunID:         runCtx.RunID,
		Pass:          2,
		Agent:         "a1",
		Dimension:     contracts.DimensionFidelity,
		Score:         1,
		Reasons:       "stale score after marker",
		VerdictPath:   contracts.VerdictPathAgreement,
		RubricVersion: "default",
		PromptVersion: "phase0-stub",
		ResolvedAt:    time.Date(2026, 4, 21, 10, 3, 0, 0, time.UTC),
	}))

	resolver := FilesystemResolver{RepoDir: runCtx.RunsBase, Now: fixedNow()}
	_, ok, err := resolver.Resolve(runCtx, pkg, candidates)
	require.Error(t, err)
	assert.False(t, ok)
	assert.Contains(t, err.Error(), "done marker does not match")
	stagingRulesPath, err := runCtx.ResolveRunRelative("staging/rules")
	require.NoError(t, err)
	assert.NoDirExists(t, stagingRulesPath)
}

func TestVerifyStep60ArtifactSnapshot_AllowsNoComplianceRules(t *testing.T) {
	artifacts := step60ArtifactSnapshot{
		Scores: []contracts.ScoreEntry{
			{
				SchemaVersion: "1",
				RunID:         "2026-04-21-PR430-abcdef0",
				Pass:          2,
				Agent:         "a1",
				Dimension:     contracts.DimensionFidelity,
				Score:         90,
				Reasons:       "resolver fixture pass2",
				VerdictPath:   contracts.VerdictPathAgreement,
				RubricVersion: "default",
				PromptVersion: "phase0-stub",
				ResolvedAt:    time.Date(2026, 4, 21, 10, 2, 0, 0, time.UTC),
			},
		},
		Pairwise: []contracts.PairwiseEntry{
			{
				SchemaVersion: "1",
				RunID:         "2026-04-21-PR430-abcdef0",
				AgentA:        "a1",
				AgentB:        "a1",
				Winner:        contracts.PairwiseWinnerB,
				Margin:        contracts.PairwiseMarginClear,
				Justification: "resolver fixture",
				VerdictPath:   contracts.VerdictPathAgreement,
				RubricVersion: "default",
				PromptVersion: "phase0-stub",
				ResolvedAt:    time.Date(2026, 4, 21, 10, 2, 0, 0, time.UTC),
			},
		},
	}
	scoresCount, scoresHash, err := step70FinalScoresState(artifacts.Scores)
	require.NoError(t, err)
	complianceCount, complianceHash, err := step70FinalComplianceState(artifacts.Compliance)
	require.NoError(t, err)
	pairwiseCount, pairwiseHash, err := step70FinalPairwiseState(artifacts.Pairwise)
	require.NoError(t, err)

	err = verifyStep60ArtifactSnapshot(contracts.Step60DoneMarker{
		CompletedAgents: []contracts.AgentID{"a1"},
		Dimensions:      append([]contracts.Dimension(nil), step70CanonicalDimensions...),
		ExpectedCounts: contracts.Step60ExpectedCounts{
			Scores:     int64(scoresCount),
			Compliance: int64(complianceCount),
			Pairwise:   int64(pairwiseCount),
		},
		ContentHashes: contracts.Step60DoneContentHashes{
			ScoresFinal:     scoresHash,
			ComplianceFinal: complianceHash,
			PairwiseFinal:   pairwiseHash,
		},
	}, artifacts)
	require.NoError(t, err)
}

func TestFilesystemResolver_RejectsStep60RawArtifactsThatDoNotMatchDoneMarker(t *testing.T) {
	runCtx, pkg, candidates := seedFilesystemResolverFixture(t)
	rawPath, err := runCtx.ResolveRunRelative("60/scores-B-raw.jsonl")
	require.NoError(t, err)
	require.NoError(t, internalio.AppendJSONL(rawPath, contracts.RawScoreEntry{
		SchemaVersion: "1",
		RunID:         runCtx.RunID,
		Pass:          2,
		Agent:         "a1",
		JudgeRole:     contracts.JudgeRolePrimary,
		Dimension:     contracts.DimensionFidelity,
		Score:         1,
		Reasons:       "stale raw after marker",
		OutputSha256:  strings.Repeat("1", 64),
		RubricVersion: "default",
		PromptVersion: "phase0-stub",
		ResolvedAt:    time.Date(2026, 4, 21, 10, 3, 0, 0, time.UTC),
	}))

	resolver := FilesystemResolver{RepoDir: runCtx.RunsBase, Now: fixedNow()}
	_, ok, err := resolver.Resolve(runCtx, pkg, candidates)
	require.Error(t, err)
	assert.False(t, ok)
	assert.Contains(t, err.Error(), "raw hashes do not match")
}

func TestFilesystemResolver_RejectsStep60InputsThatDoNotMatchDoneMarker(t *testing.T) {
	runCtx, pkg, candidates := seedFilesystemResolverFixture(t)
	diffPath, err := runCtx.ResolveRunRelative("50-pass2/a1/diff.patch")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteAtomic(diffPath, []byte("mutated pass2 diff\n")))

	resolver := FilesystemResolver{RepoDir: runCtx.RunsBase, Now: fixedNow()}
	_, ok, err := resolver.Resolve(runCtx, pkg, candidates)
	require.Error(t, err)
	assert.False(t, ok)
	assert.Contains(t, err.Error(), "input hashes do not match")
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

func TestRun_DivergedSunsetMarkerBlocksExecution(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR304")
	require.NoError(t, os.WriteFile(filepath.Join(runCtx.RunsBase, sunsetMarkerFile+".diverged"), []byte("{}"), 0o644))

	err := Run(context.Background(), 304, runCtx, pkg, candidates, store, Deps{
		Git:      &fakeGit{head: resolver.target.BestShaBefore},
		Resolver: resolver,
		Now:      fixedNow(),
	})
	require.ErrorIs(t, err, ErrBlockedBySentinel)
	assert.Contains(t, err.Error(), sunsetMarkerFile+".diverged")
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

func TestRun_RejectsCandidatesHashMismatchAtEntry(t *testing.T) {
	runCtx, pkg, candidates, store, _ := newFixture(t, "PR430")
	candidates.CandidatesHash = strings.Repeat("f", 64)

	err := Run(context.Background(), 430, runCtx, pkg, candidates, store, Deps{Now: fixedNow()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "candidates invalid")

	decisionPath, pathErr := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, pathErr)
	assert.NoFileExists(t, decisionPath)
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

func TestFindPlannedRegistryMatches_RejectsPayloadMismatchForMatchingOpID(t *testing.T) {
	runCtx, _, candidates, _, resolver := newFixtureWithResolver(t, "PR417")
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)

	planned := intention.PlannedAdoption.Entries[0]
	_, err := internalio.AppendRegistryEntry(runCtx.RulesRegistryPath(), contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         planned.RuleID,
			RulePath:       planned.RulePath,
			Sha256:         strings.Repeat("f", 64),
			IdempotencyKey: planned.OpID,
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        runCtx.RunID,
			At:             fixedNow()(),
		},
	})
	require.NoError(t, err)

	_, err = findPlannedRegistryMatches(runCtx, intention)
	require.ErrorIs(t, err, ErrRegistryDivergence)
}

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

func TestAppendRegistryRollbacks_IgnoresMismatchedExistingRollbackTarget(t *testing.T) {
	runCtx, _, candidates, _, resolver := newFixtureWithResolver(t, "PR120")
	appendResult, _ := seedRegistryAdd(t, runCtx.RulesRegistryPath(), resolver, runCtx.RunID, candidates.CandidatesHash)
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.RegistryAppendResult = &appendResult
	intention.AppendedEntryOpIDs = append(intention.AppendedEntryOpIDs, intention.PlannedAdoption.Entries[0].OpID)

	bogusRollback := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindRolledBack,
		Value: contracts.RuleRegistryRolledBack{
			Kind:           contracts.RegistryKindRolledBack,
			SchemaVersion:  "1",
			TargetOpID:     intention.PlannedAdoption.Entries[0].OpID,
			TargetOffset:   appendResult.Offset + 999,
			TargetSha256:   strings.Repeat("f", 64),
			ByRunID:        runCtx.RunID,
			RollbackReason: contracts.RollbackReasonTransactionalFailure,
			FailedStep:     contracts.FailedStep70,
			VersionSeq:     2,
			PrevHash:       appendResult.Sha256,
			At:             fixedNow()(),
		},
	}
	_, err := internalio.AppendRegistryEntry(runCtx.RulesRegistryPath(), bogusRollback)
	require.NoError(t, err)

	rollbackResult, err := appendRegistryRollbacks(runCtx, intention, contracts.RollbackReasonTransactionalFailure, fixedNow()())
	require.NoError(t, err)
	require.NotNil(t, rollbackResult)

	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	require.NoError(t, err)
	require.Len(t, lines, 3)
	assert.Equal(t, contracts.RegistryKindRolledBack, lines[2].Entry.Kind)
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

func TestPromoteRuleSidecarAndCleanup_FsyncParentDirsAfterDeletion(t *testing.T) {
	runCtx, _, _, _, _ := newFixtureWithResolver(t, "PR413")
	originalSync := syncStagingParentDir
	var calls []string
	syncStagingParentDir = func(path string) error {
		calls = append(calls, filepath.Clean(path))
		return nil
	}
	t.Cleanup(func() {
		syncStagingParentDir = originalSync
	})

	stagedPath := mustStagedRulePath(t, runCtx, "rules/rule-a.md")
	require.NoError(t, internalio.WriteAtomic(stagedPath, []byte("rule-a body\n")))
	require.NoError(t, promoteRuleSidecar(stagedPath, filepath.Join(runCtx.RunsBase, "rules", "rule-a.md"), sha256String("rule-a body\n"), ""))

	require.NoError(t, internalio.WriteAtomic(mustStagedRulePath(t, runCtx, "rules/rule-b.md"), []byte("rule-b body\n")))
	require.NoError(t, cleanupStagedRuleSidecars(runCtx))

	assert.Contains(t, calls, filepath.Clean(filepath.Dir(stagedPath)))
	assert.Contains(t, calls, filepath.Clean(runCtx.RunDir()))
}

// F10: after the first rule sidecar in a multi-entry adoption is published and
// its staged copy removed, a resume must recognise the destination's matching
// SHA as already-published instead of escalating the whole batch to
// needs_manual_recovery via errRulePublishStagedMissing.
func TestPromoteRuleSidecar_TreatsMissingStagedWithMatchingDestinationAsPublished(t *testing.T) {
	runCtx, _, _, _, _ := newFixtureWithResolver(t, "PR4101")
	body := "rule-a body\n"
	stagedPath := mustStagedRulePath(t, runCtx, "rules/rule-a.md")

	dstPath := filepath.Join(runCtx.RunsBase, "rules", "rule-a.md")
	require.NoError(t, internalio.WriteAtomic(dstPath, []byte(body)))
	// staged file intentionally absent to simulate a crash after the first
	// entry in the batch was published and its staged copy fsynced away.
	assert.NoFileExists(t, stagedPath)

	require.NoError(t, promoteRuleSidecar(stagedPath, dstPath, sha256String(body), ""))
	// Destination unchanged and still holds the planned bytes.
	got, err := os.ReadFile(dstPath)
	require.NoError(t, err)
	assert.Equal(t, body, string(got))
}

// F10: if the staged file is missing AND the destination does not hold the
// planned SHA, the original errRulePublishStagedMissing signal is preserved so
// the batch still escalates to needs_manual_recovery rather than silently
// committing stale bytes.
func TestPromoteRuleSidecar_MissingStagedWithWrongDestinationStillFails(t *testing.T) {
	runCtx, _, _, _, _ := newFixtureWithResolver(t, "PR4102")
	stagedPath := mustStagedRulePath(t, runCtx, "rules/rule-a.md")
	dstPath := filepath.Join(runCtx.RunsBase, "rules", "rule-a.md")
	require.NoError(t, internalio.WriteAtomic(dstPath, []byte("stale bytes\n")))

	err := promoteRuleSidecar(stagedPath, dstPath, sha256String("rule-a body\n"), "")
	require.ErrorIs(t, err, errRulePublishStagedMissing)
}

func TestPromoteRuleSidecar_AllowsUpdateWhenDestinationMatchesPrevSha(t *testing.T) {
	runCtx, _, _, _, _ := newFixtureWithResolver(t, "PR41025")
	oldBody := "old body\n"
	newBody := "new body\n"
	stagedPath := mustStagedRulePath(t, runCtx, "rules/rule-a.md")
	require.NoError(t, internalio.WriteAtomic(stagedPath, []byte(newBody)))

	dstPath := filepath.Join(runCtx.RunsBase, "rules", "rule-a.md")
	require.NoError(t, internalio.WriteAtomic(dstPath, []byte(oldBody)))

	require.NoError(t, promoteRuleSidecar(stagedPath, dstPath, sha256String(newBody), sha256String(oldBody)))

	got, err := os.ReadFile(dstPath)
	require.NoError(t, err)
	assert.Equal(t, newBody, string(got))
	assert.NoFileExists(t, stagedPath)
}

func TestPromoteRuleSidecar_RejectsUpdateWhenDestinationMissing(t *testing.T) {
	runCtx, _, _, _, _ := newFixtureWithResolver(t, "PR41026")
	oldBody := "old body\n"
	newBody := "new body\n"
	stagedPath := mustStagedRulePath(t, runCtx, "rules/rule-a.md")
	require.NoError(t, internalio.WriteAtomic(stagedPath, []byte(newBody)))

	dstPath := filepath.Join(runCtx.RunsBase, "rules", "rule-a.md")
	assert.NoFileExists(t, dstPath)

	err := promoteRuleSidecar(stagedPath, dstPath, sha256String(newBody), sha256String(oldBody))
	require.ErrorIs(t, err, errRulePublishConflict)
	assert.NoFileExists(t, dstPath)
	assert.FileExists(t, stagedPath)
}

// F10: multi-entry adoption resuming after a crash-between-publishes must
// complete without re-publishing the entry whose destination already holds
// the planned SHA.
func TestPromoteStagedRuleSidecars_MultiEntryResumeAfterFirstPublish(t *testing.T) {
	runCtx, _, candidates, _, resolver := newFixtureWithResolver(t, "PR4103")
	resolver.target.RulesToAppend = []contracts.RuleRegistryEntry{
		adoptAddedEntryWithBody(runCtx.RunID, "rule-a", "rule-a body\n"),
		adoptAddedEntryWithBody(runCtx.RunID, "rule-b", "rule-b body\n"),
	}
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)

	// Prepare staging dir + entry-b staged file. Entry-a was already published
	// and its staged copy removed; entry-b's staged file still exists.
	require.NoError(t, internalio.WriteAtomic(mustStagedRulePath(t, runCtx, "rules/rule-b.md"), []byte("rule-b body\n")))
	dstA := filepath.Join(runCtx.RunsBase, "rules", "rule-a.md")
	require.NoError(t, internalio.WriteAtomic(dstA, []byte("rule-a body\n")))

	store := newTrackingStore(intentionPath(t, runCtx))
	require.NoError(t, promoteStagedRuleSidecars(runCtx, &intention, store))

	dstB := filepath.Join(runCtx.RunsBase, "rules", "rule-b.md")
	got, err := os.ReadFile(dstB)
	require.NoError(t, err)
	assert.Equal(t, "rule-b body\n", string(got))

	// Both entries were persisted as published (even the one recognised via
	// matching destination SHA with a missing staged file).
	require.NotEmpty(t, intention.PublishedRuleOpIDs)
	assert.ElementsMatch(t, []string{
		intention.PlannedAdoption.Entries[0].OpID,
		intention.PlannedAdoption.Entries[1].OpID,
	}, intention.PublishedRuleOpIDs)

	// Staging directory cleaned up after the last publish.
	stagingDir, err := runCtx.ResolveRunRelative("staging")
	require.NoError(t, err)
	_, err = os.Stat(stagingDir)
	assert.True(t, os.IsNotExist(err), "staging dir must be removed after successful promotion")
}

// F10: intention.PublishedRuleOpIDs persisted by a previous tick means the
// resume path skips the already-published entry's staged file, but success
// still requires the canonical destination to hold the planned bytes.
func TestPromoteStagedRuleSidecars_SkipsPublishedOpIDsOnResume(t *testing.T) {
	runCtx, _, candidates, _, resolver := newFixtureWithResolver(t, "PR4104")
	resolver.target.RulesToAppend = []contracts.RuleRegistryEntry{
		adoptAddedEntryWithBody(runCtx.RunID, "rule-a", "rule-a body\n"),
		adoptAddedEntryWithBody(runCtx.RunID, "rule-b", "rule-b body\n"),
	}
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.PublishedRuleOpIDs = []string{intention.PlannedAdoption.Entries[0].OpID}

	// Only entry-b's staged file exists; entry-a is already marked published.
	require.NoError(t, internalio.WriteAtomic(mustStagedRulePath(t, runCtx, "rules/rule-b.md"), []byte("rule-b body\n")))
	require.NoError(t, internalio.WriteAtomic(filepath.Join(runCtx.RunsBase, "rules", "rule-a.md"), []byte("rule-a body\n")))

	require.NoError(t, promoteStagedRuleSidecars(runCtx, &intention, nil))

	// rule-a had no staged file, but its destination was verified.
	assert.FileExists(t, filepath.Join(runCtx.RunsBase, "rules", "rule-a.md"))
	assert.FileExists(t, filepath.Join(runCtx.RunsBase, "rules", "rule-b.md"))
}

func TestPromoteStagedRuleSidecars_MissingStagingDirRequiresPublishedDestinations(t *testing.T) {
	runCtx, _, candidates, _, resolver := newFixtureWithResolver(t, "PR4105")
	resolver.target.RulesToAppend = []contracts.RuleRegistryEntry{
		adoptAddedEntryWithBody(runCtx.RunID, "rule-a", "rule-a body\n"),
	}
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)

	err := promoteStagedRuleSidecars(runCtx, &intention, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, errRulePublishStagedMissing)

	require.NoError(t, internalio.WriteAtomic(filepath.Join(runCtx.RunsBase, "rules", "rule-a.md"), []byte("rule-a body\n")))
	require.NoError(t, promoteStagedRuleSidecars(runCtx, &intention, nil))
}

func TestPromoteRuleSidecar_RejectsSymlinkDestination(t *testing.T) {
	runCtx, _, _, _, _ := newFixtureWithResolver(t, "PR414")
	body := "rule-a body\n"
	stagedPath := mustStagedRulePath(t, runCtx, "rules/rule-a.md")
	require.NoError(t, internalio.WriteAtomic(stagedPath, []byte(body)))

	externalPath := filepath.Join(realTempDir(t), "external-rule.md")
	require.NoError(t, os.WriteFile(externalPath, []byte(body), 0o644))

	dstPath := filepath.Join(runCtx.RunsBase, "rules", "rule-a.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(dstPath), 0o755))
	require.NoError(t, os.Symlink(externalPath, dstPath))

	err := promoteRuleSidecar(stagedPath, dstPath, sha256String(body), "")
	require.ErrorIs(t, err, errRulePublishDestinationType)
	assert.FileExists(t, stagedPath)
	info, statErr := os.Lstat(dstPath)
	require.NoError(t, statErr)
	assert.NotZero(t, info.Mode()&os.ModeSymlink)
}

func TestPromoteRuleSidecar_DetectsDestinationSwapToSymlinkBeforeRead(t *testing.T) {
	runCtx, _, _, _, _ := newFixtureWithResolver(t, "PR4141")
	body := "rule-a body\n"
	stagedPath := mustStagedRulePath(t, runCtx, "rules/rule-a.md")
	require.NoError(t, internalio.WriteAtomic(stagedPath, []byte(body)))

	dstPath := filepath.Join(runCtx.RunsBase, "rules", "rule-a.md")
	require.NoError(t, internalio.WriteAtomic(dstPath, []byte("old body\n")))

	externalPath := filepath.Join(realTempDir(t), "external-rule.md")
	require.NoError(t, os.WriteFile(externalPath, []byte(body), 0o644))

	originalHook := promoteRuleSidecarBeforeDestinationRead
	promoteRuleSidecarBeforeDestinationRead = func(path string) {
		require.NoError(t, os.Remove(path))
		require.NoError(t, os.Symlink(externalPath, path))
	}
	t.Cleanup(func() {
		promoteRuleSidecarBeforeDestinationRead = originalHook
	})

	err := promoteRuleSidecar(stagedPath, dstPath, sha256String(body), "")
	require.ErrorIs(t, err, errRulePublishDestinationType)
	assert.FileExists(t, stagedPath)
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
	require.FileExists(t, intentionPath(t, runCtx))
	intention, loadErr := store.Load()
	require.NoError(t, loadErr)
	require.NotNil(t, intention)
	assert.Equal(t, contracts.IntentionStagePlanning, intention.Stage)

	decisionPath, pathErr := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, pathErr)
	assert.NoFileExists(t, decisionPath)
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
	stageFixtureRuleSidecars(t, runCtx, resolver.target)

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

func TestRun_ReadsBestShaBeforeFromRemoteHeadAfterLockAndOverridesPrefilledValue(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR20")
	resolver.target.BestShaBefore = strings.Repeat("9", 40)
	git := &fakeGit{head: strings.Repeat("4", 40)}

	require.NoError(t, Run(context.Background(), 20, runCtx, pkg, candidates, store, Deps{Git: git, Resolver: resolver, Now: fixedNow()}))

	adopt := mustDecisionAdopt(t, readDecision(t, runCtx))
	assert.Equal(t, strings.Repeat("4", 40), adopt.BestShaBefore)
	assert.Equal(t, 1, git.remoteHeadCalls)
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

func TestCleanupWorktrees_RejectsPathOutsideWorktreeBase(t *testing.T) {
	runCtx, err := internalio.NewRunContext("2026-04-21-PR999-abcdef0", realTempDir(t), realTempDir(t))
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
			{Agent: "a1", Pass: 1, Path: filepath.Join(realTempDir(t), "escape"), Branch: "stub/pass1/a1", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("a", 40)},
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

func TestCleanupWorktrees_UnregisteredMissingPathIsNoop(t *testing.T) {
	runCtx, pkg, _, _, _ := newFixture(t, "PR203")
	git := &fakeGit{removeWorktreeErr: ErrWorktreeUnregistered}

	require.NoError(t, cleanupWorktrees(context.Background(), runCtx, pkg, git))

	require.Len(t, git.removeWorktreeCalls, len(pkg.Worktrees))
	for _, wt := range pkg.Worktrees {
		assert.NoFileExists(t, wt.Path)
	}
}

func TestCleanupWorktrees_UnregisteredExistingPathRemovedUnderBase(t *testing.T) {
	runCtx, pkg, _, _, _ := newFixture(t, "PR204")
	for _, wt := range pkg.Worktrees {
		require.NoError(t, os.MkdirAll(wt.Path, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(wt.Path, "leftover.txt"), []byte("leftover\n"), 0o644))
	}
	git := &fakeGit{removeWorktreeErr: ErrWorktreeUnregistered}

	require.NoError(t, cleanupWorktrees(context.Background(), runCtx, pkg, git))

	require.Len(t, git.removeWorktreeCalls, len(pkg.Worktrees))
	for _, wt := range pkg.Worktrees {
		assert.NoDirExists(t, wt.Path)
	}
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

func (r *fixtureResolver) Resolve(runCtx internalio.RunContext, _ *contracts.TaskPackage, _ *contracts.Candidates) (Target, bool, error) {
	if err := stageFixtureRuleSidecarsForResolver(runCtx, r.target); err != nil {
		return Target{}, false, err
	}
	return r.target, true, nil
}

func stageFixtureRuleSidecarsForResolver(runCtx internalio.RunContext, target Target) error {
	for _, entry := range target.RulesToAppend {
		ruleID, rulePath, sha, err := registryEntryRuleSidecar(entry)
		if err != nil {
			return err
		}
		body := fixtureRuleBody(ruleID)
		if sha256String(body) != sha {
			continue
		}
		if err := internalio.WriteAtomic(mustStagedRulePathForResolver(runCtx, rulePath), []byte(body)); err != nil {
			return err
		}
	}
	return nil
}

func registryEntryRuleSidecar(entry contracts.RuleRegistryEntry) (string, string, string, error) {
	switch v := entry.Value.(type) {
	case contracts.RuleRegistryAdded:
		return v.RuleID, v.RulePath, v.Sha256, nil
	case contracts.RuleRegistryUpdated:
		return v.RuleID, v.RulePath, v.Sha256, nil
	default:
		return "", "", "", fmt.Errorf("unsupported fixture registry entry %T", entry.Value)
	}
}

func mustStagedRulePathForResolver(runCtx internalio.RunContext, rulePath string) string {
	path, err := stagedRuleSidecarPath(runCtx, rulePath)
	if err != nil {
		panic(err)
	}
	return path
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

func (r *sequenceResolver) Resolve(runCtx internalio.RunContext, _ *contracts.TaskPackage, _ *contracts.Candidates) (Target, bool, error) {
	if len(r.targets) == 0 {
		return Target{}, false, nil
	}
	if r.index >= len(r.targets) {
		target := r.targets[len(r.targets)-1]
		stageFixtureRuleSidecarsForResolver(runCtx, target)
		return target, true, nil
	}
	target := r.targets[r.index]
	r.index++
	stageFixtureRuleSidecarsForResolver(runCtx, target)
	return target, true, nil
}

type fakePushCall struct {
	branch   string
	target   string
	expected string
}

type fakeGit struct {
	head                string
	pushErr             error
	removeWorktreeErr   error
	pushCalls           []fakePushCall
	onPush              func(fakePushCall)
	remoteHeadCalls     int
	removeWorktreeCalls []string
}

type cancelOnPushGit struct {
	head   string
	cancel context.CancelFunc
}

func (g *fakeGit) RemoteHead(_ context.Context, _ string) (string, error) {
	g.remoteHeadCalls++
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

func (g *fakeGit) RemoveWorktree(_ context.Context, path string) error {
	g.removeWorktreeCalls = append(g.removeWorktreeCalls, path)
	return g.removeWorktreeErr
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
	tempRuns := realTempDir(t)
	worktreeBase := realTempDir(t)
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
	pass1ManifestPath, err := runCtx.ManifestPath(1, "a1")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(pass1ManifestPath, contracts.Manifest{
		Kind: contracts.ManifestKindSuccess,
		Value: contracts.ManifestSuccess{
			Kind:          contracts.ManifestKindSuccess,
			SchemaVersion: "1",
			RunID:         runID,
			Pass:          1,
			Agent:         "a1",
			BranchName:    pkg.Worktrees[0].Branch,
			HeadSHA:       strings.Repeat("1", 40),
			BaseSHA:       strings.Repeat("a", 40),
			DiffPath:      "20-pass1/a1/diff.patch",
			SessionPath:   "20-pass1/a1/session.jsonl",
			ChecklistPath: "20-pass1/a1/checklist-result.json",
			PromptVersion: "stub",
			StartedAt:     time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
			FinishedAt:    time.Date(2026, 4, 21, 10, 1, 0, 0, time.UTC),
		},
	}))
	require.NoError(t, internalio.WriteAtomic(mustRunPath(t, runCtx, "20-pass1/a1/diff.patch"), []byte("pass1 diff\n")))
	require.NoError(t, internalio.WriteAtomic(mustRunPath(t, runCtx, "20-pass1/a1/session.jsonl"), []byte("{}\n")))
	require.NoError(t, internalio.WriteAtomic(mustRunPath(t, runCtx, "20-pass1/a1/checklist-result.json"), []byte("{}\n")))

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
	pass2Diff := []byte("pass2 diff\n")
	require.NoError(t, internalio.WriteAtomic(mustRunPath(t, runCtx, "50-pass2/a1/diff.patch"), pass2Diff))
	require.NoError(t, internalio.WriteAtomic(mustRunPath(t, runCtx, "50-pass2/a1/session.jsonl"), []byte("{}\n")))
	require.NoError(t, internalio.WriteAtomic(mustRunPath(t, runCtx, "50-pass2/a1/checklist-result.json"), []byte("{}\n")))
	pass2OutputHash := sha256String(string(pass2Diff))

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
	pass1ScorePath, err := runCtx.ResolveRunRelative("30/scores-A.jsonl")
	require.NoError(t, err)
	for _, dimension := range resolverScoreDimensions() {
		require.NoError(t, internalio.AppendJSONL(pass1ScorePath, contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         runID,
			Pass:          1,
			Agent:         "a1",
			Dimension:     dimension,
			Score:         80,
			Reasons:       "resolver fixture pass1",
			VerdictPath:   contracts.VerdictPathAgreement,
			RubricVersion: "default",
			PromptVersion: "phase0-stub",
			ResolvedAt:    time.Date(2026, 4, 21, 10, 2, 0, 0, time.UTC),
		}))
		require.NoError(t, internalio.AppendJSONL(scorePath, contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         runID,
			Pass:          2,
			Agent:         "a1",
			Dimension:     dimension,
			Score:         90,
			Reasons:       "resolver fixture pass2",
			VerdictPath:   contracts.VerdictPathAgreement,
			RubricVersion: "default",
			PromptVersion: "phase0-stub",
			ResolvedAt:    time.Date(2026, 4, 21, 10, 2, 0, 0, time.UTC),
		}))
	}

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
	require.NoError(t, internalio.WriteJSONAtomic(mustRunPath(t, runCtx, "40/candidates.json"), candidates))
	compliancePath, err := runCtx.ResolveRunRelative("60/compliance-B.jsonl")
	require.NoError(t, err)
	require.NoError(t, internalio.AppendJSONL(compliancePath, contracts.ComplianceEntry{
		SchemaVersion: "1",
		RunID:         runID,
		Pass:          2,
		Agent:         "a1",
		RuleID:        candidate.CandidateID,
		Verdict:       contracts.ComplianceVerdictCompliant,
		Rationale:     "candidate judged compliant",
		VerdictPath:   contracts.VerdictPathAgreement,
		RubricVersion: "default",
		PromptVersion: "phase0-stub",
		ResolvedAt:    time.Date(2026, 4, 21, 10, 2, 0, 0, time.UTC),
	}))
	scoreRawPath, err := runCtx.ResolveRunRelative("60/scores-B-raw.jsonl")
	require.NoError(t, err)
	complianceRawPath, err := runCtx.ResolveRunRelative("60/compliance-B-raw.jsonl")
	require.NoError(t, err)
	for _, dimension := range resolverScoreDimensions() {
		require.NoError(t, internalio.AppendJSONL(scoreRawPath, contracts.RawScoreEntry{
			SchemaVersion: "1",
			RunID:         runID,
			Pass:          2,
			Agent:         "a1",
			JudgeRole:     contracts.JudgeRolePrimary,
			Dimension:     dimension,
			Score:         90,
			Reasons:       "resolver fixture pass2",
			OutputSha256:  pass2OutputHash,
			RubricVersion: "default",
			PromptVersion: "phase0-stub",
			ResolvedAt:    time.Date(2026, 4, 21, 10, 2, 0, 0, time.UTC),
		}))
	}
	require.NoError(t, internalio.AppendJSONL(complianceRawPath, contracts.RawComplianceEntry{
		SchemaVersion: "1",
		RunID:         runID,
		Pass:          2,
		Agent:         "a1",
		JudgeRole:     contracts.JudgeRolePrimary,
		RuleID:        candidate.CandidateID,
		Verdict:       contracts.ComplianceVerdictCompliant,
		Rationale:     "candidate judged compliant",
		OutputSha256:  pass2OutputHash,
		RubricVersion: "default",
		PromptVersion: "phase0-stub",
		ResolvedAt:    time.Date(2026, 4, 21, 10, 2, 0, 0, time.UTC),
	}))
	writeStep60DoneMarkerForResolverFixture(t, runCtx)
	return runCtx, pkg, candidates
}

func writeStep60DoneMarkerForResolverFixture(t *testing.T, runCtx internalio.RunContext) {
	t.Helper()
	artifacts, err := loadStep60Artifacts(runCtx)
	require.NoError(t, err)
	scoresCount, scoresHash, err := step70FinalScoresState(artifacts.Scores)
	require.NoError(t, err)
	complianceCount, complianceHash, err := step70FinalComplianceState(artifacts.Compliance)
	require.NoError(t, err)
	pairwiseCount, pairwiseHash, err := step70FinalPairwiseState(artifacts.Pairwise)
	require.NoError(t, err)

	pkg, err := internalio.ReadJSON[contracts.TaskPackage](runCtx.TaskPackagePath())
	require.NoError(t, err)
	inputHashes, completedAgents, err := currentStep60InputHashes(runCtx, &pkg)
	require.NoError(t, err)
	scoresRawHash, err := step70ReducedRawScoresHash(runCtx)
	require.NoError(t, err)
	complianceRawHash, err := step70ReducedRawComplianceHash(runCtx)
	require.NoError(t, err)
	marker := contracts.Step60DoneMarker{
		CompletedAgents: completedAgents,
		Dimensions: []contracts.Dimension{
			contracts.DimensionFidelity,
			contracts.DimensionCorrectness,
			contracts.DimensionMaintainability,
			contracts.DimensionDiscipline,
			contracts.DimensionCommunication,
		},
		ExpectedCounts: contracts.Step60ExpectedCounts{
			Scores:     int64(scoresCount),
			Compliance: int64(complianceCount),
			Pairwise:   int64(pairwiseCount),
		},
		InputHashes: inputHashes,
		ContentHashes: contracts.Step60DoneContentHashes{
			ScoresFinal:     scoresHash,
			ComplianceFinal: complianceHash,
			PairwiseFinal:   pairwiseHash,
		},
		RawHashes: contracts.StepDoneRawHashes{
			ScoresRaw:     scoresRawHash,
			ComplianceRaw: complianceRawHash,
		},
		ResolvedAt: time.Date(2026, 4, 21, 10, 2, 0, 0, time.UTC),
	}
	require.NoError(t, marker.Validate())
	markerPath, err := runCtx.ResolveRunRelative("60/done.marker")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(markerPath, marker))
}

func resolverScoreDimensions() []contracts.Dimension {
	return []contracts.Dimension{
		contracts.DimensionFidelity,
		contracts.DimensionCorrectness,
		contracts.DimensionMaintainability,
		contracts.DimensionDiscipline,
		contracts.DimensionCommunication,
	}
}

func mustStagedRulePath(t *testing.T, runCtx internalio.RunContext, rulePath string) string {
	t.Helper()
	path, err := stagedRuleSidecarPath(runCtx, rulePath)
	require.NoError(t, err)
	return path
}

func mustRunPath(t *testing.T, runCtx internalio.RunContext, rel string) string {
	t.Helper()
	path, err := runCtx.ResolveRunRelative(rel)
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
	stageFixtureRuleSidecars(t, runCtx, resolver.target)
	// Idempotency key requires the target.TargetSHA to be known; re-derive
	// against the empty candidates hash used by newFixture.
	return runCtx, pkg, candidates, store, resolver
}

func stageFixtureRuleSidecars(t *testing.T, runCtx internalio.RunContext, target Target) {
	t.Helper()
	for _, entry := range target.RulesToAppend {
		ruleID, rulePath, sha, err := registryEntryRuleSidecar(entry)
		require.NoError(t, err)
		body := fixtureRuleBody(ruleID)
		if sha256String(body) != sha {
			continue
		}
		require.NoError(t, internalio.WriteAtomic(mustStagedRulePath(t, runCtx, rulePath), []byte(body)))
	}
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
		Sha256:         sha256String(fixtureRuleBody(ruleID)),
		IdempotencyKey: key,
		VersionSeq:     1,
		PrevHash:       "",
		ByRunID:        runID,
		At:             time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}
	v.IdempotencyKey = contracts.ComputeAdoptIdempotencyKey(string(runID), targetSHA, strings.Repeat("1", 40), candidatesHash)
	return contracts.RuleRegistryEntry{Kind: v.Kind, Value: v}
}

func fixtureRuleBody(ruleID string) string {
	return ruleID + " body\n"
}

func adoptAddedEntryWithBody(runID contracts.RunID, ruleID, body string) contracts.RuleRegistryEntry {
	return contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         ruleID,
			RulePath:       "rules/" + ruleID + ".md",
			Sha256:         sha256String(body),
			IdempotencyKey: strings.Repeat("0", 64),
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        runID,
			At:             time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
		},
	}
}

func adoptUpdatedEntryWithBody(runID contracts.RunID, ruleID, prevBody, body string) contracts.RuleRegistryEntry {
	return contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindUpdated,
		Value: contracts.RuleRegistryUpdated{
			Kind:           contracts.RegistryKindUpdated,
			SchemaVersion:  "1",
			RuleID:         ruleID,
			RulePath:       "rules/" + ruleID + ".md",
			Sha256:         sha256String(body),
			PrevSha256:     sha256String(prevBody),
			IdempotencyKey: strings.Repeat("0", 64),
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        runID,
			At:             time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
		},
	}
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
	ruleID, rulePath, sha, err := registryEntryRuleSidecar(entry)
	require.NoError(t, err)
	body := fixtureRuleBody(ruleID)
	if sha256String(body) == sha {
		require.NoError(t, internalio.WriteAtomic(filepath.Join(filepath.Dir(path), filepath.FromSlash(rulePath)), []byte(body)))
	}
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

func readStateKinds(t *testing.T, runCtx internalio.RunContext) []contracts.StateKind {
	t.Helper()
	events := readStateEvents(t, runCtx)
	kinds := make([]contracts.StateKind, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	return kinds
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

func mustNeedsManualRecoveryEvent(t *testing.T, entry contracts.StateEntry) contracts.StateEntryNeedsManualRecovery {
	t.Helper()
	switch v := entry.Value.(type) {
	case contracts.StateEntryNeedsManualRecovery:
		return v
	case *contracts.StateEntryNeedsManualRecovery:
		require.NotNil(t, v)
		return *v
	default:
		t.Fatalf("expected needs_manual_recovery event, got kind=%s type=%T", entry.Kind, entry.Value)
		return contracts.StateEntryNeedsManualRecovery{}
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
