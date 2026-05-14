package step60_scorepairwise

import (
	"context"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/nishimoto265/harnest/internal/judges"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPairwiseEntriesFromDecision_NormalizesTieMargin(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{agents: []contracts.AgentID{"a1", "a2", "a3"}})
	resolvedAt := time.Date(2026, 4, 21, 15, 4, 5, 0, time.UTC)

	entries, err := pairwiseEntriesFromDecision(Input{
		IO:                    runIO,
		TaskPackage:           &pkg,
		PairwiseMode:          judges.PairwiseModeBasic,
		RubricVersion:         "default",
		PairwisePromptVersion: "pairwise-test",
	}, []judges.PairwisePair{{
		Agent: "a1",
	}}, judges.PairwiseDecision{
		Action: judges.PairwiseDecisionInconclusive,
		AgentDecisions: []judges.PairwiseAgentDecision{{
			Agent:         "a1",
			Winner:        contracts.PairwiseWinnerB,
			Margin:        contracts.PairwiseMarginDecisive,
			Justification: "pass2 won local comparison",
		}},
	}, nil, resolvedAt)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, contracts.PairwiseWinnerTie, entries[0].Winner)
	assert.Equal(t, contracts.PairwiseMarginSlight, entries[0].Margin)
}

func TestRun_PairwiseModesControlJudgeFanout(t *testing.T) {
	tests := []struct {
		name            string
		mode            judges.PairwiseMode
		wantComparisons int
		wantOrders      []string
	}{
		{
			name:            "single",
			mode:            judges.PairwiseModeSingle,
			wantComparisons: 0,
		},
		{
			name:            "basic",
			mode:            judges.PairwiseModeBasic,
			wantComparisons: 3,
			wantOrders:      []string{"a1:AB", "a2:AB", "a3:AB"},
		},
		{
			name:            "strict",
			mode:            judges.PairwiseModeStrict,
			wantComparisons: 6,
			wantOrders:      []string{"a1:AB", "a1:BA", "a2:AB", "a2:BA", "a3:AB", "a3:BA"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runIO, pkg := seedStep60Fixture(t, fixtureOptions{
				agents:          []contracts.AgentID{"a1", "a2", "a3"},
				writePass1Score: true,
			})
			pairwiseJudge := &recordingPairwiseJudge{}
			decisionJudge := &recordingPairwiseDecisionJudge{}

			require.NoError(t, Run(context.Background(), Input{
				IO:                    runIO,
				TaskPackage:           &pkg,
				PairwiseMode:          tt.mode,
				PairwiseJudge:         pairwiseJudge,
				PairwiseDecisionJudge: decisionJudge,
				Now:                   func() time.Time { return time.Date(2026, 4, 21, 15, 4, 5, 0, time.UTC) },
			}))

			assert.Equal(t, tt.wantOrders, pairwiseJudge.orders)
			assert.Equal(t, 1, decisionJudge.calls)
			assert.Equal(t, tt.mode, decisionJudge.mode)
			assert.Equal(t, 3, decisionJudge.pairCount)
			assert.Equal(t, tt.wantComparisons, decisionJudge.comparisonCount)

			pairwise := mustReadJSONL[contracts.PairwiseEntry](t, runIO, "60/pairwise.jsonl")
			require.Len(t, pairwise, 3)
			for _, entry := range pairwise {
				assert.Equal(t, contracts.PairwiseWinnerB, entry.Winner)
				assert.Equal(t, contracts.PairwiseMarginClear, entry.Margin)
				assert.Contains(t, entry.Justification, "mode="+string(tt.mode)+" decision=adopt")
				assert.Contains(t, entry.Justification, "final=recorded final decision")
			}
		})
	}
}
