package stepio

import (
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Phase 0-bootstrap-1 gate 3rd-round finding #5: Step70Response.Validate()
// enforces Promoted ↔ Decision.Action consistency.

func validAdoptDecision() contracts.Decision {
	return contracts.Decision{
		Action: contracts.DecisionActionAdopt,
		Value: contracts.DecisionAdopt{
			Action:         contracts.DecisionActionAdopt,
			SchemaVersion:  "1",
			RunID:          "2026-04-20-PR42-abcdef0",
			IdempotencyKey: "0000000000000000000000000000000000000000000000000000000000000001",
			BestShaBefore:  "1111111111111111111111111111111111111111",
			TargetSha:      "2222222222222222222222222222222222222222",
			CandidatesHash: "0000000000000000000000000000000000000000000000000000000000000002",
			RegistryAppendResult: contracts.RegistryAppendResult{
				Offset: 0,
				Sha256: "0000000000000000000000000000000000000000000000000000000000000003",
			},
			DecidedAt: time.Now(),
		},
	}
}

func validRejectDecision() contracts.Decision {
	return contracts.Decision{
		Action: contracts.DecisionActionReject,
		Value: contracts.DecisionReject{
			Action:        contracts.DecisionActionReject,
			SchemaVersion: "1",
			RunID:         "2026-04-20-PR42-abcdef0",
			Reason:        "below_threshold",
			DecidedAt:     time.Now(),
		},
	}
}

func validNoopDecision() contracts.Decision {
	return contracts.Decision{
		Action: contracts.DecisionActionNoop,
		Value: contracts.DecisionNoop{
			Action:        contracts.DecisionActionNoop,
			SchemaVersion: "1",
			RunID:         "2026-04-20-PR42-abcdef0",
			Reason:        "no_candidates",
			DecidedAt:     time.Now(),
		},
	}
}

func validRollbackDecision() contracts.Decision {
	return contracts.Decision{
		Action: contracts.DecisionActionRollback,
		Value: contracts.DecisionRollback{
			Action:         contracts.DecisionActionRollback,
			SchemaVersion:  "1",
			RunID:          "2026-04-20-PR42-abcdef0",
			RollbackReason: contracts.RollbackReasonLeaseFailure,
			FailedStep:     contracts.FailedStep70,
			DecidedAt:      time.Now(),
		},
	}
}

func TestStep70Response_Validate_Adopt_Promoted_True(t *testing.T) {
	r := Step70Response{
		RunID:    "2026-04-20-PR42-abcdef0",
		Decision: validAdoptDecision(),
		Promoted: true,
	}
	assert.NoError(t, r.Validate())
}

func TestStep70Response_Validate_Adopt_Promoted_False_Rejected(t *testing.T) {
	// adopt + promoted=false → inconsistent (adopt means we successfully promoted).
	r := Step70Response{
		RunID:    "2026-04-20-PR42-abcdef0",
		Decision: validAdoptDecision(),
		Promoted: false,
	}
	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep70AdoptRequiresPromoted)
}

func TestStep70Response_Validate_Reject_Promoted_True_Rejected(t *testing.T) {
	r := Step70Response{
		RunID:    "2026-04-20-PR42-abcdef0",
		Decision: validRejectDecision(),
		Promoted: true,
	}
	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep70RejectMustNotPromote)
}

func TestStep70Response_Validate_Reject_Promoted_False(t *testing.T) {
	r := Step70Response{
		RunID:    "2026-04-20-PR42-abcdef0",
		Decision: validRejectDecision(),
		Promoted: false,
	}
	assert.NoError(t, r.Validate())
}

func TestStep70Response_Validate_Noop_Promoted_True_Rejected(t *testing.T) {
	r := Step70Response{
		RunID:    "2026-04-20-PR42-abcdef0",
		Decision: validNoopDecision(),
		Promoted: true,
	}
	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep70NoopMustNotPromote)
}

func TestStep70Response_Validate_Noop_Promoted_False(t *testing.T) {
	r := Step70Response{
		RunID:    "2026-04-20-PR42-abcdef0",
		Decision: validNoopDecision(),
		Promoted: false,
	}
	assert.NoError(t, r.Validate())
}

func TestStep70Response_Validate_Rollback_Promoted_True_Rejected(t *testing.T) {
	r := Step70Response{
		RunID:    "2026-04-20-PR42-abcdef0",
		Decision: validRollbackDecision(),
		Promoted: true,
	}
	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep70RollbackMustNotPromote)
}

func TestStep70Response_Validate_Rollback_Promoted_False(t *testing.T) {
	r := Step70Response{
		RunID:    "2026-04-20-PR42-abcdef0",
		Decision: validRollbackDecision(),
		Promoted: false,
	}
	assert.NoError(t, r.Validate())
}

func TestStep70Response_Validate_MissingDecisionValue(t *testing.T) {
	r := Step70Response{
		RunID:    "2026-04-20-PR42-abcdef0",
		Decision: contracts.Decision{Action: contracts.DecisionActionAdopt, Value: nil},
		Promoted: true,
	}
	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep70DecisionMissing)
}

func TestStep70Response_Validate_BadRunID(t *testing.T) {
	r := Step70Response{
		RunID:    "not-a-run-id",
		Decision: validAdoptDecision(),
		Promoted: true,
	}
	assert.Error(t, r.Validate())
}
