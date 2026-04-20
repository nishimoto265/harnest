package contracts

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/validation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TaskPackage: validator タグが正しく効くかの基本カバレッジ。
func TestTaskPackage_Valid(t *testing.T) {
	pkg := TaskPackage{
		SchemaVersion:           "1",
		RunID:                   "2026-04-20-PR42-abcdef0",
		PR:                      42,
		Title:                   "fix: example",
		BaseSHA:                 "1111111111111111111111111111111111111111",
		BestBranch:              "auto-improve/best",
		ReconstructedTaskPrompt: "hello",
		Worktrees:               make([]WorktreeAllocation, 6),
		CreatedAt:               time.Now(),
	}
	// Populate 6 worktrees minimally.
	for i := range pkg.Worktrees {
		pass := 1
		if i >= 3 {
			pass = 2
		}
		pkg.Worktrees[i] = WorktreeAllocation{
			Agent:   AgentID([]string{"a1", "a2", "a3", "a1", "a2", "a3"}[i]),
			Pass:    pass,
			Path:    "/tmp/wt",
			Branch:  "b",
			BaseSHA: "1111111111111111111111111111111111111111",
			HeadSHA: "1111111111111111111111111111111111111111",
		}
	}
	assert.NoError(t, validation.Instance().Struct(pkg))
}

func TestTaskPackage_Reject_BadRunID(t *testing.T) {
	pkg := TaskPackage{
		SchemaVersion: "1",
		RunID:         "not-a-valid-run-id",
		PR:            1,
		Title:         "x",
		BaseSHA:       "1111111111111111111111111111111111111111",
		BestBranch:    "b",
		Worktrees:     []WorktreeAllocation{},
		CreatedAt:     time.Now(),
	}
	assert.Error(t, validation.Instance().Struct(pkg))
}

func TestTaskPackage_Reject_WrongWorktreeCount(t *testing.T) {
	pkg := TaskPackage{
		SchemaVersion:           "1",
		RunID:                   "2026-04-20-PR42-abcdef0",
		PR:                      1,
		Title:                   "x",
		BaseSHA:                 "1111111111111111111111111111111111111111",
		BestBranch:              "b",
		ReconstructedTaskPrompt: "p",
		Worktrees:               []WorktreeAllocation{}, // len != 6
		CreatedAt:               time.Now(),
	}
	assert.Error(t, validation.Instance().Struct(pkg))
}

// ChecklistResult: 3-symbol verdict.
func TestChecklistResult_Valid(t *testing.T) {
	cr := ChecklistResult{
		SchemaVersion: "1",
		RunID:         "2026-04-20-PR42-abcdef0",
		Pass:          1,
		Agent:         "a1",
		Items: []ChecklistItem{
			{RuleID: "r-1", Verdict: ChecklistItemCompliant},
			{RuleID: "r-2", Verdict: ChecklistItemNA},
			{RuleID: "r-3", Verdict: ChecklistItemException, Rationale: "ok", ExceptionReason: "because"},
		},
	}
	assert.NoError(t, validation.Instance().Struct(cr))
}

func TestChecklistResult_Reject_BadVerdict(t *testing.T) {
	cr := ChecklistResult{
		SchemaVersion: "1",
		RunID:         "2026-04-20-PR42-abcdef0",
		Pass:          1,
		Agent:         "a1",
		Items:         []ChecklistItem{{RuleID: "r-1", Verdict: "wrong"}},
	}
	assert.Error(t, validation.Instance().Struct(cr))
}

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
		AgentB:        "a2",
		Winner:        PairwiseWinnerA,
		Margin:        PairwiseMarginClear,
		VerdictPath:   VerdictPathSingle,
		RubricVersion: "v1",
		PromptVersion: "p1",
		ResolvedAt:    time.Now(),
	}
	assert.NoError(t, validation.Instance().Struct(p))
}

func TestPairwiseEntry_Reject_BadWinner(t *testing.T) {
	p := PairwiseEntry{
		SchemaVersion: "1",
		RunID:         "2026-04-20-PR42-abcdef0",
		AgentA:        "a1",
		AgentB:        "a2",
		Winner:        "X",
		Margin:        PairwiseMarginClear,
		VerdictPath:   VerdictPathSingle,
		RubricVersion: "v1",
		PromptVersion: "p1",
		ResolvedAt:    time.Now(),
	}
	assert.Error(t, validation.Instance().Struct(p))
}

// Candidates schema roundtrip: JSON marshal → strict decode.
func TestCandidates_Roundtrip(t *testing.T) {
	data := `{
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "candidates": [
    {
      "candidate_id": "c1",
      "kind": "new",
      "title": "Prefer clarity",
      "proposed_body_path": "40/candidates/c1.md",
      "proposed_body_sha256": "0000000000000000000000000000000000000000000000000000000000000001"
    }
  ],
  "candidates_hash": "0000000000000000000000000000000000000000000000000000000000000002",
  "created_at": "2026-04-20T13:00:00Z"
}`
	var c Candidates
	require.NoError(t, json.Unmarshal([]byte(data), &c))
	require.NoError(t, validation.Instance().Struct(c))
	assert.Len(t, c.Candidates, 1)
	assert.Equal(t, CandidateKindNew, c.Candidates[0].Kind)
}

func TestClassificationEntry_Valid(t *testing.T) {
	e := ClassificationEntry{
		SchemaVersion:   "1",
		RunID:           "2026-04-20-PR42-abcdef0",
		CandidateID:     "c1",
		Kind:            CandidateKindUpdate,
		SimilarityScore: 80,
		MatchedRuleID:   "r-1",
		ClassifiedAt:    time.Now(),
	}
	assert.NoError(t, validation.Instance().Struct(e))
}

// IntentionRecord
func TestIntentionRecord_Valid_Planning(t *testing.T) {
	i := IntentionRecord{
		SchemaVersion:      "1",
		Stage:              IntentionStagePlanning,
		IdempotencyKey:     "0000000000000000000000000000000000000000000000000000000000000001",
		RunID:              "2026-04-20-PR42-abcdef0",
		BestShaBefore:      "1111111111111111111111111111111111111111",
		TargetSha:          "2222222222222222222222222222222222222222",
		CandidatesHash:     "0000000000000000000000000000000000000000000000000000000000000002",
		RegistryHeadBefore: "",
		StartedAt:          time.Now(),
	}
	assert.NoError(t, validation.Instance().Struct(i))
}

func TestIntentionRecord_Reject_BadStage(t *testing.T) {
	i := IntentionRecord{
		SchemaVersion:  "1",
		Stage:          "bogus",
		IdempotencyKey: "0000000000000000000000000000000000000000000000000000000000000001",
		RunID:          "2026-04-20-PR42-abcdef0",
		BestShaBefore:  "1111111111111111111111111111111111111111",
		TargetSha:      "2222222222222222222222222222222222222222",
		CandidatesHash: "0000000000000000000000000000000000000000000000000000000000000002",
		StartedAt:      time.Now(),
	}
	assert.Error(t, validation.Instance().Struct(i))
}

func TestIntentionRecord_AllStagesEnumerated(t *testing.T) {
	// 仕様: Stage enum は 8 種 (planning / branch_pushed / registry_appended /
	// decision_written / rolling_back_branch_reverted /
	// rolling_back_registry_appended / rolling_back_decision_written /
	// needs_manual_recovery)。enum そのものの validator 動作確認.
	all := []IntentionStage{
		IntentionStagePlanning,
		IntentionStageBranchPushed,
		IntentionStageRegistryAppended,
		IntentionStageDecisionWritten,
		IntentionStageRollingBackBranchReverted,
		IntentionStageRollingBackRegistryAppended,
		IntentionStageRollingBackDecisionWritten,
		IntentionStageNeedsManualRecovery,
	}
	for _, s := range all {
		i := IntentionRecord{
			SchemaVersion:  "1",
			Stage:          s,
			IdempotencyKey: "0000000000000000000000000000000000000000000000000000000000000001",
			RunID:          "2026-04-20-PR42-abcdef0",
			BestShaBefore:  "1111111111111111111111111111111111111111",
			TargetSha:      "2222222222222222222222222222222222222222",
			CandidatesHash: "0000000000000000000000000000000000000000000000000000000000000002",
			StartedAt:      time.Now(),
		}
		assert.NoError(t, validation.Instance().Struct(i), string(s))
	}
	assert.Len(t, all, 8)
}
