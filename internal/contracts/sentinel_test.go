package contracts

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validNeedsRecoverySentinel() NeedsRecoverySentinel {
	return NeedsRecoverySentinel{
		RunID:      "2026-04-20-PR42-abcdef0",
		PR:         42,
		Reason:     RollbackReasonRemoteDivergence,
		FailedStep: FailedStep70,
		CreatedAt:  time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC),
	}
}

func TestNeedsRecoverySentinel_RoundTrip(t *testing.T) {
	want := validNeedsRecoverySentinel()
	data, err := MarshalStrict(want)
	require.NoError(t, err)

	var got NeedsRecoverySentinel
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, want, got)
}

func TestNeedsRecoverySentinel_RejectsDuplicateTopLevelKey(t *testing.T) {
	data := []byte(`{"run_id":"2026-04-20-PR42-abcdef0","run_id":"2026-04-21-PR42-abcdef0","pr":42,"reason":"remote_divergence","failed_step":"70","created_at":"2026-04-20T12:00:00Z"}`)
	var s NeedsRecoverySentinel
	err := json.Unmarshal(data, &s)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDuplicateJSONKey)
}

func TestNeedsRecoverySentinel_RejectsBadReason(t *testing.T) {
	data := []byte(`{"run_id":"2026-04-20-PR42-abcdef0","pr":42,"reason":"bogus","failed_step":"70","created_at":"2026-04-20T12:00:00Z"}`)
	var s NeedsRecoverySentinel
	assert.Error(t, json.Unmarshal(data, &s))
}

func TestNeedsRecoverySentinel_RejectsBadCreatedAt(t *testing.T) {
	data := []byte(`{"run_id":"2026-04-20-PR42-abcdef0","pr":42,"reason":"remote_divergence","failed_step":"70","created_at":"2026/04/20 12:00:00"}`)
	var s NeedsRecoverySentinel
	assert.Error(t, json.Unmarshal(data, &s))
}

func TestNeedsRecoverySentinel_Validate_RejectsReasonFailedStepMismatch(t *testing.T) {
	s := validNeedsRecoverySentinel()
	s.Reason = RollbackReasonWorktreeRescueLoop
	s.FailedStep = FailedStep70

	err := s.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrReasonFailedStepMismatch)
}

func TestNeedsRecoverySentinel_Validate_AcceptsWorktreeRescueLoopForStep20(t *testing.T) {
	s := validNeedsRecoverySentinel()
	s.Reason = RollbackReasonWorktreeRescueLoop
	s.FailedStep = FailedStep20
	assert.NoError(t, s.Validate())
}
