package contracts

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistry_Added_Parse(t *testing.T) {
	var e RuleRegistryEntry
	require.NoError(t, json.Unmarshal([]byte(fixtureRegistryAdded()), &e))
	assert.Equal(t, RegistryKindAdded, e.Kind)
}

func TestRegistry_Updated_Parse(t *testing.T) {
	data := `{
  "kind": "updated",
  "schema_version": "1",
  "rule_id": "r-0001",
  "rule_path": "rules/r-0001.md",
  "sha256": "0000000000000000000000000000000000000000000000000000000000000010",
  "prev_sha256": "0000000000000000000000000000000000000000000000000000000000000001",
  "idempotency_key": "0000000000000000000000000000000000000000000000000000000000000003",
  "version_seq": 2,
  "prev_hash": "0000000000000000000000000000000000000000000000000000000000000099",
  "by_run_id": "2026-04-21-PR43-abcdef1",
  "at": "2026-04-21T12:00:00Z"
}`
	var e RuleRegistryEntry
	require.NoError(t, json.Unmarshal([]byte(data), &e))
	assert.Equal(t, RegistryKindUpdated, e.Kind)
}

func TestRegistry_RolledBack_Parse(t *testing.T) {
	data := `{
  "kind": "rolled_back",
  "schema_version": "1",
  "target_op_id": "0000000000000000000000000000000000000000000000000000000000000002",
  "target_offset": 1024,
  "target_sha256": "0000000000000000000000000000000000000000000000000000000000000030",
  "by_run_id": "2026-04-22-PR44-abcdef2",
  "rollback_reason": "lease_failure",
  "failed_step": "70",
  "version_seq": 3,
  "prev_hash": "0000000000000000000000000000000000000000000000000000000000000088",
  "at": "2026-04-22T12:00:00Z"
}`
	var e RuleRegistryEntry
	require.NoError(t, json.Unmarshal([]byte(data), &e))
	v := e.Value.(RuleRegistryRolledBack)
	assert.Equal(t, RollbackReasonLeaseFailure, v.RollbackReason)
	assert.Equal(t, FailedStep70, v.FailedStep)
	assert.EqualValues(t, 1024, v.TargetOffset)
}

func TestRegistry_StatusChanged_Parse(t *testing.T) {
	data := `{
  "kind": "status_changed",
  "schema_version": "1",
  "rule_id": "r-0001",
  "prev_status": "active",
  "new_status": "deprecated",
  "transition": "deprecate",
  "op_id": "0000000000000000000000000000000000000000000000000000000000000050",
  "version_seq": 4,
  "prev_hash": "0000000000000000000000000000000000000000000000000000000000000077",
  "by_sunset_run_id": "sunset-2026-04-22",
  "at": "2026-04-22T00:00:00Z"
}`
	var e RuleRegistryEntry
	require.NoError(t, json.Unmarshal([]byte(data), &e))
	assert.Equal(t, RegistryKindStatusChanged, e.Kind)
}

// finding #5: Archived / Restored variant は prev_status / new_status を持つ.
func TestRegistry_Archived_Parse(t *testing.T) {
	data := `{
  "kind": "archived",
  "schema_version": "1",
  "rule_id": "r-0001",
  "prev_status": "deprecated",
  "new_status": "archived",
  "op_id": "0000000000000000000000000000000000000000000000000000000000000050",
  "version_seq": 5,
  "prev_hash": "0000000000000000000000000000000000000000000000000000000000000077",
  "by_sunset_run_id": "sunset-2026-04-22",
  "at": "2026-04-22T00:00:00Z"
}`
	var e RuleRegistryEntry
	require.NoError(t, json.Unmarshal([]byte(data), &e))
	v := e.Value.(RuleRegistryArchived)
	assert.Equal(t, RuleStatusDeprecated, v.PrevStatus)
	assert.Equal(t, RuleStatusArchived, v.NewStatus)
}

func TestRegistry_Archived_Reject_MissingPrevStatus(t *testing.T) {
	data := `{
  "kind": "archived",
  "schema_version": "1",
  "rule_id": "r-0001",
  "new_status": "archived",
  "op_id": "0000000000000000000000000000000000000000000000000000000000000050",
  "version_seq": 5,
  "prev_hash": "0000000000000000000000000000000000000000000000000000000000000077",
  "by_sunset_run_id": "sunset-2026-04-22",
  "at": "2026-04-22T00:00:00Z"
}`
	var e RuleRegistryEntry
	assert.Error(t, json.Unmarshal([]byte(data), &e))
}

func TestRegistry_Restored_Parse(t *testing.T) {
	data := `{
  "kind": "restored",
  "schema_version": "1",
  "rule_id": "r-0001",
  "prev_status": "archived",
  "new_status": "active",
  "op_id": "0000000000000000000000000000000000000000000000000000000000000060",
  "version_seq": 6,
  "prev_hash": "0000000000000000000000000000000000000000000000000000000000000066",
  "by_sunset_run_id": "sunset-2026-05-01",
  "at": "2026-05-01T00:00:00Z"
}`
	var e RuleRegistryEntry
	require.NoError(t, json.Unmarshal([]byte(data), &e))
	v := e.Value.(RuleRegistryRestored)
	assert.Equal(t, RuleStatusArchived, v.PrevStatus)
	assert.Equal(t, RuleStatusActive, v.NewStatus)
}

func TestRegistry_Restored_Reject_MissingNewStatus(t *testing.T) {
	data := `{
  "kind": "restored",
  "schema_version": "1",
  "rule_id": "r-0001",
  "prev_status": "archived",
  "op_id": "0000000000000000000000000000000000000000000000000000000000000060",
  "version_seq": 6,
  "prev_hash": "0000000000000000000000000000000000000000000000000000000000000066",
  "by_sunset_run_id": "sunset-2026-05-01",
  "at": "2026-05-01T00:00:00Z"
}`
	var e RuleRegistryEntry
	assert.Error(t, json.Unmarshal([]byte(data), &e))
}

func TestRegistry_Validate_RejectsTaggedUnionMismatches(t *testing.T) {
	fixtures := map[RegistryKind]string{
		RegistryKindAdded: fixtureRegistryAdded(),
		RegistryKindUpdated: `{
  "kind": "updated",
  "schema_version": "1",
  "rule_id": "r-0001",
  "rule_path": "rules/r-0001.md",
  "sha256": "0000000000000000000000000000000000000000000000000000000000000010",
  "prev_sha256": "0000000000000000000000000000000000000000000000000000000000000001",
  "idempotency_key": "0000000000000000000000000000000000000000000000000000000000000003",
  "version_seq": 2,
  "prev_hash": "0000000000000000000000000000000000000000000000000000000000000099",
  "by_run_id": "2026-04-21-PR43-abcdef1",
  "at": "2026-04-21T12:00:00Z"
}`,
		RegistryKindRolledBack: `{
  "kind": "rolled_back",
  "schema_version": "1",
  "target_op_id": "0000000000000000000000000000000000000000000000000000000000000002",
  "target_offset": 1024,
  "target_sha256": "0000000000000000000000000000000000000000000000000000000000000030",
  "by_run_id": "2026-04-22-PR44-abcdef2",
  "rollback_reason": "lease_failure",
  "failed_step": "70",
  "version_seq": 3,
  "prev_hash": "0000000000000000000000000000000000000000000000000000000000000088",
  "at": "2026-04-22T12:00:00Z"
}`,
		RegistryKindStatusChanged: `{
  "kind": "status_changed",
  "schema_version": "1",
  "rule_id": "r-0001",
  "prev_status": "active",
  "new_status": "deprecated",
  "transition": "deprecate",
  "op_id": "0000000000000000000000000000000000000000000000000000000000000050",
  "version_seq": 4,
  "prev_hash": "0000000000000000000000000000000000000000000000000000000000000077",
  "by_sunset_run_id": "sunset-2026-04-22",
  "at": "2026-04-22T00:00:00Z"
}`,
		RegistryKindArchived: `{
  "kind": "archived",
  "schema_version": "1",
  "rule_id": "r-0001",
  "prev_status": "deprecated",
  "new_status": "archived",
  "op_id": "0000000000000000000000000000000000000000000000000000000000000050",
  "version_seq": 5,
  "prev_hash": "0000000000000000000000000000000000000000000000000000000000000077",
  "by_sunset_run_id": "sunset-2026-04-22",
  "at": "2026-04-22T00:00:00Z"
}`,
		RegistryKindRestored: `{
  "kind": "restored",
  "schema_version": "1",
  "rule_id": "r-0001",
  "prev_status": "archived",
  "new_status": "active",
  "op_id": "0000000000000000000000000000000000000000000000000000000000000060",
  "version_seq": 6,
  "prev_hash": "0000000000000000000000000000000000000000000000000000000000000066",
  "by_sunset_run_id": "sunset-2026-05-01",
  "at": "2026-05-01T00:00:00Z"
}`,
	}
	parse := func(kind RegistryKind) RuleRegistryEntry {
		var e RuleRegistryEntry
		require.NoError(t, json.Unmarshal([]byte(fixtures[kind]), &e))
		return e
	}
	tests := []struct {
		name     string
		entry    RuleRegistryEntry
		mutate   func(*RuleRegistryEntry)
		expected error
	}{
		{"added outer mismatch", parse(RegistryKindAdded), func(e *RuleRegistryEntry) { e.Kind = RegistryKindUpdated }, ErrRegistryVariantTypeMismatch},
		{"added inner mismatch", parse(RegistryKindAdded), func(e *RuleRegistryEntry) {
			v := e.Value.(RuleRegistryAdded)
			v.Kind = RegistryKindUpdated
			e.Value = v
		}, ErrRegistryVariantKindMismatch},
		{"updated outer mismatch", parse(RegistryKindUpdated), func(e *RuleRegistryEntry) { e.Kind = RegistryKindAdded }, ErrRegistryVariantTypeMismatch},
		{"updated inner mismatch", parse(RegistryKindUpdated), func(e *RuleRegistryEntry) {
			v := e.Value.(RuleRegistryUpdated)
			v.Kind = RegistryKindAdded
			e.Value = v
		}, ErrRegistryVariantKindMismatch},
		{"rolled_back outer mismatch", parse(RegistryKindRolledBack), func(e *RuleRegistryEntry) { e.Kind = RegistryKindAdded }, ErrRegistryVariantTypeMismatch},
		{"rolled_back inner mismatch", parse(RegistryKindRolledBack), func(e *RuleRegistryEntry) {
			v := e.Value.(RuleRegistryRolledBack)
			v.Kind = RegistryKindAdded
			e.Value = v
		}, ErrRegistryVariantKindMismatch},
		{"status_changed outer mismatch", parse(RegistryKindStatusChanged), func(e *RuleRegistryEntry) { e.Kind = RegistryKindArchived }, ErrRegistryVariantTypeMismatch},
		{"status_changed inner mismatch", parse(RegistryKindStatusChanged), func(e *RuleRegistryEntry) {
			v := e.Value.(RuleRegistryStatusChanged)
			v.Kind = RegistryKindArchived
			e.Value = v
		}, ErrRegistryVariantKindMismatch},
		{"archived outer mismatch", parse(RegistryKindArchived), func(e *RuleRegistryEntry) { e.Kind = RegistryKindRestored }, ErrRegistryVariantTypeMismatch},
		{"archived inner mismatch", parse(RegistryKindArchived), func(e *RuleRegistryEntry) {
			v := e.Value.(RuleRegistryArchived)
			v.Kind = RegistryKindRestored
			e.Value = v
		}, ErrRegistryVariantKindMismatch},
		{"restored outer mismatch", parse(RegistryKindRestored), func(e *RuleRegistryEntry) { e.Kind = RegistryKindArchived }, ErrRegistryVariantTypeMismatch},
		{"restored inner mismatch", parse(RegistryKindRestored), func(e *RuleRegistryEntry) {
			v := e.Value.(RuleRegistryRestored)
			v.Kind = RegistryKindArchived
			e.Value = v
		}, ErrRegistryVariantKindMismatch},
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

func TestRegistry_Reject_WrongKind(t *testing.T) {
	var e RuleRegistryEntry
	err := json.Unmarshal([]byte(`{"kind":"bogus"}`), &e)
	assert.ErrorIs(t, err, ErrUnknownRegistryKind)
}

func TestRegistry_Reject_UnknownKey(t *testing.T) {
	data := strings.Replace(fixtureRegistryAdded(), `"rule_path"`, `"unknown": 1, "rule_path"`, 1)
	var e RuleRegistryEntry
	assert.Error(t, json.Unmarshal([]byte(data), &e))
}

func TestRegistry_Reject_MissingRequired(t *testing.T) {
	// rule_id を削除
	data := strings.Replace(fixtureRegistryAdded(), `"rule_id": "r-0001",`, ``, 1)
	var e RuleRegistryEntry
	assert.Error(t, json.Unmarshal([]byte(data), &e))
}

func TestRegistry_Reject_TrailingJSON(t *testing.T) {
	data := fixtureRegistryAdded() + `{"more": 1}`
	var e RuleRegistryEntry
	assert.Error(t, json.Unmarshal([]byte(data), &e))
}

func TestRegistry_Reject_TrailingBytes(t *testing.T) {
	data := fixtureRegistryAdded() + "garbage"
	var e RuleRegistryEntry
	assert.Error(t, json.Unmarshal([]byte(data), &e))
}
