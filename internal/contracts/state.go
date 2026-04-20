package contracts

import (
	"encoding/json"
	"time"
)

// StateKind is the discriminator for processed.jsonl entries.
// io-contracts.md §6.1 Resume (crash-resistant execution).
//
// Non-terminal (resume 対象):
//   started / step_done / interrupted / promoting / warning
// Terminal (detect が再 queue しない):
//   completed / failed / promoted / rollback / skipped / timeout / needs_manual_recovery
type StateKind string

const (
	StateKindStarted              StateKind = "started"
	StateKindStepDone             StateKind = "step_done"
	StateKindInterrupted          StateKind = "interrupted"
	StateKindPromoting            StateKind = "promoting"
	StateKindWarning              StateKind = "warning"
	StateKindCompleted            StateKind = "completed"
	StateKindFailed               StateKind = "failed"
	StateKindPromoted             StateKind = "promoted"
	StateKindRollback             StateKind = "rollback"
	StateKindSkipped              StateKind = "skipped"
	StateKindTimeout              StateKind = "timeout"
	StateKindNeedsManualRecovery  StateKind = "needs_manual_recovery"
)

// IsTerminal reports whether the given StateKind is a terminal event
// (detect が再 queue しない / resume 対象外).
func (k StateKind) IsTerminal() bool {
	switch k {
	case StateKindCompleted,
		StateKindFailed,
		StateKindPromoted,
		StateKindRollback,
		StateKindSkipped,
		StateKindTimeout,
		StateKindNeedsManualRecovery:
		return true
	default:
		return false
	}
}

// InterruptedReason enum (io-contracts.md §state vocabulary 拡張).
type InterruptedReason string

const (
	InterruptedReasonRateLimit     InterruptedReason = "rate_limit"
	InterruptedReasonBudget        InterruptedReason = "budget"
	InterruptedReasonContext       InterruptedReason = "context"
	InterruptedReasonSignal        InterruptedReason = "signal"
	InterruptedReasonUnknown       InterruptedReason = "unknown"
	InterruptedReasonPrePushCrash  InterruptedReason = "pre_push_crash"
)

// WarningKind enum (io-contracts.md §warning event kind × emitter table、closed enum).
type WarningKind string

const (
	WarningKindRegistrySizeHigh     WarningKind = "registry_size_high"
	WarningKindRegistrySizeCritical WarningKind = "registry_size_critical"
	WarningKindRescueRetry          WarningKind = "rescue_retry"
)

// StateEntry is one row appended to `<runs_base>/processed.jsonl`.
// Tagged union over `kind`. All variants carry `step` (required for every
// non-terminal + terminal event, io-contracts.md §resume vocabulary).
type StateEntry struct {
	Kind  StateKind     `json:"kind"`
	Value StateVariant  `json:"-"`
}

// StateVariant is implemented by every StateEntry variant struct.
type StateVariant interface {
	stateVariant()
}

// Common non-warning variants share (pr, run_id, step, at) + kind-specific fields.

// StateEntryStarted: step=10 固定 (step10 開始前の意味).
type StateEntryStarted struct {
	Kind   StateKind `json:"kind" validate:"required,eq=started"`
	PR     int       `json:"pr" validate:"required,gt=0"`
	RunID  RunID     `json:"run_id" validate:"required,run_id_fmt"`
	Step   FailedStep `json:"step" validate:"required,oneof=10 20 30 40 50 60 70"`
	At     time.Time `json:"at" validate:"required"`
}

func (StateEntryStarted) stateVariant() {}

// StateEntryStepDone: step 完了時 append.
type StateEntryStepDone struct {
	Kind  StateKind  `json:"kind" validate:"required,eq=step_done"`
	PR    int        `json:"pr" validate:"required,gt=0"`
	RunID RunID      `json:"run_id" validate:"required,run_id_fmt"`
	Step  FailedStep `json:"step" validate:"required,oneof=10 20 30 40 50 60 70"`
	At    time.Time  `json:"at" validate:"required"`
}

func (StateEntryStepDone) stateVariant() {}

// StateEntryInterrupted: 非 terminal、resume 対象. detail 300字 cap + sidecar.
type StateEntryInterrupted struct {
	Kind              StateKind         `json:"kind" validate:"required,eq=interrupted"`
	PR                int               `json:"pr" validate:"required,gt=0"`
	RunID             RunID             `json:"run_id" validate:"required,run_id_fmt"`
	Step              FailedStep        `json:"step" validate:"required,oneof=10 20 30 40 50 60 70"`
	Reason            InterruptedReason `json:"reason" validate:"required,oneof=rate_limit budget context signal unknown pre_push_crash"`
	Detail            string            `json:"detail,omitempty" validate:"omitempty,max=300"`
	DetailOverflowRef *OverflowRef      `json:"detail_overflow_ref,omitempty" validate:"omitempty"`
	At                time.Time         `json:"at" validate:"required"`
}

func (StateEntryInterrupted) stateVariant() {}

// StateEntryPromoting: step70 planning 完了時 step70 自身が append.
type StateEntryPromoting struct {
	Kind  StateKind  `json:"kind" validate:"required,eq=promoting"`
	PR    int        `json:"pr" validate:"required,gt=0"`
	RunID RunID      `json:"run_id" validate:"required,run_id_fmt"`
	Step  FailedStep `json:"step" validate:"required,eq=70"`
	At    time.Time  `json:"at" validate:"required"`
}

func (StateEntryPromoting) stateVariant() {}

// StateEntryWarning: 運用 alert. pr / run_id は optional (io-contracts.md rev22).
// kind enum は closed (registry_size_high / registry_size_critical / rescue_retry).
type StateEntryWarning struct {
	Kind              StateKind    `json:"kind" validate:"required,eq=warning"`
	PR                *int         `json:"pr,omitempty" validate:"omitempty,gt=0"`
	RunID             *RunID       `json:"run_id,omitempty" validate:"omitempty,run_id_fmt"`
	Step              FailedStep   `json:"step" validate:"required,oneof=10 20 30 40 50 60 70"`
	WarningKind       WarningKind  `json:"warning_kind" validate:"required,oneof=registry_size_high registry_size_critical rescue_retry"`
	Count             *int64       `json:"count,omitempty" validate:"omitempty,gte=0"`
	Detail            string       `json:"detail,omitempty" validate:"omitempty,max=300"`
	DetailOverflowRef *OverflowRef `json:"detail_overflow_ref,omitempty" validate:"omitempty"`
	At                time.Time    `json:"at" validate:"required"`
}

func (StateEntryWarning) stateVariant() {}

// StateEntryCompleted: terminal. detail に `sentinel_manually_cleared` などを
// 格納するユースケースがある (recover --clear-sentinel / --finalize-cleanup).
type StateEntryCompleted struct {
	Kind              StateKind    `json:"kind" validate:"required,eq=completed"`
	PR                int          `json:"pr" validate:"required,gt=0"`
	RunID             RunID        `json:"run_id" validate:"required,run_id_fmt"`
	Step              FailedStep   `json:"step" validate:"required,oneof=10 20 30 40 50 60 70"`
	Detail            string       `json:"detail,omitempty" validate:"omitempty,max=300"`
	DetailOverflowRef *OverflowRef `json:"detail_overflow_ref,omitempty" validate:"omitempty"`
	At                time.Time    `json:"at" validate:"required"`
}

func (StateEntryCompleted) stateVariant() {}

// StateEntryFailed: terminal. reason は実装側で決定する短い文字列.
type StateEntryFailed struct {
	Kind              StateKind    `json:"kind" validate:"required,eq=failed"`
	PR                int          `json:"pr" validate:"required,gt=0"`
	RunID             RunID        `json:"run_id" validate:"required,run_id_fmt"`
	Step              FailedStep   `json:"step" validate:"required,oneof=10 20 30 40 50 60 70"`
	Reason            string       `json:"reason" validate:"required,max=200"`
	Detail            string       `json:"detail,omitempty" validate:"omitempty,max=300"`
	DetailOverflowRef *OverflowRef `json:"detail_overflow_ref,omitempty" validate:"omitempty"`
	At                time.Time    `json:"at" validate:"required"`
}

func (StateEntryFailed) stateVariant() {}

// StateEntryPromoted: terminal. step70 が promotion.lock 保持中に自己 append.
type StateEntryPromoted struct {
	Kind  StateKind  `json:"kind" validate:"required,eq=promoted"`
	PR    int        `json:"pr" validate:"required,gt=0"`
	RunID RunID      `json:"run_id" validate:"required,run_id_fmt"`
	Step  FailedStep `json:"step" validate:"required,eq=70"`
	At    time.Time  `json:"at" validate:"required"`
}

func (StateEntryPromoted) stateVariant() {}

// StateEntryRollback: terminal (per-PR). step70 が自己 append.
type StateEntryRollback struct {
	Kind           StateKind      `json:"kind" validate:"required,eq=rollback"`
	PR             int            `json:"pr" validate:"required,gt=0"`
	RunID          RunID          `json:"run_id" validate:"required,run_id_fmt"`
	Step           FailedStep     `json:"step" validate:"required,eq=70"`
	RollbackReason RollbackReason `json:"rollback_reason" validate:"required,oneof=lease_failure remote_divergence registry_divergence worktree_rescue_loop manual_abort_pending_cleanup transactional_failure"`
	FailedStep     FailedStep     `json:"failed_step" validate:"required,oneof=10 20 30 40 50 60 70"`
	At             time.Time      `json:"at" validate:"required"`
}

func (StateEntryRollback) stateVariant() {}

// StateEntrySkipped: terminal (detect 時点で既処理判定等).
type StateEntrySkipped struct {
	Kind              StateKind    `json:"kind" validate:"required,eq=skipped"`
	PR                int          `json:"pr" validate:"required,gt=0"`
	RunID             RunID        `json:"run_id" validate:"required,run_id_fmt"`
	Step              FailedStep   `json:"step" validate:"required,oneof=10 20 30 40 50 60 70"`
	Detail            string       `json:"detail,omitempty" validate:"omitempty,max=300"`
	DetailOverflowRef *OverflowRef `json:"detail_overflow_ref,omitempty" validate:"omitempty"`
	At                time.Time    `json:"at" validate:"required"`
}

func (StateEntrySkipped) stateVariant() {}

// StateEntryTimeout: terminal (step20/50 全 agent timeout 等).
type StateEntryTimeout struct {
	Kind  StateKind  `json:"kind" validate:"required,eq=timeout"`
	PR    int        `json:"pr" validate:"required,gt=0"`
	RunID RunID      `json:"run_id" validate:"required,run_id_fmt"`
	Step  FailedStep `json:"step" validate:"required,oneof=10 20 30 40 50 60 70"`
	At    time.Time  `json:"at" validate:"required"`
}

func (StateEntryTimeout) stateVariant() {}

// StateEntryNeedsManualRecovery: terminal (per-PR + global block via sentinel).
// failed_step は io-contracts.md §needs_manual_recovery.failed_step に準拠.
type StateEntryNeedsManualRecovery struct {
	Kind              StateKind      `json:"kind" validate:"required,eq=needs_manual_recovery"`
	PR                int            `json:"pr" validate:"required,gt=0"`
	RunID             RunID          `json:"run_id" validate:"required,run_id_fmt"`
	Step              FailedStep     `json:"step" validate:"required,oneof=10 20 30 40 50 60 70"`
	Reason            RollbackReason `json:"reason" validate:"required,oneof=lease_failure remote_divergence registry_divergence worktree_rescue_loop manual_abort_pending_cleanup transactional_failure"`
	FailedStep        FailedStep     `json:"failed_step" validate:"required,oneof=10 20 30 40 50 60 70"`
	Detail            string         `json:"detail,omitempty" validate:"omitempty,max=300"`
	DetailOverflowRef *OverflowRef   `json:"detail_overflow_ref,omitempty" validate:"omitempty"`
	At                time.Time      `json:"at" validate:"required"`
}

func (StateEntryNeedsManualRecovery) stateVariant() {}

// UnmarshalJSON implements strict tagged-union decoding for StateEntry.
func (e *StateEntry) UnmarshalJSON(data []byte) error {
	var env struct {
		Kind StateKind `json:"kind"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return err
	}
	switch env.Kind {
	case StateKindStarted:
		var v StateEntryStarted
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind, e.Value = env.Kind, v
	case StateKindStepDone:
		var v StateEntryStepDone
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind, e.Value = env.Kind, v
	case StateKindInterrupted:
		var v StateEntryInterrupted
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind, e.Value = env.Kind, v
	case StateKindPromoting:
		var v StateEntryPromoting
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind, e.Value = env.Kind, v
	case StateKindWarning:
		var v StateEntryWarning
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind, e.Value = env.Kind, v
	case StateKindCompleted:
		var v StateEntryCompleted
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind, e.Value = env.Kind, v
	case StateKindFailed:
		var v StateEntryFailed
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind, e.Value = env.Kind, v
	case StateKindPromoted:
		var v StateEntryPromoted
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind, e.Value = env.Kind, v
	case StateKindRollback:
		var v StateEntryRollback
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind, e.Value = env.Kind, v
	case StateKindSkipped:
		var v StateEntrySkipped
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind, e.Value = env.Kind, v
	case StateKindTimeout:
		var v StateEntryTimeout
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind, e.Value = env.Kind, v
	case StateKindNeedsManualRecovery:
		var v StateEntryNeedsManualRecovery
		if err := decodeStrict(data, &v); err != nil {
			return err
		}
		if err := validateStruct(v); err != nil {
			return err
		}
		e.Kind, e.Value = env.Kind, v
	default:
		return ErrUnknownStateKind
	}
	return nil
}

// MarshalJSON emits the inner variant JSON.
func (e StateEntry) MarshalJSON() ([]byte, error) {
	if e.Value == nil {
		return nil, ErrUnknownStateKind
	}
	return json.Marshal(e.Value)
}
