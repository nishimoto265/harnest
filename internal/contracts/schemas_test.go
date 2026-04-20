package contracts

import (
	"encoding/json"
	"errors"
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

// finding #5: TaskPackage.Validate() が 3×2 matrix invariant を強制する。
func validTaskPackage() TaskPackage {
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
	agents := []AgentID{"a1", "a2", "a3", "a1", "a2", "a3"}
	for i := range pkg.Worktrees {
		pass := 1
		if i >= 3 {
			pass = 2
		}
		pkg.Worktrees[i] = WorktreeAllocation{
			Agent:   agents[i],
			Pass:    pass,
			Path:    "/tmp/wt",
			Branch:  "b",
			BaseSHA: "1111111111111111111111111111111111111111",
			HeadSHA: "1111111111111111111111111111111111111111",
		}
	}
	return pkg
}

func TestTaskPackage_Validate_Valid(t *testing.T) {
	assert.NoError(t, validTaskPackage().Validate())
}

func TestTaskPackage_Validate_Reject_PassCountMismatch(t *testing.T) {
	// pass==1 が 4 (distinct agents)、pass==2 が 2 → len=6 は満たすが matrix invariant 違反。
	pkg := validTaskPackage()
	// worktrees[3] is the pass2/a1 row. Move it to pass=1 with a new agent a4
	// (避: 重複判定が先に走らないよう distinct agent に置く).
	pkg.Worktrees[3].Pass = 1
	pkg.Worktrees[3].Agent = "a4"
	err := pkg.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTaskPackagePassCountMismatch)
}

func TestTaskPackage_Validate_Reject_AllPass1(t *testing.T) {
	pkg := validTaskPackage()
	for i := range pkg.Worktrees {
		pkg.Worktrees[i].Pass = 1
	}
	err := pkg.Validate()
	require.Error(t, err)
	// All-pass-1 causes tag validation (oneof=1 2) to pass but matrix enforces
	// per-pass count == 3 → pass=1 has 6, pass=2 has 0.
	// With current implementation: duplicate detection triggers first
	// (3 agents × 2 copies within pass 1).
	assert.Truef(t, errors.Is(err, ErrTaskPackageAgentDuplicate) || errors.Is(err, ErrTaskPackagePassCountMismatch), "err=%v", err)
}

func TestTaskPackage_Validate_Reject_DuplicateAgentWithinPass(t *testing.T) {
	pkg := validTaskPackage()
	// pass1 の worktrees[0..2] を全て a1 に → duplicate.
	pkg.Worktrees[0].Agent = "a1"
	pkg.Worktrees[1].Agent = "a1"
	pkg.Worktrees[2].Agent = "a1"
	err := pkg.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTaskPackageAgentDuplicate)
}

func TestTaskPackage_Validate_Reject_PassAgentSetDiffer(t *testing.T) {
	pkg := validTaskPackage()
	// pass2 の agent set を {a4,a5,a6} に置換 → pass1 = {a1,a2,a3} と不一致.
	pkg.Worktrees[3].Agent = "a4"
	pkg.Worktrees[4].Agent = "a5"
	pkg.Worktrees[5].Agent = "a6"
	err := pkg.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTaskPackagePassAgentMismatch)
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

// finding #1: stage に応じた required field を enforce する Validate() の動作確認。
func validIntentionBase() IntentionRecord {
	return IntentionRecord{
		SchemaVersion:  "1",
		IdempotencyKey: "0000000000000000000000000000000000000000000000000000000000000001",
		RunID:          "2026-04-20-PR42-abcdef0",
		BestShaBefore:  "1111111111111111111111111111111111111111",
		TargetSha:      "2222222222222222222222222222222222222222",
		CandidatesHash: "0000000000000000000000000000000000000000000000000000000000000002",
		StartedAt:      time.Now(),
	}
}

func TestIntentionRecord_Validate_Planning_NoExtraRequired(t *testing.T) {
	r := validIntentionBase()
	r.Stage = IntentionStagePlanning
	assert.NoError(t, r.Validate())
}

func TestIntentionRecord_Validate_RegistryAppended_RequiresAppendResult(t *testing.T) {
	r := validIntentionBase()
	r.Stage = IntentionStageRegistryAppended
	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIntentionMissingRegistryAppendResult)

	// populate と OK になる
	r.RegistryAppendResult = &RegistryAppendResult{Offset: 0, Sha256: "0000000000000000000000000000000000000000000000000000000000000003"}
	assert.NoError(t, r.Validate())
}

func TestIntentionRecord_Validate_DecisionWritten_RequiresAppendResult(t *testing.T) {
	r := validIntentionBase()
	r.Stage = IntentionStageDecisionWritten
	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIntentionMissingRegistryAppendResult)
}

func TestIntentionRecord_Validate_RollingBackRegistryAppended_RequiresAppendResultAndRecovery(t *testing.T) {
	r := validIntentionBase()
	r.Stage = IntentionStageRollingBackRegistryAppended
	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIntentionMissingRegistryAppendResult)

	// append_result 埋めると次は recovery_reason 欠落で fail.
	r.RegistryAppendResult = &RegistryAppendResult{Offset: 42, Sha256: "0000000000000000000000000000000000000000000000000000000000000003"}
	err = r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIntentionMissingRecoveryReason)

	// recovery_reason 埋めると次は failed_step 欠落.
	r.RecoveryReason = RollbackReasonLeaseFailure
	err = r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIntentionMissingFailedStep)

	r.FailedStep = FailedStep70
	assert.NoError(t, r.Validate())
}

func TestIntentionRecord_Validate_RollingBackDecisionWritten_RequiresAll(t *testing.T) {
	r := validIntentionBase()
	r.Stage = IntentionStageRollingBackDecisionWritten
	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIntentionMissingRegistryAppendResult)
}

func TestIntentionRecord_Validate_RollingBackBranchReverted_RequiresRecovery(t *testing.T) {
	r := validIntentionBase()
	r.Stage = IntentionStageRollingBackBranchReverted
	// このステージは registry_append_result 要求なし、recovery_reason/failed_step 要求あり.
	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIntentionMissingRecoveryReason)
}

func TestIntentionRecord_Validate_NeedsManualRecovery_RequiresRecoveryAndFailedStep(t *testing.T) {
	r := validIntentionBase()
	r.Stage = IntentionStageNeedsManualRecovery
	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIntentionMissingRecoveryReason)

	r.RecoveryReason = RollbackReasonRemoteDivergence
	err = r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIntentionMissingFailedStep)

	r.FailedStep = FailedStep70
	assert.NoError(t, r.Validate())
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
