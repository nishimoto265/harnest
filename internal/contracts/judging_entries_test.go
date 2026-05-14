package contracts

import (
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/validation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ScoreEntry / ComplianceEntry / PairwiseEntry 最小 validator 動作確認.
func TestScoreEntry_Valid(t *testing.T) {
	s := ScoreEntry{
		SchemaVersion: "1",
		RunID:         "2026-04-20-PR42-abcdef0",
		Pass:          1,
		Agent:         "a1",
		Dimension:     DimensionFidelity,
		Score:         95,
		VerdictPath:   VerdictPathAgreement,
		RubricVersion: "v1",
		PromptVersion: "p1",
		ResolvedAt:    time.Now(),
	}
	assert.NoError(t, validation.Instance().Struct(s))
}

func TestScoreEntry_Reject_ScoreOutOfRange(t *testing.T) {
	s := ScoreEntry{
		SchemaVersion: "1",
		RunID:         "2026-04-20-PR42-abcdef0",
		Pass:          1,
		Agent:         "a1",
		Dimension:     DimensionFidelity,
		Score:         101,
		VerdictPath:   VerdictPathSingle,
		RubricVersion: "v1",
		PromptVersion: "p1",
		ResolvedAt:    time.Now(),
	}
	assert.Error(t, validation.Instance().Struct(s))
}

func TestComplianceEntry_Valid(t *testing.T) {
	e := ComplianceEntry{
		SchemaVersion: "1",
		RunID:         "2026-04-20-PR42-abcdef0",
		Pass:          1,
		Agent:         "a1",
		RuleID:        "r-1",
		Verdict:       ComplianceVerdictValidException,
		VerdictPath:   VerdictPathSingle,
		RubricVersion: "v1",
		PromptVersion: "p1",
		ResolvedAt:    time.Now(),
	}
	assert.NoError(t, validation.Instance().Struct(e))
}

func TestPairwiseEntry_Valid(t *testing.T) {
	p := PairwiseEntry{
		SchemaVersion: "1",
		RunID:         "2026-04-20-PR42-abcdef0",
		AgentA:        "a1",
		AgentB:        "a1",
		Winner:        PairwiseWinnerA,
		Margin:        PairwiseMarginClear,
		VerdictPath:   VerdictPathSingle,
		RubricVersion: "v1",
		PromptVersion: "p1",
		ResolvedAt:    time.Now(),
	}
	assert.NoError(t, p.Validate())
}

func TestPairwiseEntry_Reject_BadWinner(t *testing.T) {
	p := PairwiseEntry{
		SchemaVersion: "1",
		RunID:         "2026-04-20-PR42-abcdef0",
		AgentA:        "a1",
		AgentB:        "a1",
		Winner:        "X",
		Margin:        PairwiseMarginClear,
		VerdictPath:   VerdictPathSingle,
		RubricVersion: "v1",
		PromptVersion: "p1",
		ResolvedAt:    time.Now(),
	}
	assert.Error(t, p.Validate())
}

func TestPairwiseEntry_Reject_CrossAgentComparison(t *testing.T) {
	p := PairwiseEntry{
		SchemaVersion: "1",
		RunID:         "2026-04-20-PR42-abcdef0",
		AgentA:        "a1",
		AgentB:        "a2",
		Winner:        PairwiseWinnerA,
		Margin:        PairwiseMarginClear,
		VerdictPath:   VerdictPathSingle,
		RubricVersion: "v1",
		PromptVersion: "p1",
		ResolvedAt:    time.Now(),
	}
	err := p.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPairwiseAgentMismatch)
}
