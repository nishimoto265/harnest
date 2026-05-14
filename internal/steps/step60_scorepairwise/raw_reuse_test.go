package step60_scorepairwise

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/judges"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	assert.Equal(t, before["60/scores-B-raw.jsonl"], after["60/scores-B-raw.jsonl"])
	assert.Equal(t, before["60/compliance-B-raw.jsonl"], after["60/compliance-B-raw.jsonl"])
	assert.Equal(t, beforeMarker.RawHashes, afterMarker.RawHashes)
	assert.Equal(t, beforeMarker.ContentHashes, afterMarker.ContentHashes)
}

func TestRun_RerunWithoutMarker_RebuildsFromRawWithoutRejudging(t *testing.T) {
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
	require.NoError(t, os.Remove(mustResolve(t, runIO, "60/done.marker")))

	paths, err := resolveStep60Paths(runIO)
	require.NoError(t, err)
	rawState, err := loadStep60RawState(paths)
	require.NoError(t, err)
	manifest, err := internalio.LoadScorableManifest(runIO, 2, "a1")
	require.NoError(t, err)
	outputPath, ok, err := resolveExistingManifestArtifact(runIO, manifest.DiffPath)
	require.NoError(t, err)
	require.True(t, ok)
	outputHash, err := fileSHA256(outputPath)
	require.NoError(t, err)
	_, reusable, err := tryReuseRawPanelResult(
		runIO,
		rawState,
		"a1",
		outputHash,
		"default",
		"phase0-stub",
		map[string]struct{}{"stub-rubric-rule": {}},
		true,
	)
	require.NoError(t, err)
	require.True(t, reusable)

	var called bool
	noJudge := unexpectedCallJudge{called: &called}
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     noJudge,
		Secondary:   noJudge,
		Arbiter:     noJudge,
		Now:         func() time.Time { return now },
	}))
	assert.False(t, called)
}

func TestRun_RerunWithoutMarker_PreservesSeparateScoreAndComplianceVerdictPaths(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	// F18: expected compliance coverage is derived from pass1 rows, so the
	// test must seed pass1 compliance for rule "disputed" for step60 to
	// admit the reuse path on a marker-less resume.
	writePass1Compliance(t, runIO, pkg.RunID, "a1", map[string]contracts.ComplianceVerdict{"disputed": contracts.ComplianceVerdictCompliant})
	now := time.Date(2026, 4, 21, 11, 35, 0, 0, time.UTC)

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary: scriptedJudge{
			score:        80,
			reasonPrefix: "primary",
			compliance:   map[string]contracts.ComplianceVerdict{"disputed": contracts.ComplianceVerdictViolated},
		},
		Secondary: scriptedJudge{
			score:        70,
			reasonPrefix: "secondary",
			compliance:   map[string]contracts.ComplianceVerdict{"disputed": contracts.ComplianceVerdictCompliant},
		},
		Arbiter: scriptedJudge{
			score:        80,
			reasonPrefix: "primary",
			compliance:   map[string]contracts.ComplianceVerdict{"disputed": contracts.ComplianceVerdictValidException},
		},
		Now: func() time.Time { return now },
	}))

	beforeScores := mustReadJSONL[contracts.ScoreEntry](t, runIO, "60/scores-B.jsonl")
	beforeCompliance := mustReadJSONL[contracts.ComplianceEntry](t, runIO, "60/compliance-B.jsonl")
	require.Len(t, beforeCompliance, 1)
	for _, row := range beforeScores {
		assert.Equal(t, contracts.VerdictPathArbitrated, row.VerdictPath)
	}
	assert.Equal(t, contracts.VerdictPathArbiterOverruled, beforeCompliance[0].VerdictPath)

	require.NoError(t, os.Remove(mustResolve(t, runIO, "60/done.marker")))

	var called bool
	noJudge := unexpectedCallJudge{called: &called}
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     noJudge,
		Secondary:   noJudge,
		Arbiter:     noJudge,
		Now:         func() time.Time { return now },
	}))
	assert.False(t, called)

	afterScores := mustReadJSONL[contracts.ScoreEntry](t, runIO, "60/scores-B.jsonl")
	afterCompliance := mustReadJSONL[contracts.ComplianceEntry](t, runIO, "60/compliance-B.jsonl")
	assert.Equal(t, beforeScores, afterScores)
	assert.Equal(t, beforeCompliance, afterCompliance)
}

func TestRun_RerunWithoutMarker_ReusesDisputedOnlyArbiterCompliance(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	// F18: pass1 must declare the rule-id coverage expected during step60
	// reuse. Without this seeding, the raw rows' "agreed"/"disputed" rule
	// IDs are treated as stale and judges are re-invoked.
	writePass1Compliance(t, runIO, pkg.RunID, "a1", map[string]contracts.ComplianceVerdict{
		"agreed":   contracts.ComplianceVerdictCompliant,
		"disputed": contracts.ComplianceVerdictCompliant,
	})
	now := time.Date(2026, 4, 21, 11, 40, 0, 0, time.UTC)

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary: scriptedJudge{
			score:        80,
			reasonPrefix: "primary",
			compliance: map[string]contracts.ComplianceVerdict{
				"agreed":   contracts.ComplianceVerdictCompliant,
				"disputed": contracts.ComplianceVerdictViolated,
			},
		},
		Secondary: scriptedJudge{
			score:        80,
			reasonPrefix: "secondary",
			compliance: map[string]contracts.ComplianceVerdict{
				"agreed":   contracts.ComplianceVerdictCompliant,
				"disputed": contracts.ComplianceVerdictValidException,
			},
		},
		Arbiter: scriptedJudge{
			score:        80,
			reasonPrefix: "arbiter",
			compliance: map[string]contracts.ComplianceVerdict{
				"disputed": contracts.ComplianceVerdictCompliant,
			},
		},
		Now: func() time.Time { return now },
	}))

	beforeCompliance := mustReadJSONL[contracts.ComplianceEntry](t, runIO, "60/compliance-B.jsonl")
	require.NoError(t, os.Remove(mustResolve(t, runIO, "60/done.marker")))

	var called bool
	noJudge := unexpectedCallJudge{called: &called}
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     noJudge,
		Secondary:   noJudge,
		Arbiter:     noJudge,
		Now:         func() time.Time { return now },
	}))
	assert.False(t, called)
	assert.Equal(t, beforeCompliance, mustReadJSONL[contracts.ComplianceEntry](t, runIO, "60/compliance-B.jsonl"))
}

func TestRun_RerunWithoutMarker_RejectsFullCoverageArbiterRawReuse(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	writePass1Compliance(t, runIO, pkg.RunID, "a1", map[string]contracts.ComplianceVerdict{
		"agreed":   contracts.ComplianceVerdictCompliant,
		"disputed": contracts.ComplianceVerdictCompliant,
	})
	now := time.Date(2026, 4, 21, 11, 42, 0, 0, time.UTC)

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary: scriptedJudge{
			score:        80,
			reasonPrefix: "primary",
			compliance: map[string]contracts.ComplianceVerdict{
				"agreed":   contracts.ComplianceVerdictCompliant,
				"disputed": contracts.ComplianceVerdictViolated,
			},
		},
		Secondary: scriptedJudge{
			score:        80,
			reasonPrefix: "secondary",
			compliance: map[string]contracts.ComplianceVerdict{
				"agreed":   contracts.ComplianceVerdictCompliant,
				"disputed": contracts.ComplianceVerdictValidException,
			},
		},
		Arbiter: scriptedJudge{
			score:        80,
			reasonPrefix: "arbiter",
			compliance: map[string]contracts.ComplianceVerdict{
				"disputed": contracts.ComplianceVerdictCompliant,
			},
		},
		Now: func() time.Time { return now },
	}))

	rawRows := mustReadJSONL[contracts.RawComplianceEntry](t, runIO, "60/compliance-B-raw.jsonl")
	var legacyFullCoverageRow contracts.RawComplianceEntry
	var primaryAgreed, secondaryAgreed contracts.RawComplianceEntry
	for _, row := range rawRows {
		if row.JudgeRole == contracts.JudgeRoleArbiter && row.RuleID == "disputed" {
			legacyFullCoverageRow = row
		}
		if row.JudgeRole == contracts.JudgeRolePrimary && row.RuleID == "agreed" {
			primaryAgreed = row
		}
		if row.JudgeRole == contracts.JudgeRoleSecondary && row.RuleID == "agreed" {
			secondaryAgreed = row
		}
	}
	require.NotEmpty(t, legacyFullCoverageRow.OutputSha256)
	primaryAgreedHash, err := rawComplianceEntryHash(primaryAgreed)
	require.NoError(t, err)
	secondaryAgreedHash, err := rawComplianceEntryHash(secondaryAgreed)
	require.NoError(t, err)
	legacyFullCoverageRow.RuleID = "agreed"
	legacyFullCoverageRow.Verdict = contracts.ComplianceVerdictCompliant
	legacyFullCoverageRow.Rationale = "legacy full-coverage arbiter row"
	legacyFullCoverageRow.PrimaryRef = &contracts.RawJudgeRef{Role: contracts.JudgeRolePrimary, Sha256: primaryAgreedHash}
	legacyFullCoverageRow.SecondaryRef = &contracts.RawJudgeRef{Role: contracts.JudgeRoleSecondary, Sha256: secondaryAgreedHash}
	require.NoError(t, internalio.AppendJSONL(mustResolve(t, runIO, "60/compliance-B-raw.jsonl"), legacyFullCoverageRow))
	require.NoError(t, os.Remove(mustResolve(t, runIO, "60/done.marker")))

	var called bool
	callTracker := unexpectedCallJudge{called: &called}
	err = Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     callTracker,
		Secondary:   callTracker,
		Arbiter:     callTracker,
		Now:         func() time.Time { return now.Add(time.Minute) },
	})
	require.Error(t, err)
	assert.True(t, called, "full-coverage arbiter raw rows must not satisfy strict disputed-only reuse")
}

func TestRun_RegeneratesOverflowSidecarsInsteadOfTrustingJudgeRefs(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	judge := overflowRefJudge{}

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judge,
		Secondary:   judge,
		Arbiter:     judge,
		Now:         func() time.Time { return time.Date(2026, 4, 21, 16, 45, 0, 0, time.UTC) },
	}))

	for _, row := range mustReadJSONL[contracts.RawScoreEntry](t, runIO, "60/scores-B-raw.jsonl") {
		assert.Nil(t, row.ReasonsOverflowRef)
	}
	for _, row := range mustReadJSONL[contracts.RawComplianceEntry](t, runIO, "60/compliance-B-raw.jsonl") {
		assert.Nil(t, row.RationaleOverflowRef)
	}
}

func TestRun_CorruptOverflowSidecarInvalidatesRawReuse(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	now := time.Date(2026, 4, 21, 17, 15, 0, 0, time.UTC)

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return now },
	}))
	require.NoError(t, os.Remove(mustResolve(t, runIO, "60/done.marker")))

	rawScoresPath := mustResolve(t, runIO, "60/scores-B-raw.jsonl")
	rawScores := mustReadJSONL[contracts.RawScoreEntry](t, runIO, "60/scores-B-raw.jsonl")
	require.NotEmpty(t, rawScores)
	ref, sidecarPath := writeStep60ReasonsSidecar(t, runIO, "overflow sidecar contents\n")
	rawScores[0].Reasons = ""
	rawScores[0].ReasonsOverflowRef = &ref
	rewriteRawScores(t, rawScoresPath, rawScores)
	require.NoError(t, os.WriteFile(sidecarPath, []byte("corrupt"), 0o644))

	counter := &countingJudge{delegate: judges.NewPrimaryStub()}
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     counter,
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return now },
	}))
	assert.Greater(t, counter.callCount(), int32(0))
}

func TestRun_CorruptOverflowSidecarInvalidatesDoneMarker(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})
	now := time.Date(2026, 4, 21, 17, 30, 0, 0, time.UTC)

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return now },
	}))

	rawScoresPath := mustResolve(t, runIO, "60/scores-B-raw.jsonl")
	rawScores := mustReadJSONL[contracts.RawScoreEntry](t, runIO, "60/scores-B-raw.jsonl")
	ref, sidecarPath := writeStep60ReasonsSidecar(t, runIO, "overflow sidecar contents\n")
	rawScores[0].Reasons = ""
	rawScores[0].ReasonsOverflowRef = &ref
	rewriteRawScores(t, rawScoresPath, rawScores)

	markerPath := mustResolve(t, runIO, "60/done.marker")
	marker := mustReadJSON[contracts.Step60DoneMarker](t, markerPath)
	marker.RawHashes.ScoresRaw = mustHashReducedRawScores(t, runIO)
	require.NoError(t, internalio.WriteJSONAtomic(markerPath, marker))
	require.NoError(t, os.WriteFile(sidecarPath, []byte("corrupt"), 0o644))

	counter := &countingJudge{delegate: judges.NewPrimaryStub()}
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     counter,
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return now },
	}))
	assert.Greater(t, counter.callCount(), int32(0))
}
