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
