package step70_decide

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveWinningAgent_CollapsesLatestScoresAndPairwiseRows(t *testing.T) {
	runCtx := newResolverRunContext(t)
	scoresPath := mustResolveResolverPath(t, runCtx, "60/scores-B.jsonl")
	pairwisePath := mustResolveResolverPath(t, runCtx, "60/pairwise.jsonl")

	// Stale rerun rows would make a1 win unless resolver collapses by key.
	require.NoError(t, internalio.AppendJSONL(scoresPath, contracts.ScoreEntry{
		SchemaVersion: "1",
		RunID:         runCtx.RunID,
		Pass:          2,
		Agent:         "a1",
		Dimension:     contracts.DimensionFidelity,
		Score:         100,
		Reasons:       "stale",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}))
	require.NoError(t, internalio.AppendJSONL(scoresPath, contracts.ScoreEntry{
		SchemaVersion: "1",
		RunID:         runCtx.RunID,
		Pass:          2,
		Agent:         "a1",
		Dimension:     contracts.DimensionFidelity,
		Score:         1,
		Reasons:       "fresh",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    time.Date(2026, 4, 21, 10, 5, 0, 0, time.UTC),
	}))
	require.NoError(t, internalio.AppendJSONL(scoresPath, contracts.ScoreEntry{
		SchemaVersion: "1",
		RunID:         runCtx.RunID,
		Pass:          2,
		Agent:         "a2",
		Dimension:     contracts.DimensionFidelity,
		Score:         80,
		Reasons:       "fresh",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    time.Date(2026, 4, 21, 10, 5, 0, 0, time.UTC),
	}))

	require.NoError(t, internalio.AppendJSONL(pairwisePath, contracts.PairwiseEntry{
		SchemaVersion: "1",
		RunID:         runCtx.RunID,
		AgentA:        "a1",
		AgentB:        "a1",
		Winner:        contracts.PairwiseWinnerB,
		Margin:        contracts.PairwiseMarginClear,
		Justification: "stale",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}))
	require.NoError(t, internalio.AppendJSONL(pairwisePath, contracts.PairwiseEntry{
		SchemaVersion: "1",
		RunID:         runCtx.RunID,
		AgentA:        "a1",
		AgentB:        "a1",
		Winner:        contracts.PairwiseWinnerTie,
		Margin:        contracts.PairwiseMarginSlight,
		Justification: "fresh",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    time.Date(2026, 4, 21, 10, 5, 0, 0, time.UTC),
	}))
	require.NoError(t, internalio.AppendJSONL(pairwisePath, contracts.PairwiseEntry{
		SchemaVersion: "1",
		RunID:         runCtx.RunID,
		AgentA:        "a2",
		AgentB:        "a2",
		Winner:        contracts.PairwiseWinnerB,
		Margin:        contracts.PairwiseMarginClear,
		Justification: "fresh",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    time.Date(2026, 4, 21, 10, 5, 0, 0, time.UTC),
	}))

	winningAgent, ok, err := resolveWinningAgent(runCtx)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, contracts.AgentID("a2"), winningAgent)
}

func TestLatestRuleSha256_UsesRollbackAwareEffectiveState(t *testing.T) {
	lines := []registryLine{
		{Entry: contracts.RuleRegistryEntry{
			Kind: contracts.RegistryKindAdded,
			Value: contracts.RuleRegistryAdded{
				Kind:           contracts.RegistryKindAdded,
				SchemaVersion:  "1",
				RuleID:         "rule-a",
				RulePath:       "rules/rule-a.md",
				Sha256:         strings.Repeat("1", 64),
				IdempotencyKey: strings.Repeat("a", 64),
				VersionSeq:     1,
				ByRunID:        "2026-04-21-PR42-abcdef0",
				At:             time.Now().UTC(),
			},
		}},
		{Entry: contracts.RuleRegistryEntry{
			Kind: contracts.RegistryKindUpdated,
			Value: contracts.RuleRegistryUpdated{
				Kind:           contracts.RegistryKindUpdated,
				SchemaVersion:  "1",
				RuleID:         "rule-a",
				RulePath:       "rules/rule-a.md",
				Sha256:         strings.Repeat("2", 64),
				PrevSha256:     strings.Repeat("1", 64),
				IdempotencyKey: strings.Repeat("b", 64),
				VersionSeq:     2,
				PrevHash:       strings.Repeat("f", 64),
				ByRunID:        "2026-04-21-PR42-abcdef0",
				At:             time.Now().UTC(),
			},
		}},
		{Entry: contracts.RuleRegistryEntry{
			Kind: contracts.RegistryKindRolledBack,
			Value: contracts.RuleRegistryRolledBack{
				Kind:           contracts.RegistryKindRolledBack,
				SchemaVersion:  "1",
				TargetOpID:     strings.Repeat("b", 64),
				TargetOffset:   0,
				TargetSha256:   "",
				ByRunID:        "2026-04-21-PR42-abcdef0",
				RollbackReason: contracts.RollbackReasonTransactionalFailure,
				FailedStep:     contracts.FailedStep70,
				VersionSeq:     3,
				PrevHash:       strings.Repeat("e", 64),
				At:             time.Now().UTC(),
			},
		}},
	}
	addedPayload, err := contracts.CanonicalMarshal(lines[0].Entry)
	require.NoError(t, err)
	updatedPayload, err := contracts.CanonicalMarshal(lines[1].Entry)
	require.NoError(t, err)
	sum := sha256.Sum256(updatedPayload)
	rolledBack := lines[2].Entry.Value.(contracts.RuleRegistryRolledBack)
	rolledBack.TargetOffset = int64(len(addedPayload) + 1)
	rolledBack.TargetSha256 = hex.EncodeToString(sum[:])
	lines[2].Entry.Value = rolledBack

	got, err := latestRuleSha256(lines, "rule-a")
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("1", 64), got)
}

func TestLatestRuleSha256_RejectsInvalidExistingRulePath(t *testing.T) {
	lines := []registryLine{
		{Entry: contracts.RuleRegistryEntry{
			Kind: contracts.RegistryKindAdded,
			Value: contracts.RuleRegistryAdded{
				Kind:           contracts.RegistryKindAdded,
				SchemaVersion:  "1",
				RuleID:         "rule-a",
				RulePath:       "../needs-recovery/pwn.md",
				Sha256:         strings.Repeat("1", 64),
				IdempotencyKey: strings.Repeat("a", 64),
				VersionSeq:     1,
				ByRunID:        "2026-04-21-PR42-abcdef0",
				At:             time.Now().UTC(),
			},
		}},
	}

	_, err := latestRuleSha256(lines, "rule-a")
	require.ErrorContains(t, err, "invalid rule_path")
}

func newResolverRunContext(t *testing.T) internalio.RunContext {
	t.Helper()
	runsBase := filepath.Join(t.TempDir(), "runs")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")
	runCtx, err := internalio.NewRunContext("2026-04-21-PR42-abcdef0", runsBase, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunDir(), 0o755))
	return runCtx
}

func mustResolveResolverPath(t *testing.T, runCtx internalio.RunContext, rel string) string {
	t.Helper()
	path, err := runCtx.ResolveRunRelative(rel)
	require.NoError(t, err)
	return path
}
