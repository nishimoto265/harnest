package step40_classify

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_EmptyInputsFailClosed(t *testing.T) {
	cfg := newTestConfig(t)

	got, err := Run(context.Background(), cfg)
	require.ErrorContains(t, err, "step30 done.marker is missing or invalid")
	assert.Nil(t, got)
}

func TestRun_ValidEmptyComplianceArtifactEmitsZeroCandidates(t *testing.T) {
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
	writeCompliance(t, cfg.IO)

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Empty(t, got.Candidates)
}

func TestRun_LowScoresWithoutComplianceViolationsProduceScoreConcernCandidates(t *testing.T) {
	cfg := newTestConfig(t)
	now := time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC)
	writeScores(t, cfg.IO,
		contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         cfg.IO.RunID,
			Pass:          1,
			Agent:         "a1",
			Dimension:     contracts.DimensionCorrectness,
			Score:         83,
			Reasons:       "Client component meta tags may not render into the document head.",
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
			Dimension:     contracts.DimensionMaintainability,
			Score:         84,
			Reasons:       "Route-group error handlers duplicate nearly identical implementation details.",
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
			RuleID:        "stub-rubric-rule",
			Verdict:       contracts.ComplianceVerdictNA,
			Rationale:     "No active rule applies.",
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "default",
			PromptVersion: "phase0",
			ResolvedAt:    now,
		},
	)

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, got.Candidates, 2)

	assert.Equal(t, "score-client-component-meta-tags", experimentLessonTitleID(got.Candidates[0].Title))
	assert.Equal(t, "score-deduplicate-route-group-error-handlers", experimentLessonTitleID(got.Candidates[1].Title))
	assertCandidateBodies(t, cfg.IO, got.Candidates)
	assertExperimentChecklist(t, cfg.IO, "score-client-component-meta-tags", "score-deduplicate-route-group-error-handlers")

	correctnessBodyPath, err := cfg.IO.ResolveRunRelative(got.Candidates[0].ProposedBodyPath)
	require.NoError(t, err)
	correctnessBody, err := os.ReadFile(correctnessBodyPath)
	require.NoError(t, err)
	assert.Contains(t, string(correctnessBody), "a1/correctness score 83: Client component meta tags may not render into the document head.")

	maintainabilityBodyPath, err := cfg.IO.ResolveRunRelative(got.Candidates[1].ProposedBodyPath)
	require.NoError(t, err)
	maintainabilityBody, err := os.ReadFile(maintainabilityBodyPath)
	require.NoError(t, err)
	assert.Contains(t, string(maintainabilityBody), "a2/maintainability score 84: Route-group error handlers duplicate nearly identical implementation details.")
}

func TestRun_ExplicitIssuesProduceLessonsAndSuppressScoreConcernFallback(t *testing.T) {
	cfg := newTestConfig(t)
	now := time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC)
	writeScores(t, cfg.IO,
		contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         cfg.IO.RunID,
			Pass:          1,
			Agent:         "a1",
			Dimension:     contracts.DimensionCorrectness,
			Score:         83,
			Reasons:       "Client component meta tags may not render into the document head.",
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "default",
			PromptVersion: "phase0",
			ResolvedAt:    now,
		},
	)
	writeCompliance(t, cfg.IO)
	writeIssues(t, cfg.IO,
		contracts.IssueEntry{
			SchemaVersion:  "1",
			RunID:          cfg.IO.RunID,
			Pass:           1,
			Agent:          "a1",
			JudgeRole:      contracts.JudgeRolePrimary,
			IssueID:        "issue-1111111111111111",
			Severity:       contracts.IssueSeverityHigh,
			Category:       "routing",
			Title:          "Extract proxy not-found matching",
			Evidence:       "proxy.ts mixes request id injection with 404 rewrite pattern matching.",
			ProposedLesson: "Keep route matching helpers separate from request mutation middleware.",
			ChecklistItem:  "Separate proxy 404 matching logic from request mutation logic.",
			OutputSha256:   strings.Repeat("a", 64),
			RubricVersion:  "default",
			PromptVersion:  "phase0",
			ResolvedAt:     now,
		},
	)

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, got.Candidates, 1)
	assert.Equal(t, "issue-routing-extract-proxy-not-found-matching", experimentLessonTitleID(got.Candidates[0].Title))
	assertExperimentChecklist(t, cfg.IO, "issue-routing-extract-proxy-not-found-matching")

	bodyPath, err := cfg.IO.ResolveRunRelative(got.Candidates[0].ProposedBodyPath)
	require.NoError(t, err)
	body, err := os.ReadFile(bodyPath)
	require.NoError(t, err)
	assert.Contains(t, string(body), "- issue: a1/primary/high: proxy.ts mixes request id injection with 404 rewrite pattern matching.")
	assert.Contains(t, string(body), "Apply this proposed lesson: Keep route matching helpers separate from request mutation middleware.")
	assert.NotContains(t, string(body), "Client component meta tags may not render into the document head.")
}

func TestRun_MergesComplianceAndExplicitIssueForSameLesson(t *testing.T) {
	cfg := newTestConfig(t)
	now := time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC)
	writeScores(t, cfg.IO, contracts.ScoreEntry{
		SchemaVersion: "1",
		RunID:         cfg.IO.RunID,
		Pass:          1,
		Agent:         "a1",
		Dimension:     contracts.DimensionCorrectness,
		Score:         82,
		Reasons:       "Route-level behavior is not directly tested.",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "default",
		PromptVersion: "phase0",
		ResolvedAt:    now,
	})
	writeCompliance(t, cfg.IO, contracts.ComplianceEntry{
		SchemaVersion: "1",
		RunID:         cfg.IO.RunID,
		Pass:          1,
		Agent:         "a1",
		RuleID:        "issue-test-coverage-dynamic-recipe-route-behavior-is-untested",
		Verdict:       contracts.ComplianceVerdictViolated,
		Rationale:     "The route data dependency changed without route-level tests.",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "default",
		PromptVersion: "phase0",
		ResolvedAt:    now,
	})
	writeIssues(t, cfg.IO, contracts.IssueEntry{
		SchemaVersion:  "1",
		RunID:          cfg.IO.RunID,
		Pass:           1,
		Agent:          "a2",
		JudgeRole:      contracts.JudgeRolePrimary,
		IssueID:        "issue-1111111111111111",
		Severity:       contracts.IssueSeverityMedium,
		Category:       "test-coverage",
		Title:          "dynamic recipe route behavior is untested",
		Evidence:       "The dynamic route fetches new data but only component tests were added.",
		ProposedLesson: "Dynamic route changes need route-level render and notFound tests.",
		ChecklistItem:  "Dynamic route changes include route-level behavior tests.",
		OutputSha256:   strings.Repeat("a", 64),
		RubricVersion:  "default",
		PromptVersion:  "phase0",
		ResolvedAt:     now,
	})

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, got.Candidates, 1)
	assertCandidateBodies(t, cfg.IO, got.Candidates)
	assert.Equal(t, "issue-test-coverage-dynamic-recipe-route-behavior-is-untested", experimentLessonTitleID(got.Candidates[0].Title))

	bodyPath, err := cfg.IO.ResolveRunRelative(got.Candidates[0].ProposedBodyPath)
	require.NoError(t, err)
	body, err := os.ReadFile(bodyPath)
	require.NoError(t, err)
	assert.Contains(t, string(body), "- compliance: The route data dependency changed without route-level tests.")
	assert.Contains(t, string(body), "- issue: a2/primary/medium: The dynamic route fetches new data but only component tests were added.")
	assert.Contains(t, string(body), "Apply this proposed lesson: Dynamic route changes need route-level render and notFound tests.")
}

func TestRun_CapsCandidatesAtTenBySeverity(t *testing.T) {
	cfg := newTestConfig(t)
	now := time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC)
	writeScores(t, cfg.IO, contracts.ScoreEntry{
		SchemaVersion: "1",
		RunID:         cfg.IO.RunID,
		Pass:          1,
		Agent:         "a1",
		Dimension:     contracts.DimensionCorrectness,
		Score:         90,
		Reasons:       "Baseline score row required by step40.",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "default",
		PromptVersion: "phase0",
		ResolvedAt:    now,
	})
	writeCompliance(t, cfg.IO)
	issues := make([]contracts.IssueEntry, 0, 12)
	for i := 0; i < 12; i++ {
		severity := contracts.IssueSeverityLow
		if i < 2 {
			severity = contracts.IssueSeverityHigh
		}
		issues = append(issues, contracts.IssueEntry{
			SchemaVersion:  "1",
			RunID:          cfg.IO.RunID,
			Pass:           1,
			Agent:          contracts.AgentID("a1"),
			JudgeRole:      contracts.JudgeRolePrimary,
			IssueID:        fmt.Sprintf("issue-%016d", i+1),
			Severity:       severity,
			Category:       "candidate-cap",
			Title:          fmt.Sprintf("candidate %02d", i+1),
			Evidence:       fmt.Sprintf("Evidence for candidate %02d.", i+1),
			ProposedLesson: fmt.Sprintf("Guidance for candidate %02d.", i+1),
			ChecklistItem:  fmt.Sprintf("Checklist for candidate %02d.", i+1),
			OutputSha256:   strings.Repeat("a", 64),
			RubricVersion:  "default",
			PromptVersion:  "phase0",
			ResolvedAt:     now,
		})
	}
	writeIssues(t, cfg.IO, issues...)

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, got.Candidates, maxStep40Candidates)
	assertCandidateBodies(t, cfg.IO, got.Candidates)

	titles := make([]string, 0, len(got.Candidates))
	for _, candidate := range got.Candidates {
		titles = append(titles, experimentLessonTitleID(candidate.Title))
	}
	assert.Contains(t, titles, "issue-candidate-cap-candidate-01")
	assert.Contains(t, titles, "issue-candidate-cap-candidate-02")
}

func TestRun_ExplicitIssuesKeepDistinctLessonsWithoutSimilarityBuckets(t *testing.T) {
	cfg := newTestConfig(t)
	now := time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC)
	writeScores(t, cfg.IO, contracts.ScoreEntry{
		SchemaVersion: "1",
		RunID:         cfg.IO.RunID,
		Pass:          1,
		Agent:         "a1",
		Dimension:     contracts.DimensionCorrectness,
		Score:         83,
		Reasons:       "Baseline score row required by step40.",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "default",
		PromptVersion: "phase0",
		ResolvedAt:    now,
	})
	writeCompliance(t, cfg.IO)
	writeIssues(t, cfg.IO,
		contracts.IssueEntry{
			SchemaVersion:  "1",
			RunID:          cfg.IO.RunID,
			Pass:           1,
			Agent:          "a1",
			JudgeRole:      contracts.JudgeRolePrimary,
			IssueID:        "issue-1111111111111111",
			Severity:       contracts.IssueSeverityMedium,
			Category:       "completeness",
			Title:          "Only en.json locale file updated with Error translations",
			Evidence:       "messages/en.json gets the new Error section but no other locale files are modified.",
			ProposedLesson: "When adding i18n keys, verify all supported locale files are updated or confirm fallback behavior covers missing keys.",
			ChecklistItem:  "Verify all locale files are updated when adding new translation keys.",
			OutputSha256:   strings.Repeat("a", 64),
			RubricVersion:  "default",
			PromptVersion:  "phase0",
			ResolvedAt:     now,
		},
		contracts.IssueEntry{
			SchemaVersion:  "1",
			RunID:          cfg.IO.RunID,
			Pass:           1,
			Agent:          "a2",
			JudgeRole:      contracts.JudgeRoleSecondary,
			IssueID:        "issue-2222222222222222",
			Severity:       contracts.IssueSeverityLow,
			Category:       "maintainability",
			Title:          "Only en.json locale updated",
			Evidence:       "The patch adds Error messages only to en.json.",
			ProposedLesson: "Add new translation keys to all supported locale files, not just one.",
			ChecklistItem:  "Add new translation keys to all supported locale files, not just one.",
			OutputSha256:   strings.Repeat("b", 64),
			RubricVersion:  "default",
			PromptVersion:  "phase0",
			ResolvedAt:     now,
		},
		contracts.IssueEntry{
			SchemaVersion:  "1",
			RunID:          cfg.IO.RunID,
			Pass:           1,
			Agent:          "a3",
			JudgeRole:      contracts.JudgeRolePrimary,
			IssueID:        "issue-3333333333333333",
			Severity:       contracts.IssueSeverityLow,
			Category:       "edge-case",
			Title:          "Terms proxy rewrite does not handle trailing slash",
			Evidence:       "The middleware compares /terms only and misses /terms/.",
			ProposedLesson: "Normalize pathname trailing slashes before comparing middleware route guards.",
			ChecklistItem:  "Verify path comparisons in middleware handle trailing slash variations.",
			OutputSha256:   strings.Repeat("c", 64),
			RubricVersion:  "default",
			PromptVersion:  "phase0",
			ResolvedAt:     now,
		},
	)

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, got.Candidates, 3)

	assert.Equal(t, "issue-completeness-only-en-json-locale-file-updated-with-a957a83b1cd7", experimentLessonTitleID(got.Candidates[0].Title))
	assert.Equal(t, "issue-edge-case-terms-proxy-rewrite-does-not-handle-trailing-slash", experimentLessonTitleID(got.Candidates[1].Title))
	assert.Equal(t, "issue-maintainability-only-en-json-locale-updated", experimentLessonTitleID(got.Candidates[2].Title))

	bodyPath, err := cfg.IO.ResolveRunRelative(got.Candidates[0].ProposedBodyPath)
	require.NoError(t, err)
	body, err := os.ReadFile(bodyPath)
	require.NoError(t, err)
	assert.Contains(t, string(body), "messages/en.json gets the new Error section")
	assert.NotContains(t, string(body), "The patch adds Error messages only to en.json")
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

	assert.Equal(t, "cand-2026-04-21-pr42-abcdef0-001", got.Candidates[0].CandidateID)
	assert.Equal(t, "rule-a", experimentLessonTitleID(got.Candidates[0].Title))
	assert.Equal(t, contracts.CandidateKindNew, got.Candidates[0].Kind)
	assert.Empty(t, got.Candidates[0].TargetRuleID)

	assert.Equal(t, "cand-2026-04-21-pr42-abcdef0-002", got.Candidates[1].CandidateID)
	assert.Equal(t, "rule-b", experimentLessonTitleID(got.Candidates[1].Title))
	assert.Equal(t, contracts.CandidateKindNew, got.Candidates[1].Kind)
	assert.Empty(t, got.Candidates[1].TargetRuleID)

	assertCandidateBodies(t, cfg.IO, got.Candidates)
	assertExperimentChecklist(t, cfg.IO, "rule-a", "rule-b")

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
