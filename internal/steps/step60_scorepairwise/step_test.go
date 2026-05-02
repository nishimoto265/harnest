package step60_scorepairwise

import (
	"context"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/judges"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_HappyPath(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		agents:          []contracts.AgentID{"a1", "a2", "a3"},
		writePass1Score: true,
	})

	now := time.Date(2026, 4, 21, 15, 4, 5, 0, time.UTC)
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return now },
	}))

	donePath := mustResolve(t, runIO, "60/done.marker")
	marker := mustReadJSON[contracts.Step60DoneMarker](t, donePath)
	require.NoError(t, marker.Validate())
	assert.Equal(t, []contracts.AgentID{"a1", "a2", "a3"}, marker.CompletedAgents)
	assert.Equal(t, canonicalDimensions, marker.Dimensions)
	assert.EqualValues(t, 15, marker.ExpectedCounts.Scores)
	assert.EqualValues(t, 3, marker.ExpectedCounts.Compliance)
	assert.EqualValues(t, 3, marker.ExpectedCounts.Pairwise)
	assert.Equal(t, now, marker.ResolvedAt)

	scores := mustReadJSONL[contracts.ScoreEntry](t, runIO, "60/scores-B.jsonl")
	require.Len(t, scores, 15)
	for _, score := range scores {
		assert.Equal(t, contracts.VerdictPathAgreement, score.VerdictPath)
		assert.Equal(t, now, score.ResolvedAt)
	}

	compliance := mustReadJSONL[contracts.ComplianceEntry](t, runIO, "60/compliance-B.jsonl")
	require.Len(t, compliance, 3)
	for _, entry := range compliance {
		assert.Equal(t, contracts.VerdictPathAgreement, entry.VerdictPath)
		assert.Equal(t, now, entry.ResolvedAt)
	}

	pairwise := mustReadJSONL[contracts.PairwiseEntry](t, runIO, "60/pairwise.jsonl")
	require.Len(t, pairwise, 3)
	for _, entry := range pairwise {
		assert.Equal(t, entry.AgentA, entry.AgentB)
		assert.Equal(t, contracts.PairwiseWinnerTie, entry.Winner)
		assert.Equal(t, contracts.PairwiseMarginSlight, entry.Margin)
		assert.Equal(t, contracts.VerdictPathSingle, entry.VerdictPath)
		assert.Contains(t, entry.Justification, "mode=basic decision=inconclusive")
		assert.Contains(t, entry.Justification, "A_avg_tenths=820 B_avg_tenths=820")
		assert.Equal(t, now, entry.ResolvedAt)
	}

	rawScores := mustReadJSONL[contracts.RawScoreEntry](t, runIO, "60/scores-B-raw.jsonl")
	require.Len(t, rawScores, 30)
	rawCompliance := mustReadJSONL[contracts.RawComplianceEntry](t, runIO, "60/compliance-B-raw.jsonl")
	require.Len(t, rawCompliance, 6)

	assert.Len(t, marker.ContentHashes.ScoresFinal, 64)
	assert.Len(t, marker.ContentHashes.ComplianceFinal, 64)
	assert.Len(t, marker.ContentHashes.PairwiseFinal, 64)
	assert.Len(t, marker.RawHashes.ScoresRaw, 64)
	assert.Len(t, marker.RawHashes.ComplianceRaw, 64)

	assert.Equal(t, marker.ContentHashes.ScoresFinal, mustHashFinalScores(t, scores))
	assert.Equal(t, marker.ContentHashes.ComplianceFinal, mustHashFinalCompliance(t, compliance))
	assert.Equal(t, marker.ContentHashes.PairwiseFinal, mustHashFinalPairwise(t, pairwise))
	assert.Equal(t, marker.RawHashes.ScoresRaw, mustHashReducedRawScores(t, runIO))
	assert.Equal(t, marker.RawHashes.ComplianceRaw, mustHashReducedRawCompliance(t, runIO))
}

func TestRun_RejectsTaskPackageRunIDMismatch(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})
	pkg.RunID = "2026-04-22-PR42-bbbbbbb"

	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return time.Date(2026, 4, 21, 15, 4, 5, 0, time.UTC) },
	})
	require.ErrorContains(t, err, "task package run_id mismatch")
}

func TestAppendJSONLWithParentDirSync_ReturnsAppendError(t *testing.T) {
	err := appendJSONLWithParentDirSync("relative/path.jsonl", contracts.ScoreEntry{})
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrPathNotAbsolute)
}

func TestRun_FreshRunsAreByteIdentical(t *testing.T) {
	now := time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC)

	runIOA, pkgA := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})
	runIOB, pkgB := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIOA,
		TaskPackage: &pkgA,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return now },
	}))
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIOB,
		TaskPackage: &pkgB,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return now },
	}))

	assertArtifactsByteIdentical(t, runIOA, runIOB)
}

func TestRun_NormalizesRawResolvedAtToRunSnapshot(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})

	judgeResolvedAt := time.Date(2026, 4, 20, 23, 59, 59, 0, time.UTC)
	runResolvedAt := time.Date(2026, 4, 21, 18, 0, 0, 0, time.UTC)
	primary := scriptedJudge{score: 80, reasonPrefix: "primary", compliance: map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant}, resolvedAt: judgeResolvedAt}
	secondary := scriptedJudge{score: 70, reasonPrefix: "secondary", compliance: map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictViolated}, resolvedAt: judgeResolvedAt}
	arbiter := scriptedJudge{score: 80, reasonPrefix: "primary", compliance: map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant}, resolvedAt: judgeResolvedAt}

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     primary,
		Secondary:   secondary,
		Arbiter:     arbiter,
		Now:         func() time.Time { return runResolvedAt },
	}))

	for _, entry := range mustReadJSONL[contracts.ScoreEntry](t, runIO, "60/scores-B.jsonl") {
		assert.Equal(t, runResolvedAt, entry.ResolvedAt)
	}
	for _, entry := range mustReadJSONL[contracts.ComplianceEntry](t, runIO, "60/compliance-B.jsonl") {
		assert.Equal(t, runResolvedAt, entry.ResolvedAt)
	}
	for _, entry := range mustReadJSONL[contracts.RawScoreEntry](t, runIO, "60/scores-B-raw.jsonl") {
		assert.Equal(t, runResolvedAt, entry.ResolvedAt)
	}
	for _, entry := range mustReadJSONL[contracts.RawComplianceEntry](t, runIO, "60/compliance-B-raw.jsonl") {
		assert.Equal(t, runResolvedAt, entry.ResolvedAt)
	}

	marker := mustReadJSON[contracts.Step60DoneMarker](t, mustResolve(t, runIO, "60/done.marker"))
	assert.Equal(t, runResolvedAt, marker.ResolvedAt)
}

func TestRun_GoldenHashes(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})

	now := time.Date(2026, 4, 21, 17, 0, 0, 0, time.UTC)
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return now },
	}))

	marker := mustReadJSON[contracts.Step60DoneMarker](t, mustResolve(t, runIO, "60/done.marker"))
	assert.Equal(t, "f24957610c33e2667e3bc04cf8fae00992b05c42ea27f4e1f762c08351b7b4d0", marker.ContentHashes.ScoresFinal)
	assert.Equal(t, "a7684f4f2d558b499008ea67464f3f3894da8fae81446ca276659efa97bfdfa4", marker.ContentHashes.ComplianceFinal)
	assert.Equal(t, "8bd0877ee9d11a879451f0c22f368f5373a34d657a7b61768b26a39e28b35621", marker.ContentHashes.PairwiseFinal)
}
