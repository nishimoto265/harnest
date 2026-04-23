package registryview

import (
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuild_RollbackRequiresExactOffsetAndSha(t *testing.T) {
	added := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         "rule-1",
			RulePath:       "rules/rule-1.md",
			Sha256:         strings.Repeat("1", 64),
			IdempotencyKey: strings.Repeat("2", 64),
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        "2026-04-21-PR1-abcdef0",
			At:             time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
		},
	}
	result, _, err := registryAppendResult(added, 0)
	require.NoError(t, err)

	_, err = Build([]contracts.RuleRegistryEntry{
		added,
		{
			Kind: contracts.RegistryKindRolledBack,
			Value: contracts.RuleRegistryRolledBack{
				Kind:           contracts.RegistryKindRolledBack,
				SchemaVersion:  "1",
				TargetOpID:     strings.Repeat("2", 64),
				TargetOffset:   result.Offset + 1,
				TargetSha256:   result.Sha256,
				ByRunID:        "2026-04-21-PR2-bcdef01",
				RollbackReason: contracts.RollbackReasonTransactionalFailure,
				FailedStep:     contracts.FailedStep70,
				VersionSeq:     2,
				PrevHash:       result.Sha256,
				At:             time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC),
			},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rollback target mismatch")
}

func TestBuild_RollbackAppliesWhenOffsetAndShaMatch(t *testing.T) {
	added := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         "rule-1",
			RulePath:       "rules/rule-1.md",
			Sha256:         strings.Repeat("1", 64),
			IdempotencyKey: strings.Repeat("2", 64),
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        "2026-04-21-PR1-abcdef0",
			At:             time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
		},
	}
	result, _, err := registryAppendResult(added, 0)
	require.NoError(t, err)

	states, err := Build([]contracts.RuleRegistryEntry{
		added,
		{
			Kind: contracts.RegistryKindRolledBack,
			Value: contracts.RuleRegistryRolledBack{
				Kind:           contracts.RegistryKindRolledBack,
				SchemaVersion:  "1",
				TargetOpID:     strings.Repeat("2", 64),
				TargetOffset:   result.Offset,
				TargetSha256:   result.Sha256,
				ByRunID:        "2026-04-21-PR2-bcdef01",
				RollbackReason: contracts.RollbackReasonTransactionalFailure,
				FailedStep:     contracts.FailedStep70,
				VersionSeq:     2,
				PrevHash:       result.Sha256,
				At:             time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC),
			},
		},
	})
	require.NoError(t, err)
	assert.Empty(t, Active(states))
}

func TestBuild_RollbackFailsWhenTargetOpIDDoesNotExist(t *testing.T) {
	_, err := Build([]contracts.RuleRegistryEntry{
		{
			Kind: contracts.RegistryKindRolledBack,
			Value: contracts.RuleRegistryRolledBack{
				Kind:           contracts.RegistryKindRolledBack,
				SchemaVersion:  "1",
				TargetOpID:     strings.Repeat("9", 64),
				TargetOffset:   0,
				TargetSha256:   strings.Repeat("8", 64),
				ByRunID:        "2026-04-21-PR3-cdef012",
				RollbackReason: contracts.RollbackReasonTransactionalFailure,
				FailedStep:     contracts.FailedStep70,
				VersionSeq:     1,
				PrevHash:       "",
				At:             time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
			},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "target op_id not found")
}

func TestBuild_UpdateRequiresMatchingPrevSHA256(t *testing.T) {
	_, err := Build([]contracts.RuleRegistryEntry{
		{
			Kind: contracts.RegistryKindAdded,
			Value: contracts.RuleRegistryAdded{
				Kind:           contracts.RegistryKindAdded,
				SchemaVersion:  "1",
				RuleID:         "rule-1",
				RulePath:       "rules/rule-1.md",
				Sha256:         strings.Repeat("1", 64),
				IdempotencyKey: strings.Repeat("2", 64),
				VersionSeq:     1,
				PrevHash:       "",
				ByRunID:        "2026-04-21-PR1-abcdef0",
				At:             time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
			},
		},
		{
			Kind: contracts.RegistryKindUpdated,
			Value: contracts.RuleRegistryUpdated{
				Kind:           contracts.RegistryKindUpdated,
				SchemaVersion:  "1",
				RuleID:         "rule-1",
				RulePath:       "rules/rule-1.md",
				Sha256:         strings.Repeat("3", 64),
				PrevSha256:     strings.Repeat("9", 64),
				IdempotencyKey: strings.Repeat("4", 64),
				VersionSeq:     2,
				PrevHash:       strings.Repeat("5", 64),
				ByRunID:        "2026-04-21-PR2-bcdef01",
				At:             time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC),
			},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prev_sha256 mismatch")
}

// TestBuild_RollbackRejectsTargetStaleAfterStatusChanged exercises F17:
// a promotion (added / updated) that is still the latest *promotion* but
// has had a status_changed applied on top must fail rollback, otherwise
// the status_changed mutation would be silently erased by restoring the
// pre-promotion snapshot.
func TestBuild_RollbackRejectsTargetStaleAfterStatusChanged(t *testing.T) {
	added := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         "rule-1",
			RulePath:       "rules/rule-1.md",
			Sha256:         strings.Repeat("1", 64),
			IdempotencyKey: strings.Repeat("2", 64),
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        "2026-04-21-PR1-abcdef0",
			At:             time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
		},
	}
	addedResult, nextOffset, err := registryAppendResult(added, 0)
	require.NoError(t, err)

	statusChanged := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindStatusChanged,
		Value: contracts.RuleRegistryStatusChanged{
			Kind:          contracts.RegistryKindStatusChanged,
			SchemaVersion: "1",
			RuleID:        "rule-1",
			PrevStatus:    contracts.RuleStatusActive,
			NewStatus:     contracts.RuleStatusDeprecated,
			Transition:    contracts.SunsetTransitionDeprecate,
			OpID:          strings.Repeat("3", 64),
			VersionSeq:    2,
			PrevHash:      addedResult.Sha256,
			BySunsetRunID: "2026-04-21-PR2-bcdef01",
			At:            time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC),
		},
	}
	statusResult, _, err := registryAppendResult(statusChanged, nextOffset)
	require.NoError(t, err)

	_, err = Build([]contracts.RuleRegistryEntry{
		added,
		statusChanged,
		{
			Kind: contracts.RegistryKindRolledBack,
			Value: contracts.RuleRegistryRolledBack{
				Kind:           contracts.RegistryKindRolledBack,
				SchemaVersion:  "1",
				TargetOpID:     strings.Repeat("2", 64),
				TargetOffset:   addedResult.Offset,
				TargetSha256:   addedResult.Sha256,
				ByRunID:        "2026-04-21-PR3-cdef012",
				RollbackReason: contracts.RollbackReasonTransactionalFailure,
				FailedStep:     contracts.FailedStep70,
				VersionSeq:     3,
				PrevHash:       statusResult.Sha256,
				At:             time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
			},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stale")
}

// TestBuild_RollbackRejectsTargetStaleAfterArchived exercises F17 with an
// archive mutation between the promotion and the rollback.
func TestBuild_RollbackRejectsTargetStaleAfterArchived(t *testing.T) {
	added := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         "rule-1",
			RulePath:       "rules/rule-1.md",
			Sha256:         strings.Repeat("1", 64),
			IdempotencyKey: strings.Repeat("2", 64),
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        "2026-04-21-PR1-abcdef0",
			At:             time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
		},
	}
	addedResult, nextOffset, err := registryAppendResult(added, 0)
	require.NoError(t, err)

	archived := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindArchived,
		Value: contracts.RuleRegistryArchived{
			Kind:          contracts.RegistryKindArchived,
			SchemaVersion: "1",
			RuleID:        "rule-1",
			PrevStatus:    contracts.RuleStatusActive,
			NewStatus:     contracts.RuleStatusArchived,
			OpID:          strings.Repeat("4", 64),
			VersionSeq:    2,
			PrevHash:      addedResult.Sha256,
			BySunsetRunID: "2026-04-21-PR2-bcdef01",
			At:            time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC),
		},
	}
	archivedResult, _, err := registryAppendResult(archived, nextOffset)
	require.NoError(t, err)

	_, err = Build([]contracts.RuleRegistryEntry{
		added,
		archived,
		{
			Kind: contracts.RegistryKindRolledBack,
			Value: contracts.RuleRegistryRolledBack{
				Kind:           contracts.RegistryKindRolledBack,
				SchemaVersion:  "1",
				TargetOpID:     strings.Repeat("2", 64),
				TargetOffset:   addedResult.Offset,
				TargetSha256:   addedResult.Sha256,
				ByRunID:        "2026-04-21-PR3-cdef012",
				RollbackReason: contracts.RollbackReasonTransactionalFailure,
				FailedStep:     contracts.FailedStep70,
				VersionSeq:     3,
				PrevHash:       archivedResult.Sha256,
				At:             time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
			},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stale")
}

// TestBuild_RollbackRejectsTargetStaleAfterRestored covers the symmetric
// case where archive+restore occur on top of a promotion — rolling back to
// the original add would drop both later mutations.
func TestBuild_RollbackRejectsTargetStaleAfterRestored(t *testing.T) {
	added := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         "rule-1",
			RulePath:       "rules/rule-1.md",
			Sha256:         strings.Repeat("1", 64),
			IdempotencyKey: strings.Repeat("2", 64),
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        "2026-04-21-PR1-abcdef0",
			At:             time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
		},
	}
	addedResult, nextOffset, err := registryAppendResult(added, 0)
	require.NoError(t, err)

	archived := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindArchived,
		Value: contracts.RuleRegistryArchived{
			Kind:          contracts.RegistryKindArchived,
			SchemaVersion: "1",
			RuleID:        "rule-1",
			PrevStatus:    contracts.RuleStatusActive,
			NewStatus:     contracts.RuleStatusArchived,
			OpID:          strings.Repeat("5", 64),
			VersionSeq:    2,
			PrevHash:      addedResult.Sha256,
			BySunsetRunID: "2026-04-21-PR2-bcdef01",
			At:            time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC),
		},
	}
	archivedResult, afterArchivedOffset, err := registryAppendResult(archived, nextOffset)
	require.NoError(t, err)

	restored := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindRestored,
		Value: contracts.RuleRegistryRestored{
			Kind:          contracts.RegistryKindRestored,
			SchemaVersion: "1",
			RuleID:        "rule-1",
			PrevStatus:    contracts.RuleStatusArchived,
			NewStatus:     contracts.RuleStatusActive,
			OpID:          strings.Repeat("6", 64),
			VersionSeq:    3,
			PrevHash:      archivedResult.Sha256,
			BySunsetRunID: "2026-04-21-PR3-cdef012",
			At:            time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}
	restoredResult, _, err := registryAppendResult(restored, afterArchivedOffset)
	require.NoError(t, err)

	_, err = Build([]contracts.RuleRegistryEntry{
		added,
		archived,
		restored,
		{
			Kind: contracts.RegistryKindRolledBack,
			Value: contracts.RuleRegistryRolledBack{
				Kind:           contracts.RegistryKindRolledBack,
				SchemaVersion:  "1",
				TargetOpID:     strings.Repeat("2", 64),
				TargetOffset:   addedResult.Offset,
				TargetSha256:   addedResult.Sha256,
				ByRunID:        "2026-04-21-PR4-def0123",
				RollbackReason: contracts.RollbackReasonTransactionalFailure,
				FailedStep:     contracts.FailedStep70,
				VersionSeq:     4,
				PrevHash:       restoredResult.Sha256,
				At:             time.Date(2026, 4, 21, 13, 0, 0, 0, time.UTC),
			},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stale")
}

func TestBuild_RollbackFailsWhenTargetIsNotCurrentPromotion(t *testing.T) {
	added := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         "rule-1",
			RulePath:       "rules/rule-1.md",
			Sha256:         strings.Repeat("1", 64),
			IdempotencyKey: strings.Repeat("2", 64),
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        "2026-04-21-PR1-abcdef0",
			At:             time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
		},
	}
	addedResult, nextOffset, err := registryAppendResult(added, 0)
	require.NoError(t, err)

	updated := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindUpdated,
		Value: contracts.RuleRegistryUpdated{
			Kind:           contracts.RegistryKindUpdated,
			SchemaVersion:  "1",
			RuleID:         "rule-1",
			RulePath:       "rules/rule-1.md",
			Sha256:         strings.Repeat("3", 64),
			PrevSha256:     strings.Repeat("1", 64),
			IdempotencyKey: strings.Repeat("4", 64),
			VersionSeq:     2,
			PrevHash:       addedResult.Sha256,
			ByRunID:        "2026-04-21-PR2-bcdef01",
			At:             time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC),
		},
	}
	updatedResult, _, err := registryAppendResult(updated, nextOffset)
	require.NoError(t, err)

	_, err = Build([]contracts.RuleRegistryEntry{
		added,
		updated,
		{
			Kind: contracts.RegistryKindRolledBack,
			Value: contracts.RuleRegistryRolledBack{
				Kind:           contracts.RegistryKindRolledBack,
				SchemaVersion:  "1",
				TargetOpID:     strings.Repeat("2", 64),
				TargetOffset:   addedResult.Offset,
				TargetSha256:   addedResult.Sha256,
				ByRunID:        "2026-04-21-PR3-cdef012",
				RollbackReason: contracts.RollbackReasonTransactionalFailure,
				FailedStep:     contracts.FailedStep70,
				VersionSeq:     3,
				PrevHash:       updatedResult.Sha256,
				At:             time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
			},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not current promotion")
}
