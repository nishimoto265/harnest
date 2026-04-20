package contracts

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixtureDecisionAdopt() string {
	candidatesHash := "0000000000000000000000000000000000000000000000000000000000000002"
	return fmt.Sprintf(`{
  "action": "adopt",
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "idempotency_key": "%s",
  "best_sha_before": "1111111111111111111111111111111111111111",
  "target_sha": "2222222222222222222222222222222222222222",
  "candidates_hash": "%s",
  "registry_append_result": {"offset": 0, "sha256": "0000000000000000000000000000000000000000000000000000000000000003"},
  "decided_at": "2026-04-20T12:00:00Z"
}`, ComputeAdoptIdempotencyKey("2026-04-20-PR42-abcdef0", "2222222222222222222222222222222222222222", "1111111111111111111111111111111111111111", candidatesHash), candidatesHash)
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
	candidatesHash := "0000000000000000000000000000000000000000000000000000000000000002"
	data := `{
  "action": "adopt",
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "idempotency_key": "` + ComputeAdoptIdempotencyKey("2026-04-20-PR42-abcdef0", "2222222222222222222222222222222222222222", "1111111111111111111111111111111111111111", candidatesHash) + `",
  "best_sha_before": "1111111111111111111111111111111111111111",
  "target_sha": "2222222222222222222222222222222222222222",
  "candidates_hash": "` + candidatesHash + `",
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
	candidatesHash := "0000000000000000000000000000000000000000000000000000000000000002"
	d := Decision{
		Action: DecisionActionReject,
		Value: DecisionAdopt{
			Action:         DecisionActionAdopt,
			SchemaVersion:  "1",
			RunID:          "2026-04-20-PR42-abcdef0",
			IdempotencyKey: ComputeAdoptIdempotencyKey("2026-04-20-PR42-abcdef0", "2222222222222222222222222222222222222222", "1111111111111111111111111111111111111111", candidatesHash),
			BestShaBefore:  "1111111111111111111111111111111111111111",
			TargetSha:      "2222222222222222222222222222222222222222",
			CandidatesHash: candidatesHash,
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
	candidatesHash := "0000000000000000000000000000000000000000000000000000000000000002"
	d := Decision{
		Action: DecisionActionAdopt,
		Value: DecisionAdopt{
			Action:         DecisionActionReject,
			SchemaVersion:  "1",
			RunID:          "2026-04-20-PR42-abcdef0",
			IdempotencyKey: ComputeAdoptIdempotencyKey("2026-04-20-PR42-abcdef0", "2222222222222222222222222222222222222222", "1111111111111111111111111111111111111111", candidatesHash),
			BestShaBefore:  "1111111111111111111111111111111111111111",
			TargetSha:      "2222222222222222222222222222222222222222",
			CandidatesHash: candidatesHash,
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

func TestDecision_Validate_AcceptsValueAndPointerVariants(t *testing.T) {
	parse := func(raw string) Decision {
		var d Decision
		require.NoError(t, json.Unmarshal([]byte(raw), &d))
		return d
	}

	adopt := parse(fixtureDecisionAdopt())
	reject := parse(`{"action":"reject","schema_version":"1","run_id":"2026-04-20-PR42-abcdef0","reason":"below_threshold","decided_at":"2026-04-20T12:00:00Z"}`)
	noop := parse(`{"action":"noop","schema_version":"1","run_id":"2026-04-20-PR42-abcdef0","reason":"no_candidates","decided_at":"2026-04-20T12:00:00Z"}`)
	rollback := parse(`{
  "action": "rollback",
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "rollback_reason": "lease_failure",
  "failed_step": "70",
  "decided_at": "2026-04-20T12:00:00Z"
}`)

	tests := []struct {
		name string
		d    Decision
	}{
		{name: "adopt value", d: adopt},
		{name: "adopt pointer", d: func() Decision { v := adopt.Value.(DecisionAdopt); return Decision{Action: adopt.Action, Value: &v} }()},
		{name: "reject value", d: reject},
		{name: "reject pointer", d: func() Decision { v := reject.Value.(DecisionReject); return Decision{Action: reject.Action, Value: &v} }()},
		{name: "noop value", d: noop},
		{name: "noop pointer", d: func() Decision { v := noop.Value.(DecisionNoop); return Decision{Action: noop.Action, Value: &v} }()},
		{name: "rollback value", d: rollback},
		{name: "rollback pointer", d: func() Decision {
			v := rollback.Value.(DecisionRollback)
			return Decision{Action: rollback.Action, Value: &v}
		}()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NoError(t, tt.d.Validate())
		})
	}
}

func TestDecision_Validate_RejectsForgedAdoptIdempotencyKey(t *testing.T) {
	candidatesHash := "0000000000000000000000000000000000000000000000000000000000000002"
	d := Decision{
		Action: DecisionActionAdopt,
		Value: DecisionAdopt{
			Action:         DecisionActionAdopt,
			SchemaVersion:  "1",
			RunID:          "2026-04-20-PR42-abcdef0",
			IdempotencyKey: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
			BestShaBefore:  "1111111111111111111111111111111111111111",
			TargetSha:      "2222222222222222222222222222222222222222",
			CandidatesHash: candidatesHash,
			RegistryAppendResult: RegistryAppendResult{
				Offset: 0,
				Sha256: "0000000000000000000000000000000000000000000000000000000000000003",
			},
			DecidedAt: time.Now(),
		},
	}

	err := d.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDecisionIdempotencyKeyMismatch)
}
