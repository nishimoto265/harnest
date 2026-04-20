package contracts

import (
	"encoding/json"
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
