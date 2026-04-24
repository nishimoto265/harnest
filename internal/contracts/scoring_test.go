package contracts

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRawScoreEntry_RoundTrip(t *testing.T) {
	entry := RawScoreEntry{
		SchemaVersion: "1",
		RunID:         "2026-04-20-PR42-abcdef0",
		Pass:          1,
		Agent:         "a1",
		JudgeRole:     JudgeRoleArbiter,
		Dimension:     DimensionFidelity,
		Score:         92,
		Reasons:       "panel resolved",
		OutputSha256:  "0000000000000000000000000000000000000000000000000000000000000001",
		PrimaryRef: &RawJudgeRef{
			Role:   JudgeRolePrimary,
			Sha256: "0000000000000000000000000000000000000000000000000000000000000002",
		},
		SecondaryRef: &RawJudgeRef{
			Role:   JudgeRoleSecondary,
			Sha256: "0000000000000000000000000000000000000000000000000000000000000003",
		},
		RubricVersion: "v1",
		PromptVersion: "p1",
		ResolvedAt:    time.Now(),
	}
	data, err := MarshalStrict(entry)
	require.NoError(t, err)

	var decoded RawScoreEntry
	require.NoError(t, decodeStrict(data, &decoded))
	assert.Equal(t, entry.JudgeRole, decoded.JudgeRole)
	assert.NotNil(t, decoded.PrimaryRef)
	assert.NotNil(t, decoded.SecondaryRef)
}

func TestRawScoreEntry_RejectsArbiterWithoutRefs(t *testing.T) {
	entry := RawScoreEntry{
		SchemaVersion: "1",
		RunID:         "2026-04-20-PR42-abcdef0",
		Pass:          1,
		Agent:         "a1",
		JudgeRole:     JudgeRoleArbiter,
		Dimension:     DimensionFidelity,
		Score:         92,
		OutputSha256:  "0000000000000000000000000000000000000000000000000000000000000001",
		RubricVersion: "v1",
		PromptVersion: "p1",
		ResolvedAt:    time.Now(),
	}
	_, err := MarshalStrict(entry)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRawJudgeRefsRequired)
}

func TestRawScoreEntry_RejectsPrimaryWithRefs(t *testing.T) {
	entry := RawScoreEntry{
		SchemaVersion: "1",
		RunID:         "2026-04-20-PR42-abcdef0",
		Pass:          1,
		Agent:         "a1",
		JudgeRole:     JudgeRolePrimary,
		Dimension:     DimensionFidelity,
		Score:         92,
		OutputSha256:  "0000000000000000000000000000000000000000000000000000000000000001",
		PrimaryRef: &RawJudgeRef{
			Role:   JudgeRolePrimary,
			Sha256: "0000000000000000000000000000000000000000000000000000000000000002",
		},
		RubricVersion: "v1",
		PromptVersion: "p1",
		ResolvedAt:    time.Now(),
	}
	_, err := MarshalStrict(entry)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRawJudgeRefsForbidden)
}

func TestRawComplianceEntry_RoundTrip(t *testing.T) {
	entry := RawComplianceEntry{
		SchemaVersion: "1",
		RunID:         "2026-04-20-PR42-abcdef0",
		Pass:          2,
		Agent:         "a2",
		JudgeRole:     JudgeRoleArbiter,
		RuleID:        "r-1",
		Verdict:       ComplianceVerdictViolated,
		OutputSha256:  "0000000000000000000000000000000000000000000000000000000000000004",
		PrimaryRef: &RawJudgeRef{
			Role:   JudgeRolePrimary,
			Sha256: "0000000000000000000000000000000000000000000000000000000000000005",
		},
		SecondaryRef: &RawJudgeRef{
			Role:   JudgeRoleSecondary,
			Sha256: "0000000000000000000000000000000000000000000000000000000000000006",
		},
		RubricVersion: "v1",
		PromptVersion: "p1",
		ResolvedAt:    time.Now(),
	}
	data, err := MarshalStrict(entry)
	require.NoError(t, err)

	var decoded RawComplianceEntry
	require.NoError(t, decodeStrict(data, &decoded))
	assert.Equal(t, entry.Verdict, decoded.Verdict)
	assert.Equal(t, entry.JudgeRole, decoded.JudgeRole)
}

func TestScoreEntry_Validate_RejectsOverflowRefOutsidePassPrefix(t *testing.T) {
	entry := ScoreEntry{
		SchemaVersion: "1",
		RunID:         "2026-04-20-PR42-abcdef0",
		Pass:          1,
		Agent:         "a1",
		Dimension:     DimensionFidelity,
		Score:         92,
		ReasonsOverflowRef: &OverflowRef{
			Path:   "60/reasons/overflow.txt",
			Sha256: "0000000000000000000000000000000000000000000000000000000000000001",
		},
		VerdictPath:   VerdictPathAgreement,
		RubricVersion: "v1",
		PromptVersion: "p1",
		ResolvedAt:    time.Now(),
	}
	err := entry.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrOverflowRefPathPrefixMismatch)
}

func TestRawComplianceEntry_Validate_RejectsOverflowRefOutsidePassPrefix(t *testing.T) {
	entry := RawComplianceEntry{
		SchemaVersion: "1",
		RunID:         "2026-04-20-PR42-abcdef0",
		Pass:          2,
		Agent:         "a2",
		JudgeRole:     JudgeRolePrimary,
		RuleID:        "r-1",
		Verdict:       ComplianceVerdictViolated,
		RationaleOverflowRef: &OverflowRef{
			Path:   "30/reasons/overflow.txt",
			Sha256: "0000000000000000000000000000000000000000000000000000000000000002",
		},
		OutputSha256:  "0000000000000000000000000000000000000000000000000000000000000003",
		RubricVersion: "v1",
		PromptVersion: "p1",
		ResolvedAt:    time.Now(),
	}
	err := entry.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrOverflowRefPathPrefixMismatch)
}

func TestStep30DoneMarker_RoundTrip(t *testing.T) {
	marker := Step30DoneMarker{
		CompletedAgents: []AgentID{"a1", "a2", "a3"},
		Dimensions:      []Dimension{DimensionFidelity, DimensionCorrectness},
		ExpectedCounts: Step30ExpectedCounts{
			Scores:     10,
			Compliance: 3,
		},
		ContentHashes: Step30DoneContentHashes{
			ScoresFinal:     "0000000000000000000000000000000000000000000000000000000000000007",
			ComplianceFinal: "0000000000000000000000000000000000000000000000000000000000000008",
		},
		RawHashes: StepDoneRawHashes{
			ScoresRaw:     "0000000000000000000000000000000000000000000000000000000000000009",
			ComplianceRaw: "000000000000000000000000000000000000000000000000000000000000000a",
		},
		ResolvedAt: time.Now(),
	}
	data, err := MarshalStrict(marker)
	require.NoError(t, err)

	var decoded Step30DoneMarker
	require.NoError(t, decodeStrict(data, &decoded))
	assert.EqualValues(t, 10, decoded.ExpectedCounts.Scores)
}

func TestStep60DoneMarker_RoundTrip(t *testing.T) {
	marker := Step60DoneMarker{
		CompletedAgents: []AgentID{"a1", "a2", "a3"},
		Dimensions:      []Dimension{DimensionFidelity, DimensionCorrectness},
		ExpectedCounts: Step60ExpectedCounts{
			Scores:     10,
			Compliance: 3,
			Pairwise:   2,
		},
		InputHashes: Step60DoneInputHashes{
			Pass1Scores:        "0000000000000000000000000000000000000000000000000000000000000001",
			Pass1Compliance:    "0000000000000000000000000000000000000000000000000000000000000002",
			Pass2Outputs:       "0000000000000000000000000000000000000000000000000000000000000003",
			CandidateRules:     "0000000000000000000000000000000000000000000000000000000000000004",
			ExpectedCompliance: "0000000000000000000000000000000000000000000000000000000000000005",
		},
		ContentHashes: Step60DoneContentHashes{
			ScoresFinal:     "0000000000000000000000000000000000000000000000000000000000000007",
			ComplianceFinal: "0000000000000000000000000000000000000000000000000000000000000008",
			PairwiseFinal:   "000000000000000000000000000000000000000000000000000000000000000b",
		},
		RawHashes: StepDoneRawHashes{
			ScoresRaw:     "0000000000000000000000000000000000000000000000000000000000000009",
			ComplianceRaw: "000000000000000000000000000000000000000000000000000000000000000a",
		},
		ResolvedAt: time.Now(),
	}
	data, err := MarshalStrict(marker)
	require.NoError(t, err)

	var decoded Step60DoneMarker
	require.NoError(t, decodeStrict(data, &decoded))
	assert.EqualValues(t, 2, decoded.ExpectedCounts.Pairwise)
}

func TestStep60DoneMarker_RejectsMissingPairwiseFinal(t *testing.T) {
	data := []byte(`{
  "completed_agents":["a1","a2","a3"],
  "dimensions":["fidelity","correctness"],
  "expected_counts":{"scores":10,"compliance":3,"pairwise":2},
  "input_hashes":{"pass1_scores":"0000000000000000000000000000000000000000000000000000000000000001","pass1_compliance":"0000000000000000000000000000000000000000000000000000000000000002","pass2_outputs":"0000000000000000000000000000000000000000000000000000000000000003","candidate_rules":"0000000000000000000000000000000000000000000000000000000000000004","expected_compliance":"0000000000000000000000000000000000000000000000000000000000000005"},
  "content_hashes":{"scores_final":"0000000000000000000000000000000000000000000000000000000000000007","compliance_final":"0000000000000000000000000000000000000000000000000000000000000008"},
  "raw_hashes":{"scores_raw":"0000000000000000000000000000000000000000000000000000000000000009","compliance_raw":"000000000000000000000000000000000000000000000000000000000000000a"},
  "resolved_at":"2026-04-20T12:00:00Z"
}`)
	var marker Step60DoneMarker
	assert.Error(t, decodeStrict(data, &marker))
}
