package step70_decide

import (
	"context"
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
func TestFilesystemResolver_SelectsNextPromotablePairwiseWinner(t *testing.T) {
	runCtx, pkg, candidates := seedFilesystemResolverFixture(t)
	seedAdditionalResolverAgent(t, runCtx, pkg, "a2", strings.Repeat("3", 40), 80, 90)
	seedResolverAgentScores(t, runCtx, "a1", sha256String("pass2 diff\n"), 94, 95)
	writeStep60DoneMarkerForResolverFixture(t, runCtx)

	resolver := FilesystemResolver{
		RepoDir: runCtx.RunsBase,
		Now:     fixedNow(),
	}
	target, ok, err := resolver.Resolve(runCtx, pkg, candidates)

	require.NoError(t, err)
	require.True(t, ok)
	assert.True(t, target.PolicyOnly)
	assert.Empty(t, target.TargetSHA)
}
func TestStep70Pass2OutputHashesFailsWhenPass2ScorableButPass1IsNotScorable(t *testing.T) {
	runCtx, pkg, _, _, _ := newFixture(t, "PR421")
	seedAdditionalResolverAgent(t, runCtx, pkg, "a2", strings.Repeat("2", 40), 80, 90)

	pass1ManifestPath, err := runCtx.ManifestPath(1, "a1")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(pass1ManifestPath, contracts.Manifest{
		Kind: contracts.ManifestKindTimeout,
		Value: contracts.ManifestTimeout{
			Kind:           contracts.ManifestKindTimeout,
			SchemaVersion:  "1",
			RunID:          runCtx.RunID,
			Pass:           1,
			Agent:          "a1",
			TimeoutSeconds: 60,
			StartedAt:      time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
			FinishedAt:     time.Date(2026, 4, 21, 10, 1, 0, 0, time.UTC),
		},
	}))

	pass2Branch := ""
	for _, wt := range pkg.Worktrees {
		if wt.Pass == 2 && wt.Agent == "a1" {
			pass2Branch = wt.Branch
			break
		}
	}
	require.NotEmpty(t, pass2Branch)
	pass2DiffPath := "50-pass2/a1/diff.patch"
	pass2ManifestPath, err := runCtx.ManifestPath(2, "a1")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(pass2ManifestPath, contracts.Manifest{
		Kind: contracts.ManifestKindSuccess,
		Value: contracts.ManifestSuccess{
			Kind:          contracts.ManifestKindSuccess,
			SchemaVersion: "1",
			RunID:         runCtx.RunID,
			Pass:          2,
			Agent:         "a1",
			BranchName:    pass2Branch,
			HeadSHA:       strings.Repeat("3", 40),
			BaseSHA:       strings.Repeat("a", 40),
			DiffPath:      pass2DiffPath,
			SessionPath:   "50-pass2/a1/session.jsonl",
			ChecklistPath: "50-pass2/a1/checklist-result.json",
			PromptVersion: "stub",
			StartedAt:     time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
			FinishedAt:    time.Date(2026, 4, 21, 10, 1, 0, 0, time.UTC),
		},
	}))
	require.NoError(t, internalio.WriteAtomic(mustRunPath(t, runCtx, pass2DiffPath), []byte("pass2 diff for a1\n")))
	require.NoError(t, internalio.WriteAtomic(mustRunPath(t, runCtx, "50-pass2/a1/session.jsonl"), []byte("{}\n")))
	require.NoError(t, internalio.WriteAtomic(mustRunPath(t, runCtx, "50-pass2/a1/checklist-result.json"), []byte("{}\n")))

	hashes, completedAgents, err := step70Pass2OutputHashes(runCtx, pkg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pass2 scorable agent missing matching pass1 scorable manifest")
	assert.Nil(t, hashes)
	assert.Nil(t, completedAgents)
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
	runCtx, err := internalio.NewRunContext("2026-04-21-PR430-abcdef0", realTempDir(t), realTempDir(t))
	require.NoError(t, err)
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
	scoresCount, scoresHash, err := step70FinalScoresState(runCtx, artifacts.Scores)
	require.NoError(t, err)
	complianceCount, complianceHash, err := step70FinalComplianceState(runCtx, artifacts.Compliance)
	require.NoError(t, err)
	pairwiseCount, pairwiseHash, err := step70FinalPairwiseState(runCtx, artifacts.Pairwise)
	require.NoError(t, err)

	err = verifyStep60ArtifactSnapshot(runCtx, contracts.Step60DoneMarker{
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
func TestFilesystemResolver_RejectsMissingFinalScoreOverflowSidecar(t *testing.T) {
	runCtx, pkg, candidates := seedFilesystemResolverFixture(t)
	ref, sidecarPath := writeResolverReasonsSidecar(t, runCtx, "overflow sidecar contents\n")
	scorePath, err := runCtx.ResolveRunRelative("60/scores-B.jsonl")
	require.NoError(t, err)
	require.NoError(t, internalio.AppendJSONL(scorePath, contracts.ScoreEntry{
		SchemaVersion:      "1",
		RunID:              runCtx.RunID,
		Pass:               2,
		Agent:              "a1",
		Dimension:          contracts.DimensionFidelity,
		Score:              90,
		Reasons:            "",
		ReasonsOverflowRef: &ref,
		VerdictPath:        contracts.VerdictPathAgreement,
		RubricVersion:      "default",
		PromptVersion:      "phase0-stub",
		ResolvedAt:         time.Date(2026, 4, 21, 10, 3, 0, 0, time.UTC),
	}))
	writeStep60DoneMarkerForResolverFixture(t, runCtx)
	require.NoError(t, os.Remove(sidecarPath))

	resolver := FilesystemResolver{RepoDir: runCtx.RunsBase, Now: fixedNow()}
	_, ok, err := resolver.Resolve(runCtx, pkg, candidates)
	require.Error(t, err)
	assert.False(t, ok)
	assert.Contains(t, err.Error(), "hash step60 scores")
}
func TestFilesystemResolver_RejectsMissingPairwiseOverflowSidecar(t *testing.T) {
	runCtx, pkg, candidates := seedFilesystemResolverFixture(t)
	ref, sidecarPath := writeResolverReasonsSidecar(t, runCtx, "pairwise overflow sidecar contents\n")
	pairwisePath, err := runCtx.ResolveRunRelative("60/pairwise.jsonl")
	require.NoError(t, err)
	require.NoError(t, internalio.AppendJSONL(pairwisePath, contracts.PairwiseEntry{
		SchemaVersion:            "1",
		RunID:                    runCtx.RunID,
		AgentA:                   "a1",
		AgentB:                   "a1",
		Winner:                   contracts.PairwiseWinnerB,
		Margin:                   contracts.PairwiseMarginClear,
		Justification:            "",
		JustificationOverflowRef: &ref,
		VerdictPath:              contracts.VerdictPathAgreement,
		RubricVersion:            "default",
		PromptVersion:            "phase0-stub",
		ResolvedAt:               time.Date(2026, 4, 21, 10, 3, 0, 0, time.UTC),
	}))
	writeStep60DoneMarkerForResolverFixture(t, runCtx)
	require.NoError(t, os.Remove(sidecarPath))

	resolver := FilesystemResolver{RepoDir: runCtx.RunsBase, Now: fixedNow()}
	_, ok, err := resolver.Resolve(runCtx, pkg, candidates)
	require.Error(t, err)
	assert.False(t, ok)
	assert.Contains(t, err.Error(), "hash step60 pairwise")
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
