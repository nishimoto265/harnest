package contracts

import "time"

// NeedsRecoverySentinel is the durable `<runs_base>/needs-recovery/<run_id>.json`
// contract described in io-contracts.md §step70 recovery / sentinel sections.
type NeedsRecoverySentinel struct {
	RunID      RunID          `json:"run_id" validate:"required,run_id_fmt"`
	PR         int            `json:"pr" validate:"required,gt=0"`
	Reason     RollbackReason `json:"reason" validate:"required,oneof=lease_failure remote_divergence registry_divergence worktree_rescue_loop manual_abort_pending_cleanup transactional_failure"`
	FailedStep FailedStep     `json:"failed_step" validate:"required,oneof=10 20 30 40 50 60 70"`
	CreatedAt  time.Time      `json:"created_at" validate:"required"`
}

func (s *NeedsRecoverySentinel) UnmarshalJSON(data []byte) error {
	type alias NeedsRecoverySentinel
	var a alias
	if err := decodeStrict(data, &a); err != nil {
		return err
	}
	*s = NeedsRecoverySentinel(a)
	return s.Validate()
}

func (s NeedsRecoverySentinel) Validate() error {
	if err := validateStruct(s); err != nil {
		return err
	}
	return validateReasonFailedStepPair(s.Reason, s.FailedStep)
}
