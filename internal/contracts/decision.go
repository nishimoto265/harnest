package contracts

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// RollbackReason / NeedsRecoveryReason is the unified enum shared by:
//   - Decision rollback variant (.RollbackReason)
//   - IntentionRecord.RecoveryReason (rollback / needs_manual_recovery path)
//   - RuleRegistryRolledBack.RollbackReason
//   - StateEntryNeedsManualRecovery.Reason
//
// io-contracts.md §step70 rollback_reason/needs_manual_recovery.reason 共通 enum.
type RollbackReason string

const (
	RollbackReasonLeaseFailure              RollbackReason = "lease_failure"
	RollbackReasonRemoteDivergence          RollbackReason = "remote_divergence"
	RollbackReasonRegistryDivergence        RollbackReason = "registry_divergence"
	RollbackReasonWorktreeRescueLoop        RollbackReason = "worktree_rescue_loop"
	RollbackReasonManualAbortPendingCleanup RollbackReason = "manual_abort_pending_cleanup"
	RollbackReasonTransactionalFailure      RollbackReason = "transactional_failure"
)

// FailedStep: `"10" | "20" | "30" | "40" | "50" | "60" | "70"`
// (io-contracts.md §needs_manual_recovery.failed_step).
type FailedStep string

const (
	FailedStep10 FailedStep = "10"
	FailedStep20 FailedStep = "20"
	FailedStep30 FailedStep = "30"
	FailedStep40 FailedStep = "40"
	FailedStep50 FailedStep = "50"
	FailedStep60 FailedStep = "60"
	FailedStep70 FailedStep = "70"
)

// DecisionAction is the Decision tagged-union discriminator.
type DecisionAction string

const (
	DecisionActionAdopt    DecisionAction = "adopt"
	DecisionActionReject   DecisionAction = "reject"
	DecisionActionNoop     DecisionAction = "noop"
	DecisionActionRollback DecisionAction = "rollback"
)

// Decision is the step70 output written atomically to `<run>/70/decision.json`.
// Tagged union over `action`. io-contracts.md §step70, §Decision variant 確定:
// `error` は廃止し rollback に統合 (rollback variant が rollback_reason /
// failed_step を保持).
type Decision struct {
	Action DecisionAction  `json:"action"`
	Value  DecisionVariant `json:"-"`
}

// DecisionVariant is implemented by the four Decision variant structs.
type DecisionVariant interface {
	decisionVariant()
}

var (
	ErrDecisionVariantTypeMismatch    = errors.New("contracts: decision: action does not match variant type")
	ErrDecisionVariantActionMismatch  = errors.New("contracts: decision: action does not match inner action field")
	ErrDecisionIdempotencyKeyMismatch = errors.New("contracts: decision: adopt idempotency_key does not match derived value")
)

// DecisionAdopt: rule set が採用され best_branch に push された.
type DecisionAdopt struct {
	Action         DecisionAction `json:"action" validate:"required,eq=adopt"`
	SchemaVersion  string         `json:"schema_version" validate:"required,oneof=1"`
	RunID          RunID          `json:"run_id" validate:"required,run_id_fmt"`
	IdempotencyKey string         `json:"idempotency_key" validate:"required,sha256_hex"`
	BestShaBefore  string         `json:"best_sha_before" validate:"required,sha1_hex"`
	TargetSha      string         `json:"target_sha" validate:"required,sha1_hex"`
	CandidatesHash string         `json:"candidates_hash" validate:"required,sha256_hex"`

	// RegistryAppendResult: step70 stage 4 で registry に append した結果.
	RegistryAppendResult RegistryAppendResult `json:"registry_append_result" validate:"required"`

	DecidedAt time.Time `json:"decided_at" validate:"required"`
}

func (DecisionAdopt) decisionVariant() {}

func (d DecisionAdopt) Validate() error {
	if err := validateStruct(d); err != nil {
		return err
	}
	expected := ComputeAdoptIdempotencyKey(string(d.RunID), d.TargetSha, d.BestShaBefore, d.CandidatesHash)
	if d.IdempotencyKey != expected {
		return fmt.Errorf("%w: got=%s want=%s", ErrDecisionIdempotencyKeyMismatch, d.IdempotencyKey, expected)
	}
	return nil
}

// DecisionReject: candidates は生成されたが閾値未満で reject.
type DecisionReject struct {
	Action        DecisionAction `json:"action" validate:"required,eq=reject"`
	SchemaVersion string         `json:"schema_version" validate:"required,oneof=1"`
	RunID         RunID          `json:"run_id" validate:"required,run_id_fmt"`
	// Reason: free-text (300 字 cap).
	Reason string `json:"reason" validate:"required,max=300"`

	DecidedAt time.Time `json:"decided_at" validate:"required"`
}

func (DecisionReject) decisionVariant() {}

// DecisionNoop: step40 で候補 0 件等、promotion 不要で best 維持.
type DecisionNoop struct {
	Action        DecisionAction `json:"action" validate:"required,eq=noop"`
	SchemaVersion string         `json:"schema_version" validate:"required,oneof=1"`
	RunID         RunID          `json:"run_id" validate:"required,run_id_fmt"`
	// Reason: 短い free-text (200 字 cap). 例: "no_candidates" / "below_threshold".
	Reason string `json:"reason" validate:"required,max=200"`

	DecidedAt time.Time `json:"decided_at" validate:"required"`
}

func (DecisionNoop) decisionVariant() {}

// DecisionRollback: adopt 途中失敗で rollback.
//
//	rollback_reason + failed_step は **必須** (io-contracts.md §Rollback path).
type DecisionRollback struct {
	Action         DecisionAction `json:"action" validate:"required,eq=rollback"`
	SchemaVersion  string         `json:"schema_version" validate:"required,oneof=1"`
	RunID          RunID          `json:"run_id" validate:"required,run_id_fmt"`
	IdempotencyKey string         `json:"idempotency_key,omitempty" validate:"omitempty,sha256_hex"`

	RollbackReason RollbackReason `json:"rollback_reason" validate:"required,oneof=lease_failure remote_divergence registry_divergence worktree_rescue_loop manual_abort_pending_cleanup transactional_failure"`
	FailedStep     FailedStep     `json:"failed_step" validate:"required,oneof=10 20 30 40 50 60 70"`

	BestShaBefore string `json:"best_sha_before,omitempty" validate:"omitempty,sha1_hex"`
	TargetSha     string `json:"target_sha,omitempty" validate:"omitempty,sha1_hex"`

	Detail string `json:"detail,omitempty" validate:"omitempty,max=300"`

	DecidedAt time.Time `json:"decided_at" validate:"required"`
}

func (DecisionRollback) decisionVariant() {}

// RegistryAppendResult is the (offset, sha256) tuple recorded in intention /
// decision after a successful append to rules-registry.jsonl.
//
// Numeric type 規約: offset は int64 (uint64 禁止, io-contracts.md rev23/rev24).
// offset は Go zero-value (0) が合法値 (= 最初の entry) のため、`required` tag
// では欠落検出できない。custom UnmarshalJSON で JSON 側 field 存在を物理検証する.
type RegistryAppendResult struct {
	Offset int64  `json:"offset" validate:"gte=0"`
	Sha256 string `json:"sha256" validate:"required,sha256_hex"`
}

// ErrRegistryAppendResultMissingOffset is returned when the `offset` key is
// absent from a RegistryAppendResult JSON body (zero-value 問題回避).
var ErrRegistryAppendResultMissingOffset = errors.New("contracts: registry_append_result: offset field is required")

// UnmarshalJSON enforces physical presence of the `offset` field (can't rely on
// Go zero-value since 0 is a valid registry offset).
func (r *RegistryAppendResult) UnmarshalJSON(data []byte) error {
	type alias RegistryAppendResult
	var a alias
	if err := decodeStrictWithRequiredFields(data, &a, map[string]error{
		"offset": ErrRegistryAppendResultMissingOffset,
	}); err != nil {
		return err
	}
	*r = RegistryAppendResult(a)
	return validateStruct(*r)
}

// UnmarshalJSON implements strict tagged-union decoding for Decision.
func (d *Decision) UnmarshalJSON(data []byte) error {
	var env struct {
		Action DecisionAction `json:"action"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return err
	}
	switch env.Action {
	case DecisionActionAdopt:
		var v DecisionAdopt
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		d.Action = env.Action
		d.Value = v
	case DecisionActionReject:
		var v DecisionReject
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		d.Action = env.Action
		d.Value = v
	case DecisionActionNoop:
		var v DecisionNoop
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		d.Action = env.Action
		d.Value = v
	case DecisionActionRollback:
		var v DecisionRollback
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		d.Action = env.Action
		d.Value = v
	default:
		return ErrUnknownDecisionAction
	}
	return nil
}

// MarshalJSON emits the inner variant JSON.
func (d Decision) MarshalJSON() ([]byte, error) {
	if d.Value == nil {
		return nil, ErrUnknownDecisionAction
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(d.Value)
}

// Validate runs tag-based validation on the inner variant (Phase 0-bootstrap-1
// gate 3rd-round finding #1 / #2). Called automatically by EncodeStrict /
// MarshalStrict so producers cannot bypass variant validation on the write
// path.
func (d Decision) Validate() error {
	if d.Value == nil {
		return ErrUnknownDecisionAction
	}
	expected, inner, _, err := decisionVariantMetadata(d.Value)
	if err != nil {
		return err
	}
	if err := validateTaggedUnionDiscriminator(d.Action, expected, inner, ErrDecisionVariantTypeMismatch, ErrDecisionVariantActionMismatch); err != nil {
		return err
	}
	return runValidation(d.Value)
}

func decisionVariantMetadata(v DecisionVariant) (expected DecisionAction, inner DecisionAction, runID RunID, err error) {
	switch vv := v.(type) {
	case DecisionAdopt:
		return DecisionActionAdopt, vv.Action, vv.RunID, nil
	case *DecisionAdopt:
		if vv == nil {
			return "", "", "", ErrUnknownDecisionAction
		}
		return DecisionActionAdopt, vv.Action, vv.RunID, nil
	case DecisionReject:
		return DecisionActionReject, vv.Action, vv.RunID, nil
	case *DecisionReject:
		if vv == nil {
			return "", "", "", ErrUnknownDecisionAction
		}
		return DecisionActionReject, vv.Action, vv.RunID, nil
	case DecisionNoop:
		return DecisionActionNoop, vv.Action, vv.RunID, nil
	case *DecisionNoop:
		if vv == nil {
			return "", "", "", ErrUnknownDecisionAction
		}
		return DecisionActionNoop, vv.Action, vv.RunID, nil
	case DecisionRollback:
		return DecisionActionRollback, vv.Action, vv.RunID, nil
	case *DecisionRollback:
		if vv == nil {
			return "", "", "", ErrUnknownDecisionAction
		}
		return DecisionActionRollback, vv.Action, vv.RunID, nil
	default:
		return "", "", "", ErrUnknownDecisionAction
	}
}
