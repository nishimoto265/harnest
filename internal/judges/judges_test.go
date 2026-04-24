package judges

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStubJudgeScoreOutputReturnsValidFixture(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		judge       Judge
		wantArbiter bool
	}{
		{name: "primary", judge: NewPrimaryStub(), wantArbiter: false},
		{name: "secondary", judge: NewSecondaryStub(), wantArbiter: false},
		{name: "arbiter", judge: NewArbiterStub(), wantArbiter: true},
	}

	input := JudgeInput{
		RunID:      "2026-04-21-PR42-abcdef0",
		Pass:       1,
		Agent:      "a1",
		OutputPath: "/tmp/auto-improve/output.patch",
		RubricPath: "/tmp/auto-improve/rubrics/default.md",
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			output, err := tt.judge.ScoreOutput(context.Background(), input)
			require.NoError(t, err)
			require.NoError(t, output.ValidateFor(input))

			assert.Len(t, output.Scores, 5)
			assert.NotEmpty(t, output.Compliance)
			assert.Equal(t, tt.wantArbiter, output.Arbiter)
		})
	}
}

func TestJudgeOutputValidate_RejectsDuplicateComplianceRuleIDs(t *testing.T) {
	input := JudgeInput{
		RunID:      "2026-04-21-PR42-abcdef0",
		Pass:       1,
		Agent:      "a1",
		OutputPath: "/tmp/auto-improve/output.patch",
		RubricPath: "/tmp/auto-improve/rubrics/default.md",
	}
	output, err := NewPrimaryStub().ScoreOutput(context.Background(), input)
	require.NoError(t, err)
	require.Len(t, output.Compliance, 1)

	duplicate := output.Compliance[0]
	duplicate.Verdict = contracts.ComplianceVerdictViolated
	duplicate.ResolvedAt = duplicate.ResolvedAt.Add(time.Second)
	output.Compliance = append(output.Compliance, duplicate)

	err = output.ValidateFor(input)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrJudgeOutputDuplicateCompliance)
}

func TestJudgeOutputValidateFor_RejectsMissingExpectedComplianceRule(t *testing.T) {
	input := JudgeInput{
		RunID:                     "2026-04-21-PR1-deadbee",
		Pass:                      1,
		Agent:                     "a1",
		OutputPath:                filepath.Join(t.TempDir(), "out.patch"),
		RubricPath:                filepath.Join(t.TempDir(), "rubric.md"),
		ExpectedComplianceRuleIDs: []string{"active-rule", "candidate-rule"},
	}
	require.NoError(t, os.WriteFile(input.OutputPath, []byte("diff"), 0o644))
	require.NoError(t, os.WriteFile(input.RubricPath, []byte("# rubric"), 0o644))

	output, err := NewPrimaryStub().ScoreOutput(context.Background(), input)
	require.NoError(t, err)
	output.Compliance = output.Compliance[:1]

	err = output.ValidateFor(input)
	assert.ErrorIs(t, err, ErrJudgeOutputMissingCompliance)
	assert.ErrorContains(t, err, "candidate-rule")
}
