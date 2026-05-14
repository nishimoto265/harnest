package step70_decide

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveWinningAgent_CollapsesLatestScoresAndPairwiseRows(t *testing.T) {
	runCtx := newResolverRunContext(t)
	scoresPath := mustResolveResolverPath(t, runCtx, "60/scores-B.jsonl")
	pairwisePath := mustResolveResolverPath(t, runCtx, "60/pairwise.jsonl")

	// Stale rerun rows would make a1 win unless resolver collapses by key.
	require.NoError(t, internalio.AppendJSONL(scoresPath, contracts.ScoreEntry{
		SchemaVersion: "1",
		RunID:         runCtx.RunID,
		Pass:          2,
		Agent:         "a1",
		Dimension:     contracts.DimensionFidelity,
		Score:         100,
		Reasons:       "stale",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}))
	require.NoError(t, internalio.AppendJSONL(scoresPath, contracts.ScoreEntry{
		SchemaVersion: "1",
		RunID:         runCtx.RunID,
		Pass:          2,
		Agent:         "a1",
		Dimension:     contracts.DimensionFidelity,
		Score:         1,
		Reasons:       "fresh",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    time.Date(2026, 4, 21, 10, 5, 0, 0, time.UTC),
	}))
	require.NoError(t, internalio.AppendJSONL(scoresPath, contracts.ScoreEntry{
		SchemaVersion: "1",
		RunID:         runCtx.RunID,
		Pass:          2,
		Agent:         "a2",
		Dimension:     contracts.DimensionFidelity,
		Score:         80,
		Reasons:       "fresh",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    time.Date(2026, 4, 21, 10, 5, 0, 0, time.UTC),
	}))

	require.NoError(t, internalio.AppendJSONL(pairwisePath, contracts.PairwiseEntry{
		SchemaVersion: "1",
		RunID:         runCtx.RunID,
		AgentA:        "a1",
		AgentB:        "a1",
		Winner:        contracts.PairwiseWinnerB,
		Margin:        contracts.PairwiseMarginClear,
		Justification: "stale",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}))
	require.NoError(t, internalio.AppendJSONL(pairwisePath, contracts.PairwiseEntry{
		SchemaVersion: "1",
		RunID:         runCtx.RunID,
		AgentA:        "a1",
		AgentB:        "a1",
		Winner:        contracts.PairwiseWinnerTie,
		Margin:        contracts.PairwiseMarginSlight,
		Justification: "fresh",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    time.Date(2026, 4, 21, 10, 5, 0, 0, time.UTC),
	}))
	require.NoError(t, internalio.AppendJSONL(pairwisePath, contracts.PairwiseEntry{
		SchemaVersion: "1",
		RunID:         runCtx.RunID,
		AgentA:        "a2",
		AgentB:        "a2",
		Winner:        contracts.PairwiseWinnerB,
		Margin:        contracts.PairwiseMarginClear,
		Justification: "fresh",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    time.Date(2026, 4, 21, 10, 5, 0, 0, time.UTC),
	}))

	winningAgent, ok, err := testResolveWinningAgent(t, runCtx)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, contracts.AgentID("a2"), winningAgent)
}

func TestFilesystemResolver_AllDuplicateCandidatesDoesNotRequireStep60Artifacts(t *testing.T) {
	runCtx := newResolverRunContext(t)
	items := []contracts.Candidate{{
		CandidateID:        "cand-1",
		Kind:               contracts.CandidateKindDuplicate,
		TargetRuleID:       "r-existing",
		Title:              "Duplicate existing lesson",
		ProposedBodyPath:   "40/candidates/cand-1.md",
		ProposedBodySha256: strings.Repeat("1", 64),
	}}
	candidates := &contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          runCtx.RunID,
		Candidates:     items,
		CandidatesHash: contracts.CanonicalCandidatesHash(items),
	}
	pkg := &contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runCtx.RunID,
		PR:                      42,
		Title:                   "test",
		BaseSHA:                 strings.Repeat("a", 40),
		BestBranch:              "auto-improve/best",
		ReconstructedTaskPrompt: "test task",
		Worktrees: []contracts.WorktreeAllocation{
			{Agent: "a1", Pass: 1, Path: filepath.Join(runCtx.WorktreeBase, "p1-a1"), Branch: "b1", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("a", 40)},
		},
		CreatedAt: time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}

	target, ok, err := (FilesystemResolver{}).Resolve(runCtx, pkg, candidates)

	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, target)
}

func TestLatestRuleSha256_UsesRollbackAwareEffectiveState(t *testing.T) {
	lines := []registryLine{
		{Entry: contracts.RuleRegistryEntry{
			Kind: contracts.RegistryKindAdded,
			Value: contracts.RuleRegistryAdded{
				Kind:           contracts.RegistryKindAdded,
				SchemaVersion:  "1",
				RuleID:         "rule-a",
				RulePath:       "rules/rule-a.md",
				Sha256:         strings.Repeat("1", 64),
				IdempotencyKey: strings.Repeat("a", 64),
				VersionSeq:     1,
				ByRunID:        "2026-04-21-PR42-abcdef0",
				At:             time.Now().UTC(),
			},
		}},
		{Entry: contracts.RuleRegistryEntry{
			Kind: contracts.RegistryKindUpdated,
			Value: contracts.RuleRegistryUpdated{
				Kind:           contracts.RegistryKindUpdated,
				SchemaVersion:  "1",
				RuleID:         "rule-a",
				RulePath:       "rules/rule-a.md",
				Sha256:         strings.Repeat("2", 64),
				PrevSha256:     strings.Repeat("1", 64),
				IdempotencyKey: strings.Repeat("b", 64),
				VersionSeq:     2,
				PrevHash:       strings.Repeat("f", 64),
				ByRunID:        "2026-04-21-PR42-abcdef0",
				At:             time.Now().UTC(),
			},
		}},
		{Entry: contracts.RuleRegistryEntry{
			Kind: contracts.RegistryKindRolledBack,
			Value: contracts.RuleRegistryRolledBack{
				Kind:           contracts.RegistryKindRolledBack,
				SchemaVersion:  "1",
				TargetOpID:     strings.Repeat("b", 64),
				TargetOffset:   0,
				TargetSha256:   "",
				ByRunID:        "2026-04-21-PR42-abcdef0",
				RollbackReason: contracts.RollbackReasonTransactionalFailure,
				FailedStep:     contracts.FailedStep70,
				VersionSeq:     3,
				PrevHash:       strings.Repeat("e", 64),
				At:             time.Now().UTC(),
			},
		}},
	}
	addedPayload, err := contracts.CanonicalMarshal(lines[0].Entry)
	require.NoError(t, err)
	updatedPayload, err := contracts.CanonicalMarshal(lines[1].Entry)
	require.NoError(t, err)
	sum := sha256.Sum256(updatedPayload)
	rolledBack := lines[2].Entry.Value.(contracts.RuleRegistryRolledBack)
	rolledBack.TargetOffset = int64(len(addedPayload) + 1)
	rolledBack.TargetSha256 = hex.EncodeToString(sum[:])
	lines[2].Entry.Value = rolledBack

	got, err := latestRuleSha256(lines, "rule-a")
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("1", 64), got)
}

func TestLatestRuleSha256_RejectsInvalidExistingRulePath(t *testing.T) {
	lines := []registryLine{
		{Entry: contracts.RuleRegistryEntry{
			Kind: contracts.RegistryKindAdded,
			Value: contracts.RuleRegistryAdded{
				Kind:           contracts.RegistryKindAdded,
				SchemaVersion:  "1",
				RuleID:         "rule-a",
				RulePath:       "../needs-recovery/pwn.md",
				Sha256:         strings.Repeat("1", 64),
				IdempotencyKey: strings.Repeat("a", 64),
				VersionSeq:     1,
				ByRunID:        "2026-04-21-PR42-abcdef0",
				At:             time.Now().UTC(),
			},
		}},
	}

	_, err := latestRuleSha256(lines, "rule-a")
	require.ErrorContains(t, err, "invalid rule_path")
}

func TestMaterializeRuleSidecarRejectsExternalSymlink(t *testing.T) {
	runCtx := newResolverRunContext(t)
	externalPath := filepath.Join(realTempDir(t), "external.md")
	body := "# external\npwned\n"
	require.NoError(t, os.WriteFile(externalPath, []byte(body), 0o644))

	srcPath := mustResolveResolverPath(t, runCtx, "40/candidates/loot.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(srcPath), 0o755))
	require.NoError(t, os.Symlink(externalPath, srcPath))

	err := materializeRuleSidecar(runCtx, contracts.Candidate{
		CandidateID:        "loot",
		Kind:               contracts.CandidateKindNew,
		Title:              "Loot",
		ProposedBodyPath:   "40/candidates/loot.md",
		ProposedBodySha256: sha256String(body),
	}, "rules/r-loot.md")
	require.Error(t, err)
	assert.True(t, errors.Is(err, internalio.ErrUnsafePath))
	assert.NoFileExists(t, mustStagedRulePath(t, runCtx, "rules/r-loot.md"))
}

func TestGeneratedRuleID_IsValidAndDeterministic(t *testing.T) {
	id1 := generatedRuleID("cand-2026-04-23-PR1-a0f77d2-001")
	id2 := generatedRuleID("cand-2026-04-23-PR1-a0f77d2-001")
	require.Equal(t, id1, id2)
	require.NoError(t, contracts.ValidateRuleID(id1))
	assert.True(t, strings.HasPrefix(id1, "r-"))
}

func TestPromotionGatePassed_DoesNotHardGateCandidateComplianceEvidence(t *testing.T) {
	runCtx := newResolverRunContext(t)
	candidates := resolverGateCandidates(runCtx.RunID)
	seedResolverGateScores(t, runCtx, 80, map[contracts.Dimension]int{})
	seedResolverGateScoresPass2(t, runCtx, 90, map[contracts.Dimension]int{})

	ok, err := testPromotionGatePassed(t, runCtx, "a1", candidates)
	require.NoError(t, err)
	assert.True(t, ok)

	seedResolverGateCompliance(t, runCtx, "cand-1", contracts.ComplianceVerdictViolated)
	ok, err = testPromotionGatePassed(t, runCtx, "a1", candidates)
	require.NoError(t, err)
	assert.True(t, ok)

	seedResolverGateCompliance(t, runCtx, "cand-1", contracts.ComplianceVerdictCompliant)
	ok, err = testPromotionGatePassed(t, runCtx, "a1", candidates)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestPromotionGatePassed_AllowsTinyDeltaWhenPairwiseAlreadyWon(t *testing.T) {
	runCtx := newResolverRunContext(t)
	candidates := resolverGateCandidates(runCtx.RunID)
	seedResolverGateScores(t, runCtx, 80, map[contracts.Dimension]int{})
	seedResolverGateScoresPass2(t, runCtx, 82, map[contracts.Dimension]int{})
	seedResolverGateCompliance(t, runCtx, "cand-1", contracts.ComplianceVerdictCompliant)

	ok, err := testPromotionGatePassed(t, runCtx, "a1", candidates)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestPromotionGatePassed_AllowsCriticalScoreRegressionWhenPairwiseAlreadyWon(t *testing.T) {
	runCtx := newResolverRunContext(t)
	candidates := resolverGateCandidates(runCtx.RunID)
	seedResolverGateScores(t, runCtx, 80, map[contracts.Dimension]int{
		contracts.DimensionCorrectness: 95,
	})
	seedResolverGateScoresPass2(t, runCtx, 92, map[contracts.Dimension]int{
		contracts.DimensionCorrectness: 94,
	})
	seedResolverGateCompliance(t, runCtx, "cand-1", contracts.ComplianceVerdictCompliant)

	ok, err := testPromotionGatePassed(t, runCtx, "a1", candidates)
	require.NoError(t, err)
	assert.True(t, ok)
}

func testResolveWinningAgent(t *testing.T, runCtx internalio.RunContext) (contracts.AgentID, bool, error) {
	t.Helper()
	artifacts, err := loadStep60Artifacts(runCtx)
	if err != nil {
		return "", false, err
	}
	return resolveWinningAgentFromArtifacts(artifacts)
}

func testPromotionGatePassed(t *testing.T, runCtx internalio.RunContext, agent contracts.AgentID, candidates *contracts.Candidates) (bool, error) {
	t.Helper()
	artifacts, err := loadStep60Artifacts(runCtx)
	if err != nil {
		return false, err
	}
	return promotionGatePassedWithArtifacts(runCtx, artifacts, agent, candidates)
}

func newResolverRunContext(t *testing.T) internalio.RunContext {
	t.Helper()
	runsBase := filepath.Join(realTempDir(t), "runs")
	worktreeBase := filepath.Join(realTempDir(t), "worktrees")
	runCtx, err := internalio.NewRunContext("2026-04-21-PR42-abcdef0", runsBase, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunDir(), 0o755))
	return runCtx
}

func mustResolveResolverPath(t *testing.T, runCtx internalio.RunContext, rel string) string {
	t.Helper()
	path, err := runCtx.ResolveRunRelative(rel)
	require.NoError(t, err)
	return path
}

func resolverGateCandidates(runID contracts.RunID) *contracts.Candidates {
	candidate := contracts.Candidate{
		CandidateID:        "cand-1",
		Kind:               contracts.CandidateKindNew,
		Title:              "Candidate",
		ProposedBodyPath:   "40/candidates/cand-1.md",
		ProposedBodySha256: strings.Repeat("a", 64),
	}
	return &contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          runID,
		Candidates:     []contracts.Candidate{candidate},
		CandidatesHash: contracts.CanonicalCandidatesHash([]contracts.Candidate{candidate}),
		CreatedAt:      time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}
}

func seedResolverGateScores(t *testing.T, runCtx internalio.RunContext, score int, overrides map[contracts.Dimension]int) {
	t.Helper()
	seedResolverGateScoreRows(t, runCtx, "30/scores-A.jsonl", 1, score, overrides)
}

func seedResolverGateScoresPass2(t *testing.T, runCtx internalio.RunContext, score int, overrides map[contracts.Dimension]int) {
	t.Helper()
	seedResolverGateScoreRows(t, runCtx, "60/scores-B.jsonl", 2, score, overrides)
}

func seedResolverGateScoreRows(t *testing.T, runCtx internalio.RunContext, rel string, pass int, score int, overrides map[contracts.Dimension]int) {
	t.Helper()
	path := mustResolveResolverPath(t, runCtx, rel)
	for _, dimension := range []contracts.Dimension{
		contracts.DimensionFidelity,
		contracts.DimensionCorrectness,
		contracts.DimensionMaintainability,
		contracts.DimensionDiscipline,
		contracts.DimensionCommunication,
	} {
		rowScore := score
		if override, ok := overrides[dimension]; ok {
			rowScore = override
		}
		require.NoError(t, internalio.AppendJSONL(path, contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         runCtx.RunID,
			Pass:          pass,
			Agent:         "a1",
			Dimension:     dimension,
			Score:         rowScore,
			Reasons:       "resolver gate fixture",
			VerdictPath:   contracts.VerdictPathAgreement,
			RubricVersion: "default",
			PromptVersion: "phase0-stub",
			ResolvedAt:    time.Date(2026, 4, 21, 10, 2, 0, 0, time.UTC),
		}))
	}
}

func seedResolverGateCompliance(t *testing.T, runCtx internalio.RunContext, ruleID string, verdict contracts.ComplianceVerdict) {
	t.Helper()
	path := mustResolveResolverPath(t, runCtx, "60/compliance-B.jsonl")
	require.NoError(t, internalio.AppendJSONL(path, contracts.ComplianceEntry{
		SchemaVersion: "1",
		RunID:         runCtx.RunID,
		Pass:          2,
		Agent:         "a1",
		RuleID:        ruleID,
		Verdict:       verdict,
		Rationale:     "resolver gate fixture",
		VerdictPath:   contracts.VerdictPathAgreement,
		RubricVersion: "default",
		PromptVersion: "phase0-stub",
		ResolvedAt:    time.Date(2026, 4, 21, 10, 2, 0, 0, time.UTC),
	}))
}
