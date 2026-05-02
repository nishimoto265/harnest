package step40_classify

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_IgnoresSupersededComplianceViolations(t *testing.T) {
	cfg := newTestConfig(t)
	now := time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC)
	writeScores(t, cfg.IO, contracts.ScoreEntry{
		SchemaVersion: "1",
		RunID:         cfg.IO.RunID,
		Pass:          1,
		Agent:         "a1",
		Dimension:     contracts.DimensionFidelity,
		Score:         90,
		Reasons:       "No material scoring concern.",
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

	ruleABodyPath, err := cfg.IO.ResolveRunRelative(got.Candidates[indexOfCandidate(t, got.Candidates, "cand-2026-04-21-pr42-abcdef0-001")].ProposedBodyPath)
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

	built, err := buildCandidates(cfg.IO, now, scores, compliance, nil, nil, filepath.Dir(cfg.registryPath()))
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
