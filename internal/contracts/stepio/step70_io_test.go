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

func validCandidates() contracts.Candidates {
	items := []contracts.Candidate{
		{
			CandidateID:        "c1",
			Kind:               contracts.CandidateKindNew,
			Title:              "tighten validation",
			ProposedBodyPath:   "40/candidates/c1.md",
			ProposedBodySha256: "0000000000000000000000000000000000000000000000000000000000000009",
		},
	}
	return contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          "2026-04-20-PR42-abcdef0",
		Candidates:     items,
		CandidatesHash: contracts.CanonicalCandidatesHash(items),
		CreatedAt:      time.Now(),
	}
}

func validStep70Request() Step70Request {
	return Step70Request{
		TaskPackage:  buildTaskPackage(),
		Candidates:   validCandidates(),
		RegistryPath: "/tmp/runs/rules-registry.jsonl",
	}
}

func TestStep70Request_Validate_Valid(t *testing.T) {
	assert.NoError(t, validStep70Request().Validate())
}

func TestStep70Request_Validate_RejectsMissingRegistryPath(t *testing.T) {
	r := validStep70Request()
	r.RegistryPath = ""
	assert.Error(t, r.Validate())
}

func TestStep70Request_Validate_RejectsRunIDMismatch(t *testing.T) {
	r := validStep70Request()
	r.Candidates.RunID = "2026-04-21-PR42-abcdef0"
	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep70RequestRunIDMismatch)
}

func TestStep70Request_Validate_RejectsTamperedCandidatesHash(t *testing.T) {
	r := validStep70Request()
	r.Candidates.CandidatesHash = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrCandidatesHashMismatch)
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

func TestStep70Response_Validate_RejectsDecisionVariantTypeMismatch(t *testing.T) {
	r := Step70Response{
		RunID: "2026-04-20-PR42-abcdef0",
		Decision: contracts.Decision{
			Action: contracts.DecisionActionReject,
			Value:  validAdoptDecision().Value,
		},
		Promoted: false,
	}
	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrDecisionVariantTypeMismatch)
}

func TestStep70Response_Validate_RejectsDecisionInnerActionMismatch(t *testing.T) {
	r := Step70Response{
		RunID: "2026-04-20-PR42-abcdef0",
		Decision: contracts.Decision{
			Action: contracts.DecisionActionAdopt,
			Value: contracts.DecisionAdopt{
				Action:         contracts.DecisionActionReject,
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
		},
		Promoted: true,
	}
	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrDecisionVariantActionMismatch)
}

func TestStep70Response_Validate_RejectsResponseRunIDMismatch(t *testing.T) {
	r := Step70Response{
		RunID:    "2026-04-21-PR42-abcdef0",
		Decision: validAdoptDecision(),
		Promoted: true,
	}
	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep70ResponseRunIDMismatch)
}
