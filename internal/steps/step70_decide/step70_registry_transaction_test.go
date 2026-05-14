package step70_decide

import (
	"context"
	"fmt"
	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/nishimoto265/harnest/internal/contracts/stepio"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestNextRegistryVersion_UsesGlobalSequence(t *testing.T) {
	lines := []registryLine{
		{
			Entry: contracts.RuleRegistryEntry{
				Kind: contracts.RegistryKindAdded,
				Value: contracts.RuleRegistryAdded{
					Kind:       contracts.RegistryKindAdded,
					RuleID:     "rule-a",
					VersionSeq: 2,
				},
			},
		},
		{
			Entry: contracts.RuleRegistryEntry{
				Kind: contracts.RegistryKindUpdated,
				Value: contracts.RuleRegistryUpdated{
					Kind:       contracts.RegistryKindUpdated,
					RuleID:     "rule-b",
					VersionSeq: 5,
				},
			},
		},
		{
			Entry: contracts.RuleRegistryEntry{
				Kind: contracts.RegistryKindStatusChanged,
				Value: contracts.RuleRegistryStatusChanged{
					Kind:       contracts.RegistryKindStatusChanged,
					RuleID:     "rule-a",
					VersionSeq: 4,
				},
			},
		},
		{
			Entry: contracts.RuleRegistryEntry{
				Kind: contracts.RegistryKindArchived,
				Value: contracts.RuleRegistryArchived{
					Kind:       contracts.RegistryKindArchived,
					RuleID:     "rule-c",
					VersionSeq: 7,
				},
			},
		},
	}

	assert.EqualValues(t, 8, nextRegistryVersion(lines))
}
func TestFindPlannedRegistryMatches_RejectsPayloadMismatchForMatchingOpID(t *testing.T) {
	runCtx, _, candidates, _, resolver := newFixtureWithResolver(t, "PR417")
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)

	planned := intention.PlannedAdoption.Entries[0]
	_, err := internalio.AppendRegistryEntry(runCtx.RulesRegistryPath(), contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         planned.RuleID,
			RulePath:       planned.RulePath,
			Sha256:         strings.Repeat("f", 64),
			IdempotencyKey: planned.OpID,
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        runCtx.RunID,
			At:             fixedNow()(),
		},
	})
	require.NoError(t, err)

	_, err = findPlannedRegistryMatches(runCtx, intention)
	require.ErrorIs(t, err, ErrRegistryDivergence)
}
func TestAppendRegistryRollbacks_IgnoresMismatchedExistingRollbackTarget(t *testing.T) {
	runCtx, _, candidates, _, resolver := newFixtureWithResolver(t, "PR120")
	appendResult, _ := seedRegistryAdd(t, runCtx.RulesRegistryPath(), resolver, runCtx.RunID, candidates.CandidatesHash)
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.RegistryAppendResult = &appendResult
	intention.AppendedEntryOpIDs = append(intention.AppendedEntryOpIDs, intention.PlannedAdoption.Entries[0].OpID)

	bogusRollback := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindRolledBack,
		Value: contracts.RuleRegistryRolledBack{
			Kind:           contracts.RegistryKindRolledBack,
			SchemaVersion:  "1",
			TargetOpID:     intention.PlannedAdoption.Entries[0].OpID,
			TargetOffset:   appendResult.Offset + 999,
			TargetSha256:   strings.Repeat("f", 64),
			ByRunID:        runCtx.RunID,
			RollbackReason: contracts.RollbackReasonTransactionalFailure,
			FailedStep:     contracts.FailedStep70,
			VersionSeq:     2,
			PrevHash:       appendResult.Sha256,
			At:             fixedNow()(),
		},
	}
	_, err := internalio.AppendRegistryEntry(runCtx.RulesRegistryPath(), bogusRollback)
	require.NoError(t, err)

	rollbackResult, err := appendRegistryRollbacks(runCtx, intention, contracts.RollbackReasonTransactionalFailure, fixedNow()())
	require.NoError(t, err)
	require.NotNil(t, rollbackResult)

	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	require.NoError(t, err)
	require.Len(t, lines, 3)
	assert.Equal(t, contracts.RegistryKindRolledBack, lines[2].Entry.Kind)
}
func TestFindRegistryByIdempotencyKey_RebuildsMandatoryIndexBeforeLookup(t *testing.T) {
	runCtx, _, candidates, _, _ := newFixture(t, "PR14")
	targetKey := contracts.ComputeAdoptIdempotencyKey(string(runCtx.RunID), strings.Repeat("2", 40), strings.Repeat("1", 40), candidates.CandidatesHash)
	specs := make([]seedRegistrySpec, 0, internalio.RegistryMandatoryIndexAt)
	for i := 0; i < internalio.RegistryMandatoryIndexAt; i++ {
		key := fmt.Sprintf("%064x", i+1)
		if i == internalio.RegistryMandatoryIndexAt/2 {
			key = targetKey
		}
		specs = append(specs, seedRegistrySpec{
			RuleID:         fmt.Sprintf("seed-%04d", i),
			IdempotencyKey: key,
			ByRunID:        contracts.RunID(fmt.Sprintf("2026-04-21-PR%02d-abcdef0", (i%90)+10)),
		})
	}
	results := writeSeedRegistryAdds(t, runCtx.RulesRegistryPath(), specs)
	targetResult := results[targetKey]

	require.NoError(t, internalio.WriteAtomic(runCtx.RulesIdempotencyIndexPath(), []byte("{\"idempotency_key\":\""+strings.Repeat("f", 64)+"\",\"registry_offset\":0,\"registry_sha256\":\""+strings.Repeat("e", 64)+"\",\"kind\":\"added\",\"at\":\"2026-04-21T00:01:40Z\"}\n")))

	found, ok, err := findRegistryByIdempotencyKey(runCtx, targetKey)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, targetResult, found)
}
func TestRun_ReadsBestShaBeforeFromRemoteHeadAfterLockAndOverridesPrefilledValue(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR20")
	resolver.target.BestShaBefore = strings.Repeat("9", 40)
	git := &fakeGit{head: strings.Repeat("4", 40)}

	require.NoError(t, Run(context.Background(), 20, runCtx, pkg, candidates, store, Deps{Git: git, Resolver: resolver, Now: fixedNow()}))

	adopt := mustDecisionAdopt(t, readDecision(t, runCtx))
	assert.Equal(t, strings.Repeat("4", 40), adopt.BestShaBefore)
	assert.Equal(t, 1, git.remoteHeadCalls)
}
func TestRun_RejectsPersistedDecisionThatFailsRequestBoundValidation(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR21")
	appendResult, _ := seedRegistryAdd(t, runCtx.RulesRegistryPath(), resolver, runCtx.RunID, candidates.CandidatesHash)
	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(decisionPath, contracts.Decision{
		Action: contracts.DecisionActionAdopt,
		Value: contracts.DecisionAdopt{
			Action:               contracts.DecisionActionAdopt,
			SchemaVersion:        "1",
			RunID:                runCtx.RunID,
			IdempotencyKey:       contracts.ComputeAdoptIdempotencyKey(string(runCtx.RunID), resolver.target.TargetSHA, resolver.target.BestShaBefore, strings.Repeat("f", 64)),
			BestShaBefore:        resolver.target.BestShaBefore,
			TargetSha:            resolver.target.TargetSHA,
			CandidatesHash:       strings.Repeat("f", 64),
			RegistryAppendResult: appendResult,
			DecidedAt:            fixedNow()(),
		},
	}))

	err = Run(context.Background(), 21, runCtx, pkg, candidates, store, Deps{Git: &fakeGit{head: resolver.target.TargetSHA}, Resolver: unexpectedResolver{t: t}, Now: fixedNow()})
	require.Error(t, err)
	assert.ErrorIs(t, err, stepio.ErrStep70AdoptCandidatesHashMismatch)
}
