package stepio

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Phase 0-bootstrap-1 gate 3rd-round finding #5: Step70Response.validate()
// enforces Promoted ↔ Decision.Action consistency.

func validAdoptDecision() contracts.Decision {
	candidatesHash := contracts.CanonicalCandidatesHash(validCandidates().Candidates)
	return contracts.Decision{
		Action: contracts.DecisionActionAdopt,
		Value: contracts.DecisionAdopt{
			Action:        contracts.DecisionActionAdopt,
			SchemaVersion: "1",
			RunID:         "2026-04-20-PR42-abcdef0",
			IdempotencyKey: contracts.ComputeAdoptIdempotencyKey(
				"2026-04-20-PR42-abcdef0",
				"2222222222222222222222222222222222222222",
				"1111111111111111111111111111111111111111",
				candidatesHash,
			),
			BestShaBefore:  "1111111111111111111111111111111111111111",
			TargetSha:      "2222222222222222222222222222222222222222",
			CandidatesHash: candidatesHash,
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

func validStep70Response() Step70Response {
	return newTestStep70Response("2026-04-20-PR42-abcdef0", validAdoptDecision(), true)
}

func newTestStep70Response(runID contracts.RunID, decision contracts.Decision, promoted bool) Step70Response {
	return Step70Response{
		payload: step70ResponsePayload{
			RunID:    runID,
			Decision: decision,
			Promoted: promoted,
		},
		requestBoundChecked: true,
	}
}

func mustMarshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}

func mustDecisionAdopt(t *testing.T, d contracts.Decision) contracts.DecisionAdopt {
	t.Helper()
	switch v := d.Value.(type) {
	case contracts.DecisionAdopt:
		return v
	case *contracts.DecisionAdopt:
		require.NotNil(t, v)
		return *v
	default:
		t.Fatalf("expected adopt decision, got %T", d.Value)
		return contracts.DecisionAdopt{}
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

func TestStep70Request_Validate_RejectsRelativeRegistryPath(t *testing.T) {
	r := validStep70Request()
	r.RegistryPath = "tmp/runs/rules-registry.jsonl"

	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRegistryPathNotAbsolute)
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
	r := newTestStep70Response("2026-04-20-PR42-abcdef0", validAdoptDecision(), true)
	assert.NoError(t, r.validate())
}

func TestStep70Response_Validate_Adopt_Promoted_False_Rejected(t *testing.T) {
	// adopt + promoted=false → inconsistent (adopt means we successfully promoted).
	r := newTestStep70Response("2026-04-20-PR42-abcdef0", validAdoptDecision(), false)
	err := r.validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep70AdoptRequiresPromoted)
}

func TestStep70Response_Validate_Reject_Promoted_True_Rejected(t *testing.T) {
	r := newTestStep70Response("2026-04-20-PR42-abcdef0", validRejectDecision(), true)
	err := r.validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep70RejectMustNotPromote)
}

func TestStep70Response_Validate_Reject_Promoted_False(t *testing.T) {
	r := newTestStep70Response("2026-04-20-PR42-abcdef0", validRejectDecision(), false)
	assert.NoError(t, r.validate())
}

func TestStep70Response_Validate_Noop_Promoted_True_Rejected(t *testing.T) {
	r := newTestStep70Response("2026-04-20-PR42-abcdef0", validNoopDecision(), true)
	err := r.validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep70NoopMustNotPromote)
}

func TestStep70Response_Validate_Noop_Promoted_False(t *testing.T) {
	r := newTestStep70Response("2026-04-20-PR42-abcdef0", validNoopDecision(), false)
	assert.NoError(t, r.validate())
}

func TestStep70Response_Validate_Rollback_Promoted_True_Rejected(t *testing.T) {
	r := newTestStep70Response("2026-04-20-PR42-abcdef0", validRollbackDecision(), true)
	err := r.validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep70RollbackMustNotPromote)
}

func TestStep70Response_Validate_Rollback_Promoted_False(t *testing.T) {
	r := newTestStep70Response("2026-04-20-PR42-abcdef0", validRollbackDecision(), false)
	assert.NoError(t, r.validate())
}

func TestStep70Response_Validate_MissingDecisionValue(t *testing.T) {
	r := newTestStep70Response("2026-04-20-PR42-abcdef0", contracts.Decision{Action: contracts.DecisionActionAdopt, Value: nil}, true)
	err := r.validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep70DecisionMissing)
}

func TestStep70Response_Validate_BadRunID(t *testing.T) {
	r := newTestStep70Response("not-a-run-id", validAdoptDecision(), true)
	assert.Error(t, r.validate())
}

func TestStep70Response_Validate_RejectsDecisionVariantTypeMismatch(t *testing.T) {
	r := newTestStep70Response("2026-04-20-PR42-abcdef0", contracts.Decision{
		Action: contracts.DecisionActionReject,
		Value:  validAdoptDecision().Value,
	}, false)
	err := r.validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrDecisionVariantTypeMismatch)
}

func TestStep70Response_Validate_RejectsDecisionInnerActionMismatch(t *testing.T) {
	candidatesHash := contracts.CanonicalCandidatesHash(validCandidates().Candidates)
	r := newTestStep70Response("2026-04-20-PR42-abcdef0", contracts.Decision{
		Action: contracts.DecisionActionAdopt,
		Value: contracts.DecisionAdopt{
			Action:        contracts.DecisionActionReject,
			SchemaVersion: "1",
			RunID:         "2026-04-20-PR42-abcdef0",
			IdempotencyKey: contracts.ComputeAdoptIdempotencyKey(
				"2026-04-20-PR42-abcdef0",
				"2222222222222222222222222222222222222222",
				"1111111111111111111111111111111111111111",
				candidatesHash,
			),
			BestShaBefore:  "1111111111111111111111111111111111111111",
			TargetSha:      "2222222222222222222222222222222222222222",
			CandidatesHash: candidatesHash,
			RegistryAppendResult: contracts.RegistryAppendResult{
				Offset: 0,
				Sha256: "0000000000000000000000000000000000000000000000000000000000000003",
			},
			DecidedAt: time.Now(),
		},
	}, true)
	err := r.validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrDecisionVariantActionMismatch)
}

func TestStep70Response_Validate_RejectsResponseRunIDMismatch(t *testing.T) {
	r := newTestStep70Response("2026-04-21-PR42-abcdef0", validAdoptDecision(), true)
	err := r.validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep70ResponseRunIDMismatch)
}

func TestStep70Response_Validate_RejectsForgedAdoptIdempotencyKey(t *testing.T) {
	r := newTestStep70Response("2026-04-20-PR42-abcdef0", validAdoptDecision(), true)
	adopt := mustDecisionAdopt(t, r.payload.Decision)
	adopt.IdempotencyKey = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	r.payload.Decision.Value = adopt

	err := r.validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrDecisionIdempotencyKeyMismatch)
}

func TestStep70Response_validate_AcceptsPointerDecisionAdopt(t *testing.T) {
	r := validStep70Response()
	adopt := mustDecisionAdopt(t, r.payload.Decision)
	r.payload.Decision.Value = &adopt

	assert.NoError(t, r.validate())
}

func TestDecodeAndValidateStep70Response_RejectsDuplicateTopLevelKey(t *testing.T) {
	req := validStep70Request()
	raw := string(mustMarshalJSON(t, validStep70Response()))
	raw = strings.Replace(raw, `"promoted":true`, `"promoted":true,"promoted":false`, 1)

	_, err := DecodeAndValidateStep70Response([]byte(raw), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrDuplicateJSONKey)
}

func TestDecodeAndValidateStep70Response_RejectsUnknownTopLevelField(t *testing.T) {
	req := validStep70Request()
	raw := string(mustMarshalJSON(t, validStep70Response()))
	raw = strings.Replace(raw, `"promoted":true`, `"unexpected":true,"promoted":true`, 1)

	_, err := DecodeAndValidateStep70Response([]byte(raw), req)
	require.Error(t, err)
}

func TestDecodeAndValidateStep70Response_RejectsTrailingTokens(t *testing.T) {
	req := validStep70Request()
	raw := append(mustMarshalJSON(t, validStep70Response()), []byte(`{"extra":true}`)...)

	_, err := DecodeAndValidateStep70Response(raw, req)
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrTrailingJSON)
}

func TestDecodeAndValidateStep70Response_RejectsPromotedActionMismatch(t *testing.T) {
	req := validStep70Request()
	resp := validStep70Response()
	resp.payload.Promoted = false

	_, err := DecodeAndValidateStep70Response(mustMarshalJSON(t, resp), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep70AdoptRequiresPromoted)
}

func TestDecodeAndValidateStep70Response_RejectsDecisionRunIDMismatch(t *testing.T) {
	req := validStep70Request()
	resp := validStep70Response()
	resp.payload.RunID = "2026-04-21-PR42-abcdef0"

	_, err := DecodeAndValidateStep70Response(mustMarshalJSON(t, resp), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep70ResponseRunIDMismatch)
}

func TestDecodeAndValidateStep70Response_RejectsRequestRunIDMismatch(t *testing.T) {
	req := validStep70Request()
	req.TaskPackage.RunID = "2026-04-21-PR42-abcdef0"
	req.Candidates.RunID = req.TaskPackage.RunID
	req.Candidates.CandidatesHash = contracts.CanonicalCandidatesHash(req.Candidates.Candidates)
	resp := validStep70Response()

	_, err := DecodeAndValidateStep70Response(mustMarshalJSON(t, resp), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep70RequestResponseRunIDMismatch)
}

func TestDecodeAndValidateStep70Response_RejectsCandidatesHashMismatch(t *testing.T) {
	req := validStep70Request()
	resp := validStep70Response()
	adopt := mustDecisionAdopt(t, resp.payload.Decision)
	adopt.CandidatesHash = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	adopt.IdempotencyKey = contracts.ComputeAdoptIdempotencyKey(string(adopt.RunID), adopt.TargetSha, adopt.BestShaBefore, adopt.CandidatesHash)
	resp.payload.Decision.Value = adopt

	_, err := DecodeAndValidateStep70Response(mustMarshalJSON(t, resp), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep70AdoptCandidatesHashMismatch)
}

func TestDecodeAndValidateStep70Response_RejectsForgedIdempotencyKey(t *testing.T) {
	req := validStep70Request()
	resp := validStep70Response()
	adopt := mustDecisionAdopt(t, resp.payload.Decision)
	adopt.IdempotencyKey = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	resp.payload.Decision.Value = adopt

	_, err := DecodeAndValidateStep70Response(mustMarshalJSON(t, resp), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrDecisionIdempotencyKeyMismatch)
}

func TestDecodeAndValidateStep70Response_AcceptsPointerDecisionAdopt(t *testing.T) {
	req := validStep70Request()
	resp := validStep70Response()
	adopt := mustDecisionAdopt(t, resp.payload.Decision)
	resp.payload.Decision.Value = &adopt

	got, err := DecodeAndValidateStep70Response(mustMarshalJSON(t, resp), req)
	require.NoError(t, err)
	assert.Equal(t, req.TaskPackage.RunID, got.RunID())
	assert.True(t, got.RequestBound())
	assert.NoError(t, got.Validate())
}

func TestStep70Request_UnmarshalJSON_RejectsDuplicateTopLevelKey(t *testing.T) {
	data := []byte(`{
  "task_package": {
    "schema_version": "1",
    "run_id": "2026-04-20-PR42-abcdef0",
    "pr": 42,
    "title": "fix",
    "base_sha": "1111111111111111111111111111111111111111",
    "best_branch": "auto-improve/best",
    "reconstructed_task_prompt": "hello",
    "worktrees": [
      {"agent":"a1","pass":1,"path":"/tmp/wt/pass1-a1","branch":"b-pass1-a1","base_sha":"1111111111111111111111111111111111111111","head_sha":"1111111111111111111111111111111111111111"},
      {"agent":"a2","pass":1,"path":"/tmp/wt/pass1-a2","branch":"b-pass1-a2","base_sha":"1111111111111111111111111111111111111111","head_sha":"1111111111111111111111111111111111111111"},
      {"agent":"a3","pass":1,"path":"/tmp/wt/pass1-a3","branch":"b-pass1-a3","base_sha":"1111111111111111111111111111111111111111","head_sha":"1111111111111111111111111111111111111111"},
      {"agent":"a1","pass":2,"path":"/tmp/wt/pass2-a1","branch":"b-pass2-a1","base_sha":"1111111111111111111111111111111111111111","head_sha":"1111111111111111111111111111111111111111"},
      {"agent":"a2","pass":2,"path":"/tmp/wt/pass2-a2","branch":"b-pass2-a2","base_sha":"1111111111111111111111111111111111111111","head_sha":"1111111111111111111111111111111111111111"},
      {"agent":"a3","pass":2,"path":"/tmp/wt/pass2-a3","branch":"b-pass2-a3","base_sha":"1111111111111111111111111111111111111111","head_sha":"1111111111111111111111111111111111111111"}
    ],
    "created_at": "2026-04-20T12:00:00Z"
  },
  "candidates": {
    "schema_version": "1",
    "run_id": "2026-04-20-PR42-abcdef0",
    "candidates": [],
    "candidates_hash": "4f53cda18c2baa0c0354bb5f9a3ecbe5edc3d5f9d9f54a2e4f3b68d5c4d6f6f8",
    "created_at": "2026-04-20T12:00:00Z"
  },
  "registry_path": "/tmp/runs/rules-registry.jsonl",
  "registry_path": "/tmp/runs/rules-registry-2.jsonl"
}`)
	var req Step70Request
	err := json.Unmarshal(data, &req)
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrDuplicateJSONKey)
}

func TestStep70Request_UnmarshalJSON_RejectsDuplicateNestedStructKey(t *testing.T) {
	data := []byte(`{
  "task_package": {
    "schema_version": "1",
    "run_id": "2026-04-20-PR42-abcdef0",
    "pr": 42,
    "title": "fix",
    "base_sha": "1111111111111111111111111111111111111111",
    "best_branch": "auto-improve/best",
    "reconstructed_task_prompt": "hello",
    "worktrees": [
      {"agent":"a1","pass":1,"path":"/tmp/wt/pass1-a1","path":"/tmp/wt/pass1-a1-dup","branch":"b-pass1-a1","base_sha":"1111111111111111111111111111111111111111","head_sha":"1111111111111111111111111111111111111111"},
      {"agent":"a2","pass":1,"path":"/tmp/wt/pass1-a2","branch":"b-pass1-a2","base_sha":"1111111111111111111111111111111111111111","head_sha":"1111111111111111111111111111111111111111"},
      {"agent":"a3","pass":1,"path":"/tmp/wt/pass1-a3","branch":"b-pass1-a3","base_sha":"1111111111111111111111111111111111111111","head_sha":"1111111111111111111111111111111111111111"},
      {"agent":"a1","pass":2,"path":"/tmp/wt/pass2-a1","branch":"b-pass2-a1","base_sha":"1111111111111111111111111111111111111111","head_sha":"1111111111111111111111111111111111111111"},
      {"agent":"a2","pass":2,"path":"/tmp/wt/pass2-a2","branch":"b-pass2-a2","base_sha":"1111111111111111111111111111111111111111","head_sha":"1111111111111111111111111111111111111111"},
      {"agent":"a3","pass":2,"path":"/tmp/wt/pass2-a3","branch":"b-pass2-a3","base_sha":"1111111111111111111111111111111111111111","head_sha":"1111111111111111111111111111111111111111"}
    ],
    "created_at": "2026-04-20T12:00:00Z"
  },
  "candidates": {
    "schema_version": "1",
    "run_id": "2026-04-20-PR42-abcdef0",
    "candidates": [],
    "candidates_hash": "4f53cda18c2baa0c0354bb5f9a3ecbe5edc3d5f9d9f54a2e4f3b68d5c4d6f6f8",
    "created_at": "2026-04-20T12:00:00Z"
  },
  "registry_path": "/tmp/runs/rules-registry.jsonl"
}`)
	var req Step70Request
	err := json.Unmarshal(data, &req)
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrDuplicateJSONKey)
}
