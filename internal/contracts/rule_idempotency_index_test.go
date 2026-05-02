package contracts

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
