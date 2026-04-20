package contracts

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type duplicateEmitter struct{}

func (duplicateEmitter) MarshalJSON() ([]byte, error) {
	return []byte(`{"x":1,"x":2}`), nil
}

type floatEmitter struct{}

func (floatEmitter) MarshalJSON() ([]byte, error) {
	return []byte(`{"score":1.5}`), nil
}

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

func TestCanonicalMarshal_RejectsForbiddenKindsInAnonymousEmbeddedStructs(t *testing.T) {
	type embeddedUnexported struct {
		Score float64 `json:"score"`
	}
	type EmbeddedExported struct {
		Score float64 `json:"score"`
	}
	type withEmbeddedUnexported struct {
		embeddedUnexported
	}
	type withEmbeddedExported struct {
		EmbeddedExported
	}

	_, err := CanonicalMarshal(withEmbeddedUnexported{
		embeddedUnexported: embeddedUnexported{Score: 1.0},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCanonicalForbiddenKind)

	_, err = CanonicalMarshal(withEmbeddedExported{
		EmbeddedExported: EmbeddedExported{Score: 1.0},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCanonicalForbiddenKind)
}

func TestCanonicalMarshal_IgnoresConflictingEmbeddedTaggedFieldsThatJSONDrops(t *testing.T) {
	embeddedAType := reflect.StructOf([]reflect.StructField{{
		Name: "Score",
		Type: reflect.TypeOf(float64(0)),
		Tag:  `json:"score"`,
	}})
	embeddedBType := reflect.StructOf([]reflect.StructField{{
		Name: "Value",
		Type: reflect.TypeOf(int(0)),
		Tag:  `json:"score"`,
	}})
	payloadType := reflect.StructOf([]reflect.StructField{
		{Name: "EmbeddedA", Type: embeddedAType, Anonymous: true},
		{Name: "EmbeddedB", Type: embeddedBType, Anonymous: true},
	})
	v := reflect.New(payloadType).Elem()
	v.Field(0).Field(0).SetFloat(1.5)
	v.Field(1).Field(0).SetInt(2)

	raw, err := json.Marshal(v.Interface())
	require.NoError(t, err)
	assert.Equal(t, `{}`, string(raw))

	canonical, err := CanonicalMarshal(v.Interface())
	require.NoError(t, err)
	assert.Equal(t, `{}`, string(canonical))
}

func TestCanonicalMarshal_UsesTaggedFieldOverConflictingUntaggedEmbeddedField(t *testing.T) {
	type Tagged struct {
		Score int `json:"Score"`
	}
	type Untagged struct {
		Score float64
	}
	type payload struct {
		Tagged
		Untagged
	}

	v := payload{
		Tagged:   Tagged{Score: 2},
		Untagged: Untagged{Score: 1.5},
	}

	raw, err := json.Marshal(v)
	require.NoError(t, err)
	assert.Equal(t, `{"Score":2}`, string(raw))

	canonical, err := CanonicalMarshal(v)
	require.NoError(t, err)
	assert.Equal(t, `{"Score":2}`, string(canonical))
}

func TestCanonicalMarshal_UsesTaggedOverrideInsteadOfHiddenPromotedEmbeddedField(t *testing.T) {
	embeddedType := reflect.StructOf([]reflect.StructField{{
		Name: "Score",
		Type: reflect.TypeOf(float64(0)),
		Tag:  `json:"score"`,
	}})
	payloadType := reflect.StructOf([]reflect.StructField{
		{Name: "Embedded", Type: embeddedType, Anonymous: true},
		{Name: "Score", Type: reflect.TypeOf(int(0)), Tag: `json:"score"`},
	})
	v := reflect.New(payloadType).Elem()
	v.Field(0).Field(0).SetFloat(1.5)
	v.Field(1).SetInt(2)

	raw, err := json.Marshal(v.Interface())
	require.NoError(t, err)
	assert.Equal(t, `{"score":2}`, string(raw))

	canonical, err := CanonicalMarshal(v.Interface())
	require.NoError(t, err)
	assert.Equal(t, `{"score":2}`, string(canonical))
}

func TestCanonicalMarshal_RejectsActiveDeeplyNestedPromotedEmbeddedField(t *testing.T) {
	type Level1 struct {
		Score float64 `json:"score"`
	}
	type Level2 struct {
		Level1
	}
	type payload struct {
		Level2
	}

	v := payload{
		Level2: Level2{Level1: Level1{Score: 1.5}},
	}

	raw, err := json.Marshal(v)
	require.NoError(t, err)
	assert.Equal(t, `{"score":1.5}`, string(raw))

	_, err = CanonicalMarshal(v)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCanonicalForbiddenKind)
}

func TestCanonicalMarshal_SkipsUnexportedNonEmbeddedStructFields(t *testing.T) {
	type hiddenFloat struct {
		Score float64 `json:"score"`
	}
	type payload struct {
		hidden hiddenFloat
	}

	data, err := CanonicalMarshal(payload{
		hidden: hiddenFloat{Score: 1.0},
	})
	require.NoError(t, err)
	assert.Equal(t, `{}`, string(data))
}

func TestCanonicalMarshal_RespectsOmitEmptyBeforeForbiddenKindCheck(t *testing.T) {
	type payload struct {
		F float64 `json:"f,omitempty"`
	}

	data, err := CanonicalMarshal(payload{})
	require.NoError(t, err)
	assert.Equal(t, `{}`, string(data))

	_, err = CanonicalMarshal(payload{F: 1.5})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCanonicalForbiddenKind)
}

func TestCanonicalMarshal_AllowsDistinctSlicesSharingBackingArray(t *testing.T) {
	backing := []int{1, 2, 3}
	value := struct {
		A []int `json:"a"`
		B []int `json:"b"`
	}{
		A: backing[:2],
		B: backing[:3],
	}

	data, err := CanonicalMarshal(value)
	require.NoError(t, err)
	assert.Equal(t, `{"a":[1,2],"b":[1,2,3]}`, string(data))
}

func TestCanonicalCandidatesHash_NormalizesNilAndEmptySlices(t *testing.T) {
	assert.Equal(t, CanonicalCandidatesHash(nil), CanonicalCandidatesHash([]Candidate{}))
}

func TestCanonicalMarshal_RejectsDuplicateKeysFromRawMessage(t *testing.T) {
	type payload struct {
		Raw json.RawMessage `json:"raw"`
	}

	_, err := CanonicalMarshal(payload{
		Raw: json.RawMessage(`{"x":1,"x":2}`),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDuplicateJSONKey)
}

func TestCanonicalMarshal_RejectsDuplicateKeysFromCustomMarshalJSON(t *testing.T) {
	type payload struct {
		Value duplicateEmitter `json:"value"`
	}

	_, err := CanonicalMarshal(payload{Value: duplicateEmitter{}})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDuplicateJSONKey)
}

func TestCanonicalMarshal_RejectsForbiddenKindsFromCustomMarshalJSON(t *testing.T) {
	type payload struct {
		Value floatEmitter `json:"value"`
	}

	_, err := CanonicalMarshal(payload{Value: floatEmitter{}})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCanonicalNonInteger)
}

func TestCanonicalMarshal_RejectsPointerCycles(t *testing.T) {
	type node struct {
		Next *node `json:"next"`
	}

	n := &node{}
	n.Next = n

	_, err := CanonicalMarshal(n)
	require.Error(t, err)

	var unsupported *json.UnsupportedValueError
	require.ErrorAs(t, err, &unsupported)
}

func TestCanonicalMarshal_RejectsMapCycles(t *testing.T) {
	m := map[string]any{}
	m["self"] = m

	_, err := CanonicalMarshal(m)
	require.Error(t, err)

	var unsupported *json.UnsupportedValueError
	require.ErrorAs(t, err, &unsupported)
}

func TestCanonicalMarshal_RejectsSliceCycles(t *testing.T) {
	s := make([]any, 1)
	s[0] = &s

	_, err := CanonicalMarshal(s)
	require.Error(t, err)

	var unsupported *json.UnsupportedValueError
	require.ErrorAs(t, err, &unsupported)
}
