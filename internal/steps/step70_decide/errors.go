package step70_decide

import "errors"

// Public sentinel errors for the step70 staged-transaction machinery.
// Callers (and GitOps implementations) wrap these so the decision code can
// classify the failure into the correct rollback reason without relying on
// string matching.
var (
	// ErrBlockedBySentinel signals that a durable needs-recovery sentinel from
	// another incomplete run is present, so step70 must not begin.
	ErrBlockedBySentinel = errors.New("step70: blocked by needs_manual_recovery sentinel")

	// ErrLeaseFailure: git push --force-with-lease rejected because remote
	// advanced past best_sha_before since planning (lease mismatch). Caller
	// must route to the rollback path with reason=lease_failure.
	ErrLeaseFailure = errors.New("step70: git push lease mismatch")

	// ErrRemoteDivergence: remote HEAD is neither target_sha nor
	// best_sha_before — branch state is unknown, so automatic recovery is
	// unsafe and the flow transitions to needs_manual_recovery.
	ErrRemoteDivergence = errors.New("step70: remote best_branch diverged from both target_sha and best_sha_before")

	// ErrRegistryDivergence: registry HEAD changed and no idempotency match
	// was found in the tail scan — another promoter appended to the registry
	// while we were planning.
	ErrRegistryDivergence = errors.New("step70: rules-registry advanced during planning with no idempotency match")

	// ErrNeedsManualRecovery signals the caller that a needs-recovery sentinel
	// was written and the flow exited without completing.
	ErrNeedsManualRecovery = errors.New("step70: needs_manual_recovery sentinel written")

	errSentinelRollbackHandled = errors.New("step70: rollback completed after sentinel recheck")
)
