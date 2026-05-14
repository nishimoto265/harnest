package step70_decide

import (
	"fmt"
	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fixtureResolver struct {
	target Target
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
func seedAdditionalResolverAgent(t *testing.T, runCtx internalio.RunContext, pkg *contracts.TaskPackage, agent contracts.AgentID, headSHA string, pass1Score, pass2Score int) {
	t.Helper()
	pass1Branch := ""
	pass2Branch := ""
	for _, wt := range pkg.Worktrees {
		if wt.Agent != agent {
			continue
		}
		switch wt.Pass {
		case 1:
			pass1Branch = wt.Branch
		case 2:
			pass2Branch = wt.Branch
		}
	}
	require.NotEmpty(t, pass1Branch)
	require.NotEmpty(t, pass2Branch)

	pass1ManifestPath, err := runCtx.ManifestPath(1, agent)
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(pass1ManifestPath, contracts.Manifest{
		Kind: contracts.ManifestKindSuccess,
		Value: contracts.ManifestSuccess{
			Kind:          contracts.ManifestKindSuccess,
			SchemaVersion: "1",
			RunID:         runCtx.RunID,
			Pass:          1,
			Agent:         agent,
			BranchName:    pass1Branch,
			HeadSHA:       strings.Repeat("1", 40),
			BaseSHA:       strings.Repeat("a", 40),
			DiffPath:      filepath.Join("20-pass1", string(agent), "diff.patch"),
			SessionPath:   filepath.Join("20-pass1", string(agent), "session.jsonl"),
			ChecklistPath: filepath.Join("20-pass1", string(agent), "checklist-result.json"),
			PromptVersion: "stub",
			StartedAt:     time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
			FinishedAt:    time.Date(2026, 4, 21, 10, 1, 0, 0, time.UTC),
		},
	}))
	require.NoError(t, internalio.WriteAtomic(mustRunPath(t, runCtx, filepath.Join("20-pass1", string(agent), "diff.patch")), []byte("pass1 diff\n")))
	require.NoError(t, internalio.WriteAtomic(mustRunPath(t, runCtx, filepath.Join("20-pass1", string(agent), "session.jsonl")), []byte("{}\n")))
	require.NoError(t, internalio.WriteAtomic(mustRunPath(t, runCtx, filepath.Join("20-pass1", string(agent), "checklist-result.json")), []byte("{}\n")))

	pass2ManifestPath, err := runCtx.ManifestPath(2, agent)
	require.NoError(t, err)
	pass2DiffPath := filepath.Join("50-pass2", string(agent), "diff.patch")
	require.NoError(t, internalio.WriteJSONAtomic(pass2ManifestPath, contracts.Manifest{
		Kind: contracts.ManifestKindSuccess,
		Value: contracts.ManifestSuccess{
			Kind:          contracts.ManifestKindSuccess,
			SchemaVersion: "1",
			RunID:         runCtx.RunID,
			Pass:          2,
			Agent:         agent,
			BranchName:    pass2Branch,
			HeadSHA:       headSHA,
			BaseSHA:       strings.Repeat("a", 40),
			DiffPath:      pass2DiffPath,
			SessionPath:   filepath.Join("50-pass2", string(agent), "session.jsonl"),
			ChecklistPath: filepath.Join("50-pass2", string(agent), "checklist-result.json"),
			PromptVersion: "stub",
			StartedAt:     time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
			FinishedAt:    time.Date(2026, 4, 21, 10, 1, 0, 0, time.UTC),
		},
	}))
	pass2Diff := []byte("pass2 diff for " + string(agent) + "\n")
	require.NoError(t, internalio.WriteAtomic(mustRunPath(t, runCtx, pass2DiffPath), pass2Diff))
	require.NoError(t, internalio.WriteAtomic(mustRunPath(t, runCtx, filepath.Join("50-pass2", string(agent), "session.jsonl")), []byte("{}\n")))
	require.NoError(t, internalio.WriteAtomic(mustRunPath(t, runCtx, filepath.Join("50-pass2", string(agent), "checklist-result.json")), []byte("{}\n")))

	pairwisePath, err := runCtx.ResolveRunRelative("60/pairwise.jsonl")
	require.NoError(t, err)
	require.NoError(t, internalio.AppendJSONL(pairwisePath, contracts.PairwiseEntry{
		SchemaVersion: "1",
		RunID:         runCtx.RunID,
		AgentA:        agent,
		AgentB:        agent,
		Winner:        contracts.PairwiseWinnerB,
		Margin:        contracts.PairwiseMarginClear,
		Justification: "resolver fixture",
		VerdictPath:   contracts.VerdictPathAgreement,
		RubricVersion: "default",
		PromptVersion: "phase0-stub",
		ResolvedAt:    time.Date(2026, 4, 21, 10, 3, 0, 0, time.UTC),
	}))
	seedResolverAgentScores(t, runCtx, agent, sha256String(string(pass2Diff)), pass1Score, pass2Score)
}
func seedResolverAgentScores(t *testing.T, runCtx internalio.RunContext, agent contracts.AgentID, outputHash string, pass1Score, pass2Score int) {
	t.Helper()
	pass1ScorePath, err := runCtx.ResolveRunRelative("30/scores-A.jsonl")
	require.NoError(t, err)
	pass2ScorePath, err := runCtx.ResolveRunRelative("60/scores-B.jsonl")
	require.NoError(t, err)
	scoreRawPath, err := runCtx.ResolveRunRelative("60/scores-B-raw.jsonl")
	require.NoError(t, err)
	compliancePath, err := runCtx.ResolveRunRelative("60/compliance-B.jsonl")
	require.NoError(t, err)
	complianceRawPath, err := runCtx.ResolveRunRelative("60/compliance-B-raw.jsonl")
	require.NoError(t, err)
	for _, dimension := range resolverScoreDimensions() {
		require.NoError(t, internalio.AppendJSONL(pass1ScorePath, contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         runCtx.RunID,
			Pass:          1,
			Agent:         agent,
			Dimension:     dimension,
			Score:         pass1Score,
			Reasons:       "resolver fixture pass1 override",
			VerdictPath:   contracts.VerdictPathAgreement,
			RubricVersion: "default",
			PromptVersion: "phase0-stub",
			ResolvedAt:    time.Date(2026, 4, 21, 10, 4, 0, 0, time.UTC),
		}))
		require.NoError(t, internalio.AppendJSONL(pass2ScorePath, contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         runCtx.RunID,
			Pass:          2,
			Agent:         agent,
			Dimension:     dimension,
			Score:         pass2Score,
			Reasons:       "resolver fixture pass2 override",
			VerdictPath:   contracts.VerdictPathAgreement,
			RubricVersion: "default",
			PromptVersion: "phase0-stub",
			ResolvedAt:    time.Date(2026, 4, 21, 10, 4, 0, 0, time.UTC),
		}))
		require.NoError(t, internalio.AppendJSONL(scoreRawPath, contracts.RawScoreEntry{
			SchemaVersion: "1",
			RunID:         runCtx.RunID,
			Pass:          2,
			Agent:         agent,
			JudgeRole:     contracts.JudgeRolePrimary,
			Dimension:     dimension,
			Score:         pass2Score,
			Reasons:       "resolver fixture pass2 override",
			OutputSha256:  outputHash,
			RubricVersion: "default",
			PromptVersion: "phase0-stub",
			ResolvedAt:    time.Date(2026, 4, 21, 10, 4, 0, 0, time.UTC),
		}))
	}
	require.NoError(t, internalio.AppendJSONL(compliancePath, contracts.ComplianceEntry{
		SchemaVersion: "1",
		RunID:         runCtx.RunID,
		Pass:          2,
		Agent:         agent,
		RuleID:        "cand-1",
		Verdict:       contracts.ComplianceVerdictCompliant,
		Rationale:     "candidate judged compliant",
		VerdictPath:   contracts.VerdictPathAgreement,
		RubricVersion: "default",
		PromptVersion: "phase0-stub",
		ResolvedAt:    time.Date(2026, 4, 21, 10, 4, 0, 0, time.UTC),
	}))
	require.NoError(t, internalio.AppendJSONL(complianceRawPath, contracts.RawComplianceEntry{
		SchemaVersion: "1",
		RunID:         runCtx.RunID,
		Pass:          2,
		Agent:         agent,
		JudgeRole:     contracts.JudgeRolePrimary,
		RuleID:        "cand-1",
		Verdict:       contracts.ComplianceVerdictCompliant,
		Rationale:     "candidate judged compliant",
		OutputSha256:  outputHash,
		RubricVersion: "default",
		PromptVersion: "phase0-stub",
		ResolvedAt:    time.Date(2026, 4, 21, 10, 4, 0, 0, time.UTC),
	}))
}
func writeStep60DoneMarkerForResolverFixture(t *testing.T, runCtx internalio.RunContext) {
	t.Helper()
	artifacts, err := loadStep60Artifacts(runCtx)
	require.NoError(t, err)
	scoresCount, scoresHash, err := step70FinalScoresState(runCtx, artifacts.Scores)
	require.NoError(t, err)
	complianceCount, complianceHash, err := step70FinalComplianceState(runCtx, artifacts.Compliance)
	require.NoError(t, err)
	pairwiseCount, pairwiseHash, err := step70FinalPairwiseState(runCtx, artifacts.Pairwise)
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
func writeResolverReasonsSidecar(t *testing.T, runCtx internalio.RunContext, content string) (contracts.OverflowRef, string) {
	t.Helper()
	sidecarDir := mustRunPath(t, runCtx, "60/reasons")
	require.NoError(t, os.MkdirAll(sidecarDir, 0o755))
	sum := sha256String(content)
	sidecarPath, err := internalio.WriteSidecar(sidecarDir, sum, content)
	require.NoError(t, err)
	refPath, err := internalio.SidecarRefPath(runCtx.RunDir(), sidecarPath)
	require.NoError(t, err)
	return contracts.OverflowRef{Path: refPath, Sha256: sum}, sidecarPath
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
