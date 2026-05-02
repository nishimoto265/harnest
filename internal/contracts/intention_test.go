package contracts

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/validation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// IntentionRecord
func TestIntentionRecord_Valid_Planning(t *testing.T) {
	candidatesHash := "0000000000000000000000000000000000000000000000000000000000000002"
	i := IntentionRecord{
		SchemaVersion:      "1",
		Stage:              IntentionStagePlanning,
		IdempotencyKey:     ComputeAdoptIdempotencyKey("2026-04-20-PR42-abcdef0", "2222222222222222222222222222222222222222", "1111111111111111111111111111111111111111", candidatesHash),
		RunID:              "2026-04-20-PR42-abcdef0",
		BestShaBefore:      "1111111111111111111111111111111111111111",
		TargetSha:          "2222222222222222222222222222222222222222",
		CandidatesHash:     candidatesHash,
		RegistryHeadBefore: "",
		PlannedAdoption:    validPlannedAdoption(),
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
	candidatesHash := "0000000000000000000000000000000000000000000000000000000000000002"
	return IntentionRecord{
		SchemaVersion:   "1",
		IdempotencyKey:  ComputeAdoptIdempotencyKey("2026-04-20-PR42-abcdef0", "2222222222222222222222222222222222222222", "1111111111111111111111111111111111111111", candidatesHash),
		RunID:           "2026-04-20-PR42-abcdef0",
		BestShaBefore:   "1111111111111111111111111111111111111111",
		TargetSha:       "2222222222222222222222222222222222222222",
		CandidatesHash:  candidatesHash,
		PlannedAdoption: validPlannedAdoption(),
		StartedAt:       time.Now(),
	}
}

func validPlannedAdoption() *PlannedAdoption {
	idempotencyKey := ComputeAdoptIdempotencyKey("2026-04-20-PR42-abcdef0", "2222222222222222222222222222222222222222", "1111111111111111111111111111111111111111", "0000000000000000000000000000000000000000000000000000000000000002")
	return &PlannedAdoption{
		IdempotencyKey: idempotencyKey,
		Entries: []PlannedAdoptionEntry{
			{
				OpID:     ComputePlannedAdoptionEntryOpID(idempotencyKey, 0, "r-0001"),
				Kind:     RegistryKindAdded,
				RuleID:   "r-0001",
				RulePath: "rules/r-0001.md",
				Sha256:   "0000000000000000000000000000000000000000000000000000000000000005",
			},
		},
	}
}

func TestIntentionRecord_Validate_Planning_NoExtraRequired(t *testing.T) {
	r := validIntentionBase()
	r.Stage = IntentionStagePlanning
	assert.NoError(t, r.Validate())
}

func TestIntentionRecord_Validate_BranchPushed_RequiresPlannedAdoption(t *testing.T) {
	r := validIntentionBase()
	r.Stage = IntentionStageBranchPushed
	r.PlannedAdoption = nil

	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIntentionMissingPlannedAdoption)
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

func TestIntentionRecord_Validate_PolicyPublishing_RequiresAppendResultAndPolicyBranch(t *testing.T) {
	r := validIntentionBase()
	r.Stage = IntentionStagePolicyPublishing
	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIntentionMissingRegistryAppendResult)

	r.RegistryAppendResult = &RegistryAppendResult{Offset: 0, Sha256: "0000000000000000000000000000000000000000000000000000000000000003"}
	err = r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIntentionMissingPolicyBranch)

	r.PolicyBranch = "auto-improve/policy"
	assert.NoError(t, r.Validate(), "empty policy_head_before means bootstrap a missing policy branch")

	r.PolicyHeadBefore = "1111111111111111111111111111111111111111"
	assert.NoError(t, r.Validate())
}

func TestIntentionRecord_Validate_PolicyPublished_RequiresPolicyHeadAfter(t *testing.T) {
	r := validIntentionBase()
	r.Stage = IntentionStagePolicyPublished
	r.RegistryAppendResult = &RegistryAppendResult{Offset: 0, Sha256: "0000000000000000000000000000000000000000000000000000000000000003"}
	r.PolicyBranch = "auto-improve/policy"
	r.PolicyHeadBefore = "1111111111111111111111111111111111111111"
	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIntentionMissingPolicyHeadAfter)

	r.PolicyHeadAfter = "2222222222222222222222222222222222222222"
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
	assert.ErrorIs(t, err, ErrIntentionMissingRecoveryReason)
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
	// 仕様: Stage enum は 10 種 (planning / branch_pushed / registry_appended /
	// policy_publishing / policy_published / decision_written /
	// rolling_back_branch_reverted /
	// rolling_back_registry_appended / rolling_back_decision_written /
	// needs_manual_recovery)。enum そのものの validator 動作確認.
	all := []IntentionStage{
		IntentionStagePlanning,
		IntentionStageBranchPushed,
		IntentionStageRegistryAppended,
		IntentionStagePolicyPublishing,
		IntentionStagePolicyPublished,
		IntentionStageDecisionWritten,
		IntentionStageRollingBackBranchReverted,
		IntentionStageRollingBackRegistryAppended,
		IntentionStageRollingBackDecisionWritten,
		IntentionStageNeedsManualRecovery,
	}
	for _, s := range all {
		candidatesHash := "0000000000000000000000000000000000000000000000000000000000000002"
		i := IntentionRecord{
			SchemaVersion:   "1",
			Stage:           s,
			IdempotencyKey:  ComputeAdoptIdempotencyKey("2026-04-20-PR42-abcdef0", "2222222222222222222222222222222222222222", "1111111111111111111111111111111111111111", candidatesHash),
			RunID:           "2026-04-20-PR42-abcdef0",
			BestShaBefore:   "1111111111111111111111111111111111111111",
			TargetSha:       "2222222222222222222222222222222222222222",
			CandidatesHash:  candidatesHash,
			PlannedAdoption: validPlannedAdoption(),
			StartedAt:       time.Now(),
		}
		if s == IntentionStageNeedsManualRecovery {
			i.PlannedAdoption = nil
		}
		assert.NoError(t, validation.Instance().Struct(i), string(s))
	}
	assert.Len(t, all, 10)
}

func TestIntentionRecord_Validate_RejectsForgedIdempotencyKeyAcrossStages(t *testing.T) {
	stages := []IntentionStage{
		IntentionStagePlanning,
		IntentionStageBranchPushed,
		IntentionStageRegistryAppended,
		IntentionStagePolicyPublishing,
		IntentionStagePolicyPublished,
		IntentionStageDecisionWritten,
		IntentionStageRollingBackBranchReverted,
		IntentionStageRollingBackRegistryAppended,
		IntentionStageRollingBackDecisionWritten,
		IntentionStageNeedsManualRecovery,
	}

	for _, stage := range stages {
		t.Run(string(stage), func(t *testing.T) {
			r := validIntentionBase()
			r.Stage = stage
			r.IdempotencyKey = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
			switch stage {
			case IntentionStageRegistryAppended,
				IntentionStagePolicyPublishing,
				IntentionStagePolicyPublished,
				IntentionStageDecisionWritten,
				IntentionStageRollingBackRegistryAppended,
				IntentionStageRollingBackDecisionWritten:
				r.RegistryAppendResult = &RegistryAppendResult{
					Offset: 0,
					Sha256: "0000000000000000000000000000000000000000000000000000000000000003",
				}
			}
			switch stage {
			case IntentionStagePolicyPublishing:
				r.PolicyBranch = "auto-improve/policy"
				r.PolicyHeadBefore = "1111111111111111111111111111111111111111"
			case IntentionStagePolicyPublished:
				r.PolicyBranch = "auto-improve/policy"
				r.PolicyHeadBefore = "1111111111111111111111111111111111111111"
				r.PolicyHeadAfter = "2222222222222222222222222222222222222222"
			}
			switch stage {
			case IntentionStageRollingBackBranchReverted,
				IntentionStageRollingBackRegistryAppended,
				IntentionStageRollingBackDecisionWritten,
				IntentionStageNeedsManualRecovery:
				r.RecoveryReason = RollbackReasonLeaseFailure
				r.FailedStep = FailedStep70
			}

			err := r.Validate()
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrIntentionIdempotencyKeyMismatch)
		})
	}
}

func TestIntentionRecord_Validate_RejectsPlannedAdoptionIdempotencyMismatch(t *testing.T) {
	r := validIntentionBase()
	r.Stage = IntentionStagePlanning
	r.RegistryHeadBefore = ""
	r.PlannedAdoption.IdempotencyKey = strings.Repeat("f", 64)

	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPlannedAdoptionIdempotencyMismatch)
}

func TestIntentionRecord_UnmarshalJSON_RejectsMissingRegistryHeadBefore(t *testing.T) {
	candidatesHash := "0000000000000000000000000000000000000000000000000000000000000002"
	idempotencyKey := ComputeAdoptIdempotencyKey("2026-04-20-PR42-abcdef0", "2222222222222222222222222222222222222222", "1111111111111111111111111111111111111111", candidatesHash)
	plannedAdoption := `{"idempotency_key":"` + idempotencyKey + `","entries":[{"kind":"added","op_id":"` + ComputePlannedAdoptionEntryOpID(idempotencyKey, 0, "r-0001") + `","rule_id":"r-0001","rule_path":"rules/r-0001.md","sha256":"0000000000000000000000000000000000000000000000000000000000000005"}]}`
	data := []byte(`{
  "schema_version": "1",
  "stage": "planning",
  "idempotency_key": "` + idempotencyKey + `",
  "run_id": "2026-04-20-PR42-abcdef0",
  "best_sha_before": "1111111111111111111111111111111111111111",
  "target_sha": "2222222222222222222222222222222222222222",
  "candidates_hash": "` + candidatesHash + `",
  "planned_adoption": ` + plannedAdoption + `,
  "started_at": "2026-04-20T10:00:00Z"
}`)
	var record IntentionRecord
	err := json.Unmarshal(data, &record)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIntentionMissingRegistryHeadBefore)
}

func TestIntentionRecord_UnmarshalJSON_AcceptsExplicitEmptyRegistryHeadBefore(t *testing.T) {
	candidatesHash := "0000000000000000000000000000000000000000000000000000000000000002"
	idempotencyKey := ComputeAdoptIdempotencyKey("2026-04-20-PR42-abcdef0", "2222222222222222222222222222222222222222", "1111111111111111111111111111111111111111", candidatesHash)
	plannedAdoption := `{"idempotency_key":"` + idempotencyKey + `","entries":[{"kind":"added","op_id":"` + ComputePlannedAdoptionEntryOpID(idempotencyKey, 0, "r-0001") + `","rule_id":"r-0001","rule_path":"rules/r-0001.md","sha256":"0000000000000000000000000000000000000000000000000000000000000005"}]}`
	data := []byte(`{
  "schema_version": "1",
  "stage": "planning",
  "idempotency_key": "` + idempotencyKey + `",
  "run_id": "2026-04-20-PR42-abcdef0",
  "best_sha_before": "1111111111111111111111111111111111111111",
  "target_sha": "2222222222222222222222222222222222222222",
  "candidates_hash": "` + candidatesHash + `",
  "registry_head_before": "",
  "planned_adoption": ` + plannedAdoption + `,
  "started_at": "2026-04-20T10:00:00Z"
}`)
	var record IntentionRecord
	require.NoError(t, json.Unmarshal(data, &record))
	assert.Equal(t, "", record.RegistryHeadBefore)
}

func TestPlannedAdoption_RoundTrip(t *testing.T) {
	original := validPlannedAdoption()

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded PlannedAdoption
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, *original, decoded)
}

func TestIntentionRecord_MarshalJSON_RejectsForgedIdempotencyKey(t *testing.T) {
	record := validIntentionBase()
	record.Stage = IntentionStagePlanning
	record.RegistryHeadBefore = ""
	record.IdempotencyKey = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"

	_, err := json.Marshal(record)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIntentionIdempotencyKeyMismatch)
}
