package step40_classify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

func TestRun_EmptyInputsProduceZeroCandidates(t *testing.T) {
	cfg := newTestConfig(t)

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Empty(t, got.Candidates)
	assert.Equal(t, contracts.CanonicalCandidatesHash(nil), got.CandidatesHash)
	assert.NoError(t, got.VerifyCandidatesHash())

	stored := readCandidatesFile(t, cfg.IO)
	assert.Empty(t, stored.Candidates)
	assert.Equal(t, got.CandidatesHash, stored.CandidatesHash)

	classifications := readClassificationFile(t, cfg.IO)
	assert.Empty(t, classifications)

	classificationPath, err := cfg.IO.ResolveRunRelative(classificationJSONLPath)
	require.NoError(t, err)
	data, err := os.ReadFile(classificationPath)
	require.NoError(t, err)
	assert.Len(t, data, 0)
}

func TestRun_ComplianceViolationsOnlyProduceNewCandidates(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	writeCompliance(t, cfg.IO,
		testComplianceEntry(cfg.IO.RunID, "rule-b", contracts.ComplianceVerdictMissed),
		testComplianceEntry(cfg.IO.RunID, "rule-a", contracts.ComplianceVerdictViolated),
		testComplianceEntry(cfg.IO.RunID, "rule-b", contracts.ComplianceVerdictInvalidException),
		testComplianceEntry(cfg.IO.RunID, "rule-ignored", contracts.ComplianceVerdictCompliant),
	)

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, got.Candidates, 2)

	assert.Equal(t, "cand-2026-04-21-PR42-abcdef0-001", got.Candidates[0].CandidateID)
	assert.Equal(t, "rule-a", strings.TrimPrefix(got.Candidates[0].Title, "Rule candidate for "))
	assert.Equal(t, contracts.CandidateKindNew, got.Candidates[0].Kind)
	assert.Empty(t, got.Candidates[0].TargetRuleID)

	assert.Equal(t, "cand-2026-04-21-PR42-abcdef0-002", got.Candidates[1].CandidateID)
	assert.Equal(t, "rule-b", strings.TrimPrefix(got.Candidates[1].Title, "Rule candidate for "))
	assert.Equal(t, contracts.CandidateKindNew, got.Candidates[1].Kind)
	assert.Empty(t, got.Candidates[1].TargetRuleID)

	assertCandidateBodies(t, cfg.IO, got.Candidates)

	stored := readCandidatesFile(t, cfg.IO)
	assert.Equal(t, got.CandidatesHash, stored.CandidatesHash)
	assert.NoError(t, stored.VerifyCandidatesHash())

	classifications := readClassificationFile(t, cfg.IO)
	require.Len(t, classifications, 2)
	for _, entry := range classifications {
		assert.Equal(t, contracts.CandidateKindNew, entry.Kind)
		assert.Zero(t, entry.SimilarityScore)
		assert.Empty(t, entry.MatchedRuleID)
	}
}

func TestRun_RegistryMatchesProduceMixedNewAndUpdateCandidates(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	writeCompliance(t, cfg.IO,
		testComplianceEntry(cfg.IO.RunID, "rule-active", contracts.ComplianceVerdictViolated),
		testComplianceEntry(cfg.IO.RunID, "rule-archived", contracts.ComplianceVerdictMissed),
		testComplianceEntry(cfg.IO.RunID, "rule-restored", contracts.ComplianceVerdictInvalidException),
		testComplianceEntry(cfg.IO.RunID, "rule-fresh", contracts.ComplianceVerdictViolated),
	)
	writeRegistry(t, cfg.registryPath(),
		registryAdded("rule-active", "1111111111111111111111111111111111111111111111111111111111111111"),
		registryAdded("rule-archived", "2222222222222222222222222222222222222222222222222222222222222222"),
		registryArchived("rule-archived", "3333333333333333333333333333333333333333333333333333333333333333"),
		registryRestored("rule-restored", "4444444444444444444444444444444444444444444444444444444444444444"),
	)

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, got.Candidates, 4)

	kinds := map[string]contracts.CandidateKind{}
	targets := map[string]string{}
	for _, candidate := range got.Candidates {
		ruleID := strings.TrimPrefix(candidate.Title, "Rule candidate for ")
		kinds[ruleID] = candidate.Kind
		targets[ruleID] = candidate.TargetRuleID
	}

	assert.Equal(t, contracts.CandidateKindUpdate, kinds["rule-active"])
	assert.Equal(t, "rule-active", targets["rule-active"])
	assert.Equal(t, contracts.CandidateKindNew, kinds["rule-archived"])
	assert.Empty(t, targets["rule-archived"])
	assert.Equal(t, contracts.CandidateKindUpdate, kinds["rule-restored"])
	assert.Equal(t, "rule-restored", targets["rule-restored"])
	assert.Equal(t, contracts.CandidateKindNew, kinds["rule-fresh"])

	classifications := readClassificationFile(t, cfg.IO)
	require.Len(t, classifications, 4)
	for _, entry := range classifications {
		ruleID := strings.TrimPrefix(got.Candidates[indexOfCandidate(t, got.Candidates, entry.CandidateID)].Title, "Rule candidate for ")
		switch ruleID {
		case "rule-active", "rule-restored":
			assert.Equal(t, 90, entry.SimilarityScore)
			assert.Equal(t, ruleID, entry.MatchedRuleID)
			assert.Equal(t, contracts.CandidateKindUpdate, entry.Kind)
		default:
			assert.Zero(t, entry.SimilarityScore)
			assert.Empty(t, entry.MatchedRuleID)
			assert.Equal(t, contracts.CandidateKindNew, entry.Kind)
		}
	}
}

func TestRun_RerunTruncatesClassificationJSONL(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	writeCompliance(t, cfg.IO,
		testComplianceEntry(cfg.IO.RunID, "rule-a", contracts.ComplianceVerdictViolated),
		testComplianceEntry(cfg.IO.RunID, "rule-b", contracts.ComplianceVerdictMissed),
	)

	_, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, readClassificationFile(t, cfg.IO), 2)

	writeCompliance(t, cfg.IO, testComplianceEntry(cfg.IO.RunID, "rule-a", contracts.ComplianceVerdictViolated))

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, got.Candidates, 1)

	classifications := readClassificationFile(t, cfg.IO)
	require.Len(t, classifications, 1)
	assert.Equal(t, got.Candidates[0].CandidateID, classifications[0].CandidateID)
}

func newTestConfig(t *testing.T) Config {
	t.Helper()

	baseDir := t.TempDir()
	runsBase := filepath.Join(baseDir, "runs")
	worktreeBase := filepath.Join(baseDir, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))

	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	runIO, err := internalio.NewRunContext(runID, runsBase, worktreeBase)
	require.NoError(t, err)

	return Config{
		IO:           runIO,
		RegistryPath: runIO.RulesRegistryPath(),
		TaskPackage:  validTaskPackage(t, runIO),
		Now: func() time.Time {
			return time.Date(2026, 4, 21, 12, 34, 56, 0, time.UTC)
		},
	}
}

func validTaskPackage(t *testing.T, runIO internalio.RunContext) *contracts.TaskPackage {
	t.Helper()

	baseSHA := strings.Repeat("a", 40)
	worktrees := make([]contracts.WorktreeAllocation, 0, 6)
	for pass := 1; pass <= 2; pass++ {
		for agentIndex, agent := range []contracts.AgentID{"a1", "a2", "a3"} {
			path := filepath.Join(runIO.WorktreeBase, string(runIO.RunID), fmt.Sprintf("pass%d-%s", pass, agent))
			require.NoError(t, os.MkdirAll(path, 0o755))
			worktrees = append(worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    path,
				Branch:  fmt.Sprintf("auto-improve/%s/pass%d/%d", runIO.RunID, pass, agentIndex+1),
				BaseSHA: baseSHA,
				HeadSHA: baseSHA,
			})
		}
	}

	pkg := &contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runIO.RunID,
		PR:                      42,
		Title:                   "PR #42",
		BaseSHA:                 baseSHA,
		BestBranch:              "main",
		ReconstructedTaskPrompt: "stub prompt",
		Worktrees:               worktrees,
		CreatedAt:               time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}
	require.NoError(t, pkg.Validate())
	return pkg
}

func writeScores(t *testing.T, runIO internalio.RunContext, entries ...contracts.ScoreEntry) {
	t.Helper()
	path, err := runIO.ResolveRunRelative(scoresPath)
	require.NoError(t, err)
	writeJSONL(t, path, entries...)
}

func writeCompliance(t *testing.T, runIO internalio.RunContext, entries ...contracts.ComplianceEntry) {
	t.Helper()
	path, err := runIO.ResolveRunRelative(compliancePath)
	require.NoError(t, err)
	writeJSONL(t, path, entries...)
}

func writeRegistry(t *testing.T, path string, entries ...contracts.RuleRegistryEntry) {
	t.Helper()
	writeJSONL(t, path, entries...)
}

func writeJSONL[T any](t *testing.T, path string, entries ...T) {
	t.Helper()
	require.NoError(t, internalio.WriteAtomic(path, nil))
	for _, entry := range entries {
		require.NoError(t, internalio.AppendJSONL(path, entry))
	}
}

func readCandidatesFile(t *testing.T, runIO internalio.RunContext) contracts.Candidates {
	t.Helper()
	path, err := runIO.ResolveRunRelative(candidatesJSONPath)
	require.NoError(t, err)
	got, err := internalio.ReadJSON[contracts.Candidates](path)
	require.NoError(t, err)
	return got
}

func readClassificationFile(t *testing.T, runIO internalio.RunContext) []contracts.ClassificationEntry {
	t.Helper()
	path, err := runIO.ResolveRunRelative(classificationJSONLPath)
	require.NoError(t, err)
	got, err := internalio.ReadJSONL[contracts.ClassificationEntry](path)
	require.NoError(t, err)
	return got
}

func assertCandidateBodies(t *testing.T, runIO internalio.RunContext, candidates []contracts.Candidate) {
	t.Helper()
	for _, candidate := range candidates {
		path, err := runIO.ResolveRunRelative(candidate.ProposedBodyPath)
		require.NoError(t, err)
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, candidate.ProposedBodySha256, sha256String(string(data)))
	}
}

func sha256String(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func testScoreEntries(runID contracts.RunID) []contracts.ScoreEntry {
	now := time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC)
	return []contracts.ScoreEntry{
		{
			SchemaVersion: "1",
			RunID:         runID,
			Pass:          1,
			Agent:         "a1",
			Dimension:     contracts.DimensionFidelity,
			Score:         80,
			Reasons:       "stub score",
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "default",
			PromptVersion: "phase0",
			ResolvedAt:    now,
		},
	}
}

func testComplianceEntry(runID contracts.RunID, ruleID string, verdict contracts.ComplianceVerdict) contracts.ComplianceEntry {
	return contracts.ComplianceEntry{
		SchemaVersion: "1",
		RunID:         runID,
		Pass:          1,
		Agent:         "a1",
		RuleID:        ruleID,
		Verdict:       verdict,
		Rationale:     "stub compliance",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "default",
		PromptVersion: "phase0",
		ResolvedAt:    time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC),
	}
}

func registryAdded(ruleID, idempotencyKey string) contracts.RuleRegistryEntry {
	return contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         ruleID,
			RulePath:       filepath.Join("rules", ruleID+".md"),
			Sha256:         strings.Repeat("a", 64),
			IdempotencyKey: idempotencyKey,
			VersionSeq:     1,
			ByRunID:        "2026-04-20-PR1-aaaaaaa",
			At:             time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC),
		},
	}
}

func registryArchived(ruleID, opID string) contracts.RuleRegistryEntry {
	return contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindArchived,
		Value: contracts.RuleRegistryArchived{
			Kind:          contracts.RegistryKindArchived,
			SchemaVersion: "1",
			RuleID:        ruleID,
			PrevStatus:    contracts.RuleStatusActive,
			NewStatus:     contracts.RuleStatusArchived,
			OpID:          opID,
			VersionSeq:    1,
			BySunsetRunID: "sunset-1",
			At:            time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC),
		},
	}
}

func registryRestored(ruleID, opID string) contracts.RuleRegistryEntry {
	return contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindRestored,
		Value: contracts.RuleRegistryRestored{
			Kind:          contracts.RegistryKindRestored,
			SchemaVersion: "1",
			RuleID:        ruleID,
			PrevStatus:    contracts.RuleStatusArchived,
			NewStatus:     contracts.RuleStatusActive,
			OpID:          opID,
			VersionSeq:    1,
			BySunsetRunID: "sunset-2",
			At:            time.Date(2026, 4, 20, 11, 0, 0, 0, time.UTC),
		},
	}
}

func indexOfCandidate(t *testing.T, candidates []contracts.Candidate, candidateID string) int {
	t.Helper()
	for i, candidate := range candidates {
		if candidate.CandidateID == candidateID {
			return i
		}
	}
	t.Fatalf("candidate not found: %s", candidateID)
	return -1
}
