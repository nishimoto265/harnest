package judges

import (
	"context"
	"testing"

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
