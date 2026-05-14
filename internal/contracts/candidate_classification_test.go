package contracts

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/validation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Candidates schema roundtrip: JSON marshal → strict decode.
func TestCandidates_Roundtrip(t *testing.T) {
	items := []Candidate{{
		CandidateID:        "c1",
		Kind:               CandidateKindNew,
		Title:              "Prefer clarity",
		ProposedBodyPath:   "40/candidates/c1.md",
		ProposedBodySha256: "0000000000000000000000000000000000000000000000000000000000000001",
	}}
	data := []byte(fmt.Sprintf(`{
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "candidates": [
    {
      "candidate_id": "c1",
      "kind": "new",
      "title": "Prefer clarity",
      "proposed_body_path": "40/candidates/c1.md",
      "proposed_body_sha256": "0000000000000000000000000000000000000000000000000000000000000001"
    }
  ],
  "candidates_hash": %q,
  "created_at": "2026-04-20T13:00:00Z"
}`, CanonicalCandidatesHash(items)))
	var c Candidates
	require.NoError(t, json.Unmarshal(data, &c))
	require.NoError(t, validation.Instance().Struct(c))
	assert.Len(t, c.Candidates, 1)
	assert.Equal(t, CandidateKindNew, c.Candidates[0].Kind)
}

func TestCandidates_MarshalJSON_NormalizesNilSliceToEmptyArray(t *testing.T) {
	c := Candidates{
		SchemaVersion:  "1",
		RunID:          "2026-04-20-PR42-abcdef0",
		Candidates:     nil,
		CandidatesHash: CanonicalCandidatesHash(nil),
		CreatedAt:      time.Now(),
	}
	data, err := json.Marshal(c)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"candidates":[]`)
}

func TestClassificationEntry_Valid(t *testing.T) {
	e := ClassificationEntry{
		SchemaVersion:   "1",
		RunID:           "2026-04-20-PR42-abcdef0",
		CandidateID:     "c1",
		Kind:            CandidateKindUpdate,
		SimilarityScore: 80,
		MatchedRuleID:   "r-1",
		ClassifiedAt:    time.Now(),
	}
	assert.NoError(t, validation.Instance().Struct(e))
}

func TestClassificationEntry_UnmarshalJSON_RejectsMissingSimilarityScore(t *testing.T) {
	data := []byte(`{
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "candidate_id": "c1",
  "kind": "update",
  "classified_at": "2026-04-20T12:00:00Z"
}`)
	var e ClassificationEntry
	err := json.Unmarshal(data, &e)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrClassificationEntryMissingSimilarityScore)
}

func TestClassificationEntry_UnmarshalJSON_AcceptsZeroSimilarityScore(t *testing.T) {
	data := []byte(`{
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "candidate_id": "c1",
  "kind": "update",
  "similarity_score": 0,
  "classified_at": "2026-04-20T12:00:00Z"
}`)
	var e ClassificationEntry
	require.NoError(t, json.Unmarshal(data, &e))
	assert.Equal(t, 0, e.SimilarityScore)
}

func TestClassificationEntry_UnmarshalJSON_RejectsOutOfRangeSimilarityScore(t *testing.T) {
	tests := []struct {
		name  string
		score int
	}{
		{name: "too high", score: 101},
		{name: "negative", score: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte(fmt.Sprintf(`{
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "candidate_id": "c1",
  "kind": "update",
  "similarity_score": %d,
  "classified_at": "2026-04-20T12:00:00Z"
}`, tt.score))
			var e ClassificationEntry
			assert.Error(t, json.Unmarshal(data, &e))
		})
	}
}
