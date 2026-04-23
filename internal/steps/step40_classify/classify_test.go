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
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
)

func TestRun_EmptyInputsFailClosed(t *testing.T) {
	cfg := newTestConfig(t)

	got, err := Run(context.Background(), cfg)
	require.ErrorContains(t, err, "step30 done.marker is missing or invalid")
	assert.Nil(t, got)
}

func TestRun_ValidEmptyComplianceArtifactEmitsZeroCandidates(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	writeCompliance(t, cfg.IO)

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Empty(t, got.Candidates)
}

func TestRun_ScaffoldOnlyEvidenceEmitsZeroCandidates(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, contracts.ScoreEntry{
		SchemaVersion: "1",
		RunID:         cfg.IO.RunID,
		Pass:          1,
		Agent:         "a1",
		Dimension:     contracts.DimensionFidelity,
		Score:         80,
		Reasons:       "stub score",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "default",
		PromptVersion: "phase0",
		ResolvedAt:    time.Now().UTC(),
	})
	writeCompliance(t, cfg.IO, contracts.ComplianceEntry{
		SchemaVersion: "1",
		RunID:         cfg.IO.RunID,
		Pass:          1,
		Agent:         "a1",
		RuleID:        "rule-a",
		Verdict:       contracts.ComplianceVerdictViolated,
		Rationale:     "stub compliance",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "default",
		PromptVersion: "phase0",
		ResolvedAt:    time.Now().UTC(),
	})

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	assert.Empty(t, got.Candidates)
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
		registryAdded("rule-restored", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab"),
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

func TestRun_RerunKeepsCandidatesHashStable(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	writeCompliance(t, cfg.IO,
		testComplianceEntry(cfg.IO.RunID, "rule-updated", contracts.ComplianceVerdictViolated),
		testComplianceEntry(cfg.IO.RunID, "rule-added", contracts.ComplianceVerdictMissed),
	)
	writeRegistry(t, cfg.registryPath(),
		registryAdded("rule-updated", strings.Repeat("4", 64)),
		registryUpdated("rule-updated", strings.Repeat("5", 64)),
		registryAdded("rule-added", strings.Repeat("6", 64)),
	)

	first, err := Run(context.Background(), cfg)
	require.NoError(t, err)

	second, err := Run(context.Background(), cfg)
	require.NoError(t, err)

	assert.Equal(t, first.CandidatesHash, second.CandidatesHash)
	assert.NoError(t, second.VerifyCandidatesHash())
}

func TestRun_IgnoresSupersededComplianceViolations(t *testing.T) {
	cfg := newTestConfig(t)
	now := time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC)
	writeScores(t, cfg.IO, contracts.ScoreEntry{
		SchemaVersion: "1",
		RunID:         cfg.IO.RunID,
		Pass:          1,
		Agent:         "a1",
		Dimension:     contracts.DimensionFidelity,
		Score:         80,
		Reasons:       "Latest scoring still preserved a useful rationale.",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "default",
		PromptVersion: "phase0",
		ResolvedAt:    now,
	})
	writeCompliance(t, cfg.IO,
		contracts.ComplianceEntry{
			SchemaVersion: "1",
			RunID:         cfg.IO.RunID,
			Pass:          1,
			Agent:         "a1",
			RuleID:        "rule-a",
			Verdict:       contracts.ComplianceVerdictViolated,
			Rationale:     "Historical violation that should be ignored after the fix.",
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "default",
			PromptVersion: "phase0",
			ResolvedAt:    now,
		},
		contracts.ComplianceEntry{
			SchemaVersion: "1",
			RunID:         cfg.IO.RunID,
			Pass:          1,
			Agent:         "a1",
			RuleID:        "rule-a",
			Verdict:       contracts.ComplianceVerdictCompliant,
			Rationale:     "Latest verdict is compliant.",
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "default",
			PromptVersion: "phase0",
			ResolvedAt:    now.Add(time.Minute),
		},
	)

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	assert.Empty(t, got.Candidates)
}

func TestRun_CandidateEvidenceStaysWithinViolatingAgents(t *testing.T) {
	cfg := newTestConfig(t)
	now := time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC)
	writeScores(t, cfg.IO,
		contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         cfg.IO.RunID,
			Pass:          1,
			Agent:         "a1",
			Dimension:     contracts.DimensionFidelity,
			Score:         81,
			Reasons:       "Rule A scoring evidence should stay attached to agent a1 only.",
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "default",
			PromptVersion: "phase0",
			ResolvedAt:    now,
		},
		contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         cfg.IO.RunID,
			Pass:          1,
			Agent:         "a2",
			Dimension:     contracts.DimensionCorrectness,
			Score:         67,
			Reasons:       "Rule B scoring evidence must not leak into rule A candidates.",
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "default",
			PromptVersion: "phase0",
			ResolvedAt:    now,
		},
	)
	writeCompliance(t, cfg.IO,
		contracts.ComplianceEntry{
			SchemaVersion: "1",
			RunID:         cfg.IO.RunID,
			Pass:          1,
			Agent:         "a1",
			RuleID:        "rule-a",
			Verdict:       contracts.ComplianceVerdictViolated,
			Rationale:     "Rule A rationale from agent a1.",
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "default",
			PromptVersion: "phase0",
			ResolvedAt:    now,
		},
		contracts.ComplianceEntry{
			SchemaVersion: "1",
			RunID:         cfg.IO.RunID,
			Pass:          1,
			Agent:         "a2",
			RuleID:        "rule-b",
			Verdict:       contracts.ComplianceVerdictViolated,
			Rationale:     "Rule B rationale from agent a2.",
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "default",
			PromptVersion: "phase0",
			ResolvedAt:    now,
		},
	)

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, got.Candidates, 2)

	ruleABodyPath, err := cfg.IO.ResolveRunRelative(got.Candidates[indexOfCandidate(t, got.Candidates, "cand-2026-04-21-PR42-abcdef0-001")].ProposedBodyPath)
	require.NoError(t, err)
	ruleABody, err := os.ReadFile(ruleABodyPath)
	require.NoError(t, err)
	assert.Contains(t, string(ruleABody), "a1/fidelity: Rule A scoring evidence should stay attached to agent a1 only.")
	assert.NotContains(t, string(ruleABody), "a2/correctness: Rule B scoring evidence must not leak into rule A candidates.")
}

func TestBuildCandidates_CollectsScoreEvidenceFromViolatingAgentsBeyondComplianceSnippetCap(t *testing.T) {
	cfg := newTestConfig(t)
	now := time.Date(2026, 4, 21, 11, 15, 0, 0, time.UTC)
	scores := []contracts.ScoreEntry{
		{SchemaVersion: "1", RunID: cfg.IO.RunID, Pass: 1, Agent: "a1", Dimension: contracts.DimensionFidelity, Score: 70, Reasons: "placeholder", VerdictPath: contracts.VerdictPathSingle, RubricVersion: "default", PromptVersion: "phase0", ResolvedAt: now},
		{SchemaVersion: "1", RunID: cfg.IO.RunID, Pass: 1, Agent: "a2", Dimension: contracts.DimensionFidelity, Score: 69, Reasons: "placeholder", VerdictPath: contracts.VerdictPathSingle, RubricVersion: "default", PromptVersion: "phase0", ResolvedAt: now},
		{SchemaVersion: "1", RunID: cfg.IO.RunID, Pass: 1, Agent: "a3", Dimension: contracts.DimensionFidelity, Score: 68, Reasons: "placeholder", VerdictPath: contracts.VerdictPathSingle, RubricVersion: "default", PromptVersion: "phase0", ResolvedAt: now},
		{SchemaVersion: "1", RunID: cfg.IO.RunID, Pass: 1, Agent: "a4", Dimension: contracts.DimensionFidelity, Score: 67, Reasons: "Fourth violating agent exposed the only useful score evidence for this rule.", VerdictPath: contracts.VerdictPathSingle, RubricVersion: "default", PromptVersion: "phase0", ResolvedAt: now},
	}
	compliance := []contracts.ComplianceEntry{
		{SchemaVersion: "1", RunID: cfg.IO.RunID, Pass: 1, Agent: "a1", RuleID: "rule-cap", Verdict: contracts.ComplianceVerdictViolated, Rationale: "First compliance rationale remains substantive.", VerdictPath: contracts.VerdictPathSingle, RubricVersion: "default", PromptVersion: "phase0", ResolvedAt: now},
		{SchemaVersion: "1", RunID: cfg.IO.RunID, Pass: 1, Agent: "a2", RuleID: "rule-cap", Verdict: contracts.ComplianceVerdictViolated, Rationale: "Second compliance rationale remains substantive.", VerdictPath: contracts.VerdictPathSingle, RubricVersion: "default", PromptVersion: "phase0", ResolvedAt: now},
		{SchemaVersion: "1", RunID: cfg.IO.RunID, Pass: 1, Agent: "a3", RuleID: "rule-cap", Verdict: contracts.ComplianceVerdictViolated, Rationale: "Third compliance rationale remains substantive.", VerdictPath: contracts.VerdictPathSingle, RubricVersion: "default", PromptVersion: "phase0", ResolvedAt: now},
		{SchemaVersion: "1", RunID: cfg.IO.RunID, Pass: 1, Agent: "a4", RuleID: "rule-cap", Verdict: contracts.ComplianceVerdictViolated, Rationale: "placeholder", VerdictPath: contracts.VerdictPathSingle, RubricVersion: "default", PromptVersion: "phase0", ResolvedAt: now},
	}

	built, err := buildCandidates(cfg.IO, now, scores, compliance, nil, filepath.Dir(cfg.registryPath()))
	require.NoError(t, err)
	require.Len(t, built, 1)
	assert.Contains(t, built[0].Body, "a4/fidelity: Fourth violating agent exposed the only useful score evidence for this rule.")
}

func TestRun_UsesOverflowSidecarWhenInlineRationaleIsEmpty(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	ref, err := scorecore.WriteOverflowSidecar(cfg.IO, "30", "Sidecar rationale proves the rule was skipped on the guarded path.")
	require.NoError(t, err)

	entry := testComplianceEntry(cfg.IO.RunID, "rule-sidecar", contracts.ComplianceVerdictViolated)
	entry.Rationale = ""
	entry.RationaleOverflowRef = &ref
	writeCompliance(t, cfg.IO, entry)

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, got.Candidates, 1)

	bodyPath, err := cfg.IO.ResolveRunRelative(got.Candidates[0].ProposedBodyPath)
	require.NoError(t, err)
	body, err := os.ReadFile(bodyPath)
	require.NoError(t, err)
	assert.Contains(t, string(body), "Sidecar rationale proves the rule was skipped on the guarded path.")
}

func TestRun_UsesOverflowSidecarWhenInlineRationaleIsNonSubstantive(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	ref, err := scorecore.WriteOverflowSidecar(cfg.IO, "30", "Sidecar rationale remains usable even when inline text carries no real evidence.")
	require.NoError(t, err)

	entry := testComplianceEntry(cfg.IO.RunID, "rule-sidecar-placeholder", contracts.ComplianceVerdictViolated)
	entry.Rationale = "placeholder"
	entry.RationaleOverflowRef = &ref
	writeCompliance(t, cfg.IO, entry)

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, got.Candidates, 1)

	bodyPath, err := cfg.IO.ResolveRunRelative(got.Candidates[0].ProposedBodyPath)
	require.NoError(t, err)
	body, err := os.ReadFile(bodyPath)
	require.NoError(t, err)
	assert.Contains(t, string(body), "Sidecar rationale remains usable even when inline text carries no real evidence.")
	assert.NotContains(t, string(body), "- compliance: placeholder")
}

func TestRun_RejectsTamperedActiveRuleSidecar(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	writeCompliance(t, cfg.IO, testComplianceEntry(cfg.IO.RunID, "rule-tampered", contracts.ComplianceVerdictViolated))

	ruleBody := "# canonical body\n"
	rulePath := filepath.Join(cfg.IO.RunsBase, "rules", "rule-tampered.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(rulePath), 0o755))
	require.NoError(t, os.WriteFile(rulePath, []byte("# tampered body\n"), 0o644))
	writeRegistry(t, cfg.registryPath(), contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         "rule-tampered",
			RulePath:       filepath.Join("rules", "rule-tampered.md"),
			Sha256:         sha256String(ruleBody),
			IdempotencyKey: strings.Repeat("a", 64),
			VersionSeq:     1,
			ByRunID:        "2026-04-20-PR1-aaaaaaa",
			At:             time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC),
		},
	})

	_, err := Run(context.Background(), cfg)
	require.ErrorContains(t, err, "rule sidecar sha mismatch")
}

func TestRun_RegistryVariantsProduceExpectedCandidateKinds(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	writeCompliance(t, cfg.IO,
		testComplianceEntry(cfg.IO.RunID, "rule-updated", contracts.ComplianceVerdictViolated),
		testComplianceEntry(cfg.IO.RunID, "rule-status-active", contracts.ComplianceVerdictMissed),
		testComplianceEntry(cfg.IO.RunID, "rule-rolled-back", contracts.ComplianceVerdictInvalidException),
	)
	updatedAdded := registryAdded("rule-updated", strings.Repeat("7", 64))
	updated := registryUpdated("rule-updated", strings.Repeat("7", 64))
	statusAdded := registryAdded("rule-status-active", strings.Repeat("6", 64))
	statusDeprecated := registryStatusChanged(
		"rule-status-active",
		contracts.RuleStatusActive,
		contracts.RuleStatusDeprecated,
		contracts.SunsetTransitionDeprecate,
		strings.Repeat("8", 64),
	)
	rolledBack := registryAdded("rule-rolled-back", strings.Repeat("9", 64))
	writeRegistry(t, cfg.registryPath(),
		updatedAdded,
		updated,
		statusAdded,
		statusDeprecated,
		rolledBack,
		registryRolledBackForEntries(t, cfg.registryPath(), []contracts.RuleRegistryEntry{updatedAdded, updated, statusAdded, statusDeprecated, rolledBack}, strings.Repeat("9", 64)),
	)

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, got.Candidates, 3)

	kinds := map[string]contracts.CandidateKind{}
	targets := map[string]string{}
	for _, candidate := range got.Candidates {
		ruleID := strings.TrimPrefix(candidate.Title, "Rule candidate for ")
		kinds[ruleID] = candidate.Kind
		targets[ruleID] = candidate.TargetRuleID
	}

	assert.Equal(t, contracts.CandidateKindUpdate, kinds["rule-updated"])
	assert.Equal(t, "rule-updated", targets["rule-updated"])
	assert.Equal(t, contracts.CandidateKindUpdate, kinds["rule-status-active"])
	assert.Equal(t, "rule-status-active", targets["rule-status-active"])
	assert.Equal(t, contracts.CandidateKindNew, kinds["rule-rolled-back"])
	assert.Empty(t, targets["rule-rolled-back"])
}

func TestRun_ClassifiesDuplicateWhenExistingRuleBodyMatchesCandidate(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	writeCompliance(t, cfg.IO, testComplianceEntry(cfg.IO.RunID, "rule-dup", contracts.ComplianceVerdictViolated))

	rulesDir := filepath.Join(filepath.Dir(cfg.registryPath()), "rules")
	require.NoError(t, os.MkdirAll(rulesDir, 0o755))
	body := candidateBodyMarkdownWithEvidence(contracts.Candidate{
		CandidateID:      "cand-existing",
		Kind:             contracts.CandidateKindNew,
		Title:            "Rule candidate for rule-dup",
		Problem:          "Pass1 recorded 1 violation(s) for rule rule-dup.",
		Rationale:        "Derived from 1 compliance violation rationale(s) and 1 score reason(s) for rule-dup.",
		ProposedBodyPath: "40/candidates/cand-existing.md",
	}, candidateEvidence{
		Compliance: []string{"Rule rule-dup was skipped when the implementation touched the guarded path."},
		Scores:     []string{"a1/fidelity: Missing the guard lets regressions slip into the changed code path."},
	})
	require.NoError(t, os.WriteFile(filepath.Join(rulesDir, "rule-existing.md"), []byte(body), 0o644))
	writeRegistry(t, cfg.registryPath(), contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         "rule-existing",
			RulePath:       "rules/rule-existing.md",
			Sha256:         sha256Hex([]byte(body)),
			IdempotencyKey: strings.Repeat("7", 64),
			VersionSeq:     1,
			ByRunID:        "2026-04-20-PR1-aaaaaaa",
			At:             time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC),
		},
	})

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, got.Candidates, 1)
	assert.Equal(t, contracts.CandidateKindDuplicate, got.Candidates[0].Kind)
	assert.Equal(t, "rule-existing", got.Candidates[0].TargetRuleID)
}

func TestRun_RejectsPartialStep30WithoutDoneMarker(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	writeCompliance(t, cfg.IO, testComplianceEntry(cfg.IO.RunID, "rule-a", contracts.ComplianceVerdictViolated))
	require.NoError(t, os.Remove(mustResolveClassifyPath(t, cfg.IO, "30/done.marker")))

	_, err := Run(context.Background(), cfg)
	require.ErrorContains(t, err, "step30 done.marker is missing or invalid")
}

func TestRun_RejectsStaleStep30DoneMarkerWhenCurrentScorableSetShrinks(t *testing.T) {
	cfg := newTestConfig(t)
	now := time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC)
	writeScores(t, cfg.IO,
		contracts.ScoreEntry{SchemaVersion: "1", RunID: cfg.IO.RunID, Pass: 1, Agent: "a1", Dimension: contracts.DimensionFidelity, Score: 80, Reasons: "a1", VerdictPath: contracts.VerdictPathSingle, RubricVersion: "default", PromptVersion: "phase0", ResolvedAt: now},
		contracts.ScoreEntry{SchemaVersion: "1", RunID: cfg.IO.RunID, Pass: 1, Agent: "a2", Dimension: contracts.DimensionFidelity, Score: 80, Reasons: "a2", VerdictPath: contracts.VerdictPathSingle, RubricVersion: "default", PromptVersion: "phase0", ResolvedAt: now},
		contracts.ScoreEntry{SchemaVersion: "1", RunID: cfg.IO.RunID, Pass: 1, Agent: "a3", Dimension: contracts.DimensionFidelity, Score: 80, Reasons: "a3", VerdictPath: contracts.VerdictPathSingle, RubricVersion: "default", PromptVersion: "phase0", ResolvedAt: now},
	)
	writeCompliance(t, cfg.IO, testComplianceEntry(cfg.IO.RunID, "rule-a", contracts.ComplianceVerdictViolated))
	writePass1Manifest(t, cfg.IO, cfg.IO.RunID, "a3", false)

	_, err := Run(context.Background(), cfg)
	require.ErrorContains(t, err, "step30 done.marker is missing or invalid")
}

func TestStep30Ready_RejectsTaskPackageWithPass1WorktreesButNoResolvableManifests(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	writeCompliance(t, cfg.IO, testComplianceEntry(cfg.IO.RunID, "rule-a", contracts.ComplianceVerdictViolated))

	for _, agent := range []contracts.AgentID{"a1", "a2", "a3"} {
		manifestPath, err := cfg.IO.ManifestPath(1, agent)
		require.NoError(t, err)
		require.NoError(t, os.Remove(manifestPath))
	}

	ready, err := step30Ready(cfg.IO, cfg.TaskPackage)
	require.ErrorContains(t, err, "pass1 worktrees exist but no pass1 manifests are resolvable")
	assert.False(t, ready)
}

func TestRun_RejectsInvalidExistingRulePath(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	writeCompliance(t, cfg.IO, testComplianceEntry(cfg.IO.RunID, "rule-a", contracts.ComplianceVerdictViolated))
	require.NoError(t, os.WriteFile(cfg.registryPath(), []byte(`{"kind":"added","schema_version":"1","rule_id":"rule-a","rule_path":"../needs-recovery/pwn.md","sha256":"`+strings.Repeat("1", 64)+`","idempotency_key":"`+strings.Repeat("2", 64)+`","version_seq":1,"by_run_id":"2026-04-21-PR42-abcdef0","at":"2026-04-21T12:00:00Z"}`+"\n"), 0o644))

	_, err := Run(context.Background(), cfg)
	require.ErrorContains(t, err, "invalid rule_path")
}

func TestRun_DuplicateClassifierIgnoresRolledBackRuleBody(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	writeCompliance(t, cfg.IO, testComplianceEntry(cfg.IO.RunID, "rule-dup", contracts.ComplianceVerdictViolated))

	rulesDir := filepath.Join(filepath.Dir(cfg.registryPath()), "rules")
	require.NoError(t, os.MkdirAll(rulesDir, 0o755))
	body := candidateBodyMarkdownWithEvidence(contracts.Candidate{
		CandidateID:      "cand-existing",
		Kind:             contracts.CandidateKindUpdate,
		TargetRuleID:     "rule-dup",
		Title:            "Rule candidate for rule-dup",
		Problem:          "problem",
		Rationale:        "rationale",
		ProposedBodyPath: "40/candidates/cand-existing.md",
	}, candidateEvidence{})
	require.NoError(t, os.WriteFile(filepath.Join(rulesDir, "rule-dup.md"), []byte(body), 0o644))
	added := registryAdded("rule-dup", strings.Repeat("9", 64))
	writeRegistry(t, cfg.registryPath(),
		added,
		registryRolledBackForEntries(t, cfg.registryPath(), []contracts.RuleRegistryEntry{added}, strings.Repeat("9", 64)),
	)

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, got.Candidates, 1)
	assert.Equal(t, contracts.CandidateKindNew, got.Candidates[0].Kind)
}

func TestActiveRulesFromRegistry_Variants(t *testing.T) {
	t.Run("updated entry keeps rule active", func(t *testing.T) {
		active, err := activeRulesFromRegistry([]contracts.RuleRegistryEntry{
			registryAdded("rule-updated", strings.Repeat("0", 64)),
			registryUpdated("rule-updated", strings.Repeat("a", 64)),
		})
		require.NoError(t, err)

		assert.True(t, active["rule-updated"])
	})

	t.Run("status_changed follows archived state", func(t *testing.T) {
		active, err := activeRulesFromRegistry([]contracts.RuleRegistryEntry{
			registryArchived("rule-status-archived", strings.Repeat("b", 64)),
			registryStatusChanged(
				"rule-status-deprecated",
				contracts.RuleStatusActive,
				contracts.RuleStatusDeprecated,
				contracts.SunsetTransitionDeprecate,
				strings.Repeat("c", 64),
			),
		})
		require.NoError(t, err)

		assert.False(t, active["rule-status-archived"])
		assert.True(t, active["rule-status-deprecated"])
	})

	t.Run("rolled_back added entry restores previous inactive state", func(t *testing.T) {
		added := registryAdded("rule-rolled-back", strings.Repeat("d", 64))
		active, err := activeRulesFromRegistry([]contracts.RuleRegistryEntry{
			added,
			registryRolledBackForEntries(t, "", []contracts.RuleRegistryEntry{added}, strings.Repeat("d", 64)),
		})
		require.NoError(t, err)

		assert.False(t, active["rule-rolled-back"])
	})

	t.Run("shared rollback target only reverts the latest matching rule", func(t *testing.T) {
		shared := strings.Repeat("f", 64)
		entryA := registryAdded("rule-a", shared)
		entryB := registryAdded("rule-b", shared)
		entryC := registryAdded("rule-c", shared)
		active, err := activeRulesFromRegistry([]contracts.RuleRegistryEntry{
			entryA,
			entryB,
			entryC,
			registryRolledBackForEntries(t, "", []contracts.RuleRegistryEntry{entryA, entryB, entryC}, shared),
		})
		require.NoError(t, err)

		assert.True(t, active["rule-a"])
		assert.True(t, active["rule-b"])
		assert.False(t, active["rule-c"])
	})
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
	refreshStep30Marker(t, runIO)
}

func writeCompliance(t *testing.T, runIO internalio.RunContext, entries ...contracts.ComplianceEntry) {
	t.Helper()
	path, err := runIO.ResolveRunRelative(compliancePath)
	require.NoError(t, err)
	writeJSONL(t, path, entries...)
	refreshStep30Marker(t, runIO)
}

func writeRegistry(t *testing.T, path string, entries ...contracts.RuleRegistryEntry) {
	t.Helper()
	normalized := make([]contracts.RuleRegistryEntry, 0, len(entries))
	lastSha := make(map[string]string)
	registryBase := filepath.Dir(path)
	for _, entry := range entries {
		switch value := entry.Value.(type) {
		case contracts.RuleRegistryAdded:
			absPath := filepath.Join(registryBase, value.RulePath)
			if _, err := os.Stat(absPath); os.IsNotExist(err) {
				body := fmt.Sprintf("# %s added\n", value.RuleID)
				value.Sha256 = sha256String(body)
				writeRegistryRuleSidecar(t, registryBase, value.RulePath, body)
			}
			lastSha[value.RuleID] = value.Sha256
			entry = contracts.RuleRegistryEntry{Kind: entry.Kind, Value: value}
		case contracts.RuleRegistryUpdated:
			body := fmt.Sprintf("# %s updated\n", value.RuleID)
			if prev, ok := lastSha[value.RuleID]; ok {
				value.PrevSha256 = prev
			}
			value.Sha256 = sha256String(body)
			writeRegistryRuleSidecar(t, registryBase, value.RulePath, body)
			lastSha[value.RuleID] = value.Sha256
			entry = contracts.RuleRegistryEntry{Kind: entry.Kind, Value: value}
		}
		normalized = append(normalized, entry)
	}
	writeJSONL(t, path, normalized...)
}

func writeRegistryRuleSidecar(t *testing.T, registryBase, rulePath, body string) {
	t.Helper()
	absPath := filepath.Join(registryBase, rulePath)
	require.NoError(t, os.MkdirAll(filepath.Dir(absPath), 0o755))
	require.NoError(t, os.WriteFile(absPath, []byte(body), 0o644))
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

func mustResolveClassifyPath(t *testing.T, runIO internalio.RunContext, rel string) string {
	t.Helper()
	path, err := runIO.ResolveRunRelative(rel)
	require.NoError(t, err)
	return path
}

func readClassificationFile(t *testing.T, runIO internalio.RunContext) []contracts.ClassificationEntry {
	t.Helper()
	path, err := runIO.ResolveRunRelative(classificationJSONLPath)
	require.NoError(t, err)
	got, err := internalio.ReadJSONL[contracts.ClassificationEntry](path)
	require.NoError(t, err)
	return got
}

func refreshStep30Marker(t *testing.T, runIO internalio.RunContext) {
	t.Helper()
	scoreFinalPath, err := runIO.ResolveRunRelative(scoresPath)
	require.NoError(t, err)
	complianceFinalPath, err := runIO.ResolveRunRelative(compliancePath)
	require.NoError(t, err)
	scoreRawPath, err := runIO.ResolveRunRelative("30/scores-A-raw.jsonl")
	require.NoError(t, err)
	complianceRawPath, err := runIO.ResolveRunRelative("30/compliance-A-raw.jsonl")
	require.NoError(t, err)

	scoreFinal, err := internalio.ReadJSONL[contracts.ScoreEntry](scoreFinalPath)
	if err != nil {
		scoreFinal = nil
	}
	complianceFinal, err := internalio.ReadJSONL[contracts.ComplianceEntry](complianceFinalPath)
	if err != nil {
		complianceFinal = nil
	}

	scoreRaw := make([]contracts.RawScoreEntry, 0, len(scoreFinal))
	for _, row := range scoreFinal {
		scoreRaw = append(scoreRaw, contracts.RawScoreEntry{
			SchemaVersion: "1",
			RunID:         row.RunID,
			Pass:          row.Pass,
			Agent:         row.Agent,
			JudgeRole:     contracts.JudgeRolePrimary,
			Dimension:     row.Dimension,
			Score:         row.Score,
			Reasons:       row.Reasons,
			OutputSha256:  strings.Repeat("a", 64),
			RubricVersion: row.RubricVersion,
			PromptVersion: row.PromptVersion,
			ResolvedAt:    row.ResolvedAt,
		})
	}
	complianceRaw := make([]contracts.RawComplianceEntry, 0, len(complianceFinal))
	for _, row := range complianceFinal {
		complianceRaw = append(complianceRaw, contracts.RawComplianceEntry{
			SchemaVersion: "1",
			RunID:         row.RunID,
			Pass:          row.Pass,
			Agent:         row.Agent,
			JudgeRole:     contracts.JudgeRolePrimary,
			RuleID:        row.RuleID,
			Verdict:       row.Verdict,
			Rationale:     row.Rationale,
			OutputSha256:  strings.Repeat("a", 64),
			RubricVersion: row.RubricVersion,
			PromptVersion: row.PromptVersion,
			ResolvedAt:    row.ResolvedAt,
		})
	}
	writeJSONL(t, scoreRawPath, scoreRaw...)
	writeJSONL(t, complianceRawPath, complianceRaw...)

	agents := make([]contracts.AgentID, 0)
	seen := map[contracts.AgentID]struct{}{}
	for _, row := range scoreFinal {
		if _, ok := seen[row.Agent]; ok {
			continue
		}
		seen[row.Agent] = struct{}{}
		agents = append(agents, row.Agent)
	}
	syncPass1Manifests(t, runIO, agents)
	if len(agents) == 0 {
		return
	}
	marker, err := scorecore.BuildStep30DoneMarker(scorecore.Step30MarkerInputs{
		Agents: agents,
		Paths: scorecore.Step30MarkerPaths{
			ScoreFinal:      scoreFinalPath,
			ComplianceFinal: complianceFinalPath,
			ScoreRaw:        scoreRawPath,
			ComplianceRaw:   complianceRawPath,
		},
		ResolvedAt: time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.NoError(t, scorecore.WriteStep30DoneMarker(runIO, marker))
}

func syncPass1Manifests(t *testing.T, runIO internalio.RunContext, scorableAgents []contracts.AgentID) {
	t.Helper()
	scorable := make(map[contracts.AgentID]bool, len(scorableAgents))
	for _, agent := range scorableAgents {
		scorable[agent] = true
	}
	for _, agent := range []contracts.AgentID{"a1", "a2", "a3"} {
		writePass1Manifest(t, runIO, runIO.RunID, agent, scorable[agent])
	}
}

func writePass1Manifest(t *testing.T, runIO internalio.RunContext, runID contracts.RunID, agent contracts.AgentID, success bool) {
	t.Helper()
	path, err := runIO.ManifestPath(1, agent)
	require.NoError(t, err)
	if success {
		require.NoError(t, internalio.WriteJSONAtomic(path, contracts.Manifest{
			Kind: contracts.ManifestKindSuccess,
			Value: contracts.ManifestSuccess{
				Kind:          contracts.ManifestKindSuccess,
				SchemaVersion: "1",
				RunID:         runID,
				Pass:          1,
				Agent:         agent,
				BranchName:    "auto-improve/fixture",
				HeadSHA:       strings.Repeat("b", 40),
				BaseSHA:       strings.Repeat("a", 40),
				DiffPath:      filepath.ToSlash(filepath.Join("20-pass1", string(agent), "diff.patch")),
				SessionPath:   filepath.ToSlash(filepath.Join("20-pass1", string(agent), "session.jsonl")),
				ChecklistPath: filepath.ToSlash(filepath.Join("20-pass1", string(agent), "checklist-result.json")),
				PromptVersion: "phase0",
				StartedAt:     time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
				FinishedAt:    time.Date(2026, 4, 21, 10, 1, 0, 0, time.UTC),
			},
		}))
		return
	}
	require.NoError(t, internalio.WriteJSONAtomic(path, contracts.Manifest{
		Kind: contracts.ManifestKindError,
		Value: contracts.ManifestError{
			Kind:          contracts.ManifestKindError,
			SchemaVersion: "1",
			RunID:         runID,
			Pass:          1,
			Agent:         agent,
			ExitCode:      1,
			Reason:        "unknown",
			Detail:        "fixture non-scorable manifest",
			StartedAt:     time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
			FinishedAt:    time.Date(2026, 4, 21, 10, 1, 0, 0, time.UTC),
		},
	}))
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
			Reasons:       "Missing the guard lets regressions slip into the changed code path.",
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
		Rationale:     fmt.Sprintf("Rule %s was skipped when the implementation touched the guarded path.", ruleID),
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "default",
		PromptVersion: "phase0",
		ResolvedAt:    time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC),
	}
}

func registryAdded(ruleID, idempotencyKey string) contracts.RuleRegistryEntry {
	body := fmt.Sprintf("# %s added\n", ruleID)
	return contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         ruleID,
			RulePath:       filepath.Join("rules", ruleID+".md"),
			Sha256:         sha256String(body),
			IdempotencyKey: idempotencyKey,
			VersionSeq:     1,
			ByRunID:        "2026-04-20-PR1-aaaaaaa",
			At:             time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC),
		},
	}
}

func registryUpdated(ruleID, idempotencyKey string) contracts.RuleRegistryEntry {
	prevBody := fmt.Sprintf("# %s added\n", ruleID)
	return contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindUpdated,
		Value: contracts.RuleRegistryUpdated{
			Kind:           contracts.RegistryKindUpdated,
			SchemaVersion:  "1",
			RuleID:         ruleID,
			RulePath:       filepath.Join("rules", ruleID+".md"),
			Sha256:         strings.Repeat("b", 64),
			PrevSha256:     sha256String(prevBody),
			IdempotencyKey: idempotencyKey,
			VersionSeq:     2,
			PrevHash:       strings.Repeat("c", 64),
			ByRunID:        "2026-04-20-PR2-bbbbbbb",
			At:             time.Date(2026, 4, 20, 9, 30, 0, 0, time.UTC),
		},
	}
}

func registryStatusChanged(ruleID string, prevStatus, newStatus contracts.RuleStatus, transition contracts.SunsetTransition, opID string) contracts.RuleRegistryEntry {
	return contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindStatusChanged,
		Value: contracts.RuleRegistryStatusChanged{
			Kind:          contracts.RegistryKindStatusChanged,
			SchemaVersion: "1",
			RuleID:        ruleID,
			PrevStatus:    prevStatus,
			NewStatus:     newStatus,
			Transition:    transition,
			OpID:          opID,
			VersionSeq:    1,
			BySunsetRunID: "sunset-3",
			At:            time.Date(2026, 4, 20, 10, 30, 0, 0, time.UTC),
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

func registryRolledBackForEntries(t *testing.T, registryPath string, entries []contracts.RuleRegistryEntry, targetOpID string) contracts.RuleRegistryEntry {
	t.Helper()
	normalized := append([]contracts.RuleRegistryEntry(nil), entries...)
	if registryPath != "" {
		lastSha := make(map[string]string)
		for idx, entry := range normalized {
			switch value := entry.Value.(type) {
			case contracts.RuleRegistryAdded:
				body := fmt.Sprintf("# %s added\n", value.RuleID)
				value.Sha256 = sha256String(body)
				lastSha[value.RuleID] = value.Sha256
				normalized[idx] = contracts.RuleRegistryEntry{Kind: entry.Kind, Value: value}
			case contracts.RuleRegistryUpdated:
				body := fmt.Sprintf("# %s updated\n", value.RuleID)
				if prev, ok := lastSha[value.RuleID]; ok {
					value.PrevSha256 = prev
				}
				value.Sha256 = sha256String(body)
				lastSha[value.RuleID] = value.Sha256
				normalized[idx] = contracts.RuleRegistryEntry{Kind: entry.Kind, Value: value}
			}
		}
	}
	var (
		offset      int64
		targetFound bool
		targetHash  string
		targetOff   int64
	)
	for _, entry := range normalized {
		payload, err := contracts.CanonicalMarshal(entry)
		require.NoError(t, err)
		sum := sha256.Sum256(payload)
		hash := hex.EncodeToString(sum[:])
		switch v := entry.Value.(type) {
		case contracts.RuleRegistryAdded:
			if v.IdempotencyKey == targetOpID {
				targetFound = true
				targetHash = hash
				targetOff = offset
			}
		case contracts.RuleRegistryUpdated:
			if v.IdempotencyKey == targetOpID {
				targetFound = true
				targetHash = hash
				targetOff = offset
			}
		}
		offset += int64(len(payload) + 1)
	}
	require.True(t, targetFound)
	return contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindRolledBack,
		Value: contracts.RuleRegistryRolledBack{
			Kind:           contracts.RegistryKindRolledBack,
			SchemaVersion:  "1",
			TargetOpID:     targetOpID,
			TargetOffset:   targetOff,
			TargetSha256:   targetHash,
			ByRunID:        "2026-04-20-PR3-ccccccc",
			RollbackReason: contracts.RollbackReasonTransactionalFailure,
			FailedStep:     contracts.FailedStep70,
			VersionSeq:     2,
			PrevHash:       strings.Repeat("f", 64),
			At:             time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC),
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

func TestBestDuplicateMatch_IgnoresTemplateBoilerplate(t *testing.T) {
	candidateBody := candidateBodyMarkdown(contracts.Candidate{
		CandidateID:  "cand-1",
		Kind:         contracts.CandidateKindNew,
		Title:        "Rule candidate for rule-a",
		Problem:      "Pass1 recorded 3 violation(s) for rule rule-a.",
		Rationale:    "Phase 0 deterministic classify generated one candidate from compliance-A.jsonl for rule-a.",
		TargetRuleID: "",
	})
	activeRuleBodies := map[string]string{
		"rule-a": candidateBody,
		"rule-b": "# Existing rule\n\n- source_rule_id: rule-b\n- classification: update\n\n## Problem\nPass1 recorded 3 violation(s) for rule rule-b.\n\n## Rationale\nPhase 0 deterministic classify generated one candidate from compliance-A.jsonl for rule-b.\n",
	}

	ruleID, score := bestDuplicateMatch(candidateBody, activeRuleBodies)
	assert.Equal(t, "rule-a", ruleID)
	assert.Greater(t, score, 0.9)
}

func TestBestDuplicateMatch_BreaksEqualScoreTiesLexicographically(t *testing.T) {
	candidateBody := "# Existing rule\n\n- source_rule_id: candidate\n- classification: new\n\n## Problem\nPass1 recorded 1 violation(s) for rule candidate.\n\n## Rationale\nPhase 0 deterministic classify generated one candidate from compliance-A.jsonl for candidate.\n\n## Proposed rule\n- Keep the same normalized body.\n"
	activeRuleBodies := map[string]string{
		"rule-b": "# Existing rule\n\n- source_rule_id: rule-b\n- classification: update\n\n## Problem\nPass1 recorded 1 violation(s) for rule rule-b.\n\n## Rationale\nPhase 0 deterministic classify generated one candidate from compliance-A.jsonl for rule-b.\n\n## Proposed rule\n- Keep the same normalized body.\n",
		"rule-a": "# Existing rule\n\n- source_rule_id: rule-a\n- classification: update\n\n## Problem\nPass1 recorded 1 violation(s) for rule rule-a.\n\n## Rationale\nPhase 0 deterministic classify generated one candidate from compliance-A.jsonl for rule-a.\n\n## Proposed rule\n- Keep the same normalized body.\n",
	}

	for i := 0; i < 32; i++ {
		ruleID, score := bestDuplicateMatch(candidateBody, activeRuleBodies)
		assert.Equal(t, "rule-a", ruleID)
		assert.Equal(t, 1.0, score)
	}
}
