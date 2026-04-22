package step40_classify

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBestDuplicateMatch_IsDeterministicOnTies covers L4. The pre-fix code
// iterated the activeRuleBodies map directly, so tied scores picked a
// non-deterministic rule_id. After the fix, ties are broken by
// lexicographic rule_id order.
func TestBestDuplicateMatch_IsDeterministicOnTies(t *testing.T) {
	// Two identical bodies — tokenSetSimilarity scores identically for any
	// candidate body against either of them.
	identical := "## Problem\nshared problem description\n## Rationale\nshared rationale text\n"
	activeBodies := map[string]string{
		"rule-zulu":    identical,
		"rule-alpha":   identical,
		"rule-mike":    identical,
		"rule-bravo":   identical,
		"rule-charlie": identical,
	}
	// Candidate that matches all bodies equally (because it IS one of them).
	candidateBody := identical

	// Confirm that across many Go map iteration orderings, the resolver
	// always picks the same winning rule_id.
	seen := map[string]int{}
	for i := 0; i < 256; i++ {
		ruleID, score := bestDuplicateMatch(candidateBody, activeBodies)
		require.NotZero(t, score)
		seen[ruleID]++
	}
	require.Len(t, seen, 1, "tie-break must be deterministic across iterations; saw: %v", seen)
	// And the winner must be the lexicographically smallest rule_id.
	sortedIDs := make([]string, 0, len(activeBodies))
	for id := range activeBodies {
		sortedIDs = append(sortedIDs, id)
	}
	sort.Strings(sortedIDs)
	for winner := range seen {
		assert.Equal(t, sortedIDs[0], winner,
			"tie-break must prefer the lexicographically smallest rule_id")
	}
}

// TestBestDuplicateMatch_PrefersHigherScoreOverIDOrder ensures the tiebreak
// doesn't override a legitimate winner.
func TestBestDuplicateMatch_PrefersHigherScoreOverIDOrder(t *testing.T) {
	candidate := "## Problem\nshared distinct token set here zebra zeta\n"
	activeBodies := map[string]string{
		"rule-alpha": "## Problem\ncompletely different content alpha beta gamma\n",
		"rule-zulu":  "## Problem\nshared distinct token set here zebra zeta\n",
	}
	ruleID, _ := bestDuplicateMatch(candidate, activeBodies)
	assert.Equal(t, "rule-zulu", ruleID, "higher-similarity rule must win regardless of id order")
}
