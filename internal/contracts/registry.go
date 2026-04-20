package contracts

import (
	"encoding/json"
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
	SunsetTransitionActivate     SunsetTransition = "activate"
	SunsetTransitionDeprecate    SunsetTransition = "deprecate"
	SunsetTransitionArchive      SunsetTransition = "archive"
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
	Kind  RegistryKind         `json:"kind"`
	Value RuleRegistryVariant  `json:"-"`
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
	PrevHash       string       `json:"prev_hash" validate:"required,sha256_hex"`
	ByRunID        RunID        `json:"by_run_id" validate:"required,run_id_fmt"`
	At             time.Time    `json:"at" validate:"required"`
}

func (RuleRegistryUpdated) ruleRegistryVariant() {}

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
	PrevHash       string         `json:"prev_hash" validate:"required,sha256_hex"`
	At             time.Time      `json:"at" validate:"required"`
}

func (RuleRegistryRolledBack) ruleRegistryVariant() {}

// --- sunset entries (archive/cycle③ emits) ---

// RuleRegistryStatusChanged: rule status 遷移 (kind=status_changed).
// op_id = sha256(sunset_run_id || rule_id || transition).
type RuleRegistryStatusChanged struct {
	Kind            RegistryKind     `json:"kind" validate:"required,eq=status_changed"`
	SchemaVersion   string           `json:"schema_version" validate:"required,oneof=1"`
	RuleID          string           `json:"rule_id" validate:"required"`
	PrevStatus      RuleStatus       `json:"prev_status" validate:"required,oneof=active deprecated archived"`
	NewStatus       RuleStatus       `json:"new_status" validate:"required,oneof=active deprecated archived"`
	Transition      SunsetTransition `json:"transition" validate:"required,oneof=activate deprecate archive"`
	OpID            string           `json:"op_id" validate:"required,sha256_hex"`
	VersionSeq      int64            `json:"version_seq" validate:"required,gte=1"`
	PrevHash        string           `json:"prev_hash" validate:"required,sha256_hex"`
	BySunsetRunID   string           `json:"by_sunset_run_id" validate:"required"`
	At              time.Time        `json:"at" validate:"required"`
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
	PrevHash      string       `json:"prev_hash" validate:"required,sha256_hex"`
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
	PrevHash      string       `json:"prev_hash" validate:"required,sha256_hex"`
	BySunsetRunID string       `json:"by_sunset_run_id" validate:"required"`
	At            time.Time    `json:"at" validate:"required"`
}

func (RuleRegistryRestored) ruleRegistryVariant() {}

// UnmarshalJSON implements strict tagged-union decoding for RuleRegistryEntry.
func (e *RuleRegistryEntry) UnmarshalJSON(data []byte) error {
	var env struct {
		Kind RegistryKind `json:"kind"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return err
	}
	switch env.Kind {
	case RegistryKindAdded:
		var v RuleRegistryAdded
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind = env.Kind
		e.Value = v
	case RegistryKindUpdated:
		var v RuleRegistryUpdated
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind = env.Kind
		e.Value = v
	case RegistryKindRolledBack:
		var v RuleRegistryRolledBack
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind = env.Kind
		e.Value = v
	case RegistryKindStatusChanged:
		var v RuleRegistryStatusChanged
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind = env.Kind
		e.Value = v
	case RegistryKindArchived:
		var v RuleRegistryArchived
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind = env.Kind
		e.Value = v
	case RegistryKindRestored:
		var v RuleRegistryRestored
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind = env.Kind
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
	return json.Marshal(e.Value)
}
