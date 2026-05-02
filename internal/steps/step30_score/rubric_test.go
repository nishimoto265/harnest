package step30_score

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStep30Score_RejectsMissingExpectedActiveComplianceRule(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	rubricPath := filepath.Join(t.TempDir(), "rubric.md")
	require.NoError(t, os.WriteFile(rubricPath, []byte("## Active Rule IDs\n- active-rule\n"), 0o644))

	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			return makeJudgeOutput(input, role, 80, nil)
		},
	}
	step := New(WithPanelProvider(provider))
	step.rubricPathFn = func(internalio.RunContext) (string, error) {
		return rubricPath, nil
	}

	err := step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg})
	require.ErrorIs(t, err, judges.ErrJudgeOutputMissingCompliance)
	assert.ErrorContains(t, err, "active-rule")
}

func TestStep30Score_EnforcesDefaultFallbackComplianceRule(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	var seenExpected []string
	var seenStrict bool
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			if role == contracts.JudgeRolePrimary && input.Agent == "a1" {
				seenExpected = append([]string(nil), input.ExpectedComplianceRuleIDs...)
				seenStrict = input.EnforceExpectedCompliance
			}
			return makeJudgeOutput(input, role, 80, nil)
		},
	}

	err := New(WithPanelProvider(provider)).Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg})
	require.ErrorIs(t, err, judges.ErrJudgeOutputMissingCompliance)
	assert.Equal(t, []string{"stub-rubric-rule"}, seenExpected)
	assert.True(t, seenStrict)
}

// F16 regression: the judge must see the exact bytes that output_sha256
// was computed over. We verify that (a) JudgeInput.OutputPath is not the
// live manifest diff, and (b) rewriting the original diff after step30 has
// snapshotted it leaves the snapshot — and therefore the judge view —
// byte-identical to what was hashed.
func TestStep30Score_JudgeSeesPinnedSnapshotBytes(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})

	var seenPath string
	var seenBytes []byte
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			if role == contracts.JudgeRolePrimary && input.Agent == "a1" {
				seenPath = input.OutputPath
				if data, err := os.ReadFile(input.OutputPath); err == nil {
					seenBytes = append([]byte(nil), data...)
				}
			}
			return makeJudgeOutput(input, role, 80, []ruleVerdict{{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant}})
		},
	}

	// Locate the live diff path before Run executes.
	manifest, err := internalio.LoadScorableManifest(runCtx, 1, "a1")
	require.NoError(t, err)
	liveDiff, err := runCtx.ResolveRunRelative(manifest.DiffPath)
	require.NoError(t, err)
	liveBefore, err := os.ReadFile(liveDiff)
	require.NoError(t, err)

	step := New(WithPanelProvider(provider))
	setStepRubric(t, step, "rule-a")
	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	assert.NotEqual(t, liveDiff, seenPath, "judge must not be handed the live manifest diff")
	assert.Contains(t, seenPath, "30/snapshots/", "OutputPath must live under the pinned snapshot dir")
	assert.Equal(t, liveBefore, seenBytes, "judge-observed bytes must match the bytes that output_sha256 hashed")

	// Mutate the live diff after step30 completes — snapshot must be
	// immune so subsequent resumes score the same bytes.
	require.NoError(t, os.WriteFile(liveDiff, []byte("mutated by attacker\n"), 0o644))
	snapshotBytes, err := os.ReadFile(seenPath)
	require.NoError(t, err)
	assert.Equal(t, liveBefore, snapshotBytes, "post-run snapshot must be unaffected by live-diff mutation")
}

func TestStep30Score_RerunsWhenRubricContentChanges(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	rubricPath := filepath.Join(t.TempDir(), "rubric.md")
	require.NoError(t, os.WriteFile(rubricPath, []byte("# rubric v1\n"), 0o644))
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			score := 80
			if role == contracts.JudgeRoleSecondary {
				score = 79
			}
			return makeJudgeOutput(input, role, score, nil)
		},
	}
	step := New(WithPanelProvider(provider))
	step.rubricPathFn = func(internalio.RunContext) (string, error) {
		return rubricPath, nil
	}

	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))
	provider.reset()
	require.NoError(t, os.WriteFile(rubricPath, []byte("# rubric v2\n"), 0o644))
	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	assert.Equal(t, 3, provider.calls[contracts.JudgeRolePrimary])
	assert.Zero(t, provider.calls[contracts.JudgeRoleSecondary])
}

func TestStep30Score_UsesBundledRubricPathWithoutTouchingLegacySymlink(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	legacyDir := filepath.Join(runCtx.RunsBase, ".rubrics")
	require.NoError(t, os.MkdirAll(legacyDir, 0o755))
	target := filepath.Join(t.TempDir(), "legacy-target.md")
	require.NoError(t, os.WriteFile(target, []byte("sentinel\n"), 0o644))
	legacyPath := filepath.Join(legacyDir, "default.md")
	require.NoError(t, os.Symlink(target, legacyPath))

	var rubricPaths []string
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			rubricPaths = append(rubricPaths, input.RubricPath)
			score := 80
			if role == contracts.JudgeRoleSecondary {
				score = 79
			}
			verdicts := make([]ruleVerdict, 0, len(input.ExpectedComplianceRuleIDs))
			for _, ruleID := range input.ExpectedComplianceRuleIDs {
				verdicts = append(verdicts, ruleVerdict{ruleID: ruleID, verdict: contracts.ComplianceVerdictCompliant})
			}
			return makeJudgeOutput(input, role, score, verdicts)
		},
	}

	require.NoError(t, New(WithPanelProvider(provider)).Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	bundledPath, err := judges.DefaultRubricPath()
	require.NoError(t, err)
	require.NotEmpty(t, rubricPaths)
	for _, seen := range rubricPaths {
		assert.Equal(t, bundledPath, seen)
	}
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "sentinel\n", string(data))
	linkTarget, err := os.Readlink(legacyPath)
	require.NoError(t, err)
	assert.Equal(t, target, linkTarget)
}
