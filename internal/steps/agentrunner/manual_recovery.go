package agentrunner

import (
	"fmt"

	"github.com/nishimoto265/harnest/internal/contracts"
)

type ManualRecoveryRequiredError struct {
	Reason contracts.RollbackReason
	Detail string
	Err    error
}

func (e *ManualRecoveryRequiredError) Error() string {
	if e == nil {
		return "agentrunner: manual recovery required"
	}
	if e.Detail == "" {
		return fmt.Sprintf("agentrunner: manual recovery required: reason=%s", e.Reason)
	}
	return fmt.Sprintf("agentrunner: manual recovery required: reason=%s: %s", e.Reason, e.Detail)
}

func (e *ManualRecoveryRequiredError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
