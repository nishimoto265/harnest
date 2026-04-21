package contracts

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtureManifestSuccess returns the canonical minimal success JSON.
func fixtureManifestSuccess(t *testing.T) string {
	t.Helper()
	return `{
  "kind": "success",
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "pass": 1,
  "agent": "a1",
  "branch_name": "auto-improve/run-2026-04-20-PR42-abcdef0-pass1-a1",
  "head_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "base_sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "diff_path": "20-pass1/a1/diff.patch",
  "session_path": "20-pass1/a1/session.jsonl",
  "checklist_path": "20-pass1/a1/checklist-result.json",
  "prompt_version": "v1",
  "started_at": "2026-04-20T10:00:00Z",
  "finished_at": "2026-04-20T10:12:34Z"
}`
}

func TestManifest_Success_Parse(t *testing.T) {
	var m Manifest
	require.NoError(t, json.Unmarshal([]byte(fixtureManifestSuccess(t)), &m))
	assert.Equal(t, ManifestKindSuccess, m.Kind)
	v, ok := m.Value.(ManifestSuccess)
	require.True(t, ok)
	assert.EqualValues(t, "a1", v.Agent)
	assert.Equal(t, 1, v.Pass)
}

func TestManifest_Error_Parse(t *testing.T) {
	data := `{
  "kind": "error",
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "pass": 1,
  "agent": "a2",
  "exit_code": 1,
  "reason": "rate_limit",
  "started_at": "2026-04-20T10:00:00Z",
  "finished_at": "2026-04-20T10:01:00Z"
}`
	var m Manifest
	require.NoError(t, json.Unmarshal([]byte(data), &m))
	assert.Equal(t, ManifestKindError, m.Kind)
	_, ok := m.Value.(ManifestError)
	assert.True(t, ok)
}

func TestManifest_Error_Parse_AcceptsZeroExitCode(t *testing.T) {
	data := `{
  "kind": "error",
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "pass": 1,
  "agent": "a2",
  "exit_code": 0,
  "reason": "rate_limit",
  "started_at": "2026-04-20T10:00:00Z",
  "finished_at": "2026-04-20T10:01:00Z"
}`
	var m Manifest
	require.NoError(t, json.Unmarshal([]byte(data), &m))
	v, ok := m.Value.(ManifestError)
	require.True(t, ok)
	assert.Equal(t, 0, v.ExitCode)
}

func TestManifest_Timeout_Parse(t *testing.T) {
	data := `{
  "kind": "timeout",
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "pass": 2,
  "agent": "a3",
  "timeout_seconds": 3600,
  "started_at": "2026-04-20T10:00:00Z",
  "finished_at": "2026-04-20T11:00:00Z"
}`
	var m Manifest
	require.NoError(t, json.Unmarshal([]byte(data), &m))
	assert.Equal(t, ManifestKindTimeout, m.Kind)
}

// 失敗系 6 ケース (io-contracts.md §4 strict JSON read).
func TestManifest_Reject_UnknownTopLevelKey(t *testing.T) {
	data := strings.Replace(fixtureManifestSuccess(t), `"agent": "a1",`, `"agent": "a1","unknown_field": 1,`, 1)
	var m Manifest
	err := json.Unmarshal([]byte(data), &m)
	assert.Error(t, err, "unknown top-level key must be rejected by DisallowUnknownFields")
}

func TestManifest_Reject_UnknownVariantLevelKey(t *testing.T) {
	// variant-level = ManifestSuccess field after kind peek。
	// 同じく DisallowUnknownFields で弾かれる。
	data := strings.Replace(fixtureManifestSuccess(t), `"base_sha"`, `"foo": 1,"base_sha"`, 1)
	var m Manifest
	err := json.Unmarshal([]byte(data), &m)
	assert.Error(t, err)
}

func TestManifest_Reject_MissingRequired(t *testing.T) {
	// agent field を削除 → validator が required で reject
	data := strings.Replace(fixtureManifestSuccess(t), `"agent": "a1",`, ``, 1)
	var m Manifest
	err := json.Unmarshal([]byte(data), &m)
	assert.Error(t, err)
}

func TestManifest_Error_RejectsMissingExitCodeField(t *testing.T) {
	data := `{
  "kind": "error",
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "pass": 1,
  "agent": "a2",
  "reason": "rate_limit",
  "started_at": "2026-04-20T10:00:00Z",
  "finished_at": "2026-04-20T10:01:00Z"
}`
	var m Manifest
	err := json.Unmarshal([]byte(data), &m)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrManifestErrorMissingExitCode)
}

func TestManifestError_UnmarshalJSON_RejectsFinishedBeforeStarted(t *testing.T) {
	data := []byte(`{
  "kind": "error",
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "pass": 1,
  "agent": "a2",
  "exit_code": 1,
  "reason": "unknown",
  "started_at": "2026-04-20T10:01:00Z",
  "finished_at": "2026-04-20T10:00:00Z"
}`)
	var m ManifestError
	err := json.Unmarshal(data, &m)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrManifestFinishedBeforeStarted)
}

func TestManifest_Reject_WrongKind(t *testing.T) {
	data := `{"kind": "bogus_variant"}`
	var m Manifest
	err := json.Unmarshal([]byte(data), &m)
	assert.ErrorIs(t, err, ErrUnknownManifestKind)
}

func TestManifest_Reject_TrailingJSON(t *testing.T) {
	data := fixtureManifestSuccess(t) + `{"extra": true}`
	var m Manifest
	err := json.Unmarshal([]byte(data), &m)
	assert.Error(t, err)
	// json.Unmarshal does not return ErrTrailingJSON directly because it uses
	// the plain Unmarshal path; but our internal decodeStrict via UnmarshalJSON
	// returns ErrTrailingJSON. Confirm via decodeStrict directly.
	// ただし json.Unmarshal 自身は trailing に厳しいのでその場でエラーになる。
}

func TestManifest_Reject_TrailingBytes(t *testing.T) {
	// JSON 値の後に non-JSON bytes が続いた場合
	data := fixtureManifestSuccess(t) + "garbage"
	var m Manifest
	err := json.Unmarshal([]byte(data), &m)
	assert.Error(t, err)
}

func TestManifest_decodeStrict_TrailingJSON(t *testing.T) {
	// 本ライブラリの内部 helper が ErrTrailingJSON を返すことを確認。
	data := []byte(`{"x":1}{"y":2}`)
	var v struct {
		X int `json:"x"`
	}
	err := decodeStrict(data, &v)
	assert.True(t, errors.Is(err, ErrTrailingJSON))
}

func TestManifest_Validate_RejectsTaggedUnionMismatches(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*Manifest)
		expected error
	}{
		{
			name: "success outer kind mismatch",
			mutate: func(m *Manifest) {
				m.Kind = ManifestKindError
			},
			expected: ErrManifestVariantTypeMismatch,
		},
		{
			name: "success inner kind mismatch",
			mutate: func(m *Manifest) {
				v := m.Value.(ManifestSuccess)
				v.Kind = ManifestKindError
				m.Value = v
			},
			expected: ErrManifestVariantKindMismatch,
		},
		{
			name: "error outer kind mismatch",
			mutate: func(m *Manifest) {
				m.Kind = ManifestKindTimeout
			},
			expected: ErrManifestVariantTypeMismatch,
		},
		{
			name: "error inner kind mismatch",
			mutate: func(m *Manifest) {
				v := m.Value.(ManifestError)
				v.Kind = ManifestKindTimeout
				m.Value = v
			},
			expected: ErrManifestVariantKindMismatch,
		},
		{
			name: "timeout outer kind mismatch",
			mutate: func(m *Manifest) {
				m.Kind = ManifestKindSuccess
			},
			expected: ErrManifestVariantTypeMismatch,
		},
		{
			name: "timeout inner kind mismatch",
			mutate: func(m *Manifest) {
				v := m.Value.(ManifestTimeout)
				v.Kind = ManifestKindSuccess
				m.Value = v
			},
			expected: ErrManifestVariantKindMismatch,
		},
	}

	valid := func(data string) Manifest {
		var m Manifest
		require.NoError(t, json.Unmarshal([]byte(data), &m))
		return m
	}
	manifests := map[string]Manifest{
		"success": valid(fixtureManifestSuccess(t)),
		"error": valid(`{
  "kind": "error",
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "pass": 1,
  "agent": "a2",
  "exit_code": 1,
  "reason": "rate_limit",
  "started_at": "2026-04-20T10:00:00Z",
  "finished_at": "2026-04-20T10:01:00Z"
}`),
		"timeout": valid(`{
  "kind": "timeout",
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "pass": 2,
  "agent": "a3",
  "timeout_seconds": 3600,
  "started_at": "2026-04-20T10:00:00Z",
  "finished_at": "2026-04-20T11:00:00Z"
}`),
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m Manifest
			switch {
			case strings.Contains(tt.name, "success"):
				m = manifests["success"]
			case strings.Contains(tt.name, "error"):
				m = manifests["error"]
			default:
				m = manifests["timeout"]
			}
			tt.mutate(&m)
			err := m.Validate()
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.expected)
		})
	}
}

func TestManifest_Validate_AcceptsValueAndPointerVariants(t *testing.T) {
	success := func() Manifest {
		var m Manifest
		require.NoError(t, json.Unmarshal([]byte(fixtureManifestSuccess(t)), &m))
		return m
	}()
	errorManifest := func() Manifest {
		var m Manifest
		require.NoError(t, json.Unmarshal([]byte(`{
  "kind": "error",
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "pass": 1,
  "agent": "a2",
  "exit_code": 1,
  "reason": "rate_limit",
  "started_at": "2026-04-20T10:00:00Z",
  "finished_at": "2026-04-20T10:01:00Z"
}`), &m))
		return m
	}()
	timeout := func() Manifest {
		var m Manifest
		require.NoError(t, json.Unmarshal([]byte(`{
  "kind": "timeout",
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "pass": 2,
  "agent": "a3",
  "timeout_seconds": 3600,
  "started_at": "2026-04-20T10:00:00Z",
  "finished_at": "2026-04-20T11:00:00Z"
}`), &m))
		return m
	}()

	tests := []struct {
		name string
		m    Manifest
	}{
		{name: "success value", m: success},
		{name: "success pointer", m: func() Manifest {
			v := success.Value.(ManifestSuccess)
			return Manifest{Kind: success.Kind, Value: &v}
		}()},
		{name: "error value", m: errorManifest},
		{name: "error pointer", m: func() Manifest {
			v := errorManifest.Value.(ManifestError)
			return Manifest{Kind: errorManifest.Kind, Value: &v}
		}()},
		{name: "timeout value", m: timeout},
		{name: "timeout pointer", m: func() Manifest {
			v := timeout.Value.(ManifestTimeout)
			return Manifest{Kind: timeout.Kind, Value: &v}
		}()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NoError(t, tt.m.Validate())
		})
	}
}

func TestManifestSuccess_Validate_RejectsAbsoluteArtifactPath(t *testing.T) {
	var m Manifest
	require.NoError(t, json.Unmarshal([]byte(fixtureManifestSuccess(t)), &m))
	v := m.Value.(ManifestSuccess)
	v.DiffPath = "/tmp/diff.patch"
	m.Value = v

	err := m.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPathRelativeAbsolute)
}

func TestManifestSuccess_Validate_RejectsPassAgentPrefixMismatch(t *testing.T) {
	var m Manifest
	require.NoError(t, json.Unmarshal([]byte(fixtureManifestSuccess(t)), &m))
	v := m.Value.(ManifestSuccess)
	v.Pass = 2
	m.Value = v

	err := m.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrManifestArtifactPathPrefixMismatch)
}

func TestManifestSuccess_Validate_RejectsArtifactPathEqualToPrefix(t *testing.T) {
	var m ManifestSuccess
	require.NoError(t, json.Unmarshal([]byte(fixtureManifestSuccess(t)), &m))
	m.DiffPath = "20-pass1/a1"

	err := m.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrManifestArtifactPathPrefixMismatch)
}

func TestManifest_Validate_RejectsFinishedBeforeStarted(t *testing.T) {
	var m Manifest
	require.NoError(t, json.Unmarshal([]byte(fixtureManifestSuccess(t)), &m))
	v := m.Value.(ManifestSuccess)
	v.StartedAt = time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	v.FinishedAt = time.Date(2026, 4, 20, 11, 59, 59, 0, time.UTC)
	m.Value = v

	err := m.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrManifestFinishedBeforeStarted)
}

func TestManifest_MarshalJSON_RejectsVariantMismatch(t *testing.T) {
	now := time.Now()
	m := Manifest{
		Kind: ManifestKindSuccess,
		Value: ManifestError{
			Kind:          ManifestKindError,
			SchemaVersion: "1",
			RunID:         "2026-04-20-PR42-abcdef0",
			Pass:          1,
			Agent:         "a2",
			ExitCode:      1,
			Reason:        "rate_limit",
			StartedAt:     now,
			FinishedAt:    now,
		},
	}

	_, err := json.Marshal(m)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrManifestVariantTypeMismatch)
}
