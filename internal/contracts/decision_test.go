package contracts

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixtureDecisionAdopt() string {
	return `{
  "action": "adopt",
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "idempotency_key": "0000000000000000000000000000000000000000000000000000000000000001",
  "best_sha_before": "1111111111111111111111111111111111111111",
  "target_sha": "2222222222222222222222222222222222222222",
  "candidates_hash": "0000000000000000000000000000000000000000000000000000000000000002",
  "registry_append_result": {"offset": 0, "sha256": "0000000000000000000000000000000000000000000000000000000000000003"},
  "decided_at": "2026-04-20T12:00:00Z"
}`
}

func TestDecision_Adopt_Parse(t *testing.T) {
	var d Decision
	require.NoError(t, json.Unmarshal([]byte(fixtureDecisionAdopt()), &d))
	assert.Equal(t, DecisionActionAdopt, d.Action)
	_, ok := d.Value.(DecisionAdopt)
	assert.True(t, ok)
}

func TestDecision_Rollback_Parse(t *testing.T) {
	data := `{
  "action": "rollback",
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "rollback_reason": "lease_failure",
  "failed_step": "70",
  "decided_at": "2026-04-20T12:00:00Z"
}`
	var d Decision
	require.NoError(t, json.Unmarshal([]byte(data), &d))
	assert.Equal(t, DecisionActionRollback, d.Action)
	v := d.Value.(DecisionRollback)
	assert.Equal(t, RollbackReasonLeaseFailure, v.RollbackReason)
	assert.Equal(t, FailedStep70, v.FailedStep)
}

func TestDecision_Reject_Parse(t *testing.T) {
	data := `{"action":"reject","schema_version":"1","run_id":"2026-04-20-PR42-abcdef0","reason":"below_threshold","decided_at":"2026-04-20T12:00:00Z"}`
	var d Decision
	require.NoError(t, json.Unmarshal([]byte(data), &d))
	assert.Equal(t, DecisionActionReject, d.Action)
}

func TestDecision_Noop_Parse(t *testing.T) {
	data := `{"action":"noop","schema_version":"1","run_id":"2026-04-20-PR42-abcdef0","reason":"no_candidates","decided_at":"2026-04-20T12:00:00Z"}`
	var d Decision
	require.NoError(t, json.Unmarshal([]byte(data), &d))
	assert.Equal(t, DecisionActionNoop, d.Action)
}

// Failure cases.
func TestDecision_Reject_UnknownKey(t *testing.T) {
	data := strings.Replace(fixtureDecisionAdopt(), `"target_sha"`, `"unknown_field": true, "target_sha"`, 1)
	var d Decision
	assert.Error(t, json.Unmarshal([]byte(data), &d))
}

func TestDecision_Reject_MissingRequired_Rollback(t *testing.T) {
	// rollback variant から failed_step を欠落
	data := `{"action":"rollback","schema_version":"1","run_id":"2026-04-20-PR42-abcdef0","rollback_reason":"lease_failure","decided_at":"2026-04-20T12:00:00Z"}`
	var d Decision
	assert.Error(t, json.Unmarshal([]byte(data), &d))
}

func TestDecision_Reject_WrongAction(t *testing.T) {
	data := `{"action":"bogus"}`
	var d Decision
	err := json.Unmarshal([]byte(data), &d)
	assert.ErrorIs(t, err, ErrUnknownDecisionAction)
}

func TestDecision_Reject_TrailingBytes(t *testing.T) {
	data := fixtureDecisionAdopt() + "not-json"
	var d Decision
	assert.Error(t, json.Unmarshal([]byte(data), &d))
}

// finding #6: RegistryAppendResult.offset は JSON 上の physical 存在が必須.
// Go zero-value (0) は合法 offset のため tag required では検出できない。
func TestRegistryAppendResult_Reject_MissingOffset(t *testing.T) {
	data := `{"sha256":"0000000000000000000000000000000000000000000000000000000000000003"}`
	var r RegistryAppendResult
	err := json.Unmarshal([]byte(data), &r)
	assert.ErrorIs(t, err, ErrRegistryAppendResultMissingOffset)
}

func TestRegistryAppendResult_Accept_ZeroOffset(t *testing.T) {
	data := `{"offset":0,"sha256":"0000000000000000000000000000000000000000000000000000000000000003"}`
	var r RegistryAppendResult
	require.NoError(t, json.Unmarshal([]byte(data), &r))
	assert.EqualValues(t, 0, r.Offset)
}

func TestDecision_Adopt_Reject_MissingOffsetInAppendResult(t *testing.T) {
	// DecisionAdopt の registry_append_result 内 offset 欠落は全体の decode エラー.
	data := `{
  "action": "adopt",
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "idempotency_key": "0000000000000000000000000000000000000000000000000000000000000001",
  "best_sha_before": "1111111111111111111111111111111111111111",
  "target_sha": "2222222222222222222222222222222222222222",
  "candidates_hash": "0000000000000000000000000000000000000000000000000000000000000002",
  "registry_append_result": {"sha256": "0000000000000000000000000000000000000000000000000000000000000003"},
  "decided_at": "2026-04-20T12:00:00Z"
}`
	var d Decision
	assert.Error(t, json.Unmarshal([]byte(data), &d))
}

func TestDecision_Reject_BadRollbackReason(t *testing.T) {
	data := `{
  "action": "rollback",
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "rollback_reason": "made_up_reason",
  "failed_step": "70",
  "decided_at": "2026-04-20T12:00:00Z"
}`
	var d Decision
	assert.Error(t, json.Unmarshal([]byte(data), &d))
}

func TestDecision_Validate_RejectsOuterActionVariantTypeMismatch(t *testing.T) {
	d := Decision{
		Action: DecisionActionReject,
		Value: DecisionAdopt{
			Action:         DecisionActionAdopt,
			SchemaVersion:  "1",
			RunID:          "2026-04-20-PR42-abcdef0",
			IdempotencyKey: "0000000000000000000000000000000000000000000000000000000000000001",
			BestShaBefore:  "1111111111111111111111111111111111111111",
			TargetSha:      "2222222222222222222222222222222222222222",
			CandidatesHash: "0000000000000000000000000000000000000000000000000000000000000002",
			RegistryAppendResult: RegistryAppendResult{
				Offset: 0,
				Sha256: "0000000000000000000000000000000000000000000000000000000000000003",
			},
			DecidedAt: time.Now(),
		},
	}
	err := d.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDecisionVariantTypeMismatch)
}

func TestDecision_Validate_RejectsOuterActionInnerActionMismatch(t *testing.T) {
	d := Decision{
		Action: DecisionActionAdopt,
		Value: DecisionAdopt{
			Action:         DecisionActionReject,
			SchemaVersion:  "1",
			RunID:          "2026-04-20-PR42-abcdef0",
			IdempotencyKey: "0000000000000000000000000000000000000000000000000000000000000001",
			BestShaBefore:  "1111111111111111111111111111111111111111",
			TargetSha:      "2222222222222222222222222222222222222222",
			CandidatesHash: "0000000000000000000000000000000000000000000000000000000000000002",
			RegistryAppendResult: RegistryAppendResult{
				Offset: 0,
				Sha256: "0000000000000000000000000000000000000000000000000000000000000003",
			},
			DecidedAt: time.Now(),
		},
	}
	err := d.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDecisionVariantActionMismatch)
}
