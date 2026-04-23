package contracts

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixtureRegistryAdded() string {
	return `{
  "kind": "added",
  "schema_version": "1",
  "rule_id": "r-0001",
  "rule_path": "rules/r-0001.md",
  "sha256": "0000000000000000000000000000000000000000000000000000000000000001",
  "idempotency_key": "0000000000000000000000000000000000000000000000000000000000000002",
  "version_seq": 1,
  "prev_hash": "",
  "by_run_id": "2026-04-20-PR42-abcdef0",
  "at": "2026-04-20T12:00:00Z"
}`
}

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

func fixtureRegistryByKind(kind RegistryKind) string {
	switch kind {
	case RegistryKindAdded:
		return fixtureRegistryAdded()
	case RegistryKindUpdated:
		return `{
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
	case RegistryKindRolledBack:
		return `{
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
	case RegistryKindStatusChanged:
		return `{
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
	case RegistryKindArchived:
		return `{
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
	case RegistryKindRestored:
		return `{
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
	default:
		return ""
	}
}

func TestRegistry_RejectsVersionSeqOneWithPrevHashAcrossVariants(t *testing.T) {
	versionSeqPattern := regexp.MustCompile(`"version_seq": \d+`)
	prevHashPattern := regexp.MustCompile(`"prev_hash": "[0-9a-f]*"`)

	tests := []struct {
		name string
		kind RegistryKind
	}{
		{name: "added", kind: RegistryKindAdded},
		{name: "updated", kind: RegistryKindUpdated},
		{name: "rolled_back", kind: RegistryKindRolledBack},
		{name: "status_changed", kind: RegistryKindStatusChanged},
		{name: "archived", kind: RegistryKindArchived},
		{name: "restored", kind: RegistryKindRestored},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := versionSeqPattern.ReplaceAllString(fixtureRegistryByKind(tt.kind), `"version_seq": 1`)
			data = prevHashPattern.ReplaceAllString(data, `"prev_hash": "00000000000000000000000000000000000000000000000000000000000000aa"`)
			var e RuleRegistryEntry
			err := json.Unmarshal([]byte(data), &e)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrRegistryPrevHashMustBeEmptyOnFirst)
		})
	}
}

func TestRegistry_RejectsMissingPrevHashAfterFirstVersionAcrossVariants(t *testing.T) {
	versionSeqPattern := regexp.MustCompile(`"version_seq": \d+`)
	prevHashPattern := regexp.MustCompile(`"prev_hash": "[0-9a-f]*"`)

	tests := []struct {
		name string
		kind RegistryKind
	}{
		{name: "added", kind: RegistryKindAdded},
		{name: "updated", kind: RegistryKindUpdated},
		{name: "rolled_back", kind: RegistryKindRolledBack},
		{name: "status_changed", kind: RegistryKindStatusChanged},
		{name: "archived", kind: RegistryKindArchived},
		{name: "restored", kind: RegistryKindRestored},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := versionSeqPattern.ReplaceAllString(fixtureRegistryByKind(tt.kind), `"version_seq": 2`)
			data = prevHashPattern.ReplaceAllString(data, `"prev_hash": ""`)
			var e RuleRegistryEntry
			err := json.Unmarshal([]byte(data), &e)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrRegistryPrevHashSequenceMismatch)
		})
	}
}

func TestRegistry_RolledBack_RejectsMissingTargetOffset(t *testing.T) {
	data := `{
  "kind": "rolled_back",
  "schema_version": "1",
  "target_op_id": "0000000000000000000000000000000000000000000000000000000000000002",
  "target_sha256": "0000000000000000000000000000000000000000000000000000000000000030",
  "by_run_id": "2026-04-22-PR44-abcdef2",
  "rollback_reason": "lease_failure",
  "failed_step": "70",
  "version_seq": 3,
  "prev_hash": "0000000000000000000000000000000000000000000000000000000000000088",
  "at": "2026-04-22T12:00:00Z"
}`
	var e RuleRegistryEntry
	err := json.Unmarshal([]byte(data), &e)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRegistryRolledBackMissingTargetOffset)
}

func TestRegistry_RolledBack_RejectsDuplicateTargetOffsetKey(t *testing.T) {
	data := `{
  "kind": "rolled_back",
  "schema_version": "1",
  "target_op_id": "0000000000000000000000000000000000000000000000000000000000000002",
  "target_offset": 1,
  "target_offset": 2,
  "target_sha256": "0000000000000000000000000000000000000000000000000000000000000030",
  "by_run_id": "2026-04-22-PR44-abcdef2",
  "rollback_reason": "lease_failure",
  "failed_step": "70",
  "version_seq": 3,
  "prev_hash": "0000000000000000000000000000000000000000000000000000000000000088",
  "at": "2026-04-22T12:00:00Z"
}`
	var e RuleRegistryRolledBack
	err := json.Unmarshal([]byte(data), &e)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDuplicateJSONKey)
}

// finding #6: status_changed は active↔deprecated 遷移のみ許容。active→archived
// 等を status_changed で送ると reject。
func TestRegistry_StatusChanged_Reject_ActiveToArchived(t *testing.T) {
	data := `{
  "kind": "status_changed",
  "schema_version": "1",
  "rule_id": "r-0001",
  "prev_status": "active",
  "new_status": "archived",
  "transition": "archive",
  "op_id": "0000000000000000000000000000000000000000000000000000000000000050",
  "version_seq": 4,
  "prev_hash": "0000000000000000000000000000000000000000000000000000000000000077",
  "by_sunset_run_id": "sunset-2026-04-22",
  "at": "2026-04-22T00:00:00Z"
}`
	var e RuleRegistryEntry
	err := json.Unmarshal([]byte(data), &e)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRegistryStatusChangedInvalidTransition)
}

func TestRegistry_StatusChanged_Reject_SameStatus(t *testing.T) {
	data := `{
  "kind": "status_changed",
  "schema_version": "1",
  "rule_id": "r-0001",
  "prev_status": "active",
  "new_status": "active",
  "transition": "activate",
  "op_id": "0000000000000000000000000000000000000000000000000000000000000050",
  "version_seq": 4,
  "prev_hash": "0000000000000000000000000000000000000000000000000000000000000077",
  "by_sunset_run_id": "sunset-2026-04-22",
  "at": "2026-04-22T00:00:00Z"
}`
	var e RuleRegistryEntry
	err := json.Unmarshal([]byte(data), &e)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRegistryStatusChangedInvalidTransition)
}

func TestRegistry_StatusChanged_Reject_TransitionMismatch(t *testing.T) {
	// active → deprecated は合法だが transition=activate は整合しない。
	data := `{
  "kind": "status_changed",
  "schema_version": "1",
  "rule_id": "r-0001",
  "prev_status": "active",
  "new_status": "deprecated",
  "transition": "activate",
  "op_id": "0000000000000000000000000000000000000000000000000000000000000050",
  "version_seq": 4,
  "prev_hash": "0000000000000000000000000000000000000000000000000000000000000077",
  "by_sunset_run_id": "sunset-2026-04-22",
  "at": "2026-04-22T00:00:00Z"
}`
	var e RuleRegistryEntry
	err := json.Unmarshal([]byte(data), &e)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRegistryStatusChangedTransitionMismatch)
}

// finding #6: archived は prev ∈ {active,deprecated} AND new == archived 限定。
func TestRegistry_Archived_Reject_ArchivedToArchived(t *testing.T) {
	data := `{
  "kind": "archived",
  "schema_version": "1",
  "rule_id": "r-0001",
  "prev_status": "archived",
  "new_status": "archived",
  "op_id": "0000000000000000000000000000000000000000000000000000000000000050",
  "version_seq": 5,
  "prev_hash": "0000000000000000000000000000000000000000000000000000000000000077",
  "by_sunset_run_id": "sunset-2026-04-22",
  "at": "2026-04-22T00:00:00Z"
}`
	var e RuleRegistryEntry
	err := json.Unmarshal([]byte(data), &e)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRegistryArchivedInvalidTransition)
}

func TestRegistry_Archived_Reject_NewStatusNotArchived(t *testing.T) {
	data := `{
  "kind": "archived",
  "schema_version": "1",
  "rule_id": "r-0001",
  "prev_status": "active",
  "new_status": "deprecated",
  "op_id": "0000000000000000000000000000000000000000000000000000000000000050",
  "version_seq": 5,
  "prev_hash": "0000000000000000000000000000000000000000000000000000000000000077",
  "by_sunset_run_id": "sunset-2026-04-22",
  "at": "2026-04-22T00:00:00Z"
}`
	var e RuleRegistryEntry
	err := json.Unmarshal([]byte(data), &e)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRegistryArchivedInvalidTransition)
}

func TestRuleIdempotencyIndexEntry_RoundTrip(t *testing.T) {
	entry := RuleIdempotencyIndexEntry{
		IdempotencyKey: "0000000000000000000000000000000000000000000000000000000000000002",
		RegistryOffset: 0,
		RegistrySha256: "0000000000000000000000000000000000000000000000000000000000000030",
		Kind:           RegistryKindAdded,
		At:             time.Now(),
	}
	data, err := MarshalStrict(entry)
	require.NoError(t, err)

	var decoded RuleIdempotencyIndexEntry
	require.NoError(t, decodeStrict(data, &decoded))
	assert.Equal(t, entry.IdempotencyKey, decoded.IdempotencyKey)
	assert.EqualValues(t, 0, decoded.RegistryOffset)
	assert.Equal(t, entry.Kind, decoded.Kind)
}

func TestRuleIdempotencyIndexEntry_RejectsMissingRegistryOffset(t *testing.T) {
	data := []byte(`{
  "idempotency_key":"0000000000000000000000000000000000000000000000000000000000000002",
  "registry_sha256":"0000000000000000000000000000000000000000000000000000000000000030",
  "kind":"added",
  "at":"2026-04-22T12:00:00Z"
}`)
	var entry RuleIdempotencyIndexEntry
	err := decodeStrict(data, &entry)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRuleIdempotencyIndexMissingOffset)
}

func TestRuleIdempotencyIndexEntry_RejectsDuplicateRegistryOffsetKey(t *testing.T) {
	data := `{
  "idempotency_key": "0000000000000000000000000000000000000000000000000000000000000002",
  "registry_offset": 0,
  "registry_offset": 1,
  "registry_sha256": "0000000000000000000000000000000000000000000000000000000000000003",
  "kind": "added",
  "at": "2026-04-20T12:00:00Z"
}`
	var entry RuleIdempotencyIndexEntry
	err := json.Unmarshal([]byte(data), &entry)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDuplicateJSONKey)
}

func TestRuleRegistryEntry_MarshalJSON_RejectsVariantMismatch(t *testing.T) {
	entry := RuleRegistryEntry{
		Kind: RegistryKindAdded,
		Value: RuleRegistryUpdated{
			Kind:           RegistryKindUpdated,
			SchemaVersion:  "1",
			RuleID:         "r-0001",
			RulePath:       "rules/r-0001.md",
			Sha256:         "0000000000000000000000000000000000000000000000000000000000000010",
			PrevSha256:     "0000000000000000000000000000000000000000000000000000000000000001",
			IdempotencyKey: "0000000000000000000000000000000000000000000000000000000000000003",
			VersionSeq:     2,
			PrevHash:       "0000000000000000000000000000000000000000000000000000000000000099",
			ByRunID:        "2026-04-21-PR43-abcdef1",
			At:             time.Now(),
		},
	}

	_, err := json.Marshal(entry)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRegistryVariantTypeMismatch)
}

func TestRuleIdempotencyIndexEntry_RejectsNegativeOffset(t *testing.T) {
	data := []byte(`{
  "idempotency_key":"0000000000000000000000000000000000000000000000000000000000000002",
  "registry_offset":-1,
  "registry_sha256":"0000000000000000000000000000000000000000000000000000000000000030",
  "kind":"added",
  "at":"2026-04-22T12:00:00Z"
}`)
	var entry RuleIdempotencyIndexEntry
	assert.Error(t, decodeStrict(data, &entry))
}

func TestRuleIdempotencyIndexEntry_RejectsBadKind(t *testing.T) {
	data := []byte(`{
  "idempotency_key":"0000000000000000000000000000000000000000000000000000000000000002",
  "registry_offset":0,
  "registry_sha256":"0000000000000000000000000000000000000000000000000000000000000030",
  "kind":"bogus",
  "at":"2026-04-22T12:00:00Z"
}`)
	var entry RuleIdempotencyIndexEntry
	assert.Error(t, decodeStrict(data, &entry))
}

// finding #6: restored は prev == archived AND new ∈ {active,deprecated} 限定。
func TestRegistry_Restored_Reject_PrevNotArchived(t *testing.T) {
	data := `{
  "kind": "restored",
  "schema_version": "1",
  "rule_id": "r-0001",
  "prev_status": "active",
  "new_status": "deprecated",
  "op_id": "0000000000000000000000000000000000000000000000000000000000000060",
  "version_seq": 6,
  "prev_hash": "0000000000000000000000000000000000000000000000000000000000000066",
  "by_sunset_run_id": "sunset-2026-05-01",
  "at": "2026-05-01T00:00:00Z"
}`
	var e RuleRegistryEntry
	err := json.Unmarshal([]byte(data), &e)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRegistryRestoredInvalidTransition)
}

func TestRegistry_Restored_Reject_NewArchived(t *testing.T) {
	data := `{
  "kind": "restored",
  "schema_version": "1",
  "rule_id": "r-0001",
  "prev_status": "archived",
  "new_status": "archived",
  "op_id": "0000000000000000000000000000000000000000000000000000000000000060",
  "version_seq": 6,
  "prev_hash": "0000000000000000000000000000000000000000000000000000000000000066",
  "by_sunset_run_id": "sunset-2026-05-01",
  "at": "2026-05-01T00:00:00Z"
}`
	var e RuleRegistryEntry
	err := json.Unmarshal([]byte(data), &e)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRegistryRestoredInvalidTransition)
}

func TestRegistry_Restored_Accept_ArchivedToDeprecated(t *testing.T) {
	data := `{
  "kind": "restored",
  "schema_version": "1",
  "rule_id": "r-0001",
  "prev_status": "archived",
  "new_status": "deprecated",
  "op_id": "0000000000000000000000000000000000000000000000000000000000000060",
  "version_seq": 6,
  "prev_hash": "0000000000000000000000000000000000000000000000000000000000000066",
  "by_sunset_run_id": "sunset-2026-05-01",
  "at": "2026-05-01T00:00:00Z"
}`
	var e RuleRegistryEntry
	require.NoError(t, json.Unmarshal([]byte(data), &e))
}

func TestRegistry_Validate_AcceptsValueAndPointerVariants(t *testing.T) {
	fixtures := map[string]string{
		"added": fixtureRegistryAdded(),
		"updated": `{
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
		"rolled_back": `{
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
		"status_changed": `{
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
		"archived": `{
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
		"restored": `{
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
	parse := func(raw string) RuleRegistryEntry {
		var e RuleRegistryEntry
		require.NoError(t, json.Unmarshal([]byte(raw), &e))
		return e
	}

	added := parse(fixtures["added"])
	updated := parse(fixtures["updated"])
	rolledBack := parse(fixtures["rolled_back"])
	statusChanged := parse(fixtures["status_changed"])
	archived := parse(fixtures["archived"])
	restored := parse(fixtures["restored"])

	tests := []struct {
		name  string
		entry RuleRegistryEntry
	}{
		{name: "added value", entry: added},
		{name: "added pointer", entry: func() RuleRegistryEntry {
			v := added.Value.(RuleRegistryAdded)
			return RuleRegistryEntry{Kind: added.Kind, Value: &v}
		}()},
		{name: "updated value", entry: updated},
		{name: "updated pointer", entry: func() RuleRegistryEntry {
			v := updated.Value.(RuleRegistryUpdated)
			return RuleRegistryEntry{Kind: updated.Kind, Value: &v}
		}()},
		{name: "rolled_back value", entry: rolledBack},
		{name: "rolled_back pointer", entry: func() RuleRegistryEntry {
			v := rolledBack.Value.(RuleRegistryRolledBack)
			return RuleRegistryEntry{Kind: rolledBack.Kind, Value: &v}
		}()},
		{name: "status_changed value", entry: statusChanged},
		{name: "status_changed pointer", entry: func() RuleRegistryEntry {
			v := statusChanged.Value.(RuleRegistryStatusChanged)
			return RuleRegistryEntry{Kind: statusChanged.Kind, Value: &v}
		}()},
		{name: "archived value", entry: archived},
		{name: "archived pointer", entry: func() RuleRegistryEntry {
			v := archived.Value.(RuleRegistryArchived)
			return RuleRegistryEntry{Kind: archived.Kind, Value: &v}
		}()},
		{name: "restored value", entry: restored},
		{name: "restored pointer", entry: func() RuleRegistryEntry {
			v := restored.Value.(RuleRegistryRestored)
			return RuleRegistryEntry{Kind: restored.Kind, Value: &v}
		}()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NoError(t, tt.entry.Validate())
		})
	}
}
