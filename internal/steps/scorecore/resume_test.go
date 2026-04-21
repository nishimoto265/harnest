package scorecore

import (
	"testing"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildFinalResultFromRaw_DisagreementRequiresArbiter(t *testing.T) {
	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	primaryScores := buildRawScoresFixture(runID, contracts.JudgeRolePrimary, 80, testSha256Hex)
	secondaryScores := buildRawScoresFixture(runID, contracts.JudgeRoleSecondary, 60, testSha256Hex)
	primaryCompliance := buildRawComplianceFixture(runID, contracts.JudgeRolePrimary, testSha256Hex, []ruleVerdictFixture{
		{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant},
	})
	secondaryCompliance := buildRawComplianceFixture(runID, contracts.JudgeRoleSecondary, testSha256Hex, []ruleVerdictFixture{
		{ruleID: "rule-a", verdict: contracts.ComplianceVerdictViolated},
	})

	_, err := BuildFinalResultFromRaw(
		primaryScores,
		secondaryScores,
		nil,
		primaryCompliance,
		secondaryCompliance,
		nil,
		5,
		true,
		false,
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPanelArbiterRequired)
}

type ruleVerdictFixture struct {
	ruleID  string
	verdict contracts.ComplianceVerdict
}

func buildRawScoresFixture(runID contracts.RunID, role contracts.JudgeRole, score int, outputSha string) []contracts.RawScoreEntry {
	out := make([]contracts.RawScoreEntry, 0, 5)
	for _, dimension := range []contracts.Dimension{
		contracts.DimensionFidelity,
		contracts.DimensionCorrectness,
		contracts.DimensionMaintainability,
		contracts.DimensionDiscipline,
		contracts.DimensionCommunication,
	} {
		out = append(out, contracts.RawScoreEntry{
			SchemaVersion: "1",
			RunID:         runID,
			Pass:          1,
			Agent:         contracts.AgentID("a1"),
			JudgeRole:     role,
			Dimension:     dimension,
			Score:         score,
			Reasons:       "fixture",
			OutputSha256:  outputSha,
			RubricVersion: "default",
			PromptVersion: "phase0-stub",
		})
	}
	return out
}

func buildRawComplianceFixture(runID contracts.RunID, role contracts.JudgeRole, outputSha string, verdicts []ruleVerdictFixture) []contracts.RawComplianceEntry {
	out := make([]contracts.RawComplianceEntry, 0, len(verdicts))
	for _, verdict := range verdicts {
		out = append(out, contracts.RawComplianceEntry{
			SchemaVersion: "1",
			RunID:         runID,
			Pass:          1,
			Agent:         contracts.AgentID("a1"),
			JudgeRole:     role,
			RuleID:        verdict.ruleID,
			Verdict:       verdict.verdict,
			Rationale:     "fixture",
			OutputSha256:  outputSha,
			RubricVersion: "default",
			PromptVersion: "phase0-stub",
		})
	}
	return out
}
