package contracts

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// IntentionStage is the staged-transaction state stored in `<run>/70/intention.json`.
// io-contracts.md §step70 Stage 遷移 + rolling_back_* stages + needs_manual_recovery.
type IntentionStage string

const (
	IntentionStagePlanning         IntentionStage = "planning"
	IntentionStageBranchPushed     IntentionStage = "branch_pushed"
	IntentionStageRegistryAppended IntentionStage = "registry_appended"
	IntentionStagePolicyPublishing IntentionStage = "policy_publishing"
	IntentionStagePolicyPublished  IntentionStage = "policy_published"
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
	Stage         IntentionStage `json:"stage" validate:"required,oneof=planning branch_pushed registry_appended policy_publishing policy_published decision_written rolling_back_branch_reverted rolling_back_registry_appended rolling_back_decision_written needs_manual_recovery"`

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

	// PlannedAdoption: planning 時点で確定した adopt append payload。
	// branch_pushed recovery は Resolver を再実行せず、この payload から Stage 4
	// を再生する。
	PlannedAdoption *PlannedAdoption `json:"planned_adoption,omitempty" validate:"omitempty"`

	StartedAt time.Time `json:"started_at" validate:"required"`

	// RegistryAppendResult: stage=registry_appended 以降に populate される.
	RegistryAppendResult *RegistryAppendResult `json:"registry_append_result,omitempty" validate:"omitempty"`

	// PolicyBranch / PolicyHeadBefore / PolicyHeadAfter track the optional
	// policy_branch publish that happens after registry append and before
	// decision.json. They let recovery distinguish "not pushed yet" from
	// "pushed but decision write crashed" without rolling back local state while
	// leaving remote policy adopted.
	PolicyBranch     string `json:"policy_branch,omitempty" validate:"omitempty"`
	PolicyHeadBefore string `json:"policy_head_before,omitempty" validate:"omitempty,sha1_hex"`
	PolicyHeadAfter  string `json:"policy_head_after,omitempty" validate:"omitempty,sha1_hex"`

	// AppendedEntryOpIDs: stage4 multi-entry append 中に成功済み row の per-entry
	// idempotency key を逐次保存する。branch_pushed のままでも rollback /
	// recovery が committed row を再発見できるようにする。
	AppendedEntryOpIDs []string `json:"appended_entry_op_ids,omitempty" validate:"omitempty,dive,sha256_hex"`

	// PublishedRuleOpIDs: stage5 multi-entry sidecar publish 中に既 publish 済みの
	// per-entry op_id を逐次保存する。crash-after-first-publish の resume 時に
	// 既に destination へ landing 済みの rule sidecar を再 publish せず、かつ
	// staged file が消えた状態で errRulePublishStagedMissing に escalate しない
	// ようにする (F10)。optional field として non-breaking で拡張する。
	PublishedRuleOpIDs []string `json:"published_rule_op_ids,omitempty" validate:"omitempty,dive,sha256_hex"`

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
	ErrIntentionMissingRegistryAppendResult  = errors.New("contracts: intention: registry_append_result is required for this stage")
	ErrIntentionMissingRecoveryReason        = errors.New("contracts: intention: recovery_reason is required for this stage")
	ErrIntentionMissingFailedStep            = errors.New("contracts: intention: failed_step is required for this stage")
	ErrIntentionMissingRegistryHeadBefore    = errors.New("contracts: intention: registry_head_before field is required")
	ErrIntentionMissingPlannedAdoption       = errors.New("contracts: intention: planned_adoption is required for this stage")
	ErrIntentionMissingPolicyBranch          = errors.New("contracts: intention: policy_branch is required for this stage")
	ErrIntentionMissingPolicyHeadBefore      = errors.New("contracts: intention: policy_head_before is required for this stage")
	ErrIntentionMissingPolicyHeadAfter       = errors.New("contracts: intention: policy_head_after is required for this stage")
	ErrIntentionIdempotencyKeyMismatch       = errors.New("contracts: intention: idempotency_key does not match derived value")
	ErrPlannedAdoptionEmpty                  = errors.New("contracts: intention: planned_adoption.entries must contain at least one entry")
	ErrPlannedAdoptionIdempotencyMismatch    = errors.New("contracts: intention: planned_adoption.idempotency_key must match intention.idempotency_key")
	ErrPlannedAdoptionUnsupportedKind        = errors.New("contracts: intention: planned_adoption supports only added/updated entries")
	ErrPlannedAdoptionPrevSha256Required     = errors.New("contracts: intention: planned_adoption.updated requires prev_sha256")
	ErrPlannedAdoptionPrevSha256Forbidden    = errors.New("contracts: intention: planned_adoption.added must not set prev_sha256")
	ErrPlannedAdoptionOpIDRequired           = errors.New("contracts: intention: planned_adoption entry op_id is required")
	ErrPlannedAdoptionAppendedEntryUnknown   = errors.New("contracts: intention: appended_entry_op_ids must belong to planned_adoption entries")
	ErrPlannedAdoptionAppendedEntryDuplicate = errors.New("contracts: intention: appended_entry_op_ids must be unique")
)

type PlannedAdoption struct {
	IdempotencyKey string                 `json:"idempotency_key" validate:"required,sha256_hex"`
	Entries        []PlannedAdoptionEntry `json:"entries" validate:"required,min=1,dive"`
}

func (p PlannedAdoption) Validate(intentionIdempotencyKey string) error {
	if p.IdempotencyKey == "" {
		return ErrPlannedAdoptionIdempotencyMismatch
	}
	if intentionIdempotencyKey != "" && p.IdempotencyKey != intentionIdempotencyKey {
		return fmt.Errorf("%w: planned=%s intention=%s", ErrPlannedAdoptionIdempotencyMismatch, p.IdempotencyKey, intentionIdempotencyKey)
	}
	if len(p.Entries) == 0 {
		return ErrPlannedAdoptionEmpty
	}
	for _, entry := range p.Entries {
		if err := entry.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type PlannedAdoptionEntry struct {
	Kind       RegistryKind `json:"kind" validate:"required,oneof=added updated"`
	OpID       string       `json:"op_id" validate:"required,sha256_hex"`
	RuleID     string       `json:"rule_id" validate:"required"`
	RulePath   string       `json:"rule_path" validate:"required"`
	Sha256     string       `json:"sha256" validate:"required,sha256_hex"`
	PrevSha256 string       `json:"prev_sha256,omitempty" validate:"omitempty,sha256_hex"`
}

func (e PlannedAdoptionEntry) Validate() error {
	if err := validateStruct(e); err != nil {
		if e.OpID == "" {
			return ErrPlannedAdoptionOpIDRequired
		}
		return err
	}
	if err := ValidateRuleID(e.RuleID); err != nil {
		return err
	}
	if err := ValidateRulePath(e.RulePath); err != nil {
		return err
	}
	switch e.Kind {
	case RegistryKindAdded:
		if e.PrevSha256 != "" {
			return ErrPlannedAdoptionPrevSha256Forbidden
		}
	case RegistryKindUpdated:
		if e.PrevSha256 == "" {
			return ErrPlannedAdoptionPrevSha256Required
		}
	default:
		return ErrPlannedAdoptionUnsupportedKind
	}
	return nil
}

func (r *IntentionRecord) UnmarshalJSON(data []byte) error {
	type alias IntentionRecord
	var a alias
	if err := decodeStrictWithRequiredFields(data, &a, map[string]error{
		"registry_head_before": ErrIntentionMissingRegistryHeadBefore,
	}); err != nil {
		return err
	}
	*r = IntentionRecord(a)
	return r.Validate()
}

func (r IntentionRecord) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	type alias IntentionRecord
	return json.Marshal(alias(r))
}

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
	if r.PlannedAdoption != nil {
		if err := r.PlannedAdoption.Validate(r.IdempotencyKey); err != nil {
			return err
		}
	}
	if len(r.AppendedEntryOpIDs) > 0 {
		if r.PlannedAdoption == nil {
			return ErrIntentionMissingPlannedAdoption
		}
		allowed := make(map[string]struct{}, len(r.PlannedAdoption.Entries))
		for _, entry := range r.PlannedAdoption.Entries {
			allowed[entry.OpID] = struct{}{}
		}
		seen := make(map[string]struct{}, len(r.AppendedEntryOpIDs))
		for _, opID := range r.AppendedEntryOpIDs {
			if _, ok := allowed[opID]; !ok {
				return fmt.Errorf("%w: %s", ErrPlannedAdoptionAppendedEntryUnknown, opID)
			}
			if _, ok := seen[opID]; ok {
				return fmt.Errorf("%w: %s", ErrPlannedAdoptionAppendedEntryDuplicate, opID)
			}
			seen[opID] = struct{}{}
		}
	}
	if len(r.PublishedRuleOpIDs) > 0 {
		if r.PlannedAdoption == nil {
			return ErrIntentionMissingPlannedAdoption
		}
		allowed := make(map[string]struct{}, len(r.PlannedAdoption.Entries))
		for _, entry := range r.PlannedAdoption.Entries {
			allowed[entry.OpID] = struct{}{}
		}
		seen := make(map[string]struct{}, len(r.PublishedRuleOpIDs))
		for _, opID := range r.PublishedRuleOpIDs {
			if _, ok := allowed[opID]; !ok {
				return fmt.Errorf("%w: %s", ErrPlannedAdoptionAppendedEntryUnknown, opID)
			}
			if _, ok := seen[opID]; ok {
				return fmt.Errorf("%w: %s", ErrPlannedAdoptionAppendedEntryDuplicate, opID)
			}
			seen[opID] = struct{}{}
		}
	}
	switch r.Stage {
	case IntentionStagePlanning,
		IntentionStageBranchPushed,
		IntentionStageRegistryAppended,
		IntentionStagePolicyPublishing,
		IntentionStagePolicyPublished,
		IntentionStageDecisionWritten,
		IntentionStageRollingBackBranchReverted,
		IntentionStageRollingBackRegistryAppended,
		IntentionStageRollingBackDecisionWritten:
		if r.PlannedAdoption == nil {
			return ErrIntentionMissingPlannedAdoption
		}
	}
	switch r.Stage {
	case IntentionStageRegistryAppended,
		IntentionStagePolicyPublishing,
		IntentionStagePolicyPublished,
		IntentionStageDecisionWritten,
		IntentionStageRollingBackRegistryAppended:
		if r.RegistryAppendResult == nil {
			return ErrIntentionMissingRegistryAppendResult
		}
	}
	switch r.Stage {
	case IntentionStagePolicyPublishing,
		IntentionStagePolicyPublished:
		if strings.TrimSpace(r.PolicyBranch) == "" {
			return ErrIntentionMissingPolicyBranch
		}
		if r.PolicyHeadBefore == "" {
			return ErrIntentionMissingPolicyHeadBefore
		}
	}
	if r.Stage == IntentionStagePolicyPublished && r.PolicyHeadAfter == "" {
		return ErrIntentionMissingPolicyHeadAfter
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
		if err := validateReasonFailedStepPair(r.RecoveryReason, r.FailedStep); err != nil {
			return err
		}
	}
	return nil
}
