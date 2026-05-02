package step70_decide

import (
	"context"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestRun_NoopWhenNoTarget(t *testing.T) {
	runCtx, pkg, candidates, store, _ := newFixture(t, "PR1")
	require.NoError(t, Run(context.Background(), 1, runCtx, pkg, candidates, store, Deps{Now: fixedNow()}))

	decision := readDecision(t, runCtx)
	assert.Equal(t, contracts.DecisionActionNoop, decision.Action)
	assert.NoFileExists(t, intentionPath(t, runCtx))
}
func TestRun_DuplicateOnlyCandidatesEmitNoop(t *testing.T) {
	runCtx, pkg, _, store, _ := newFixture(t, "PR101")
	body := "# Duplicate rule\n\n- source_rule_id: rule-existing\n- classification: duplicate\n"
	candidate := contracts.Candidate{
		CandidateID:        "cand-dup",
		Kind:               contracts.CandidateKindDuplicate,
		TargetRuleID:       "rule-existing",
		Title:              "Duplicate rule",
		Problem:            "problem",
		Rationale:          "rationale",
		ProposedBodyPath:   "40/candidates/cand-dup.md",
		ProposedBodySha256: sha256String(body),
	}
	candidates := &contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          runCtx.RunID,
		Candidates:     []contracts.Candidate{candidate},
		CandidatesHash: contracts.CanonicalCandidatesHash([]contracts.Candidate{candidate}),
		CreatedAt:      time.Now().UTC(),
	}

	require.NoError(t, Run(context.Background(), 1, runCtx, pkg, candidates, store, Deps{Now: fixedNow()}))
	assert.Equal(t, contracts.DecisionActionNoop, readDecision(t, runCtx).Action)
}
func TestRun_AdoptHappyPath(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR2")
	git := &fakeGit{head: resolver.target.BestShaBefore}
	deps := Deps{Git: git, Resolver: resolver, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 2, runCtx, pkg, candidates, store, deps))

	decision := readDecision(t, runCtx)
	assert.Equal(t, contracts.DecisionActionAdopt, decision.Action)

	// promoting + promoted events persisted by step70 itself.
	events := readStateEvents(t, runCtx)
	assert.Equal(t, contracts.StateKindPromoting, events[0].Kind)
	assert.Equal(t, contracts.StateKindPromoted, events[len(events)-1].Kind)

	// Intention deleted on finalize.
	assert.NoFileExists(t, intentionPath(t, runCtx))

	// Exactly one lease push (target_sha) landed.
	require.Len(t, git.pushCalls, 1)
	assert.Equal(t, resolver.target.TargetSHA, git.pushCalls[0].target)
}
func TestRun_RejectsCandidatesHashMismatchAtEntry(t *testing.T) {
	runCtx, pkg, candidates, store, _ := newFixture(t, "PR430")
	candidates.CandidatesHash = strings.Repeat("f", 64)

	err := Run(context.Background(), 430, runCtx, pkg, candidates, store, Deps{Now: fixedNow()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "candidates invalid")

	decisionPath, pathErr := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, pathErr)
	assert.NoFileExists(t, decisionPath)
}
