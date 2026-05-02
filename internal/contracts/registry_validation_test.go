package contracts

import (
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
