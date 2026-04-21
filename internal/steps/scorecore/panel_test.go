package scorecore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPanelResolver_Resolve(t *testing.T) {
	runCtx := newTestRunContext(t)
	input := testJudgeInput(runCtx.RunID)
	outputSha := testSha256Hex

	baseScores := func(role judges.Role, score int) judges.JudgeOutput {
		return judges.JudgeOutput{
			Scores: allDimScores(runCtx.RunID, role, score),
			Compliance: []contracts.ComplianceEntry{
				complianceEntry(runCtx.RunID, "rule-a", contracts.ComplianceVerdictCompliant),
			},
			Arbiter: role == judges.RoleArbiter,
		}
	}

	t.Run("agreement", func(t *testing.T) {
		primary := fakeJudge{out: baseScores(judges.RolePrimary, 80)}
		secondary := fakeJudge{out: baseScores(judges.RoleSecondary, 79)}
		arbiter := fakeJudge{out: baseScores(judges.RoleArbiter, 90), fail: true}

		r := NewPanelResolver()
		result, err := r.Resolve(context.Background(), PanelInput{
			Primary:               primary,
			Secondary:             secondary,
			Arbiter:               arbiter,
			JudgeInput:            input,
			OutputSha256:          outputSha,
			DisagreementThreshold: 5,
			RunContext:            runCtx,
			StepDir:               "30",
		})
		require.NoError(t, err)
		assert.Equal(t, contracts.VerdictPathAgreement, result.VerdictPath)
		assert.Len(t, result.FinalScores, 5)
		assert.Len(t, result.FinalCompliance, 1)
		assert.Len(t, result.RawScores, 10)    // primary + secondary
		assert.Len(t, result.RawCompliance, 2) // primary + secondary
		for _, raw := range result.RawScores {
			assert.Nil(t, raw.PrimaryRef)
			assert.Nil(t, raw.SecondaryRef)
		}
	})

	t.Run("arbitrated", func(t *testing.T) {
		primary := fakeJudge{out: baseScores(judges.RolePrimary, 80)}
		secondary := fakeJudge{out: baseScores(judges.RoleSecondary, 60)}
		arbiter := fakeJudge{out: baseScores(judges.RoleArbiter, 75)}

		r := NewPanelResolver()
		result, err := r.Resolve(context.Background(), PanelInput{
			Primary:               primary,
			Secondary:             secondary,
			Arbiter:               arbiter,
			JudgeInput:            input,
			OutputSha256:          outputSha,
			DisagreementThreshold: 5,
			RunContext:            runCtx,
			StepDir:               "30",
		})
		require.NoError(t, err)
		assert.Equal(t, contracts.VerdictPathArbitrated, result.VerdictPath)
		assert.Len(t, result.RawScores, 15)    // primary + secondary + arbiter
		assert.Len(t, result.RawCompliance, 3) // primary + secondary + arbiter
		var arbiterRows int
		for _, raw := range result.RawScores {
			if raw.JudgeRole == contracts.JudgeRoleArbiter {
				arbiterRows++
				require.NotNil(t, raw.PrimaryRef)
				require.NotNil(t, raw.SecondaryRef)
				assert.Equal(t, contracts.JudgeRolePrimary, raw.PrimaryRef.Role)
				assert.Equal(t, contracts.JudgeRoleSecondary, raw.SecondaryRef.Role)
			}
		}
		assert.Equal(t, 5, arbiterRows)
	})

	t.Run("compliance disagreement invokes arbiter", func(t *testing.T) {
		primaryOut := baseScores(judges.RolePrimary, 80)
		secondaryOut := baseScores(judges.RoleSecondary, 80)
		secondaryOut.Compliance[0].Verdict = contracts.ComplianceVerdictViolated
		arbiterOut := baseScores(judges.RoleArbiter, 80)
		arbiterOut.Compliance[0].Verdict = contracts.ComplianceVerdictCompliant

		r := NewPanelResolver()
		result, err := r.Resolve(context.Background(), PanelInput{
			Primary:               fakeJudge{out: primaryOut},
			Secondary:             fakeJudge{out: secondaryOut},
			Arbiter:               fakeJudge{out: arbiterOut},
			JudgeInput:            input,
			OutputSha256:          outputSha,
			DisagreementThreshold: 5,
			RunContext:            runCtx,
			StepDir:               "30",
		})
		require.NoError(t, err)
		assert.Equal(t, contracts.VerdictPathArbitrated, result.VerdictPath)
		assert.Len(t, result.RawScores, 15)
		assert.Len(t, result.RawCompliance, 3)
		require.Len(t, result.FinalCompliance, 1)
		assert.Equal(t, contracts.ComplianceVerdictCompliant, result.FinalCompliance[0].Verdict)
	})

	t.Run("panel input versions override judge output", func(t *testing.T) {
		primaryOut := baseScores(judges.RolePrimary, 80)
		for i := range primaryOut.Scores {
			primaryOut.Scores[i].RubricVersion = "judge-rubric"
			primaryOut.Scores[i].PromptVersion = "judge-prompt"
		}
		primaryOut.Compliance[0].RubricVersion = "judge-rubric"
		primaryOut.Compliance[0].PromptVersion = "judge-prompt"

		r := NewPanelResolver()
		result, err := r.Resolve(context.Background(), PanelInput{
			Primary:               fakeJudge{out: primaryOut},
			JudgeInput:            input,
			OutputSha256:          outputSha,
			RubricVersion:         "step-rubric",
			PromptVersion:         "step-prompt",
			DisagreementThreshold: 5,
			RunContext:            runCtx,
			StepDir:               "30",
		})
		require.NoError(t, err)
		require.Len(t, result.RawScores, 5)
		require.Len(t, result.FinalScores, 5)
		for _, row := range result.RawScores {
			assert.Equal(t, "step-rubric", row.RubricVersion)
			assert.Equal(t, "step-prompt", row.PromptVersion)
		}
		for _, row := range result.FinalScores {
			assert.Equal(t, "step-rubric", row.RubricVersion)
			assert.Equal(t, "step-prompt", row.PromptVersion)
		}
		require.Len(t, result.RawCompliance, 1)
		require.Len(t, result.FinalCompliance, 1)
		assert.Equal(t, "step-rubric", result.RawCompliance[0].RubricVersion)
		assert.Equal(t, "step-prompt", result.RawCompliance[0].PromptVersion)
		assert.Equal(t, "step-rubric", result.FinalCompliance[0].RubricVersion)
		assert.Equal(t, "step-prompt", result.FinalCompliance[0].PromptVersion)
	})

	t.Run("arbiter_overruled", func(t *testing.T) {
		primary := fakeJudge{out: baseScores(judges.RolePrimary, 20)}
		secondary := fakeJudge{out: baseScores(judges.RoleSecondary, 25)}
		arbiter := fakeJudge{out: baseScores(judges.RoleArbiter, 90)}

		r := NewPanelResolver()
		result, err := r.Resolve(context.Background(), PanelInput{
			Primary:               primary,
			Secondary:             secondary,
			Arbiter:               arbiter,
			JudgeInput:            input,
			OutputSha256:          outputSha,
			DisagreementThreshold: 1, // tight threshold => decisive overruled
			RunContext:            runCtx,
			StepDir:               "30",
		})
		require.NoError(t, err)
		assert.Equal(t, contracts.VerdictPathArbiterOverruled, result.VerdictPath)
	})

	t.Run("single when secondary nil", func(t *testing.T) {
		primary := fakeJudge{out: baseScores(judges.RolePrimary, 80)}

		r := NewPanelResolver()
		result, err := r.Resolve(context.Background(), PanelInput{
			Primary:               primary,
			JudgeInput:            input,
			OutputSha256:          outputSha,
			DisagreementThreshold: 5,
			RunContext:            runCtx,
			StepDir:               "30",
		})
		require.NoError(t, err)
		assert.Equal(t, contracts.VerdictPathSingle, result.VerdictPath)
		assert.Len(t, result.RawScores, 5)
		assert.Len(t, result.FinalScores, 5)
	})
}

func TestPanelResolver_Resolve_RequiresArbiterOnDisagreement(t *testing.T) {
	runCtx := newTestRunContext(t)
	input := testJudgeInput(runCtx.RunID)

	r := NewPanelResolver()
	_, err := r.Resolve(context.Background(), PanelInput{
		Primary:               fakeJudge{out: judges.JudgeOutput{Scores: allDimScores(runCtx.RunID, judges.RolePrimary, 80), Compliance: []contracts.ComplianceEntry{complianceEntry(runCtx.RunID, "rule-a", contracts.ComplianceVerdictCompliant)}}},
		Secondary:             fakeJudge{out: judges.JudgeOutput{Scores: allDimScores(runCtx.RunID, judges.RoleSecondary, 60), Compliance: []contracts.ComplianceEntry{complianceEntry(runCtx.RunID, "rule-a", contracts.ComplianceVerdictCompliant)}}},
		JudgeInput:            input,
		OutputSha256:          testSha256Hex,
		DisagreementThreshold: 5,
		RunContext:            runCtx,
		StepDir:               "30",
	})
	require.ErrorIs(t, err, ErrPanelArbiterRequired)
}

func TestPanelResolver_Resolve_RejectsMismatchedJudgeIdentity(t *testing.T) {
	runCtx := newTestRunContext(t)
	input := testJudgeInput(runCtx.RunID)
	primaryOut := judges.JudgeOutput{
		Scores:     allDimScores(runCtx.RunID, judges.RolePrimary, 80),
		Compliance: []contracts.ComplianceEntry{complianceEntry(runCtx.RunID, "rule-a", contracts.ComplianceVerdictCompliant)},
	}
	primaryOut = withAgent(primaryOut, contracts.AgentID("a2"))

	r := NewPanelResolver()
	_, err := r.Resolve(context.Background(), PanelInput{
		Primary:               fakeJudge{out: primaryOut},
		JudgeInput:            input,
		OutputSha256:          testSha256Hex,
		DisagreementThreshold: 5,
		RunContext:            runCtx,
		StepDir:               "30",
	})
	require.ErrorIs(t, err, judges.ErrJudgeOutputMissingInput)
}

func TestBuildFinalResultFromRaw_RequiresArbiterOnDisagreement(t *testing.T) {
	runCtx := newTestRunContext(t)
	resolver := NewPanelResolver()
	panelInput := PanelInput{
		JudgeInput:            testJudgeInput(runCtx.RunID),
		OutputSha256:          testSha256Hex,
		DisagreementThreshold: 5,
		RunContext:            runCtx,
		StepDir:               "30",
	}

	primary, err := resolver.ResolveRole(
		context.Background(),
		panelInput,
		contracts.JudgeRolePrimary,
		fakeJudge{out: judges.JudgeOutput{Scores: allDimScores(runCtx.RunID, judges.RolePrimary, 80), Compliance: []contracts.ComplianceEntry{complianceEntry(runCtx.RunID, "rule-a", contracts.ComplianceVerdictCompliant)}}},
		nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)

	secondary, err := resolver.ResolveRole(
		context.Background(),
		panelInput,
		contracts.JudgeRoleSecondary,
		fakeJudge{out: judges.JudgeOutput{Scores: allDimScores(runCtx.RunID, judges.RoleSecondary, 60), Compliance: []contracts.ComplianceEntry{complianceEntry(runCtx.RunID, "rule-a", contracts.ComplianceVerdictCompliant)}}},
		nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)

	_, err = BuildFinalResultFromRaw(
		primary.RawScores,
		secondary.RawScores,
		nil,
		primary.RawCompliance,
		secondary.RawCompliance,
		nil,
		5,
		true,
		false,
	)
	require.ErrorIs(t, err, ErrPanelArbiterRequired)
}

func TestPanelResolver_ResolveRole_RejectsMismatchedJudgeIdentity(t *testing.T) {
	runCtx := newTestRunContext(t)
	resolver := NewPanelResolver()
	panelInput := PanelInput{
		JudgeInput:            testJudgeInput(runCtx.RunID),
		OutputSha256:          testSha256Hex,
		DisagreementThreshold: 5,
		RunContext:            runCtx,
		StepDir:               "30",
	}
	primaryOut := judges.JudgeOutput{
		Scores:     allDimScores(runCtx.RunID, judges.RolePrimary, 80),
		Compliance: []contracts.ComplianceEntry{complianceEntry(runCtx.RunID, "rule-a", contracts.ComplianceVerdictCompliant)},
	}
	primaryOut = withAgent(primaryOut, contracts.AgentID("a2"))

	_, err := resolver.ResolveRole(
		context.Background(),
		panelInput,
		contracts.JudgeRolePrimary,
		fakeJudge{out: primaryOut},
		nil,
		nil,
		nil,
		nil,
	)
	require.ErrorIs(t, err, judges.ErrJudgeOutputMissingInput)
}

func TestWriteOverflowSidecar(t *testing.T) {
	runCtx := newTestRunContext(t)
	longReason := strings.Repeat("Why-", 600) // 2400 chars > 1000

	ref, err := WriteOverflowSidecar(runCtx, "30", longReason)
	require.NoError(t, err)
	require.NoError(t, ref.Validate())
	assert.True(t, strings.HasPrefix(ref.Path, "30/reasons/"))

	abs := filepath.Join(runCtx.RunDir(), ref.Path)
	data, err := internalio.ReadJSON[map[string]any](abs + ".__doesnotexist")
	_ = data
	_ = err
	// Re-read raw bytes through the ReadSidecar equivalent path.
	stored, readErr := readAllBytes(t, abs)
	require.NoError(t, readErr)
	assert.Equal(t, longReason, string(stored))
}

func TestPanelResolver_Overflow(t *testing.T) {
	runCtx := newTestRunContext(t)
	longReason := strings.Repeat("R", ReasonsMaxChars+100)

	primaryOut := judges.JudgeOutput{
		Scores: allDimScores(runCtx.RunID, judges.RolePrimary, 70),
		Compliance: []contracts.ComplianceEntry{
			complianceEntry(runCtx.RunID, "rule-a", contracts.ComplianceVerdictCompliant),
		},
	}
	// Replace first score's reasons with an overflowing blob.
	primaryOut.Scores[0].Reasons = longReason

	r := NewPanelResolver()
	result, err := r.Resolve(context.Background(), PanelInput{
		Primary:               fakeJudge{out: primaryOut},
		JudgeInput:            testJudgeInput(runCtx.RunID),
		OutputSha256:          testSha256Hex,
		DisagreementThreshold: 5,
		RunContext:            runCtx,
		StepDir:               "30",
	})
	require.NoError(t, err)

	require.NotEmpty(t, result.RawScores)
	require.NotNil(t, result.RawScores[0].ReasonsOverflowRef)
	assert.Equal(t, "", result.RawScores[0].Reasons)
	assert.Equal(t, contracts.VerdictPathSingle, result.VerdictPath)
}

// -- helpers -----------------------------------------------------------------

type fakeJudge struct {
	out  judges.JudgeOutput
	fail bool
}

func (f fakeJudge) ScoreOutput(_ context.Context, in judges.JudgeInput) (judges.JudgeOutput, error) {
	if f.fail {
		t := f.out
		// Validate should still succeed; only caller marking `fail` intends
		// this to assert non-invocation. Return the output as-is; the test
		// asserts on VerdictPath to prove arbiter was not consulted.
		return t, nil
	}
	_ = in
	return f.out, nil
}

func newTestRunContext(t *testing.T) internalio.RunContext {
	t.Helper()
	runsBase := filepath.Join(t.TempDir(), "runs")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")
	runCtx, err := internalio.NewRunContext(contracts.RunID("2026-04-21-PR42-abcdef0"), runsBase, worktreeBase)
	require.NoError(t, err)
	return runCtx
}

const testSha256Hex = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

func testJudgeInput(runID contracts.RunID) judges.JudgeInput {
	tmp := filepath.Join("/tmp", "scorecore-test-fake-output.patch")
	return judges.JudgeInput{
		RunID:      runID,
		Pass:       1,
		Agent:      contracts.AgentID("a1"),
		OutputPath: tmp,
		RubricPath: filepath.Join("/tmp", "scorecore-test-fake-rubric.md"),
	}
}

func allDimScores(runID contracts.RunID, role judges.Role, score int) []contracts.ScoreEntry {
	_ = role
	dims := []contracts.Dimension{
		contracts.DimensionFidelity,
		contracts.DimensionCorrectness,
		contracts.DimensionMaintainability,
		contracts.DimensionDiscipline,
		contracts.DimensionCommunication,
	}
	out := make([]contracts.ScoreEntry, 0, len(dims))
	for _, d := range dims {
		out = append(out, contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         runID,
			Pass:          1,
			Agent:         contracts.AgentID("a1"),
			Dimension:     d,
			Score:         score,
			Reasons:       "stub reasoning",
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "default",
			PromptVersion: "phase0-stub",
			ResolvedAt:    time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		})
	}
	return out
}

func complianceEntry(runID contracts.RunID, ruleID string, verdict contracts.ComplianceVerdict) contracts.ComplianceEntry {
	return contracts.ComplianceEntry{
		SchemaVersion: "1",
		RunID:         runID,
		Pass:          1,
		Agent:         contracts.AgentID("a1"),
		RuleID:        ruleID,
		Verdict:       verdict,
		Rationale:     "stub compliant",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "default",
		PromptVersion: "phase0-stub",
		ResolvedAt:    time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
	}
}

func readAllBytes(t *testing.T, path string) ([]byte, error) {
	t.Helper()
	return os.ReadFile(path)
}

func withAgent(out judges.JudgeOutput, agent contracts.AgentID) judges.JudgeOutput {
	for i := range out.Scores {
		out.Scores[i].Agent = agent
	}
	for i := range out.Compliance {
		out.Compliance[i].Agent = agent
	}
	return out
}
