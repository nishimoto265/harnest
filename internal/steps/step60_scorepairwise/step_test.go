package step60_scorepairwise

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
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
		assert.Equal(t, "pass1_avg_tenths=820 pass2_avg_tenths=820", entry.Justification)
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

func TestAppendJSONLWithParentDirSync_ReturnsAppendError(t *testing.T) {
	err := appendJSONLWithParentDirSync("relative/path.jsonl", contracts.ScoreEntry{})
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrPathNotAbsolute)
}

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

func TestRun_RebuildDropsStaleRowsForAgentsNoLongerScorable(t *testing.T) {
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

	writeManifestError(t, runIO, pkg.RunID, 2, "a3")

	donePath := mustResolve(t, runIO, "60/done.marker")
	marker := mustReadJSON[contracts.Step60DoneMarker](t, donePath)
	marker.ContentHashes.PairwiseFinal = flipHexChar(marker.ContentHashes.PairwiseFinal)
	require.NoError(t, internalio.WriteJSONAtomic(donePath, marker))

	later := now.Add(2 * time.Hour)
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return later },
	}))

	rebuilt := mustReadJSON[contracts.Step60DoneMarker](t, donePath)
	assert.Equal(t, []contracts.AgentID{"a1", "a2"}, rebuilt.CompletedAgents)

	pairwise := mustReadJSONL[contracts.PairwiseEntry](t, runIO, "60/pairwise.jsonl")
	require.Len(t, pairwise, 2)
	for _, entry := range pairwise {
		assert.NotEqual(t, contracts.AgentID("a3"), entry.AgentA)
	}
}

func TestRun_ErrorsWhenPass2ManifestMissing(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		agents:             []contracts.AgentID{"a1", "a2", "a3"},
		writePass1Score:    true,
		missingPass2Agents: map[contracts.AgentID]bool{"a3": true},
	})

	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC) },
	})
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrNoScorablePass2Agents))
	assert.NoFileExists(t, mustResolve(t, runIO, "60/done.marker"))
}

func TestRun_SkipsNonScorablePass2Agent(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		agents:                 []contracts.AgentID{"a1", "a2", "a3"},
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a3": true},
	})

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC) },
	}))

	marker := mustReadJSON[contracts.Step60DoneMarker](t, mustResolve(t, runIO, "60/done.marker"))
	assert.Equal(t, []contracts.AgentID{"a1", "a2"}, marker.CompletedAgents)
	assert.EqualValues(t, 10, marker.ExpectedCounts.Scores)
	assert.EqualValues(t, 2, marker.ExpectedCounts.Compliance)
	assert.EqualValues(t, 2, marker.ExpectedCounts.Pairwise)
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

func TestRun_RerunWithoutMarkerPreservesReducedState(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})
	now := time.Date(2026, 4, 21, 11, 30, 0, 0, time.UTC)

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return now },
	}))

	before := readStep60Artifacts(t, runIO)
	beforeMarker := mustReadJSON[contracts.Step60DoneMarker](t, mustResolve(t, runIO, "60/done.marker"))
	require.NoError(t, os.Remove(mustResolve(t, runIO, "60/done.marker")))

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return now },
	}))

	afterMarker := mustReadJSON[contracts.Step60DoneMarker](t, mustResolve(t, runIO, "60/done.marker"))

	after := readStep60Artifacts(t, runIO)
	assert.NotEqual(t, before, after)
	assert.Equal(t, beforeMarker.RawHashes, afterMarker.RawHashes)
	assert.Equal(t, beforeMarker.ContentHashes, afterMarker.ContentHashes)
}

func TestRun_NoScorableAgentsReturnsTypedError(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a1": true, "a2": true, "a3": true},
	})

	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC) },
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoScorablePass2Agents)
	assert.NoFileExists(t, mustResolve(t, runIO, "60/done.marker"))
	assert.NoFileExists(t, mustResolve(t, runIO, "60/scores-B-raw.jsonl"))
}

func TestRun_ArbiterVerdictPaths(t *testing.T) {
	t.Run("arbitrated", func(t *testing.T) {
		runIO, pkg := seedStep60Fixture(t, fixtureOptions{
			writePass1Score:        true,
			nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
		})

		primary := scriptedJudge{score: 80, reasonPrefix: "primary", compliance: map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant}}
		secondary := scriptedJudge{score: 70, reasonPrefix: "secondary", compliance: map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictViolated}}
		arbiter := scriptedJudge{score: 80, reasonPrefix: "primary", compliance: map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant}}

		require.NoError(t, Run(context.Background(), Input{
			IO:          runIO,
			TaskPackage: &pkg,
			Primary:     primary,
			Secondary:   secondary,
			Arbiter:     arbiter,
			Now:         func() time.Time { return time.Date(2026, 4, 21, 13, 0, 0, 0, time.UTC) },
		}))

		scores := mustReadJSONL[contracts.ScoreEntry](t, runIO, "60/scores-B.jsonl")
		require.NotEmpty(t, scores)
		assert.Equal(t, contracts.VerdictPathArbitrated, scores[0].VerdictPath)
	})

	t.Run("arbiter_overruled", func(t *testing.T) {
		runIO, pkg := seedStep60Fixture(t, fixtureOptions{
			writePass1Score:        true,
			nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
		})

		primary := scriptedJudge{score: 80, reasonPrefix: "primary", compliance: map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant}}
		secondary := scriptedJudge{score: 70, reasonPrefix: "secondary", compliance: map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictViolated}}
		arbiter := scriptedJudge{score: 60, reasonPrefix: "arbiter", compliance: map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictNA}}

		require.NoError(t, Run(context.Background(), Input{
			IO:          runIO,
			TaskPackage: &pkg,
			Primary:     primary,
			Secondary:   secondary,
			Arbiter:     arbiter,
			Now:         func() time.Time { return time.Date(2026, 4, 21, 14, 0, 0, 0, time.UTC) },
		}))

		scores := mustReadJSONL[contracts.ScoreEntry](t, runIO, "60/scores-B.jsonl")
		require.NotEmpty(t, scores)
		assert.Equal(t, contracts.VerdictPathArbitrated, scores[0].VerdictPath)
	})
}

func TestRun_ComplianceSingleSideRuleKeepsRawProvenance(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})

	primary := scriptedJudge{
		score:        80,
		reasonPrefix: "primary",
		compliance: map[string]contracts.ComplianceVerdict{
			"shared": contracts.ComplianceVerdictCompliant,
		},
	}
	secondary := scriptedJudge{
		score:        70,
		reasonPrefix: "secondary",
		compliance: map[string]contracts.ComplianceVerdict{
			"shared":         contracts.ComplianceVerdictViolated,
			"secondary-only": contracts.ComplianceVerdictViolated,
		},
	}
	arbiter := scriptedJudge{
		score:        80,
		reasonPrefix: "primary",
		compliance: map[string]contracts.ComplianceVerdict{
			"shared":         contracts.ComplianceVerdictCompliant,
			"secondary-only": contracts.ComplianceVerdictValidException,
		},
	}

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     primary,
		Secondary:   secondary,
		Arbiter:     arbiter,
		Now:         func() time.Time { return time.Date(2026, 4, 21, 16, 0, 0, 0, time.UTC) },
	}))

	compliance := mustReadJSONL[contracts.ComplianceEntry](t, runIO, "60/compliance-B.jsonl")
	require.Len(t, compliance, 2)
	byRule := make(map[string]contracts.ComplianceEntry, len(compliance))
	for _, entry := range compliance {
		byRule[entry.RuleID] = entry
	}
	assert.Equal(t, contracts.ComplianceVerdictCompliant, byRule["shared"].Verdict)
	assert.Equal(t, contracts.VerdictPathArbitrated, byRule["shared"].VerdictPath)
	assert.Equal(t, contracts.ComplianceVerdictViolated, byRule["secondary-only"].Verdict)
	assert.Equal(t, contracts.VerdictPathSingle, byRule["secondary-only"].VerdictPath)

	rawCompliance := mustReadJSONL[contracts.RawComplianceEntry](t, runIO, "60/compliance-B-raw.jsonl")
	var secondaryOnly []contracts.RawComplianceEntry
	for _, entry := range rawCompliance {
		if entry.RuleID == "secondary-only" {
			secondaryOnly = append(secondaryOnly, entry)
		}
	}
	require.Len(t, secondaryOnly, 1)
	assert.Equal(t, contracts.JudgeRoleSecondary, secondaryOnly[0].JudgeRole)
	assert.Equal(t, contracts.ComplianceVerdictViolated, secondaryOnly[0].Verdict)
}

func TestRun_ComplianceArbiterOnlyRuleFinalizesAsSingleSource(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})

	primary := scriptedJudge{
		score:        80,
		reasonPrefix: "primary",
		compliance: map[string]contracts.ComplianceVerdict{
			"only-primary": contracts.ComplianceVerdictViolated,
		},
	}
	secondary := scriptedJudge{
		score:        70,
		reasonPrefix: "secondary",
		compliance: map[string]contracts.ComplianceVerdict{
			"only-secondary": contracts.ComplianceVerdictCompliant,
		},
	}
	arbiter := scriptedJudge{
		score:        80,
		reasonPrefix: "arbiter",
		compliance: map[string]contracts.ComplianceVerdict{
			"only-arbiter": contracts.ComplianceVerdictValidException,
		},
	}

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     primary,
		Secondary:   secondary,
		Arbiter:     arbiter,
		Now:         func() time.Time { return time.Date(2026, 4, 21, 16, 30, 0, 0, time.UTC) },
	}))

	compliance := mustReadJSONL[contracts.ComplianceEntry](t, runIO, "60/compliance-B.jsonl")
	require.Len(t, compliance, 3)
	byRule := make(map[string]contracts.ComplianceEntry, len(compliance))
	for _, entry := range compliance {
		byRule[entry.RuleID] = entry
	}
	assert.Equal(t, contracts.ComplianceVerdictValidException, byRule["only-arbiter"].Verdict)
	assert.Equal(t, contracts.VerdictPathSingle, byRule["only-arbiter"].VerdictPath)
	assert.Equal(t, contracts.ComplianceVerdictViolated, byRule["only-primary"].Verdict)
	assert.Equal(t, contracts.VerdictPathSingle, byRule["only-primary"].VerdictPath)
	assert.Equal(t, contracts.ComplianceVerdictCompliant, byRule["only-secondary"].Verdict)
	assert.Equal(t, contracts.VerdictPathSingle, byRule["only-secondary"].VerdictPath)

	rawCompliance := mustReadJSONL[contracts.RawComplianceEntry](t, runIO, "60/compliance-B-raw.jsonl")
	byRawRule := make(map[string][]contracts.RawComplianceEntry, len(rawCompliance))
	for _, entry := range rawCompliance {
		byRawRule[entry.RuleID] = append(byRawRule[entry.RuleID], entry)
	}
	require.Len(t, byRawRule["only-arbiter"], 1)
	assert.Equal(t, contracts.JudgeRolePrimary, byRawRule["only-arbiter"][0].JudgeRole)
	assert.Nil(t, byRawRule["only-arbiter"][0].PrimaryRef)
	assert.Nil(t, byRawRule["only-arbiter"][0].SecondaryRef)
	require.Len(t, byRawRule["only-primary"], 1)
	assert.Equal(t, contracts.JudgeRolePrimary, byRawRule["only-primary"][0].JudgeRole)
	require.Len(t, byRawRule["only-secondary"], 1)
	assert.Equal(t, contracts.JudgeRoleSecondary, byRawRule["only-secondary"][0].JudgeRole)
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
	assert.Equal(t, "c149fdcaf85dca93f7a473896ee7968be45a325f069b0d8c2ca523411481fd89", marker.ContentHashes.PairwiseFinal)
}

func TestReduceRawScores_KeepsArbiterWhenRefsMatchRawEntryHashes(t *testing.T) {
	resolvedAt := time.Date(2026, 4, 21, 21, 0, 0, 0, time.UTC)
	primary := contracts.RawScoreEntry{
		SchemaVersion: "1",
		RunID:         "2026-04-21-PR42-abcdef0",
		Pass:          2,
		Agent:         "a1",
		JudgeRole:     contracts.JudgeRolePrimary,
		Dimension:     contracts.DimensionFidelity,
		Score:         80,
		Reasons:       "primary",
		OutputSha256:  strings.Repeat("a", 64),
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    resolvedAt,
	}
	secondary := contracts.RawScoreEntry{
		SchemaVersion: "1",
		RunID:         primary.RunID,
		Pass:          2,
		Agent:         "a1",
		JudgeRole:     contracts.JudgeRoleSecondary,
		Dimension:     contracts.DimensionFidelity,
		Score:         79,
		Reasons:       "secondary",
		OutputSha256:  strings.Repeat("b", 64),
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    resolvedAt,
	}
	primaryHash, err := rawScoreEntryHash(primary)
	require.NoError(t, err)
	secondaryHash, err := rawScoreEntryHash(secondary)
	require.NoError(t, err)
	arbiter := contracts.RawScoreEntry{
		SchemaVersion: "1",
		RunID:         primary.RunID,
		Pass:          2,
		Agent:         "a1",
		JudgeRole:     contracts.JudgeRoleArbiter,
		Dimension:     contracts.DimensionFidelity,
		Score:         80,
		Reasons:       "arbiter",
		OutputSha256:  strings.Repeat("c", 64),
		PrimaryRef:    &contracts.RawJudgeRef{Role: contracts.JudgeRolePrimary, Sha256: primaryHash},
		SecondaryRef:  &contracts.RawJudgeRef{Role: contracts.JudgeRoleSecondary, Sha256: secondaryHash},
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    resolvedAt,
	}

	reduced := reduceRawScores([]contracts.RawScoreEntry{primary, secondary, arbiter})
	require.Len(t, reduced, 3)
	assert.Equal(t, contracts.JudgeRoleArbiter, reduced[2].JudgeRole)
}

func TestReduceRawCompliance_KeepsArbiterWhenRefsMatchRawEntryHashes(t *testing.T) {
	resolvedAt := time.Date(2026, 4, 21, 21, 0, 0, 0, time.UTC)
	primary := contracts.RawComplianceEntry{
		SchemaVersion: "1",
		RunID:         "2026-04-21-PR42-abcdef0",
		Pass:          2,
		Agent:         "a1",
		JudgeRole:     contracts.JudgeRolePrimary,
		RuleID:        "rule-1",
		Verdict:       contracts.ComplianceVerdictViolated,
		Rationale:     "primary",
		OutputSha256:  strings.Repeat("a", 64),
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    resolvedAt,
	}
	secondary := contracts.RawComplianceEntry{
		SchemaVersion: "1",
		RunID:         primary.RunID,
		Pass:          2,
		Agent:         "a1",
		JudgeRole:     contracts.JudgeRoleSecondary,
		RuleID:        "rule-1",
		Verdict:       contracts.ComplianceVerdictCompliant,
		Rationale:     "secondary",
		OutputSha256:  strings.Repeat("b", 64),
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    resolvedAt,
	}
	primaryHash, err := rawComplianceEntryHash(primary)
	require.NoError(t, err)
	secondaryHash, err := rawComplianceEntryHash(secondary)
	require.NoError(t, err)
	arbiter := contracts.RawComplianceEntry{
		SchemaVersion: "1",
		RunID:         primary.RunID,
		Pass:          2,
		Agent:         "a1",
		JudgeRole:     contracts.JudgeRoleArbiter,
		RuleID:        "rule-1",
		Verdict:       contracts.ComplianceVerdictViolated,
		Rationale:     "arbiter",
		OutputSha256:  strings.Repeat("c", 64),
		PrimaryRef:    &contracts.RawJudgeRef{Role: contracts.JudgeRolePrimary, Sha256: primaryHash},
		SecondaryRef:  &contracts.RawJudgeRef{Role: contracts.JudgeRoleSecondary, Sha256: secondaryHash},
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    resolvedAt,
	}

	reduced := reduceRawCompliance([]contracts.RawComplianceEntry{primary, secondary, arbiter})
	require.Len(t, reduced, 3)
	assert.Equal(t, contracts.JudgeRoleArbiter, reduced[2].JudgeRole)
}

func TestRun_SkipsPass2AgentWhenDeclaredArtifactIsMissing(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		agents:          []contracts.AgentID{"a1", "a2", "a3"},
		writePass1Score: true,
	})

	require.NoError(t, os.Remove(mustResolve(t, runIO, "50-pass2/a2/diff.patch")))

	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return time.Date(2026, 4, 21, 19, 0, 0, 0, time.UTC) },
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing declared pass2 diff artifact")
	assert.NoFileExists(t, mustResolve(t, runIO, "60/done.marker"))
}

func TestRun_UsesPass1Pass2ScorableIntersection(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass1Agents: map[contracts.AgentID]bool{"a2": true},
	})

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return time.Date(2026, 4, 21, 19, 30, 0, 0, time.UTC) },
	}))

	marker := mustReadJSON[contracts.Step60DoneMarker](t, mustResolve(t, runIO, "60/done.marker"))
	assert.Equal(t, []contracts.AgentID{"a1", "a3"}, marker.CompletedAgents)
	assert.EqualValues(t, 10, marker.ExpectedCounts.Scores)
	assert.EqualValues(t, 2, marker.ExpectedCounts.Pairwise)
}

func TestRun_SerializesConcurrentWriters(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})
	started := make(chan struct{})
	release := make(chan struct{})
	primary := &blockingJudge{
		delegate: scriptedJudge{
			score:        80,
			reasonPrefix: "primary",
			compliance:   map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant},
		},
		started: started,
		release: release,
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- Run(context.Background(), Input{
			IO:          runIO,
			TaskPackage: &pkg,
			Primary:     primary,
			Secondary:   judges.NewSecondaryStub(),
			Arbiter:     judges.NewArbiterStub(),
		})
	}()

	<-started

	go func() {
		errCh <- Run(context.Background(), Input{
			IO:          runIO,
			TaskPackage: &pkg,
			Primary:     primary,
			Secondary:   judges.NewSecondaryStub(),
			Arbiter:     judges.NewArbiterStub(),
		})
	}()

	select {
	case err := <-errCh:
		t.Fatalf("Run returned before lock release: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	assert.EqualValues(t, 1, primary.callCount())

	close(release)

	require.NoError(t, <-errCh)
	require.NoError(t, <-errCh)
	assert.GreaterOrEqual(t, primary.callCount(), int32(3))
}

func TestRun_StopsBeforeSecondaryJudgeWhenContextIsCanceled(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})

	ctx, cancel := context.WithCancel(context.Background())
	secondaryCalled := false

	err := Run(ctx, Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary: cancelingJudge{
			delegate: scriptedJudge{
				score:        80,
				reasonPrefix: "primary",
				compliance:   map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant},
			},
			cancel: cancel,
		},
		Secondary: unexpectedCallJudge{called: &secondaryCalled},
		Arbiter:   unexpectedCallJudge{},
		Now:       func() time.Time { return time.Date(2026, 4, 21, 20, 0, 0, 0, time.UTC) },
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, secondaryCalled)
	assert.NoFileExists(t, mustResolve(t, runIO, "60/done.marker"))
}

type fixtureOptions struct {
	agents                 []contracts.AgentID
	writePass1Score        bool
	missingPass2Agents     map[contracts.AgentID]bool
	nonScorablePass1Agents map[contracts.AgentID]bool
	nonScorablePass2Agents map[contracts.AgentID]bool
}

type scriptedJudge struct {
	score        int
	reasonPrefix string
	compliance   map[string]contracts.ComplianceVerdict
	resolvedAt   time.Time
}

func (j scriptedJudge) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	if err := input.Validate(); err != nil {
		return judges.JudgeOutput{}, err
	}
	select {
	case <-ctx.Done():
		return judges.JudgeOutput{}, ctx.Err()
	default:
	}

	scores := make([]contracts.ScoreEntry, 0, len(canonicalDimensions))
	resolvedAt := j.resolvedAt
	if resolvedAt.IsZero() {
		resolvedAt = time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	}
	for _, dimension := range canonicalDimensions {
		scores = append(scores, contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			Dimension:     dimension,
			Score:         j.score,
			Reasons:       fmt.Sprintf("%s-%s", j.reasonPrefix, dimension),
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "scripted-rubric",
			PromptVersion: "scripted-prompt",
			ResolvedAt:    resolvedAt,
		})
	}

	ruleIDs := make([]string, 0, len(j.compliance))
	for ruleID := range j.compliance {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)

	compliance := make([]contracts.ComplianceEntry, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		verdict := j.compliance[ruleID]
		compliance = append(compliance, contracts.ComplianceEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			RuleID:        ruleID,
			Verdict:       verdict,
			Rationale:     fmt.Sprintf("%s-%s", ruleID, verdict),
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "scripted-rubric",
			PromptVersion: "scripted-prompt",
			ResolvedAt:    resolvedAt,
		})
	}

	output := judges.JudgeOutput{
		Scores:     scores,
		Compliance: compliance,
	}
	return output, output.ValidateFor(input)
}

type cancelingJudge struct {
	delegate scriptedJudge
	cancel   context.CancelFunc
}

func (j cancelingJudge) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	output, err := j.delegate.ScoreOutput(ctx, input)
	if j.cancel != nil {
		j.cancel()
	}
	return output, err
}

type unexpectedCallJudge struct {
	called *bool
}

func (j unexpectedCallJudge) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	if j.called != nil {
		*j.called = true
	}
	return judges.JudgeOutput{}, errors.New("unexpected judge call")
}

type blockingJudge struct {
	delegate scriptedJudge
	started  chan struct{}
	release  chan struct{}
	once     sync.Once
	calls    int32
}

func (j *blockingJudge) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	atomic.AddInt32(&j.calls, 1)
	j.once.Do(func() {
		close(j.started)
		<-j.release
	})
	return j.delegate.ScoreOutput(ctx, input)
}

func (j *blockingJudge) callCount() int32 {
	return atomic.LoadInt32(&j.calls)
}

func seedStep60Fixture(t *testing.T, opts fixtureOptions) (internalio.RunContext, contracts.TaskPackage) {
	t.Helper()

	runsBase := filepath.Join(t.TempDir(), "runs")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")
	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	agents := opts.agents
	if len(agents) == 0 {
		agents = []contracts.AgentID{"a1", "a2", "a3"}
	}

	worktrees := make([]contracts.WorktreeAllocation, 0, len(agents)*2)
	for pass := 1; pass <= 2; pass++ {
		passDir := fmt.Sprintf("pass%d", pass)
		for _, agent := range agents {
			path := filepath.Join(worktreeBase, string(runID), passDir, string(agent))
			require.NoError(t, os.MkdirAll(path, 0o755))
			worktrees = append(worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    path,
				Branch:  filepath.ToSlash(filepath.Join("auto-improve", string(runID), passDir, string(agent))),
				BaseSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				HeadSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			})
		}
	}

	pkg := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runID,
		PR:                      42,
		Title:                   "step60 fixture",
		BaseSHA:                 "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BestBranch:              "best",
		ReconstructedTaskPrompt: "fixture prompt",
		Worktrees:               worktrees,
		CreatedAt:               time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
	}
	require.NoError(t, pkg.Validate())

	runIO, err := internalio.RunContextFromTaskPackage(pkg, runsBase, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runIO.RunDir(), 0o755))

	for _, agent := range agents {
		switch {
		case opts.nonScorablePass1Agents[agent]:
			writeManifestError(t, runIO, runID, 1, agent)
		default:
			writeManifestSuccess(t, runIO, runID, 1, agent)
		}
		switch {
		case opts.missingPass2Agents[agent]:
		case opts.nonScorablePass2Agents[agent]:
			writeManifestError(t, runIO, runID, 2, agent)
		default:
			writeManifestSuccess(t, runIO, runID, 2, agent)
		}
	}

	if opts.writePass1Score {
		writePass1Scores(t, runIO, runID, agents)
	}

	return runIO, pkg
}

func writeManifestSuccess(t *testing.T, runIO internalio.RunContext, runID contracts.RunID, pass int, agent contracts.AgentID) {
	t.Helper()

	prefix := filepath.Join("20-pass1", string(agent))
	if pass == 2 {
		prefix = filepath.Join("50-pass2", string(agent))
	}

	require.NoError(t, internalio.WriteAtomic(mustResolve(t, runIO, filepath.Join(prefix, "diff.patch")), []byte("diff\n")))
	require.NoError(t, internalio.WriteAtomic(mustResolve(t, runIO, filepath.Join(prefix, "session.jsonl")), []byte("{}\n")))
	require.NoError(t, internalio.WriteAtomic(mustResolve(t, runIO, filepath.Join(prefix, "checklist-result.json")), []byte("{}\n")))

	manifestPath, err := runIO.ManifestPath(pass, agent)
	require.NoError(t, err)
	manifest := contracts.Manifest{
		Kind: contracts.ManifestKindSuccess,
		Value: contracts.ManifestSuccess{
			Kind:          contracts.ManifestKindSuccess,
			SchemaVersion: "1",
			RunID:         runID,
			Pass:          pass,
			Agent:         agent,
			BranchName:    "auto-improve/fixture",
			HeadSHA:       "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			BaseSHA:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			DiffPath:      filepath.ToSlash(filepath.Join(prefix, "diff.patch")),
			SessionPath:   filepath.ToSlash(filepath.Join(prefix, "session.jsonl")),
			ChecklistPath: filepath.ToSlash(filepath.Join(prefix, "checklist-result.json")),
			PromptVersion: "stub-prompt-v1",
			StartedAt:     time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
			FinishedAt:    time.Date(2026, 4, 21, 0, 1, 0, 0, time.UTC),
		},
	}
	require.NoError(t, internalio.WriteJSONAtomic(manifestPath, manifest))
}

func writeManifestError(t *testing.T, runIO internalio.RunContext, runID contracts.RunID, pass int, agent contracts.AgentID) {
	t.Helper()

	manifestPath, err := runIO.ManifestPath(pass, agent)
	require.NoError(t, err)
	manifest := contracts.Manifest{
		Kind: contracts.ManifestKindError,
		Value: contracts.ManifestError{
			Kind:          contracts.ManifestKindError,
			SchemaVersion: "1",
			RunID:         runID,
			Pass:          pass,
			Agent:         agent,
			ExitCode:      1,
			Reason:        "unknown",
			Detail:        "fixture non-scorable manifest",
			StartedAt:     time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
			FinishedAt:    time.Date(2026, 4, 21, 0, 1, 0, 0, time.UTC),
		},
	}
	require.NoError(t, internalio.WriteJSONAtomic(manifestPath, manifest))
}

func writePass1Scores(t *testing.T, runIO internalio.RunContext, runID contracts.RunID, agents []contracts.AgentID) {
	t.Helper()

	path := mustResolve(t, runIO, "30/scores-A.jsonl")
	for _, agent := range agents {
		for _, entry := range primaryStubScores(runID, 1, agent) {
			require.NoError(t, internalio.AppendJSONL(path, entry))
		}
	}
}

func primaryStubScores(runID contracts.RunID, pass int, agent contracts.AgentID) []contracts.ScoreEntry {
	return []contracts.ScoreEntry{
		{SchemaVersion: "1", RunID: runID, Pass: pass, Agent: agent, Dimension: contracts.DimensionFidelity, Score: 84, Reasons: "stub primary fixture evaluated fidelity with deterministic phase-0 scoring.", VerdictPath: contracts.VerdictPathAgreement, RubricVersion: "default", PromptVersion: "phase0-stub", ResolvedAt: time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)},
		{SchemaVersion: "1", RunID: runID, Pass: pass, Agent: agent, Dimension: contracts.DimensionCorrectness, Score: 82, Reasons: "stub primary fixture evaluated correctness with deterministic phase-0 scoring.", VerdictPath: contracts.VerdictPathAgreement, RubricVersion: "default", PromptVersion: "phase0-stub", ResolvedAt: time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)},
		{SchemaVersion: "1", RunID: runID, Pass: pass, Agent: agent, Dimension: contracts.DimensionMaintainability, Score: 80, Reasons: "stub primary fixture evaluated maintainability with deterministic phase-0 scoring.", VerdictPath: contracts.VerdictPathAgreement, RubricVersion: "default", PromptVersion: "phase0-stub", ResolvedAt: time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)},
		{SchemaVersion: "1", RunID: runID, Pass: pass, Agent: agent, Dimension: contracts.DimensionDiscipline, Score: 86, Reasons: "stub primary fixture evaluated discipline with deterministic phase-0 scoring.", VerdictPath: contracts.VerdictPathAgreement, RubricVersion: "default", PromptVersion: "phase0-stub", ResolvedAt: time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)},
		{SchemaVersion: "1", RunID: runID, Pass: pass, Agent: agent, Dimension: contracts.DimensionCommunication, Score: 78, Reasons: "stub primary fixture evaluated communication with deterministic phase-0 scoring.", VerdictPath: contracts.VerdictPathAgreement, RubricVersion: "default", PromptVersion: "phase0-stub", ResolvedAt: time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)},
	}
}

func assertArtifactsByteIdentical(t *testing.T, left, right internalio.RunContext) {
	t.Helper()
	assert.Equal(t, readStep60Artifacts(t, left), readStep60Artifacts(t, right))
}

func readStep60Artifacts(t *testing.T, runIO internalio.RunContext) map[string][]byte {
	t.Helper()
	return map[string][]byte{
		"60/scores-B.jsonl":         mustReadFile(t, mustResolve(t, runIO, "60/scores-B.jsonl")),
		"60/compliance-B.jsonl":     mustReadFile(t, mustResolve(t, runIO, "60/compliance-B.jsonl")),
		"60/pairwise.jsonl":         mustReadFile(t, mustResolve(t, runIO, "60/pairwise.jsonl")),
		"60/scores-B-raw.jsonl":     mustReadFile(t, mustResolve(t, runIO, "60/scores-B-raw.jsonl")),
		"60/compliance-B-raw.jsonl": mustReadFile(t, mustResolve(t, runIO, "60/compliance-B-raw.jsonl")),
		"60/done.marker":            mustReadFile(t, mustResolve(t, runIO, "60/done.marker")),
	}
}

func mustResolve(t *testing.T, runIO internalio.RunContext, rel string) string {
	t.Helper()
	path, err := runIO.ResolveRunRelative(rel)
	require.NoError(t, err)
	return path
}

func mustReadJSON[T any](t *testing.T, path string) T {
	t.Helper()
	value, err := internalio.ReadJSON[T](path)
	require.NoError(t, err)
	return value
}

func mustReadJSONL[T any](t *testing.T, runIO internalio.RunContext, rel string) []T {
	t.Helper()
	rows, err := internalio.ReadJSONL[T](mustResolve(t, runIO, rel))
	require.NoError(t, err)
	return rows
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func mustHashFinalScores(t *testing.T, entries []contracts.ScoreEntry) string {
	t.Helper()
	hash, err := hashFinalScores(entries)
	require.NoError(t, err)
	return hash
}

func mustHashFinalCompliance(t *testing.T, entries []contracts.ComplianceEntry) string {
	t.Helper()
	hash, err := hashFinalCompliance(entries)
	require.NoError(t, err)
	return hash
}

func mustHashFinalPairwise(t *testing.T, entries []contracts.PairwiseEntry) string {
	t.Helper()
	hash, err := hashFinalPairwise(entries)
	require.NoError(t, err)
	return hash
}

func mustHashReducedRawScores(t *testing.T, runIO internalio.RunContext) string {
	t.Helper()
	hash, err := hashReducedRawScoresFile(mustResolve(t, runIO, "60/scores-B-raw.jsonl"))
	require.NoError(t, err)
	return hash
}

func mustHashReducedRawCompliance(t *testing.T, runIO internalio.RunContext) string {
	t.Helper()
	hash, err := hashReducedRawComplianceFile(mustResolve(t, runIO, "60/compliance-B-raw.jsonl"))
	require.NoError(t, err)
	return hash
}

func flipHexChar(value string) string {
	if value == "" {
		return value
	}
	if value[0] == '0' {
		return "1" + value[1:]
	}
	return "0" + value[1:]
}
