package contracts

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixtureStateStarted() string {
	return `{"kind":"started","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"10","at":"2026-04-20T10:00:00Z"}`
}

func TestState_Started_Parse(t *testing.T) {
	var e StateEntry
	require.NoError(t, json.Unmarshal([]byte(fixtureStateStarted()), &e))
	assert.Equal(t, StateKindStarted, e.Kind)
	assert.False(t, e.Kind.IsTerminal())
}

func TestState_StepDone_Parse(t *testing.T) {
	data := `{"kind":"step_done","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"20","at":"2026-04-20T10:30:00Z"}`
	var e StateEntry
	require.NoError(t, json.Unmarshal([]byte(data), &e))
	assert.Equal(t, StateKindStepDone, e.Kind)
}

func TestState_Interrupted_Parse(t *testing.T) {
	data := `{"kind":"interrupted","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"30","reason":"rate_limit","at":"2026-04-20T11:00:00Z"}`
	var e StateEntry
	require.NoError(t, json.Unmarshal([]byte(data), &e))
	assert.False(t, e.Kind.IsTerminal())
}

func TestState_Warning_GlobalTelemetry(t *testing.T) {
	// pr / run_id を欠いた global telemetry warning (io-contracts.md rev22)
	data := `{"kind":"warning","step":"70","warning_kind":"registry_size_critical","count":2001,"at":"2026-04-20T12:00:00Z"}`
	var e StateEntry
	require.NoError(t, json.Unmarshal([]byte(data), &e))
	w := e.Value.(StateEntryWarning)
	assert.Nil(t, w.PR)
	assert.Nil(t, w.RunID)
	require.NotNil(t, w.Count)
	assert.EqualValues(t, 2001, *w.Count)
}

func TestState_Warning_PRScoped(t *testing.T) {
	data := `{"kind":"warning","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"70","warning_kind":"registry_size_high","count":1501,"at":"2026-04-20T12:00:00Z"}`
	var e StateEntry
	require.NoError(t, json.Unmarshal([]byte(data), &e))
}

func TestState_NeedsManualRecovery_Parse(t *testing.T) {
	data := `{"kind":"needs_manual_recovery","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"70","reason":"remote_divergence","failed_step":"70","at":"2026-04-20T12:00:00Z"}`
	var e StateEntry
	require.NoError(t, json.Unmarshal([]byte(data), &e))
	assert.True(t, e.Kind.IsTerminal())
}

func TestState_Promoted_Terminal(t *testing.T) {
	data := `{"kind":"promoted","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"70","at":"2026-04-20T12:00:00Z"}`
	var e StateEntry
	require.NoError(t, json.Unmarshal([]byte(data), &e))
	assert.True(t, e.Kind.IsTerminal())
}

func TestState_Rollback_Parse(t *testing.T) {
	data := `{"kind":"rollback","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"70","rollback_reason":"transactional_failure","failed_step":"70","at":"2026-04-20T12:00:00Z"}`
	var e StateEntry
	require.NoError(t, json.Unmarshal([]byte(data), &e))
	assert.True(t, e.Kind.IsTerminal())
}

func TestState_Reject_UnknownTopLevel(t *testing.T) {
	data := strings.Replace(fixtureStateStarted(), `"at"`, `"unknown":1,"at"`, 1)
	var e StateEntry
	assert.Error(t, json.Unmarshal([]byte(data), &e))
}

func TestState_Reject_MissingStep(t *testing.T) {
	// 全 non-terminal / terminal event に step 必須
	data := `{"kind":"started","pr":42,"run_id":"2026-04-20-PR42-abcdef0","at":"2026-04-20T10:00:00Z"}`
	var e StateEntry
	assert.Error(t, json.Unmarshal([]byte(data), &e))
}

func TestState_Reject_WrongKind(t *testing.T) {
	var e StateEntry
	err := json.Unmarshal([]byte(`{"kind":"bogus"}`), &e)
	assert.ErrorIs(t, err, ErrUnknownStateKind)
}

func TestState_Reject_TrailingBytes(t *testing.T) {
	data := fixtureStateStarted() + "garbage"
	var e StateEntry
	assert.Error(t, json.Unmarshal([]byte(data), &e))
}

func TestState_Reject_TrailingJSON(t *testing.T) {
	data := fixtureStateStarted() + `{"more":1}`
	var e StateEntry
	assert.Error(t, json.Unmarshal([]byte(data), &e))
}

func TestState_IsTerminal_Coverage(t *testing.T) {
	// 全 StateKind を列挙し IsTerminal の期待値を verify
	nonTerm := []StateKind{StateKindStarted, StateKindStepDone, StateKindInterrupted, StateKindPromoting, StateKindWarning}
	term := []StateKind{StateKindCompleted, StateKindFailed, StateKindPromoted, StateKindRollback, StateKindSkipped, StateKindTimeout, StateKindNeedsManualRecovery}
	for _, k := range nonTerm {
		assert.False(t, k.IsTerminal(), string(k))
	}
	for _, k := range term {
		assert.True(t, k.IsTerminal(), string(k))
	}
}
