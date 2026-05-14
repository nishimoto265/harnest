package stepio

import (
	"errors"
	"testing"

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/stretchr/testify/assert"
)

// ErrTrailingJSON / ErrUnknown* are re-exports of contracts.* — confirm the
// identity so that `errors.Is` works across package boundaries.
func TestErrors_Reexport_Identity(t *testing.T) {
	assert.True(t, errors.Is(ErrTrailingJSON, contracts.ErrTrailingJSON))
	assert.True(t, errors.Is(ErrUnknownManifestKind, contracts.ErrUnknownManifestKind))
	assert.True(t, errors.Is(ErrUnknownDecisionAction, contracts.ErrUnknownDecisionAction))
	assert.True(t, errors.Is(ErrUnknownRegistryKind, contracts.ErrUnknownRegistryKind))
	assert.True(t, errors.Is(ErrUnknownStateKind, contracts.ErrUnknownStateKind))
	assert.True(t, errors.Is(ErrUnknownCandidateKind, contracts.ErrUnknownCandidateKind))
}

// Sentinels must all be distinct from one another.
func TestErrors_Distinct(t *testing.T) {
	sentinels := []error{
		ErrAgentTimeout,
		ErrAllAgentsFailed,
		ErrScoringFailed,
		ErrNoCandidates,
		ErrPromotionFailed,
		ErrBestBranchDiverged,
		ErrNotScorable,
		ErrEntryTooLarge,
	}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i == j {
				continue
			}
			assert.False(t, errors.Is(a, b), "sentinel %d and %d must be distinct", i, j)
		}
	}
}
