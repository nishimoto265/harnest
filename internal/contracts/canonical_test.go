package contracts

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalMarshal_FieldOrderInvariant(t *testing.T) {
	type orderedA struct {
		Z string `json:"z"`
		A int    `json:"a"`
	}
	type orderedB struct {
		A int    `json:"a"`
		Z string `json:"z"`
	}

	a, err := CanonicalMarshal(orderedA{Z: "last", A: 1})
	require.NoError(t, err)
	b, err := CanonicalMarshal(orderedB{A: 1, Z: "last"})
	require.NoError(t, err)

	assert.Equal(t, string(a), string(b))
}

func TestCanonicalMarshal_NestedObjectOrderInvariant(t *testing.T) {
	left := map[string]any{
		"outer": map[string]any{
			"z": 1,
			"a": map[string]any{"y": true, "x": "v"},
		},
	}
	right := map[string]any{
		"outer": map[string]any{
			"a": map[string]any{"x": "v", "y": true},
			"z": 1,
		},
	}

	l, err := CanonicalMarshal(left)
	require.NoError(t, err)
	r, err := CanonicalMarshal(right)
	require.NoError(t, err)

	assert.Equal(t, string(l), string(r))
}

func TestCanonicalCandidatesHash_HTMLStringsInvariantAcrossEscapeForms(t *testing.T) {
	rawEscaped := []byte(`[
  {
    "candidate_id": "c1",
    "kind": "new",
    "title": "use \u003ctag\u003e",
    "proposed_body_path": "40/candidates/c1.md",
    "proposed_body_sha256": "0000000000000000000000000000000000000000000000000000000000000001"
  }
]`)
	rawLiteral := []byte(`[
  {
    "candidate_id": "c1",
    "kind": "new",
    "title": "use <tag>",
    "proposed_body_path": "40/candidates/c1.md",
    "proposed_body_sha256": "0000000000000000000000000000000000000000000000000000000000000001"
  }
]`)

	var escaped []Candidate
	require.NoError(t, json.Unmarshal(rawEscaped, &escaped))
	var literal []Candidate
	require.NoError(t, json.Unmarshal(rawLiteral, &literal))

	assert.Equal(t, CanonicalCandidatesHash(escaped), CanonicalCandidatesHash(literal))
}

func TestCanonicalMarshal_RejectsNonIntegerNumbers(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{name: "decimal", value: json.Number("1.0")},
		{name: "exponent", value: json.Number("1e0")},
		{name: "fraction", value: json.Number("3.14")},
		{name: "int64 overflow positive", value: json.Number("9223372036854775808")},
		{name: "int64 overflow negative", value: json.Number("-9223372036854775809")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CanonicalMarshal(tt.value)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrCanonicalNonInteger)
		})
	}
}

func TestCanonicalMarshal_RejectsNaNAndInfinity(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{name: "NaN", value: math.NaN()},
		{name: "positive infinity", value: math.Inf(1)},
		{name: "negative infinity", value: math.Inf(-1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CanonicalMarshal(tt.value)
			require.Error(t, err)
		})
	}
}

func TestCanonicalMarshal_RejectsForbiddenNumericKindsBeforeJSONEncoding(t *testing.T) {
	type nested struct {
		Values []any `json:"values"`
	}
	type payload struct {
		Name   string `json:"name"`
		Amount any    `json:"amount"`
		Nested nested `json:"nested"`
	}
	type onlyIntegers struct {
		Int   int   `json:"int"`
		Int64 int64 `json:"int64"`
	}

	tests := []struct {
		name  string
		value any
	}{
		{
			name: "float64 field",
			value: struct {
				Value float64 `json:"value"`
			}{Value: 1.0},
		},
		{
			name: "float32 field",
			value: struct {
				Value float32 `json:"value"`
			}{Value: 0},
		},
		{
			name: "nested float64 in interface tree",
			value: payload{
				Name:   "example",
				Amount: 1,
				Nested: nested{Values: []any{map[string]any{"score": 1.0}}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CanonicalMarshal(tt.value)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrCanonicalForbiddenKind)
		})
	}

	_, err := CanonicalMarshal(map[string]any{"score": json.Number("1.0")})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCanonicalNonInteger)

	data, err := CanonicalMarshal(onlyIntegers{Int: 1, Int64: 2})
	require.NoError(t, err)
	assert.Equal(t, `{"int":1,"int64":2}`, string(data))
}

func TestCanonicalMarshal_NormalizesNegativeZero(t *testing.T) {
	data, err := CanonicalMarshal(json.Number("-0"))
	require.NoError(t, err)
	assert.Equal(t, "0", string(data))
}
