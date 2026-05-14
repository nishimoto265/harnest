package step60_scorepairwise

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/judges"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_IdempotentWhenDoneMarkerExists(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		agents:          []contracts.AgentID{"a1", "a2", "a3"},
		writePass1Score: true,
	})

	firstNow := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return firstNow },
	}))

	donePath := mustResolve(t, runIO, "60/done.marker")
	beforeStat, err := os.Stat(donePath)
	require.NoError(t, err)
	beforeMarker := mustReadFile(t, donePath)
	beforeScores := mustReadJSONL[contracts.ScoreEntry](t, runIO, "60/scores-B.jsonl")

	secondNow := firstNow.Add(2 * time.Hour)
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return secondNow },
	}))

	afterStat, err := os.Stat(donePath)
	require.NoError(t, err)
	afterMarker := mustReadFile(t, donePath)
	afterScores := mustReadJSONL[contracts.ScoreEntry](t, runIO, "60/scores-B.jsonl")

	assert.Equal(t, beforeMarker, afterMarker)
	assert.Equal(t, beforeStat.ModTime(), afterStat.ModTime())
	assert.Equal(t, beforeScores, afterScores)
}

func TestRun_RebuildsWhenPass1ScoresChangeWithDoneMarker(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})

	firstNow := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return firstNow },
	}))
	beforeMarker := mustReadJSON[contracts.Step60DoneMarker](t, mustResolve(t, runIO, "60/done.marker"))
	require.Contains(t, mustReadJSONL[contracts.PairwiseEntry](t, runIO, "60/pairwise.jsonl")[0].Justification, "A_avg_tenths=820 B_avg_tenths=820")

	appendPass1ScoresWithScore(t, runIO, pkg.RunID, []contracts.AgentID{"a1"}, 10)

	later := firstNow.Add(2 * time.Hour)
	primary := &countingJudge{delegate: judges.NewPrimaryStub()}
	secondary := &countingJudge{delegate: judges.NewSecondaryStub()}
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     primary,
		Secondary:   secondary,
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return later },
	}))

	afterMarker := mustReadJSON[contracts.Step60DoneMarker](t, mustResolve(t, runIO, "60/done.marker"))
	pairwise := mustReadJSONL[contracts.PairwiseEntry](t, runIO, "60/pairwise.jsonl")
	require.Len(t, pairwise, 1)
	assert.Greater(t, primary.callCount(), int32(0))
	assert.Greater(t, secondary.callCount(), int32(0))
	assert.NotEqual(t, beforeMarker.InputHashes.Pass1Scores, afterMarker.InputHashes.Pass1Scores)
	assert.Equal(t, later, afterMarker.ResolvedAt)
	assert.Contains(t, pairwise[0].Justification, "A_avg_tenths=100 B_avg_tenths=820")
	assert.Equal(t, contracts.PairwiseWinnerB, pairwise[0].Winner)
}

func TestRun_RerunsJudgesWhenCandidateRulesChangeWithDoneMarker(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	candidateV1 := []judges.CandidateRule{{
		ID:    "cand-1",
		Kind:  "new",
		Title: "Candidate rule",
		Body:  "first body",
	}}
	firstNow := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	require.NoError(t, Run(context.Background(), Input{
		IO:             runIO,
		TaskPackage:    &pkg,
		Primary:        scriptedJudge{score: 60, reasonPrefix: "primary-v1", compliance: map[string]contracts.ComplianceVerdict{"cand-1": contracts.ComplianceVerdictCompliant}},
		Secondary:      scriptedJudge{score: 60, reasonPrefix: "secondary-v1", compliance: map[string]contracts.ComplianceVerdict{"cand-1": contracts.ComplianceVerdictCompliant}},
		Arbiter:        judges.NewArbiterStub(),
		CandidateRules: candidateV1,
		Now:            func() time.Time { return firstNow },
	}))
	beforeMarker := mustReadJSON[contracts.Step60DoneMarker](t, mustResolve(t, runIO, "60/done.marker"))

	candidateV2 := []judges.CandidateRule{{
		ID:    "cand-1",
		Kind:  "new",
		Title: "Candidate rule",
		Body:  "second body",
	}}
	primary := &countingJudge{delegate: scriptedJudge{score: 90, reasonPrefix: "primary-v2", compliance: map[string]contracts.ComplianceVerdict{"cand-1": contracts.ComplianceVerdictCompliant}}}
	secondary := &countingJudge{delegate: scriptedJudge{score: 90, reasonPrefix: "secondary-v2", compliance: map[string]contracts.ComplianceVerdict{"cand-1": contracts.ComplianceVerdictCompliant}}}
	later := firstNow.Add(2 * time.Hour)
	require.NoError(t, Run(context.Background(), Input{
		IO:             runIO,
		TaskPackage:    &pkg,
		Primary:        primary,
		Secondary:      secondary,
		Arbiter:        judges.NewArbiterStub(),
		CandidateRules: candidateV2,
		Now:            func() time.Time { return later },
	}))

	afterMarker := mustReadJSON[contracts.Step60DoneMarker](t, mustResolve(t, runIO, "60/done.marker"))
	scores := mustReadJSONL[contracts.ScoreEntry](t, runIO, "60/scores-B.jsonl")
	require.Len(t, scores, len(canonicalDimensions))
	assert.Greater(t, primary.callCount(), int32(0))
	assert.Greater(t, secondary.callCount(), int32(0))
	assert.NotEqual(t, beforeMarker.InputHashes.CandidateRules, afterMarker.InputHashes.CandidateRules)
	assert.Equal(t, later, afterMarker.ResolvedAt)
	for _, score := range scores {
		assert.Equal(t, 90, score.Score)
	}
}

func TestRun_RebuildsWhenDoneMarkerVerificationFails(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		agents:          []contracts.AgentID{"a1", "a2", "a3"},
		writePass1Score: true,
	})

	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return now },
	}))

	donePath := mustResolve(t, runIO, "60/done.marker")
	freshMarker := mustReadJSON[contracts.Step60DoneMarker](t, donePath)

	mutatedMarker := freshMarker
	mutatedMarker.ContentHashes.ScoresFinal = flipHexChar(mutatedMarker.ContentHashes.ScoresFinal)
	require.NoError(t, internalio.WriteJSONAtomic(donePath, mutatedMarker))

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return now },
	}))

	rebuiltMarker := mustReadJSON[contracts.Step60DoneMarker](t, donePath)
	assert.Equal(t, freshMarker.ContentHashes, rebuiltMarker.ContentHashes)
	assert.Equal(t, freshMarker.RawHashes, rebuiltMarker.RawHashes)
	assert.Equal(t, freshMarker.ExpectedCounts, rebuiltMarker.ExpectedCounts)

	beforeStat, err := os.Stat(donePath)
	require.NoError(t, err)
	beforeMarkerBytes := mustReadFile(t, donePath)

	later := now.Add(2 * time.Hour)
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return later },
	}))

	afterStat, err := os.Stat(donePath)
	require.NoError(t, err)
	assert.Equal(t, beforeMarkerBytes, mustReadFile(t, donePath))
	assert.Equal(t, beforeStat.ModTime(), afterStat.ModTime())
}

func TestRun_RebuildsWhenDoneMarkerDimensionsAreNonCanonical(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		agents:          []contracts.AgentID{"a1", "a2", "a3"},
		writePass1Score: true,
	})

	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return now },
	}))

	donePath := mustResolve(t, runIO, "60/done.marker")
	freshMarker := mustReadJSON[contracts.Step60DoneMarker](t, donePath)
	mutatedMarker := mustReadJSON[contracts.Step60DoneMarker](t, donePath)
	mutatedMarker.Dimensions = []contracts.Dimension{
		contracts.DimensionCorrectness,
		contracts.DimensionFidelity,
		contracts.DimensionMaintainability,
		contracts.DimensionDiscipline,
		contracts.DimensionCommunication,
	}
	require.NoError(t, internalio.WriteJSONAtomic(donePath, mutatedMarker))

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return now },
	}))

	rebuiltMarker := mustReadJSON[contracts.Step60DoneMarker](t, donePath)
	assert.Equal(t, canonicalDimensions, rebuiltMarker.Dimensions)
	assert.Equal(t, freshMarker.ContentHashes, rebuiltMarker.ContentHashes)
	assert.Equal(t, freshMarker.RawHashes, rebuiltMarker.RawHashes)
}

func TestRun_RebuildsWhenDoneMarkerJSONIsMalformed(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		agents:          []contracts.AgentID{"a1", "a2", "a3"},
		writePass1Score: true,
	})
	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return now },
	}))
	require.NoError(t, os.WriteFile(mustResolve(t, runIO, "60/done.marker"), []byte("{"), 0o644))

	primary := &countingJudge{delegate: judges.NewPrimaryStub()}
	secondary := &countingJudge{delegate: judges.NewSecondaryStub()}
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     primary,
		Secondary:   secondary,
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return now },
	}))
	assert.Greater(t, primary.callCount(), int32(0))
	assert.Greater(t, secondary.callCount(), int32(0))
	require.NoError(t, mustReadJSON[contracts.Step60DoneMarker](t, mustResolve(t, runIO, "60/done.marker")).Validate())
}

func TestRun_RerunsJudgesWhenLegacyDoneMarkerMissingInputHashesAndCandidateChanges(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	candidateV1 := []judges.CandidateRule{{
		ID:    "cand-1",
		Kind:  "new",
		Title: "Candidate rule",
		Body:  "first body",
	}}
	firstNow := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	require.NoError(t, Run(context.Background(), Input{
		IO:             runIO,
		TaskPackage:    &pkg,
		Primary:        scriptedJudge{score: 60, reasonPrefix: "primary-v1", compliance: map[string]contracts.ComplianceVerdict{"cand-1": contracts.ComplianceVerdictCompliant}},
		Secondary:      scriptedJudge{score: 60, reasonPrefix: "secondary-v1", compliance: map[string]contracts.ComplianceVerdict{"cand-1": contracts.ComplianceVerdictCompliant}},
		Arbiter:        judges.NewArbiterStub(),
		CandidateRules: candidateV1,
		Now:            func() time.Time { return firstNow },
	}))

	donePath := mustResolve(t, runIO, "60/done.marker")
	legacyMarker := mustReadJSON[contracts.Step60DoneMarker](t, donePath)
	legacyPayload, err := json.Marshal(legacyMarker)
	require.NoError(t, err)
	var legacy map[string]any
	require.NoError(t, json.Unmarshal(legacyPayload, &legacy))
	delete(legacy, "input_hashes")
	legacyPayload, err = json.Marshal(legacy)
	require.NoError(t, err)
	require.NoError(t, internalio.WriteAtomic(donePath, legacyPayload))

	candidateV2 := []judges.CandidateRule{{
		ID:    "cand-1",
		Kind:  "new",
		Title: "Candidate rule",
		Body:  "second body",
	}}
	primary := &countingJudge{delegate: scriptedJudge{score: 90, reasonPrefix: "primary-v2", compliance: map[string]contracts.ComplianceVerdict{"cand-1": contracts.ComplianceVerdictCompliant}}}
	secondary := &countingJudge{delegate: scriptedJudge{score: 90, reasonPrefix: "secondary-v2", compliance: map[string]contracts.ComplianceVerdict{"cand-1": contracts.ComplianceVerdictCompliant}}}
	later := firstNow.Add(2 * time.Hour)
	require.NoError(t, Run(context.Background(), Input{
		IO:             runIO,
		TaskPackage:    &pkg,
		Primary:        primary,
		Secondary:      secondary,
		Arbiter:        judges.NewArbiterStub(),
		CandidateRules: candidateV2,
		Now:            func() time.Time { return later },
	}))

	scores := mustReadJSONL[contracts.ScoreEntry](t, runIO, "60/scores-B.jsonl")
	require.Len(t, scores, len(canonicalDimensions))
	assert.Greater(t, primary.callCount(), int32(0))
	assert.Greater(t, secondary.callCount(), int32(0))
	for _, score := range scores {
		assert.Equal(t, 90, score.Score)
	}
}

func TestRun_RerunsJudgesWhenDoneMarkerMissingAndCandidateRulesChange(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	candidateV1 := []judges.CandidateRule{{
		ID:    "cand-1",
		Kind:  "new",
		Title: "Candidate rule",
		Body:  "first body",
	}}
	firstNow := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	require.NoError(t, Run(context.Background(), Input{
		IO:             runIO,
		TaskPackage:    &pkg,
		Primary:        scriptedJudge{score: 60, reasonPrefix: "primary-v1", compliance: map[string]contracts.ComplianceVerdict{"cand-1": contracts.ComplianceVerdictCompliant}},
		Secondary:      scriptedJudge{score: 60, reasonPrefix: "secondary-v1", compliance: map[string]contracts.ComplianceVerdict{"cand-1": contracts.ComplianceVerdictCompliant}},
		Arbiter:        judges.NewArbiterStub(),
		CandidateRules: candidateV1,
		Now:            func() time.Time { return firstNow },
	}))
	require.NoError(t, os.Remove(mustResolve(t, runIO, "60/done.marker")))

	candidateV2 := []judges.CandidateRule{{
		ID:    "cand-1",
		Kind:  "new",
		Title: "Candidate rule",
		Body:  "second body",
	}}
	primary := &countingJudge{delegate: scriptedJudge{score: 90, reasonPrefix: "primary-v2", compliance: map[string]contracts.ComplianceVerdict{"cand-1": contracts.ComplianceVerdictCompliant}}}
	secondary := &countingJudge{delegate: scriptedJudge{score: 90, reasonPrefix: "secondary-v2", compliance: map[string]contracts.ComplianceVerdict{"cand-1": contracts.ComplianceVerdictCompliant}}}
	require.NoError(t, Run(context.Background(), Input{
		IO:             runIO,
		TaskPackage:    &pkg,
		Primary:        primary,
		Secondary:      secondary,
		Arbiter:        judges.NewArbiterStub(),
		CandidateRules: candidateV2,
		Now:            func() time.Time { return firstNow.Add(time.Hour) },
	}))

	assert.Greater(t, primary.callCount(), int32(0))
	assert.Greater(t, secondary.callCount(), int32(0))
	afterMarker := mustReadJSON[contracts.Step60DoneMarker](t, mustResolve(t, runIO, "60/done.marker"))
	candidateV1Hash, err := hashCandidateRules(candidateV1)
	require.NoError(t, err)
	assert.NotEqual(t, candidateV1Hash, afterMarker.InputHashes.CandidateRules)
}
