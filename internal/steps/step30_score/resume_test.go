package step30_score

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/judges"
	"github.com/nishimoto265/harnest/internal/steps/scorecore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStep30Score_ResumeRerunsRolesWhenOutputSHAChanges(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			score := 80
			switch role {
			case contracts.JudgeRoleSecondary:
				score = 60
			case contracts.JudgeRoleArbiter:
				score = 75
			}
			return makeJudgeOutput(input, role, score, []ruleVerdict{
				{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant},
			})
		},
	}

	step := New(WithPanelProvider(provider))
	setStepRubric(t, step, "rule-a")
	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	manifest, err := internalio.LoadScorableManifest(runCtx, 1, contracts.AgentID("a1"))
	require.NoError(t, err)
	diffAbs, err := runCtx.ResolveRunRelative(manifest.DiffPath)
	require.NoError(t, err)

	originalSHA, err := fileSha256(diffAbs)
	require.NoError(t, err)

	markerPath, err := runCtx.ResolveRunRelative("30/done.marker")
	require.NoError(t, err)
	require.NoError(t, os.Remove(markerPath))

	writeRel(t, runCtx, manifest.DiffPath, "updated fixture diff for a1\n")
	updatedSHA, err := fileSha256(diffAbs)
	require.NoError(t, err)
	require.NotEqual(t, originalSHA, updatedSHA)

	provider.reset()
	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	assert.Equal(t, 1, provider.calls[contracts.JudgeRolePrimary])
	assert.Zero(t, provider.calls[contracts.JudgeRoleSecondary])
	assert.Zero(t, provider.calls[contracts.JudgeRoleArbiter])

	scoreRawPath, err := runCtx.ResolveRunRelative("30/scores-A-raw.jsonl")
	require.NoError(t, err)
	scoreRaw, err := internalio.ReadJSONL[contracts.RawScoreEntry](scoreRawPath)
	require.NoError(t, err)
	assert.Len(t, scoreRaw, 20)
	assert.Equal(t, 5, countRawScoresForAgentAndSHA(scoreRaw, contracts.AgentID("a1"), originalSHA))
	assert.Equal(t, 5, countRawScoresForAgentAndSHA(scoreRaw, contracts.AgentID("a1"), updatedSHA))

	complianceRawPath, err := runCtx.ResolveRunRelative("30/compliance-A-raw.jsonl")
	require.NoError(t, err)
	complianceRaw, err := internalio.ReadJSONL[contracts.RawComplianceEntry](complianceRawPath)
	require.NoError(t, err)
	assert.Len(t, complianceRaw, 4)
	assert.Equal(t, 1, countRawComplianceForAgentAndSHA(complianceRaw, contracts.AgentID("a1"), originalSHA))
	assert.Equal(t, 1, countRawComplianceForAgentAndSHA(complianceRaw, contracts.AgentID("a1"), updatedSHA))
}

func TestStep30Score_ResumeRerunsWhenPromptVersionChanges(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			score := 80
			if role == contracts.JudgeRoleSecondary {
				score = 79
			}
			return makeJudgeOutput(input, role, score, []ruleVerdict{{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant}})
		},
	}

	first := New(WithPanelProvider(provider), WithPromptVersion("prompt-v1"))
	setStepRubric(t, first, "rule-a")
	require.NoError(t, first.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	provider.reset()
	second := New(WithPanelProvider(provider), WithPromptVersion("prompt-v2"))
	setStepRubric(t, second, "rule-a")
	require.NoError(t, second.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	assert.Equal(t, 3, provider.calls[contracts.JudgeRolePrimary])
	assert.Zero(t, provider.calls[contracts.JudgeRoleSecondary])
}

// step30 appends new version rows without truncating old rows, so a valid
// resume must inspect the collapsed latest rows rather than rejecting the
// file because historical old-version rows still exist on disk.
func TestStep30Score_IgnoresHistoricalOldVersionsAfterAppendOnlyRerun(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			score := 80
			if role == contracts.JudgeRoleSecondary {
				score = 79
			}
			return makeJudgeOutput(input, role, score, []ruleVerdict{{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant}})
		},
	}

	first := New(WithPanelProvider(provider), WithPromptVersion("prompt-v1"))
	setStepRubric(t, first, "rule-a")
	require.NoError(t, first.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	provider.reset()
	second := New(WithPanelProvider(provider), WithPromptVersion("prompt-v2"))
	setStepRubric(t, second, "rule-a")
	require.NoError(t, second.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))
	assert.Equal(t, 3, provider.calls[contracts.JudgeRolePrimary])
	assert.Zero(t, provider.calls[contracts.JudgeRoleSecondary])

	provider.reset()
	third := New(WithPanelProvider(provider), WithPromptVersion("prompt-v2"))
	setStepRubric(t, third, "rule-a")
	require.NoError(t, third.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	assert.Zero(t, provider.calls[contracts.JudgeRolePrimary])
	assert.Zero(t, provider.calls[contracts.JudgeRoleSecondary])
	assert.Zero(t, provider.calls[contracts.JudgeRoleArbiter])
}

func TestStep30Score_FailsClosedOnMalformedManifest(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	manifestPath, err := runCtx.ManifestPath(1, "a1")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(manifestPath, []byte(`{"kind":"success","kind":"error"}`+"\n"), 0o644))

	err = New().Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg})
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrDuplicateJSONKey)
}

func TestStep30Score_ResumeRerunsWhenRawComplianceIsMissing(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			score := 80
			if role == contracts.JudgeRoleSecondary {
				score = 79
			}
			return makeJudgeOutput(input, role, score, []ruleVerdict{{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant}})
		},
	}

	step := New(WithPanelProvider(provider))
	setStepRubric(t, step, "rule-a")
	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	complianceRawPath, err := runCtx.ResolveRunRelative("30/compliance-A-raw.jsonl")
	require.NoError(t, err)
	require.NoError(t, os.Remove(complianceRawPath))

	provider.reset()
	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	assert.Equal(t, 3, provider.calls[contracts.JudgeRolePrimary])
	assert.Zero(t, provider.calls[contracts.JudgeRoleSecondary])
}

func TestStep30Score_AllowsEmptyComplianceWithSingleJudge(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	rubricPath := filepath.Join(t.TempDir(), "rubric.md")
	require.NoError(t, os.WriteFile(rubricPath, []byte("# custom rubric without compliance rules\n"), 0o644))
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			score := 80
			switch role {
			case contracts.JudgeRoleSecondary:
				score = 60
			case contracts.JudgeRoleArbiter:
				score = 75
			}
			return makeJudgeOutput(input, role, score, nil)
		},
	}

	step := New(WithPanelProvider(provider))
	step.rubricPathFn = func(internalio.RunContext) (string, error) {
		return rubricPath, nil
	}
	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	markerPath, err := runCtx.ResolveRunRelative("30/done.marker")
	require.NoError(t, err)
	marker, err := internalio.ReadJSON[contracts.Step30DoneMarker](markerPath)
	require.NoError(t, err)
	assert.Equal(t, int64(15), marker.ExpectedCounts.Scores)
	assert.Equal(t, int64(0), marker.ExpectedCounts.Compliance)
	assert.Zero(t, provider.calls[contracts.JudgeRoleArbiter])

	complianceFinalPath, err := runCtx.ResolveRunRelative("30/compliance-A.jsonl")
	require.NoError(t, err)
	complianceFinal, err := internalio.ReadJSONL[contracts.ComplianceEntry](complianceFinalPath)
	require.NoError(t, err)
	assert.Empty(t, complianceFinal)
}

func TestStep30Score_DiscardsStaleFinalComplianceWhenRebuildIsStrictEmpty(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	rubricPath := filepath.Join(t.TempDir(), "rubric.md")
	require.NoError(t, os.WriteFile(rubricPath, []byte("## Active Rule IDs\n- rule-a\n"), 0o644))
	mode := "disputed"
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			score := 80
			rules := []ruleVerdict{{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant}}
			switch role {
			case contracts.JudgeRoleSecondary:
				score = 60
				if mode == "disputed" {
					rules = []ruleVerdict{{ruleID: "rule-a", verdict: contracts.ComplianceVerdictViolated}}
				}
			case contracts.JudgeRoleArbiter:
				score = 75
			}
			if mode == "empty" {
				rules = nil
			}
			return makeJudgeOutput(input, role, score, rules)
		},
	}

	step := New(WithPanelProvider(provider))
	step.rubricPathFn = func(internalio.RunContext) (string, error) {
		return rubricPath, nil
	}
	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	markerPath, err := runCtx.ResolveRunRelative("30/done.marker")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(markerPath, []byte("stale marker\n"), 0o644))
	complianceRawPath, err := runCtx.ResolveRunRelative("30/compliance-A-raw.jsonl")
	require.NoError(t, err)
	complianceRawBefore, err := internalio.ReadJSONL[contracts.RawComplianceEntry](complianceRawPath)
	require.NoError(t, err)
	require.NotEmpty(t, complianceRawBefore)

	mode = "empty"
	require.NoError(t, os.WriteFile(rubricPath, []byte("# custom rubric without compliance rules\n"), 0o644))
	for _, agent := range []contracts.AgentID{"a1", "a2", "a3"} {
		manifest, manifestErr := internalio.LoadScorableManifest(runCtx, 1, agent)
		require.NoError(t, manifestErr)
		writeRel(t, runCtx, manifest.DiffPath, "strict-empty fixture diff for "+string(agent)+"\n")
	}
	provider.reset()

	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))
	assert.Equal(t, 3, provider.calls[contracts.JudgeRolePrimary])
	assert.Zero(t, provider.calls[contracts.JudgeRoleSecondary])
	assert.Zero(t, provider.calls[contracts.JudgeRoleArbiter])

	complianceFinalPath, err := runCtx.ResolveRunRelative("30/compliance-A.jsonl")
	require.NoError(t, err)
	complianceFinal, err := internalio.ReadJSONL[contracts.ComplianceEntry](complianceFinalPath)
	require.NoError(t, err)
	assert.Empty(t, complianceFinal)

	complianceRaw, err := internalio.ReadJSONL[contracts.RawComplianceEntry](complianceRawPath)
	require.NoError(t, err)
	assert.Equal(t, complianceRawBefore, complianceRaw)

	marker, err := internalio.ReadJSON[contracts.Step30DoneMarker](markerPath)
	require.NoError(t, err)
	assert.Equal(t, int64(0), marker.ExpectedCounts.Compliance)

	provider.reset()
	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))
	assert.Zero(t, provider.calls[contracts.JudgeRolePrimary])
	assert.Zero(t, provider.calls[contracts.JudgeRoleSecondary])
	assert.Zero(t, provider.calls[contracts.JudgeRoleArbiter])
}

func TestStep30Score_DiscardsStaleFinalComplianceAfterInvalidMarkerRuleShrink(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	rubricPath := filepath.Join(t.TempDir(), "rubric.md")
	require.NoError(t, os.WriteFile(rubricPath, []byte("## Active Rule IDs\n- rule-a\n- rule-b\n"), 0o644))

	currentVerdicts := []ruleVerdict{
		{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant},
		{ruleID: "rule-b", verdict: contracts.ComplianceVerdictViolated},
	}
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			score := 80
			if role == contracts.JudgeRoleSecondary {
				score = 79
			}
			verdicts := append([]ruleVerdict(nil), currentVerdicts...)
			return makeJudgeOutput(input, role, score, verdicts)
		},
	}

	step := New(WithPanelProvider(provider))
	step.rubricPathFn = func(internalio.RunContext) (string, error) {
		return rubricPath, nil
	}
	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	markerPath, err := runCtx.ResolveRunRelative("30/done.marker")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(markerPath, []byte("stale marker\n"), 0o644))

	require.NoError(t, os.WriteFile(rubricPath, []byte("## Active Rule IDs\n- rule-a\n"), 0o644))
	currentVerdicts = []ruleVerdict{
		{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant},
	}
	provider.reset()

	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))
	assert.Equal(t, 3, provider.calls[contracts.JudgeRolePrimary])
	assert.Zero(t, provider.calls[contracts.JudgeRoleSecondary])

	complianceFinalPath, err := runCtx.ResolveRunRelative("30/compliance-A.jsonl")
	require.NoError(t, err)
	complianceFinal, err := internalio.ReadJSONL[contracts.ComplianceEntry](complianceFinalPath)
	require.NoError(t, err)
	require.Len(t, complianceFinal, 3)
	collapsed := scorecore.CollapseFinalCompliance(complianceFinal)
	require.Len(t, collapsed, 3)

	ruleCounts := map[string]int{}
	for _, row := range complianceFinal {
		ruleCounts[row.RuleID]++
	}
	assert.Equal(t, map[string]int{"rule-a": 3}, ruleCounts)

	complianceRawPath, err := runCtx.ResolveRunRelative("30/compliance-A-raw.jsonl")
	require.NoError(t, err)
	complianceRaw, err := internalio.ReadJSONL[contracts.RawComplianceEntry](complianceRawPath)
	require.NoError(t, err)
	require.Len(t, complianceRaw, 9)
	rawRuleCounts := map[string]int{}
	for _, row := range complianceRaw {
		rawRuleCounts[row.RuleID]++
	}
	assert.Equal(t, map[string]int{"rule-a": 6, "rule-b": 3}, rawRuleCounts)

	marker, err := internalio.ReadJSON[contracts.Step30DoneMarker](markerPath)
	require.NoError(t, err)
	assert.Equal(t, int64(3), marker.ExpectedCounts.Compliance)

	provider.reset()
	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))
	assert.Zero(t, provider.calls[contracts.JudgeRolePrimary])
	assert.Zero(t, provider.calls[contracts.JudgeRoleSecondary])
	assert.Zero(t, provider.calls[contracts.JudgeRoleArbiter])
}

func TestStep30Score_DiscardsStaleFinalComplianceAfterMarkerlessRuleShrink(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	rubricPath := filepath.Join(t.TempDir(), "rubric.md")
	require.NoError(t, os.WriteFile(rubricPath, []byte("## Active Rule IDs\n- rule-a\n- rule-b\n"), 0o644))

	currentVerdicts := []ruleVerdict{
		{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant},
		{ruleID: "rule-b", verdict: contracts.ComplianceVerdictViolated},
	}
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			score := 80
			if role == contracts.JudgeRoleSecondary {
				score = 79
			}
			return makeJudgeOutput(input, role, score, append([]ruleVerdict(nil), currentVerdicts...))
		},
	}

	step := New(WithPanelProvider(provider))
	step.rubricPathFn = func(internalio.RunContext) (string, error) {
		return rubricPath, nil
	}
	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	markerPath, err := runCtx.ResolveRunRelative("30/done.marker")
	require.NoError(t, err)
	require.NoError(t, os.Remove(markerPath))

	require.NoError(t, os.WriteFile(rubricPath, []byte("## Active Rule IDs\n- rule-a\n"), 0o644))
	currentVerdicts = []ruleVerdict{{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant}}
	provider.reset()

	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))
	assert.Equal(t, 3, provider.calls[contracts.JudgeRolePrimary])
	assert.Zero(t, provider.calls[contracts.JudgeRoleSecondary])

	complianceFinalPath, err := runCtx.ResolveRunRelative("30/compliance-A.jsonl")
	require.NoError(t, err)
	complianceFinal, err := internalio.ReadJSONL[contracts.ComplianceEntry](complianceFinalPath)
	require.NoError(t, err)
	require.Len(t, complianceFinal, 3)
	assert.Equal(t, map[string]int{"rule-a": 3}, finalComplianceRuleCounts(complianceFinal))

	complianceRawPath, err := runCtx.ResolveRunRelative("30/compliance-A-raw.jsonl")
	require.NoError(t, err)
	complianceRaw, err := internalio.ReadJSONL[contracts.RawComplianceEntry](complianceRawPath)
	require.NoError(t, err)
	require.Len(t, complianceRaw, 9)
	assert.Equal(t, map[string]int{"rule-a": 6, "rule-b": 3}, rawComplianceRuleCounts(complianceRaw))

	marker, err := internalio.ReadJSON[contracts.Step30DoneMarker](markerPath)
	require.NoError(t, err)
	assert.Equal(t, int64(3), marker.ExpectedCounts.Compliance)
	assert.Equal(t, []contracts.AgentID{"a1", "a2", "a3"}, marker.CompletedAgents)
}

func TestStep30Score_ValidMarkerDoesNotCertifyFinalRowsOutsideCurrentScorableSet(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			score := 80
			if role == contracts.JudgeRoleSecondary {
				score = 79
			}
			return makeJudgeOutput(input, role, score, []ruleVerdict{{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant}})
		},
	}

	step := New(WithPanelProvider(provider))
	setStepRubric(t, step, "rule-a")
	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	a3ManifestPath, err := runCtx.ManifestPath(1, "a3")
	require.NoError(t, err)
	require.NoError(t, os.Remove(a3ManifestPath))
	provider.reset()

	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))
	assert.Zero(t, provider.calls[contracts.JudgeRolePrimary])
	assert.Zero(t, provider.calls[contracts.JudgeRoleSecondary])
	assert.Zero(t, provider.calls[contracts.JudgeRoleArbiter])

	scoreFinalPath, err := runCtx.ResolveRunRelative("30/scores-A.jsonl")
	require.NoError(t, err)
	scoreFinal, err := internalio.ReadJSONL[contracts.ScoreEntry](scoreFinalPath)
	require.NoError(t, err)
	require.Len(t, scoreFinal, 10)
	assert.NotContains(t, finalScoreAgents(scoreFinal), contracts.AgentID("a3"))

	complianceFinalPath, err := runCtx.ResolveRunRelative("30/compliance-A.jsonl")
	require.NoError(t, err)
	complianceFinal, err := internalio.ReadJSONL[contracts.ComplianceEntry](complianceFinalPath)
	require.NoError(t, err)
	require.Len(t, complianceFinal, 2)
	assert.NotContains(t, finalComplianceAgents(complianceFinal), contracts.AgentID("a3"))

	scoreRawPath, err := runCtx.ResolveRunRelative("30/scores-A-raw.jsonl")
	require.NoError(t, err)
	scoreRaw, err := internalio.ReadJSONL[contracts.RawScoreEntry](scoreRawPath)
	require.NoError(t, err)
	assert.Contains(t, rawScoreAgents(scoreRaw), contracts.AgentID("a3"))

	markerPath, err := runCtx.ResolveRunRelative("30/done.marker")
	require.NoError(t, err)
	marker, err := internalio.ReadJSON[contracts.Step30DoneMarker](markerPath)
	require.NoError(t, err)
	assert.Equal(t, []contracts.AgentID{"a1", "a2"}, marker.CompletedAgents)
	assert.Equal(t, int64(10), marker.ExpectedCounts.Scores)
	assert.Equal(t, int64(2), marker.ExpectedCounts.Compliance)
}
