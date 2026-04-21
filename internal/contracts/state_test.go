package contracts

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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
	// warning sub-kind は `kind` 直接 (outer envelope の `warning` wrap は無い)。
	data := `{"kind":"registry_size_critical","source":"sunset_tick","count":2001,"at":"2026-04-20T12:00:00Z"}`
	var e StateEntry
	require.NoError(t, json.Unmarshal([]byte(data), &e))
	assert.True(t, e.Kind.IsWarning())
	w := e.Value.(StateEntryWarning)
	assert.Nil(t, w.PR)
	assert.Nil(t, w.RunID)
	assert.NotNil(t, w.Source)
	assert.Equal(t, WarningSourceSunsetTick, *w.Source)
	assert.Nil(t, w.Step)
	require.NotNil(t, w.Count)
	assert.EqualValues(t, 2001, *w.Count)
}

func TestState_Warning_PRScoped(t *testing.T) {
	data := `{"kind":"registry_size_high","pr":42,"run_id":"2026-04-20-PR42-abcdef0","source":"step70","step":"70","count":1501,"at":"2026-04-20T12:00:00Z"}`
	var e StateEntry
	require.NoError(t, json.Unmarshal([]byte(data), &e))
	assert.True(t, e.Kind.IsWarning())
}

func TestState_Warning_RejectsRegistryWarningWithoutCount(t *testing.T) {
	data := `{"kind":"registry_size_high","source":"step70","step":"70","pr":42,"run_id":"2026-04-20-PR42-abcdef0","at":"2026-04-20T12:00:00Z"}`
	var e StateEntry
	err := json.Unmarshal([]byte(data), &e)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStateWarningRegistryCount)
}

func TestState_Warning_RejectsRegistrySizeHighBelowMinimum(t *testing.T) {
	data := `{"kind":"registry_size_high","source":"step70","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"70","count":1499,"at":"2026-04-20T12:00:00Z"}`
	var e StateEntry
	err := json.Unmarshal([]byte(data), &e)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStateWarningRegistryHighMinimum)
}

func TestState_Warning_RejectsRegistrySizeCriticalBelowMinimum(t *testing.T) {
	data := `{"kind":"registry_size_critical","source":"sunset_tick","count":1999,"at":"2026-04-20T12:00:00Z"}`
	var e StateEntry
	err := json.Unmarshal([]byte(data), &e)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStateWarningRegistryCriticalMin)
}

func TestState_Warning_RejectsRescueRetryWithoutPRScope(t *testing.T) {
	data := `{"kind":"rescue_retry","step":"20","at":"2026-04-20T12:00:00Z"}`
	var e StateEntry
	err := json.Unmarshal([]byte(data), &e)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStateWarningRescueRetryScope)
}

func TestState_Warning_RejectsRescueRetryWrongStep(t *testing.T) {
	data := `{"kind":"rescue_retry","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"70","at":"2026-04-20T12:00:00Z"}`
	var e StateEntry
	err := json.Unmarshal([]byte(data), &e)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStateWarningRescueRetryStep)
}

func TestState_Warning_RejectsRescueRetryCount(t *testing.T) {
	data := `{"kind":"rescue_retry","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"20","count":3,"at":"2026-04-20T12:00:00Z"}`
	var e StateEntry
	err := json.Unmarshal([]byte(data), &e)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStateWarningRescueRetryCount)
}

func TestState_Warning_RejectsScopeMismatch(t *testing.T) {
	data := `{"kind":"registry_size_critical","source":"step70","pr":42,"step":"70","count":2000,"at":"2026-04-20T12:00:00Z"}`
	var e StateEntry
	err := json.Unmarshal([]byte(data), &e)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStateWarningScopeMismatch)
}

func TestState_Warning_RejectsRegistryWarningWrongStep(t *testing.T) {
	data := `{"kind":"registry_size_high","source":"step70","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"30","count":1501,"at":"2026-04-20T12:00:00Z"}`
	var e StateEntry
	err := json.Unmarshal([]byte(data), &e)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStateWarningRegistryStep)
}

func TestState_Warning_AcceptsRegistryWarningStep70(t *testing.T) {
	data := `{"kind":"registry_size_high","source":"step70","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"70","count":1501,"at":"2026-04-20T12:00:00Z"}`
	var e StateEntry
	require.NoError(t, json.Unmarshal([]byte(data), &e))
}

func TestState_Warning_AcceptsRegistryWarningSunsetTick(t *testing.T) {
	data := `{"kind":"registry_size_high","source":"sunset_tick","count":1501,"at":"2026-04-20T12:00:00Z"}`
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

func TestState_Rollback_RejectsReasonFailedStepMismatch(t *testing.T) {
	data := `{"kind":"rollback","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"70","rollback_reason":"worktree_rescue_loop","failed_step":"70","at":"2026-04-20T12:00:00Z"}`
	var e StateEntry
	err := json.Unmarshal([]byte(data), &e)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrReasonFailedStepMismatch)
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

// finding #3: started.step は eq=10 固定。step=20 の started は reject される。
func TestState_Reject_Started_StepNot10(t *testing.T) {
	data := `{"kind":"started","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"20","at":"2026-04-20T10:00:00Z"}`
	var e StateEntry
	assert.Error(t, json.Unmarshal([]byte(data), &e))
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
	nonTerm := []StateKind{
		StateKindStarted,
		StateKindStepDone,
		StateKindInterrupted,
		StateKindPromoting,
		StateKindWarningRegistrySizeHigh,
		StateKindWarningRegistrySizeCritical,
		StateKindWarningRescueRetry,
	}
	term := []StateKind{StateKindCompleted, StateKindFailed, StateKindPromoted, StateKindRollback, StateKindSkipped, StateKindTimeout, StateKindNeedsManualRecovery}
	for _, k := range nonTerm {
		assert.False(t, k.IsTerminal(), string(k))
	}
	for _, k := range term {
		assert.True(t, k.IsTerminal(), string(k))
	}
}

func TestState_Validate_RejectsTaggedUnionMismatches(t *testing.T) {
	fixtures := map[StateKind]string{
		StateKindStarted:                 fixtureStateStarted(),
		StateKindStepDone:                `{"kind":"step_done","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"20","at":"2026-04-20T10:30:00Z"}`,
		StateKindInterrupted:             `{"kind":"interrupted","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"30","reason":"rate_limit","at":"2026-04-20T11:00:00Z"}`,
		StateKindPromoting:               `{"kind":"promoting","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"70","at":"2026-04-20T12:00:00Z"}`,
		StateKindWarningRegistrySizeHigh: `{"kind":"registry_size_high","source":"step70","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"70","count":1501,"at":"2026-04-20T12:00:00Z"}`,
		StateKindCompleted:               `{"kind":"completed","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"70","at":"2026-04-20T12:00:00Z"}`,
		StateKindFailed:                  `{"kind":"failed","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"30","reason":"judge_failed","at":"2026-04-20T12:00:00Z"}`,
		StateKindPromoted:                `{"kind":"promoted","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"70","at":"2026-04-20T12:00:00Z"}`,
		StateKindRollback:                `{"kind":"rollback","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"70","rollback_reason":"transactional_failure","failed_step":"70","at":"2026-04-20T12:00:00Z"}`,
		StateKindSkipped:                 `{"kind":"skipped","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"10","at":"2026-04-20T12:00:00Z"}`,
		StateKindTimeout:                 `{"kind":"timeout","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"20","at":"2026-04-20T12:00:00Z"}`,
		StateKindNeedsManualRecovery:     `{"kind":"needs_manual_recovery","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"70","reason":"remote_divergence","failed_step":"70","at":"2026-04-20T12:00:00Z"}`,
	}
	parse := func(kind StateKind) StateEntry {
		var e StateEntry
		require.NoError(t, json.Unmarshal([]byte(fixtures[kind]), &e))
		return e
	}
	tests := []struct {
		name     string
		entry    StateEntry
		mutate   func(*StateEntry)
		expected error
	}{
		{"started outer mismatch", parse(StateKindStarted), func(e *StateEntry) { e.Kind = StateKindStepDone }, ErrStateVariantTypeMismatch},
		{"started inner mismatch", parse(StateKindStarted), func(e *StateEntry) { v := e.Value.(StateEntryStarted); v.Kind = StateKindStepDone; e.Value = v }, ErrStateVariantKindMismatch},
		{"step_done outer mismatch", parse(StateKindStepDone), func(e *StateEntry) { e.Kind = StateKindStarted }, ErrStateVariantTypeMismatch},
		{"step_done inner mismatch", parse(StateKindStepDone), func(e *StateEntry) { v := e.Value.(StateEntryStepDone); v.Kind = StateKindStarted; e.Value = v }, ErrStateVariantKindMismatch},
		{"interrupted outer mismatch", parse(StateKindInterrupted), func(e *StateEntry) { e.Kind = StateKindPromoting }, ErrStateVariantTypeMismatch},
		{"interrupted inner mismatch", parse(StateKindInterrupted), func(e *StateEntry) { v := e.Value.(StateEntryInterrupted); v.Kind = StateKindPromoting; e.Value = v }, ErrStateVariantKindMismatch},
		{"promoting outer mismatch", parse(StateKindPromoting), func(e *StateEntry) { e.Kind = StateKindInterrupted }, ErrStateVariantTypeMismatch},
		{"promoting inner mismatch", parse(StateKindPromoting), func(e *StateEntry) { v := e.Value.(StateEntryPromoting); v.Kind = StateKindInterrupted; e.Value = v }, ErrStateVariantKindMismatch},
		{"warning inner mismatch", parse(StateKindWarningRegistrySizeHigh), func(e *StateEntry) {
			v := e.Value.(StateEntryWarning)
			v.Kind = StateKindWarningRegistrySizeCritical
			e.Value = v
		}, ErrStateVariantTypeMismatch},
		{"completed outer mismatch", parse(StateKindCompleted), func(e *StateEntry) { e.Kind = StateKindFailed }, ErrStateVariantTypeMismatch},
		{"completed inner mismatch", parse(StateKindCompleted), func(e *StateEntry) { v := e.Value.(StateEntryCompleted); v.Kind = StateKindFailed; e.Value = v }, ErrStateVariantKindMismatch},
		{"failed outer mismatch", parse(StateKindFailed), func(e *StateEntry) { e.Kind = StateKindCompleted }, ErrStateVariantTypeMismatch},
		{"failed inner mismatch", parse(StateKindFailed), func(e *StateEntry) { v := e.Value.(StateEntryFailed); v.Kind = StateKindCompleted; e.Value = v }, ErrStateVariantKindMismatch},
		{"promoted outer mismatch", parse(StateKindPromoted), func(e *StateEntry) { e.Kind = StateKindRollback }, ErrStateVariantTypeMismatch},
		{"promoted inner mismatch", parse(StateKindPromoted), func(e *StateEntry) { v := e.Value.(StateEntryPromoted); v.Kind = StateKindRollback; e.Value = v }, ErrStateVariantKindMismatch},
		{"rollback outer mismatch", parse(StateKindRollback), func(e *StateEntry) { e.Kind = StateKindPromoted }, ErrStateVariantTypeMismatch},
		{"rollback inner mismatch", parse(StateKindRollback), func(e *StateEntry) { v := e.Value.(StateEntryRollback); v.Kind = StateKindPromoted; e.Value = v }, ErrStateVariantKindMismatch},
		{"skipped outer mismatch", parse(StateKindSkipped), func(e *StateEntry) { e.Kind = StateKindTimeout }, ErrStateVariantTypeMismatch},
		{"skipped inner mismatch", parse(StateKindSkipped), func(e *StateEntry) { v := e.Value.(StateEntrySkipped); v.Kind = StateKindTimeout; e.Value = v }, ErrStateVariantKindMismatch},
		{"timeout outer mismatch", parse(StateKindTimeout), func(e *StateEntry) { e.Kind = StateKindSkipped }, ErrStateVariantTypeMismatch},
		{"timeout inner mismatch", parse(StateKindTimeout), func(e *StateEntry) { v := e.Value.(StateEntryTimeout); v.Kind = StateKindSkipped; e.Value = v }, ErrStateVariantKindMismatch},
		{"needs_manual_recovery outer mismatch", parse(StateKindNeedsManualRecovery), func(e *StateEntry) { e.Kind = StateKindCompleted }, ErrStateVariantTypeMismatch},
		{"needs_manual_recovery inner mismatch", parse(StateKindNeedsManualRecovery), func(e *StateEntry) {
			v := e.Value.(StateEntryNeedsManualRecovery)
			v.Kind = StateKindCompleted
			e.Value = v
		}, ErrStateVariantKindMismatch},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := tt.entry
			tt.mutate(&entry)
			err := entry.Validate()
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.expected)
		})
	}
}

func TestState_Validate_AcceptsValueAndPointerVariants(t *testing.T) {
	fixtures := map[StateKind]string{
		StateKindStarted:                     fixtureStateStarted(),
		StateKindStepDone:                    `{"kind":"step_done","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"20","at":"2026-04-20T10:30:00Z"}`,
		StateKindInterrupted:                 `{"kind":"interrupted","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"30","reason":"rate_limit","at":"2026-04-20T11:00:00Z"}`,
		StateKindPromoting:                   `{"kind":"promoting","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"70","at":"2026-04-20T12:00:00Z"}`,
		StateKindWarningRegistrySizeHigh:     `{"kind":"registry_size_high","source":"step70","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"70","count":1501,"at":"2026-04-20T12:00:00Z"}`,
		StateKindWarningRegistrySizeCritical: `{"kind":"registry_size_critical","source":"sunset_tick","count":2001,"at":"2026-04-20T12:00:00Z"}`,
		StateKindWarningRescueRetry:          `{"kind":"rescue_retry","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"20","at":"2026-04-20T12:00:00Z"}`,
		StateKindCompleted:                   `{"kind":"completed","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"70","at":"2026-04-20T12:00:00Z"}`,
		StateKindFailed:                      `{"kind":"failed","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"30","reason":"judge_failed","at":"2026-04-20T12:00:00Z"}`,
		StateKindPromoted:                    `{"kind":"promoted","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"70","at":"2026-04-20T12:00:00Z"}`,
		StateKindRollback:                    `{"kind":"rollback","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"70","rollback_reason":"transactional_failure","failed_step":"70","at":"2026-04-20T12:00:00Z"}`,
		StateKindSkipped:                     `{"kind":"skipped","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"10","at":"2026-04-20T12:00:00Z"}`,
		StateKindTimeout:                     `{"kind":"timeout","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"20","at":"2026-04-20T12:00:00Z"}`,
		StateKindNeedsManualRecovery:         `{"kind":"needs_manual_recovery","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"70","reason":"remote_divergence","failed_step":"70","at":"2026-04-20T12:00:00Z"}`,
	}
	parse := func(kind StateKind) StateEntry {
		var e StateEntry
		require.NoError(t, json.Unmarshal([]byte(fixtures[kind]), &e))
		return e
	}

	tests := []struct {
		name  string
		entry StateEntry
	}{
		{name: "started value", entry: parse(StateKindStarted)},
		{name: "step_done value", entry: parse(StateKindStepDone)},
		{name: "interrupted value", entry: parse(StateKindInterrupted)},
		{name: "promoting value", entry: parse(StateKindPromoting)},
		{name: "registry_size_high value", entry: parse(StateKindWarningRegistrySizeHigh)},
		{name: "registry_size_critical value", entry: parse(StateKindWarningRegistrySizeCritical)},
		{name: "rescue_retry value", entry: parse(StateKindWarningRescueRetry)},
		{name: "completed value", entry: parse(StateKindCompleted)},
		{name: "failed value", entry: parse(StateKindFailed)},
		{name: "promoted value", entry: parse(StateKindPromoted)},
		{name: "rollback value", entry: parse(StateKindRollback)},
		{name: "skipped value", entry: parse(StateKindSkipped)},
		{name: "timeout value", entry: parse(StateKindTimeout)},
		{name: "needs_manual_recovery value", entry: parse(StateKindNeedsManualRecovery)},
		{name: "started pointer", entry: func() StateEntry {
			v := parse(StateKindStarted).Value.(StateEntryStarted)
			return StateEntry{Kind: StateKindStarted, Value: &v}
		}()},
		{name: "step_done pointer", entry: func() StateEntry {
			v := parse(StateKindStepDone).Value.(StateEntryStepDone)
			return StateEntry{Kind: StateKindStepDone, Value: &v}
		}()},
		{name: "interrupted pointer", entry: func() StateEntry {
			v := parse(StateKindInterrupted).Value.(StateEntryInterrupted)
			return StateEntry{Kind: StateKindInterrupted, Value: &v}
		}()},
		{name: "promoting pointer", entry: func() StateEntry {
			v := parse(StateKindPromoting).Value.(StateEntryPromoting)
			return StateEntry{Kind: StateKindPromoting, Value: &v}
		}()},
		{name: "registry_size_high pointer", entry: func() StateEntry {
			v := parse(StateKindWarningRegistrySizeHigh).Value.(StateEntryWarning)
			return StateEntry{Kind: StateKindWarningRegistrySizeHigh, Value: &v}
		}()},
		{name: "registry_size_critical pointer", entry: func() StateEntry {
			v := parse(StateKindWarningRegistrySizeCritical).Value.(StateEntryWarning)
			return StateEntry{Kind: StateKindWarningRegistrySizeCritical, Value: &v}
		}()},
		{name: "rescue_retry pointer", entry: func() StateEntry {
			v := parse(StateKindWarningRescueRetry).Value.(StateEntryWarning)
			return StateEntry{Kind: StateKindWarningRescueRetry, Value: &v}
		}()},
		{name: "completed pointer", entry: func() StateEntry {
			v := parse(StateKindCompleted).Value.(StateEntryCompleted)
			return StateEntry{Kind: StateKindCompleted, Value: &v}
		}()},
		{name: "failed pointer", entry: func() StateEntry {
			v := parse(StateKindFailed).Value.(StateEntryFailed)
			return StateEntry{Kind: StateKindFailed, Value: &v}
		}()},
		{name: "promoted pointer", entry: func() StateEntry {
			v := parse(StateKindPromoted).Value.(StateEntryPromoted)
			return StateEntry{Kind: StateKindPromoted, Value: &v}
		}()},
		{name: "rollback pointer", entry: func() StateEntry {
			v := parse(StateKindRollback).Value.(StateEntryRollback)
			return StateEntry{Kind: StateKindRollback, Value: &v}
		}()},
		{name: "skipped pointer", entry: func() StateEntry {
			v := parse(StateKindSkipped).Value.(StateEntrySkipped)
			return StateEntry{Kind: StateKindSkipped, Value: &v}
		}()},
		{name: "timeout pointer", entry: func() StateEntry {
			v := parse(StateKindTimeout).Value.(StateEntryTimeout)
			return StateEntry{Kind: StateKindTimeout, Value: &v}
		}()},
		{name: "needs_manual_recovery pointer", entry: func() StateEntry {
			v := parse(StateKindNeedsManualRecovery).Value.(StateEntryNeedsManualRecovery)
			return StateEntry{Kind: StateKindNeedsManualRecovery, Value: &v}
		}()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NoError(t, tt.entry.Validate())
		})
	}
}

func TestStateEntry_MarshalJSON_RejectsVariantMismatch(t *testing.T) {
	now := time.Now()
	entry := StateEntry{
		Kind: StateKindStarted,
		Value: StateEntryStepDone{
			Kind:  StateKindStepDone,
			PR:    42,
			RunID: "2026-04-20-PR42-abcdef0",
			Step:  FailedStep20,
			At:    now,
		},
	}

	_, err := json.Marshal(entry)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStateVariantTypeMismatch)
}
