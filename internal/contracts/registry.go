package contracts

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// RuleStatus: rules-registry の lifecycle (sunset entries で遷移).
type RuleStatus string

const (
	RuleStatusActive     RuleStatus = "active"
	RuleStatusDeprecated RuleStatus = "deprecated"
	RuleStatusArchived   RuleStatus = "archived"
)

// SunsetTransition: kind=status_changed の遷移種別. Phase 0 は 3 種に制限.
type SunsetTransition string

const (
	SunsetTransitionActivate  SunsetTransition = "activate"
	SunsetTransitionDeprecate SunsetTransition = "deprecate"
	SunsetTransitionArchive   SunsetTransition = "archive"
)

// RegistryKind: rules-registry.jsonl tagged-union discriminator.
// io-contracts.md §rules-registry entry schema.
type RegistryKind string

const (
	RegistryKindAdded         RegistryKind = "added"
	RegistryKindUpdated       RegistryKind = "updated"
	RegistryKindStatusChanged RegistryKind = "status_changed"
	RegistryKindArchived      RegistryKind = "archived"
	RegistryKindRestored      RegistryKind = "restored"
	RegistryKindRolledBack    RegistryKind = "rolled_back"
)

// RuleRegistryEntry is one row appended to `<runs_base>/rules-registry.jsonl`.
// Tagged union over `kind`. Numeric type 規約: version_seq / registry_offset は
// int64 (uint64 禁止、rev23/rev24).
type RuleRegistryEntry struct {
	Kind  RegistryKind        `json:"kind"`
	Value RuleRegistryVariant `json:"-"`
}

// RuleRegistryVariant is implemented by the six registry variant structs.
type RuleRegistryVariant interface {
	ruleRegistryVariant()
}

// --- promotion entries (step70 emits) ---

// RuleRegistryAdded: 新 rule 追加 (kind=added).
type RuleRegistryAdded struct {
	Kind           RegistryKind `json:"kind" validate:"required,eq=added"`
	SchemaVersion  string       `json:"schema_version" validate:"required,oneof=1"`
	RuleID         string       `json:"rule_id" validate:"required"`
	RulePath       string       `json:"rule_path" validate:"required"`
	Sha256         string       `json:"sha256" validate:"required,sha256_hex"`
	IdempotencyKey string       `json:"idempotency_key" validate:"required,sha256_hex"`
	VersionSeq     int64        `json:"version_seq" validate:"required,gte=1"`
	PrevHash       string       `json:"prev_hash" validate:"omitempty,sha256_hex"` // empty 許容: 初 entry
	ByRunID        RunID        `json:"by_run_id" validate:"required,run_id_fmt"`
	At             time.Time    `json:"at" validate:"required"`
}

func (RuleRegistryAdded) ruleRegistryVariant() {}

func (e RuleRegistryAdded) Validate() error {
	if err := validateStruct(e); err != nil {
		return err
	}
	if err := ValidateRuleID(e.RuleID); err != nil {
		return err
	}
	if err := ValidateRulePath(e.RulePath); err != nil {
		return err
	}
	return validateRegistryChain(e.VersionSeq, e.PrevHash)
}

// RuleRegistryUpdated: 既存 rule 更新 (kind=updated).
type RuleRegistryUpdated struct {
	Kind           RegistryKind `json:"kind" validate:"required,eq=updated"`
	SchemaVersion  string       `json:"schema_version" validate:"required,oneof=1"`
	RuleID         string       `json:"rule_id" validate:"required"`
	RulePath       string       `json:"rule_path" validate:"required"`
	Sha256         string       `json:"sha256" validate:"required,sha256_hex"`
	PrevSha256     string       `json:"prev_sha256" validate:"required,sha256_hex"`
	IdempotencyKey string       `json:"idempotency_key" validate:"required,sha256_hex"`
	VersionSeq     int64        `json:"version_seq" validate:"required,gte=1"`
	PrevHash       string       `json:"prev_hash" validate:"omitempty,sha256_hex"`
	ByRunID        RunID        `json:"by_run_id" validate:"required,run_id_fmt"`
	At             time.Time    `json:"at" validate:"required"`
}

func (RuleRegistryUpdated) ruleRegistryVariant() {}

func (e RuleRegistryUpdated) Validate() error {
	if err := validateStruct(e); err != nil {
		return err
	}
	if err := ValidateRuleID(e.RuleID); err != nil {
		return err
	}
	if err := ValidateRulePath(e.RulePath); err != nil {
		return err
	}
	return validateRegistryChain(e.VersionSeq, e.PrevHash)
}

// --- rollback entry (step70 emits, rev18) ---

// RuleRegistryRolledBack: adopt 失敗時の rollback marker (kind=rolled_back).
// target_op_id は対象 promotion entry の idempotency_key と同値.
type RuleRegistryRolledBack struct {
	Kind           RegistryKind   `json:"kind" validate:"required,eq=rolled_back"`
	SchemaVersion  string         `json:"schema_version" validate:"required,oneof=1"`
	TargetOpID     string         `json:"target_op_id" validate:"required,sha256_hex"`
	TargetOffset   int64          `json:"target_offset" validate:"gte=0"`
	TargetSha256   string         `json:"target_sha256" validate:"required,sha256_hex"`
	ByRunID        RunID          `json:"by_run_id" validate:"required,run_id_fmt"`
	RollbackReason RollbackReason `json:"rollback_reason" validate:"required,oneof=lease_failure remote_divergence registry_divergence worktree_rescue_loop manual_abort_pending_cleanup transactional_failure"`
	FailedStep     FailedStep     `json:"failed_step" validate:"required,oneof=10 20 30 40 50 60 70"`
	VersionSeq     int64          `json:"version_seq" validate:"required,gte=1"`
	PrevHash       string         `json:"prev_hash" validate:"omitempty,sha256_hex"`
	At             time.Time      `json:"at" validate:"required"`
}

func (RuleRegistryRolledBack) ruleRegistryVariant() {}

var (
	ErrRegistryPrevHashSequenceMismatch      = errors.New("contracts: registry: prev_hash is required when version_seq > 1")
	ErrRegistryPrevHashMustBeEmptyOnFirst    = errors.New("contracts: registry: prev_hash must be empty when version_seq == 1")
	ErrRegistryRolledBackMissingTargetOffset = errors.New("contracts: registry: rolled_back: target_offset field is required")
	ErrRuleIdempotencyIndexMissingOffset     = errors.New("contracts: registry: idempotency-index: registry_offset field is required")
	ErrRegistryVariantTypeMismatch           = errors.New("contracts: registry: kind does not match variant type")
	ErrRegistryVariantKindMismatch           = errors.New("contracts: registry: kind does not match inner kind field")
)

func (e *RuleRegistryRolledBack) UnmarshalJSON(data []byte) error {
	type alias RuleRegistryRolledBack
	var a alias
	if err := decodeStrictWithRequiredFields(data, &a, map[string]error{
		"target_offset": ErrRegistryRolledBackMissingTargetOffset,
	}); err != nil {
		return err
	}
	*e = RuleRegistryRolledBack(a)
	return e.Validate()
}

func (e RuleRegistryRolledBack) Validate() error {
	if err := validateStruct(e); err != nil {
		return err
	}
	if err := validateRegistryChain(e.VersionSeq, e.PrevHash); err != nil {
		return err
	}
	return validateReasonFailedStepPair(e.RollbackReason, e.FailedStep)
}

// --- sunset entries (archive/cycle③ emits) ---

// RuleRegistryStatusChanged: rule status 遷移 (kind=status_changed).
// op_id = sha256(sunset_run_id || rule_id || transition).
type RuleRegistryStatusChanged struct {
	Kind          RegistryKind     `json:"kind" validate:"required,eq=status_changed"`
	SchemaVersion string           `json:"schema_version" validate:"required,oneof=1"`
	RuleID        string           `json:"rule_id" validate:"required"`
	PrevStatus    RuleStatus       `json:"prev_status" validate:"required,oneof=active deprecated archived"`
	NewStatus     RuleStatus       `json:"new_status" validate:"required,oneof=active deprecated archived"`
	Transition    SunsetTransition `json:"transition" validate:"required,oneof=activate deprecate archive"`
	OpID          string           `json:"op_id" validate:"required,sha256_hex"`
	VersionSeq    int64            `json:"version_seq" validate:"required,gte=1"`
	PrevHash      string           `json:"prev_hash" validate:"omitempty,sha256_hex"`
	BySunsetRunID string           `json:"by_sunset_run_id" validate:"required"`
	At            time.Time        `json:"at" validate:"required"`
}

func (RuleRegistryStatusChanged) ruleRegistryVariant() {}

// RuleRegistryArchived: 明示的 archive (kind=archived).
// io-contracts.md §rules-registry: status_changed / archived / restored は
// prev_status / new_status を持つ.
type RuleRegistryArchived struct {
	Kind          RegistryKind `json:"kind" validate:"required,eq=archived"`
	SchemaVersion string       `json:"schema_version" validate:"required,oneof=1"`
	RuleID        string       `json:"rule_id" validate:"required"`
	PrevStatus    RuleStatus   `json:"prev_status" validate:"required,oneof=active deprecated archived"`
	NewStatus     RuleStatus   `json:"new_status" validate:"required,oneof=active deprecated archived"`
	OpID          string       `json:"op_id" validate:"required,sha256_hex"`
	VersionSeq    int64        `json:"version_seq" validate:"required,gte=1"`
	PrevHash      string       `json:"prev_hash" validate:"omitempty,sha256_hex"`
	BySunsetRunID string       `json:"by_sunset_run_id" validate:"required"`
	At            time.Time    `json:"at" validate:"required"`
}

func (RuleRegistryArchived) ruleRegistryVariant() {}

// RuleRegistryRestored: archive 取消し (kind=restored).
type RuleRegistryRestored struct {
	Kind          RegistryKind `json:"kind" validate:"required,eq=restored"`
	SchemaVersion string       `json:"schema_version" validate:"required,oneof=1"`
	RuleID        string       `json:"rule_id" validate:"required"`
	PrevStatus    RuleStatus   `json:"prev_status" validate:"required,oneof=active deprecated archived"`
	NewStatus     RuleStatus   `json:"new_status" validate:"required,oneof=active deprecated archived"`
	OpID          string       `json:"op_id" validate:"required,sha256_hex"`
	VersionSeq    int64        `json:"version_seq" validate:"required,gte=1"`
	PrevHash      string       `json:"prev_hash" validate:"omitempty,sha256_hex"`
	BySunsetRunID string       `json:"by_sunset_run_id" validate:"required"`
	At            time.Time    `json:"at" validate:"required"`
}

func (RuleRegistryRestored) ruleRegistryVariant() {}

// Registry lifecycle transition errors (Phase 0-bootstrap-1 gate 2nd-round
// finding #6). status_changed / archived / restored の 3 variant は
// prev_status / new_status の組合せを enforce する:
//   - status_changed: (active → deprecated) または (deprecated → active) のみ
//   - archived:       prev_status ∈ {active, deprecated} AND new_status == archived
//   - restored:       prev_status == archived AND new_status ∈ {active, deprecated}
var (
	ErrRegistryStatusChangedInvalidTransition  = errors.New("contracts: registry: status_changed allows only active↔deprecated transitions")
	ErrRegistryStatusChangedTransitionMismatch = errors.New("contracts: registry: status_changed transition field inconsistent with prev/new status")
	ErrRegistryArchivedInvalidTransition       = errors.New("contracts: registry: archived requires prev_status in {active,deprecated} and new_status == archived")
	ErrRegistryRestoredInvalidTransition       = errors.New("contracts: registry: restored requires prev_status == archived and new_status in {active,deprecated}")
)

// Validate enforces tag-based validation + status_changed transition semantics.
func (e RuleRegistryStatusChanged) Validate() error {
	if err := validateStruct(e); err != nil {
		return err
	}
	if err := ValidateRuleID(e.RuleID); err != nil {
		return err
	}
	if err := validateRegistryChain(e.VersionSeq, e.PrevHash); err != nil {
		return err
	}
	// Allowed transitions: active↔deprecated (archive は archived variant 経由).
	switch {
	case e.PrevStatus == RuleStatusActive && e.NewStatus == RuleStatusDeprecated:
		if e.Transition != SunsetTransitionDeprecate {
			return fmt.Errorf("%w: prev=%s new=%s transition=%s", ErrRegistryStatusChangedTransitionMismatch, e.PrevStatus, e.NewStatus, e.Transition)
		}
	case e.PrevStatus == RuleStatusDeprecated && e.NewStatus == RuleStatusActive:
		if e.Transition != SunsetTransitionActivate {
			return fmt.Errorf("%w: prev=%s new=%s transition=%s", ErrRegistryStatusChangedTransitionMismatch, e.PrevStatus, e.NewStatus, e.Transition)
		}
	default:
		return fmt.Errorf("%w: prev=%s new=%s", ErrRegistryStatusChangedInvalidTransition, e.PrevStatus, e.NewStatus)
	}
	return nil
}

// Validate enforces tag-based validation + archived transition semantics.
func (e RuleRegistryArchived) Validate() error {
	if err := validateStruct(e); err != nil {
		return err
	}
	if err := ValidateRuleID(e.RuleID); err != nil {
		return err
	}
	if err := validateRegistryChain(e.VersionSeq, e.PrevHash); err != nil {
		return err
	}
	if e.NewStatus != RuleStatusArchived {
		return fmt.Errorf("%w: prev=%s new=%s", ErrRegistryArchivedInvalidTransition, e.PrevStatus, e.NewStatus)
	}
	switch e.PrevStatus {
	case RuleStatusActive, RuleStatusDeprecated:
		// ok
	default:
		return fmt.Errorf("%w: prev=%s new=%s", ErrRegistryArchivedInvalidTransition, e.PrevStatus, e.NewStatus)
	}
	return nil
}

// Validate enforces tag-based validation + restored transition semantics.
func (e RuleRegistryRestored) Validate() error {
	if err := validateStruct(e); err != nil {
		return err
	}
	if err := ValidateRuleID(e.RuleID); err != nil {
		return err
	}
	if err := validateRegistryChain(e.VersionSeq, e.PrevHash); err != nil {
		return err
	}
	if e.PrevStatus != RuleStatusArchived {
		return fmt.Errorf("%w: prev=%s new=%s", ErrRegistryRestoredInvalidTransition, e.PrevStatus, e.NewStatus)
	}
	switch e.NewStatus {
	case RuleStatusActive, RuleStatusDeprecated:
		// ok
	default:
		return fmt.Errorf("%w: prev=%s new=%s", ErrRegistryRestoredInvalidTransition, e.PrevStatus, e.NewStatus)
	}
	return nil
}

// UnmarshalJSON implements strict tagged-union decoding for RuleRegistryEntry.
func (e *RuleRegistryEntry) UnmarshalJSON(data []byte) error {
	var kind RegistryKind
	if err := DecodeStrictDiscriminatorField(data, "kind", &kind); err != nil {
		return err
	}
	switch kind {
	case RegistryKindAdded:
		var v RuleRegistryAdded
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind = kind
		e.Value = v
	case RegistryKindUpdated:
		var v RuleRegistryUpdated
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind = kind
		e.Value = v
	case RegistryKindRolledBack:
		var v RuleRegistryRolledBack
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind = kind
		e.Value = v
	case RegistryKindStatusChanged:
		var v RuleRegistryStatusChanged
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind = kind
		e.Value = v
	case RegistryKindArchived:
		var v RuleRegistryArchived
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind = kind
		e.Value = v
	case RegistryKindRestored:
		var v RuleRegistryRestored
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind = kind
		e.Value = v
	default:
		return ErrUnknownRegistryKind
	}
	return nil
}

// MarshalJSON emits the inner variant JSON.
func (e RuleRegistryEntry) MarshalJSON() ([]byte, error) {
	if e.Value == nil {
		return nil, ErrUnknownRegistryKind
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(e.Value)
}

// Validate dispatches to the inner variant's Validate() (if defined — that
// covers status_changed / archived / restored transition rules) or falls
// back to tag-based validateStruct for the rest (Phase 0-bootstrap-1 gate
// 3rd-round finding #1 / #2). Called automatically by EncodeStrict /
// MarshalStrict so registry entries cannot be written with invalid lifecycle
// transitions.
func (e RuleRegistryEntry) Validate() error {
	if e.Value == nil {
		return ErrUnknownRegistryKind
	}
	expected, inner, err := ruleRegistryVariantMetadata(e.Value)
	if err != nil {
		return err
	}
	if err := validateTaggedUnionDiscriminator(e.Kind, expected, inner, ErrRegistryVariantTypeMismatch, ErrRegistryVariantKindMismatch); err != nil {
		return err
	}
	return runValidation(e.Value)
}

func ruleRegistryVariantMetadata(v RuleRegistryVariant) (expected RegistryKind, inner RegistryKind, err error) {
	switch vv := v.(type) {
	case RuleRegistryAdded:
		return RegistryKindAdded, vv.Kind, nil
	case *RuleRegistryAdded:
		if vv == nil {
			return "", "", ErrUnknownRegistryKind
		}
		return RegistryKindAdded, vv.Kind, nil
	case RuleRegistryUpdated:
		return RegistryKindUpdated, vv.Kind, nil
	case *RuleRegistryUpdated:
		if vv == nil {
			return "", "", ErrUnknownRegistryKind
		}
		return RegistryKindUpdated, vv.Kind, nil
	case RuleRegistryRolledBack:
		return RegistryKindRolledBack, vv.Kind, nil
	case *RuleRegistryRolledBack:
		if vv == nil {
			return "", "", ErrUnknownRegistryKind
		}
		return RegistryKindRolledBack, vv.Kind, nil
	case RuleRegistryStatusChanged:
		return RegistryKindStatusChanged, vv.Kind, nil
	case *RuleRegistryStatusChanged:
		if vv == nil {
			return "", "", ErrUnknownRegistryKind
		}
		return RegistryKindStatusChanged, vv.Kind, nil
	case RuleRegistryArchived:
		return RegistryKindArchived, vv.Kind, nil
	case *RuleRegistryArchived:
		if vv == nil {
			return "", "", ErrUnknownRegistryKind
		}
		return RegistryKindArchived, vv.Kind, nil
	case RuleRegistryRestored:
		return RegistryKindRestored, vv.Kind, nil
	case *RuleRegistryRestored:
		if vv == nil {
			return "", "", ErrUnknownRegistryKind
		}
		return RegistryKindRestored, vv.Kind, nil
	default:
		return "", "", ErrUnknownRegistryKind
	}
}

func validateRegistryChain(versionSeq int64, prevHash string) error {
	if versionSeq == 1 && prevHash != "" {
		return fmt.Errorf("%w: version_seq=%d prev_hash=%q", ErrRegistryPrevHashMustBeEmptyOnFirst, versionSeq, prevHash)
	}
	if versionSeq > 1 && prevHash == "" {
		return fmt.Errorf("%w: version_seq=%d prev_hash=%q", ErrRegistryPrevHashSequenceMismatch, versionSeq, prevHash)
	}
	return nil
}

type RuleIdempotencyIndexEntry struct {
	IdempotencyKey string       `json:"idempotency_key" validate:"required,sha256_hex"`
	RegistryOffset int64        `json:"registry_offset" validate:"gte=0"`
	RegistrySha256 string       `json:"registry_sha256" validate:"required,sha256_hex"`
	Kind           RegistryKind `json:"kind" validate:"required,oneof=added updated rolled_back status_changed archived restored"`
	At             time.Time    `json:"at" validate:"required"`
}

func (e *RuleIdempotencyIndexEntry) UnmarshalJSON(data []byte) error {
	type alias RuleIdempotencyIndexEntry
	var a alias
	if err := decodeStrictWithRequiredFields(data, &a, map[string]error{
		"registry_offset": ErrRuleIdempotencyIndexMissingOffset,
	}); err != nil {
		return err
	}
	*e = RuleIdempotencyIndexEntry(a)
	return e.Validate()
}

func (e RuleIdempotencyIndexEntry) Validate() error {
	return validateStruct(e)
}
