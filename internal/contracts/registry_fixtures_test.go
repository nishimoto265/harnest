package contracts

import ()

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
