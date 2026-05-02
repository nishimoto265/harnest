package contracts

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ChecklistResult: 3-symbol verdict.
func TestChecklistResult_Valid(t *testing.T) {
	cr := ChecklistResult{
		SchemaVersion: "1",
		RunID:         "2026-04-20-PR42-abcdef0",
		Pass:          1,
		Agent:         "a1",
		Items: []ChecklistItem{
			{RuleID: "r-1", Verdict: ChecklistItemCompliant},
			{RuleID: "r-2", Verdict: ChecklistItemNA},
			{RuleID: "r-3", Verdict: ChecklistItemException, Rationale: "ok", ExceptionReason: "because"},
		},
	}
	assert.NoError(t, cr.Validate())
}

func TestChecklistResult_Reject_BadVerdict(t *testing.T) {
	cr := ChecklistResult{
		SchemaVersion: "1",
		RunID:         "2026-04-20-PR42-abcdef0",
		Pass:          1,
		Agent:         "a1",
		Items:         []ChecklistItem{{RuleID: "r-1", Verdict: "wrong"}},
	}
	assert.Error(t, cr.Validate())
}

func TestChecklistResult_Reject_ExceptionWithoutRationale(t *testing.T) {
	cr := ChecklistResult{
		SchemaVersion: "1",
		RunID:         "2026-04-20-PR42-abcdef0",
		Pass:          1,
		Agent:         "a1",
		Items:         []ChecklistItem{{RuleID: "r-1", Verdict: ChecklistItemException, Rationale: "   "}},
	}
	err := cr.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrChecklistExceptionRationaleRequired)
}

func TestChecklistResult_UnmarshalBackfillsExceptionRationale(t *testing.T) {
	data := []byte(`{"schema_version":"1","run_id":"2026-04-20-PR42-abcdef0","pass":1,"agent":"a1","items":[{"rule_id":"r-1","verdict":"exception","exception_reason":"Next.js requires this exception."}]}`)

	var cr ChecklistResult
	require.NoError(t, cr.UnmarshalJSON(data))

	require.Len(t, cr.Items, 1)
	assert.Equal(t, "Next.js requires this exception.", cr.Items[0].Rationale)
	assert.Equal(t, "Next.js requires this exception.", cr.Items[0].ExceptionReason)
}
