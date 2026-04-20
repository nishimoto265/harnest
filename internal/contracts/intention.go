package contracts

import (
	"errors"
	"fmt"
	"time"
)

// IntentionStage is the staged-transaction state stored in `<run>/70/intention.json`.
// io-contracts.md §step70 Stage 遷移 + rolling_back_* stages + needs_manual_recovery.
type IntentionStage string

const (
	IntentionStagePlanning         IntentionStage = "planning"
	IntentionStageBranchPushed     IntentionStage = "branch_pushed"
	IntentionStageRegistryAppended IntentionStage = "registry_appended"
	IntentionStageDecisionWritten  IntentionStage = "decision_written"

	// Rolling back stages (rev19, Codex rev18 R1 critical 対応).
	IntentionStageRollingBackBranchReverted   IntentionStage = "rolling_back_branch_reverted"
	IntentionStageRollingBackRegistryAppended IntentionStage = "rolling_back_registry_appended"
	IntentionStageRollingBackDecisionWritten  IntentionStage = "rolling_back_decision_written"

	// Terminal-but-persisted stage: operator 介入待ち.
	IntentionStageNeedsManualRecovery IntentionStage = "needs_manual_recovery"
)

// IntentionRecord is the `<run>/70/intention.json` document (Phase 0-E 実装用、
// schema only at Phase 0-bootstrap).
//
// Stage 遷移ごとに atomic overwrite (io-contracts.md §Intention の atomic
// overwrite). idempotency_key は planning で 1 回生成し以降 reuse (rev5、Codex
// R1 #3).
type IntentionRecord struct {
	SchemaVersion string         `json:"schema_version" validate:"required,oneof=1"`
	Stage         IntentionStage `json:"stage" validate:"required,oneof=planning branch_pushed registry_appended decision_written rolling_back_branch_reverted rolling_back_registry_appended rolling_back_decision_written needs_manual_recovery"`

	// IdempotencyKey: sha256(run_id || target_sha || best_sha_before || candidates_hash).
	IdempotencyKey string `json:"idempotency_key" validate:"required,sha256_hex"`

	RunID RunID `json:"run_id" validate:"required,run_id_fmt"`

	BestShaBefore  string `json:"best_sha_before" validate:"required,sha1_hex"`
	TargetSha      string `json:"target_sha" validate:"required,sha1_hex"`
	CandidatesHash string `json:"candidates_hash" validate:"required,sha256_hex"`

	// RegistryHeadBefore: registry last-entry sha256 at planning time.
	// rules-registry.jsonl が空の場合は `""` (empty string) を許容 (planning が
	// registry 初 entry の場合).
	RegistryHeadBefore string `json:"registry_head_before" validate:"omitempty,sha256_hex"`

	StartedAt time.Time `json:"started_at" validate:"required"`

	// RegistryAppendResult: stage=registry_appended 以降に populate される.
	RegistryAppendResult *RegistryAppendResult `json:"registry_append_result,omitempty" validate:"omitempty"`

	// RecoveryReason / FailedStep: stage=needs_manual_recovery もしくは
	// rolling_back_* 時にのみ populate.
	RecoveryReason RollbackReason `json:"recovery_reason,omitempty" validate:"omitempty,oneof=lease_failure remote_divergence registry_divergence worktree_rescue_loop manual_abort_pending_cleanup transactional_failure"`
	FailedStep     FailedStep     `json:"failed_step,omitempty" validate:"omitempty,oneof=10 20 30 40 50 60 70"`
}

// Stage-based conditional required errors. io-contracts.md §step70 Stage 遷移:
//   - stage=registry_appended / decision_written / rolling_back_registry_appended /
//     rolling_back_decision_written → registry_append_result 必須 (non-nil)
//   - stage=needs_manual_recovery / rolling_back_*                       →
//     recovery_reason + failed_step 必須
var (
	ErrIntentionMissingRegistryAppendResult = errors.New("contracts: intention: registry_append_result is required for this stage")
	ErrIntentionMissingRecoveryReason       = errors.New("contracts: intention: recovery_reason is required for this stage")
	ErrIntentionMissingFailedStep           = errors.New("contracts: intention: failed_step is required for this stage")
	ErrIntentionIdempotencyKeyMismatch      = errors.New("contracts: intention: idempotency_key does not match derived value")
)

// Validate enforces stage-conditional required fields on top of tag-based
// validation. Call site (reader / UnmarshalJSON / test) should invoke this
// after validator.Struct succeeds.
func (r IntentionRecord) Validate() error {
	if err := validateStruct(r); err != nil {
		return err
	}
	expected := ComputeAdoptIdempotencyKey(string(r.RunID), r.TargetSha, r.BestShaBefore, r.CandidatesHash)
	if r.IdempotencyKey != expected {
		return fmt.Errorf("%w: got=%s want=%s", ErrIntentionIdempotencyKeyMismatch, r.IdempotencyKey, expected)
	}
	switch r.Stage {
	case IntentionStageRegistryAppended,
		IntentionStageDecisionWritten,
		IntentionStageRollingBackRegistryAppended,
		IntentionStageRollingBackDecisionWritten:
		if r.RegistryAppendResult == nil {
			return ErrIntentionMissingRegistryAppendResult
		}
	}
	switch r.Stage {
	case IntentionStageNeedsManualRecovery,
		IntentionStageRollingBackBranchReverted,
		IntentionStageRollingBackRegistryAppended,
		IntentionStageRollingBackDecisionWritten:
		if string(r.RecoveryReason) == "" {
			return ErrIntentionMissingRecoveryReason
		}
		if string(r.FailedStep) == "" {
			return ErrIntentionMissingFailedStep
		}
	}
	return nil
}
