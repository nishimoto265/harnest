package step30_score

import (
	"context"
	"os"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStep30Score_RunAndResume(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})

	step := New()
	err := step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg})
	require.NoError(t, err)

	markerPath, err := runCtx.ResolveRunRelative("30/done.marker")
	require.NoError(t, err)
	require.FileExists(t, markerPath)

	marker, err := internalio.ReadJSON[contracts.Step30DoneMarker](markerPath)
	require.NoError(t, err)
	assert.Equal(t, int64(15), marker.ExpectedCounts.Scores)    // 3 agents × 5 dims
	assert.Equal(t, int64(3), marker.ExpectedCounts.Compliance) // 3 agents × 1 stub rule
	assert.Len(t, marker.CompletedAgents, 3)

	scoreFinalPath, err := runCtx.ResolveRunRelative("30/scores-A.jsonl")
	require.NoError(t, err)
	scores, err := internalio.ReadJSONL[contracts.ScoreEntry](scoreFinalPath)
	require.NoError(t, err)
	assert.Len(t, scores, 15)

	// Resume: running again with a valid marker must be a no-op (file sizes unchanged).
	info1, err := os.Stat(scoreFinalPath)
	require.NoError(t, err)
	err = step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg})
	require.NoError(t, err)
	info2, err := os.Stat(scoreFinalPath)
	require.NoError(t, err)
	assert.Equal(t, info1.Size(), info2.Size(), "resume path must not re-append rows")

	// Invalidate the marker: a corrupt marker should be replaced, not error.
	require.NoError(t, os.WriteFile(markerPath, []byte("stub\n"), 0o644))
	err = step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg})
	require.NoError(t, err)
	rebuilt, err := internalio.ReadJSON[contracts.Step30DoneMarker](markerPath)
	require.NoError(t, err)
	assert.Equal(t, int64(15), rebuilt.ExpectedCounts.Scores)
}

func TestStep30Score_RejectsTaskPackageRunIDMismatch(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	pkg.RunID = internalio.NewRunID(100)

	err := New().Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg})
	require.ErrorContains(t, err, "task package run_id mismatch")
}

func TestStep30Score_SkipsUnscorableAgents(t *testing.T) {
	// TaskPackage requires exactly 6 worktrees (3 agents × 2 passes). Seed all
	// 3 agents but only write manifests for a1 / a2 — a3's missing manifest
	// must cause step30 to skip that agent.
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	a3ManifestPath, err := runCtx.ManifestPath(1, contracts.AgentID("a3"))
	require.NoError(t, err)
	require.NoError(t, os.Remove(a3ManifestPath))

	step := New()
	err = step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg})
	require.NoError(t, err)

	markerPath, err := runCtx.ResolveRunRelative("30/done.marker")
	require.NoError(t, err)
	marker, err := internalio.ReadJSON[contracts.Step30DoneMarker](markerPath)
	require.NoError(t, err)
	assert.Equal(t, int64(10), marker.ExpectedCounts.Scores) // only a1 + a2
	assert.Len(t, marker.CompletedAgents, 2)
}

func TestStep30Score_AllowsMultipleComplianceRowsPerAgent(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			score := 80
			if role == contracts.JudgeRoleSecondary {
				score = 79
			}
			return makeJudgeOutput(input, role, score, []ruleVerdict{
				{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant},
				{ruleID: "rule-b", verdict: contracts.ComplianceVerdictViolated},
			})
		},
	}

	step := New(WithPanelProvider(provider))
	setStepRubric(t, step, "rule-a", "rule-b")
	err := step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg})
	require.NoError(t, err)

	markerPath, err := runCtx.ResolveRunRelative("30/done.marker")
	require.NoError(t, err)
	marker, err := internalio.ReadJSON[contracts.Step30DoneMarker](markerPath)
	require.NoError(t, err)
	assert.Equal(t, int64(15), marker.ExpectedCounts.Scores)
	assert.Equal(t, int64(6), marker.ExpectedCounts.Compliance)
}

func TestStep30Score_WritesExplicitIssues(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			out := makeJudgeOutput(input, role, 80, []ruleVerdict{{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant}})
			if role == contracts.JudgeRolePrimary {
				out.Issues = []judges.Issue{{
					Severity:       contracts.IssueSeverityHigh,
					Category:       "routing",
					Title:          "Extract proxy not-found matching",
					Evidence:       "proxy.ts mixes request id injection with 404 rewrite pattern matching.",
					ProposedLesson: "Keep route matching helpers separate from request mutation middleware.",
					ChecklistItem:  "Separate proxy 404 matching logic from request mutation logic.",
				}}
			}
			return out
		},
	}

	step := New(WithPanelProvider(provider))
	setStepRubric(t, step, "rule-a")
	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	issuesPath, err := runCtx.ResolveRunRelative("30/issues-A.jsonl")
	require.NoError(t, err)
	issues, err := internalio.ReadJSONL[contracts.IssueEntry](issuesPath)
	require.NoError(t, err)
	require.Len(t, issues, 3)
	for _, issue := range issues {
		assert.Equal(t, contracts.JudgeRolePrimary, issue.JudgeRole)
		assert.Equal(t, contracts.IssueSeverityHigh, issue.Severity)
		assert.Equal(t, "routing", issue.Category)
		assert.NotEmpty(t, issue.IssueID)
		assert.Len(t, issue.OutputSha256, 64)
	}
}

func TestStep30Score_ResumeWithoutMarkerDoesNotRejudgeOrAppend(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			score := 80
			switch role {
			case contracts.JudgeRoleSecondary:
				score = 79
			case contracts.JudgeRoleArbiter:
				score = 78
			}
			return makeJudgeOutput(input, role, score, []ruleVerdict{
				{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant},
			})
		},
	}

	step := New(WithPanelProvider(provider))
	setStepRubric(t, step, "rule-a")
	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	markerPath, err := runCtx.ResolveRunRelative("30/done.marker")
	require.NoError(t, err)
	require.NoError(t, os.Remove(markerPath))

	before := statStep30Files(t, runCtx)
	provider.reset()

	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	after := statStep30Files(t, runCtx)
	assert.Equal(t, before, after)
	assert.Zero(t, provider.calls[contracts.JudgeRolePrimary])
	assert.Zero(t, provider.calls[contracts.JudgeRoleSecondary])
	assert.Zero(t, provider.calls[contracts.JudgeRoleArbiter])
}
