package step60_scorepairwise

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_JudgeSeesPinnedPass2SnapshotBytes(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})

	manifest, err := internalio.LoadScorableManifest(runIO, 2, "a1")
	require.NoError(t, err)
	liveDiff, err := runIO.ResolveRunRelative(manifest.DiffPath)
	require.NoError(t, err)
	liveBefore := mustReadFile(t, liveDiff)
	liveBeforeHash := sha256Hex(liveBefore)

	primary := &mutatingReadJudge{
		delegate: scriptedJudge{
			score:        80,
			reasonPrefix: "primary",
		},
		targetAgent: contracts.AgentID("a1"),
		mutatePath:  liveDiff,
		mutateBytes: []byte("mutated live pass2 diff\n"),
	}

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     primary,
		Secondary: scriptedJudge{
			score:        80,
			reasonPrefix: "secondary",
		},
		Arbiter: unexpectedCallJudge{},
		Now:     func() time.Time { return time.Date(2026, 4, 21, 15, 4, 5, 0, time.UTC) },
	}))

	assert.NotEqual(t, liveDiff, primary.seenPath, "judge must not be handed the live manifest diff")
	assert.Contains(t, primary.seenPath, "60/snapshots/", "OutputPath must live under the pinned snapshot dir")
	assert.Equal(t, liveBefore, primary.seenBytes, "judge-observed bytes must match the bytes that output_sha256 hashed")

	rawScores := mustReadJSONL[contracts.RawScoreEntry](t, runIO, "60/scores-B-raw.jsonl")
	require.NotEmpty(t, rawScores)
	rawOutputHashes := make(map[contracts.AgentID]string)
	for _, row := range rawScores {
		if previous, ok := rawOutputHashes[row.Agent]; ok {
			assert.Equal(t, previous, row.OutputSha256)
		} else {
			rawOutputHashes[row.Agent] = row.OutputSha256
		}
		if row.Agent != "a1" {
			continue
		}
		assert.Equal(t, liveBeforeHash, row.OutputSha256)
		assert.Equal(t, sha256Hex(primary.seenBytes), row.OutputSha256)
	}

	rawCompliance := mustReadJSONL[contracts.RawComplianceEntry](t, runIO, "60/compliance-B-raw.jsonl")
	require.NotEmpty(t, rawCompliance)
	for _, row := range rawCompliance {
		if row.Agent != "a1" {
			continue
		}
		assert.Equal(t, liveBeforeHash, row.OutputSha256)
		assert.Equal(t, sha256Hex(primary.seenBytes), row.OutputSha256)
	}

	marker := mustReadJSON[contracts.Step60DoneMarker](t, mustResolve(t, runIO, "60/done.marker"))
	require.NoError(t, marker.Validate())
	expectedPass2OutputsHash, err := hashPass2OutputHashes(rawOutputHashes)
	require.NoError(t, err)
	assert.Equal(t, expectedPass2OutputsHash, marker.InputHashes.Pass2Outputs)
	assert.Equal(t, mustHashReducedRawScores(t, runIO), marker.RawHashes.ScoresRaw)
	assert.Equal(t, mustHashReducedRawCompliance(t, runIO), marker.RawHashes.ComplianceRaw)
	assert.Equal(t, []byte("mutated live pass2 diff\n"), mustReadFile(t, liveDiff))
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

func TestRun_DeclaredScorableAgentsFailClosedWhenPass1ManifestMissing(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		agents:          []contracts.AgentID{"a1", "a2", "a3"},
		writePass1Score: true,
	})
	manifestPath, err := runIO.ManifestPath(1, "a1")
	require.NoError(t, err)
	require.NoError(t, os.Remove(manifestPath))

	err = Run(context.Background(), Input{
		IO:             runIO,
		TaskPackage:    &pkg,
		ScorableAgents: []contracts.AgentID{"a1", "a2", "a3"},
		Primary:        judges.NewPrimaryStub(),
		Secondary:      judges.NewSecondaryStub(),
		Arbiter:        judges.NewArbiterStub(),
		Now:            func() time.Time { return time.Date(2026, 4, 21, 12, 30, 0, 0, time.UTC) },
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pass2 scorable agent missing matching pass1 scorable manifest")
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

func TestRun_RebuildsWhenCompletedAgentsNoLongerMatchCurrentScorableSet(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})
	now := time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC)
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return now },
	}))
	writeManifestError(t, runIO, pkg.RunID, 2, "a3")

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

	marker := mustReadJSON[contracts.Step60DoneMarker](t, mustResolve(t, runIO, "60/done.marker"))
	assert.Equal(t, []contracts.AgentID{"a1", "a2"}, marker.CompletedAgents)
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

func TestRun_ValidatesPass2SessionAndChecklistManifestArtifactsBeforeSnapshot(t *testing.T) {
	cases := []struct {
		name       string
		removePath string
		wantError  string
	}{
		{
			name:       "session",
			removePath: "50-pass2/a1/session.jsonl",
			wantError:  "missing declared pass2 session artifact",
		},
		{
			name:       "checklist",
			removePath: "50-pass2/a1/checklist-result.json",
			wantError:  "missing declared pass2 checklist artifact",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runIO, pkg := seedStep60Fixture(t, fixtureOptions{
				agents:          []contracts.AgentID{"a1", "a2", "a3"},
				writePass1Score: true,
			})
			require.NoError(t, os.Remove(mustResolve(t, runIO, tc.removePath)))

			err := Run(context.Background(), Input{
				IO:          runIO,
				TaskPackage: &pkg,
				Primary:     judges.NewPrimaryStub(),
				Secondary:   judges.NewSecondaryStub(),
				Arbiter:     judges.NewArbiterStub(),
				Now:         func() time.Time { return time.Date(2026, 4, 21, 19, 0, 0, 0, time.UTC) },
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantError)
			assert.NoDirExists(t, mustResolve(t, runIO, "60/snapshots"))
			assert.NoFileExists(t, mustResolve(t, runIO, "60/done.marker"))
		})
	}
}

func TestRun_SkipsPass2AgentWhenPass1IsNotScorable(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass1Agents: map[contracts.AgentID]bool{"a2": true},
	})

	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return time.Date(2026, 4, 21, 19, 30, 0, 0, time.UTC) },
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pass2 scorable agent missing matching pass1 scorable manifest")
	assert.NoFileExists(t, mustResolve(t, runIO, "60/done.marker"))
}

func TestRun_FailsWhenPass2ScorableButPass1ManifestMissing(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})

	manifestPath, err := runIO.ManifestPath(1, "a2")
	require.NoError(t, err)
	require.NoError(t, os.Remove(manifestPath))

	err = Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return time.Date(2026, 4, 21, 19, 35, 0, 0, time.UTC) },
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pass2 scorable agent missing matching pass1 scorable manifest")
	assert.NoFileExists(t, mustResolve(t, runIO, "60/done.marker"))
}
