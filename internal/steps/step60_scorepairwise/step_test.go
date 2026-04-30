package step60_scorepairwise

import (
	"context"
	"encoding/json"
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

func TestPairwiseEntriesFromDecision_NormalizesTieMargin(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{agents: []contracts.AgentID{"a1", "a2", "a3"}})
	resolvedAt := time.Date(2026, 4, 21, 15, 4, 5, 0, time.UTC)

	entries, err := pairwiseEntriesFromDecision(Input{
		IO:                    runIO,
		TaskPackage:           &pkg,
		PairwiseMode:          judges.PairwiseModeBasic,
		RubricVersion:         "default",
		PairwisePromptVersion: "pairwise-test",
	}, []judges.PairwisePair{{
		Agent: "a1",
	}}, judges.PairwiseDecision{
		Action: judges.PairwiseDecisionInconclusive,
		AgentDecisions: []judges.PairwiseAgentDecision{{
			Agent:         "a1",
			Winner:        contracts.PairwiseWinnerB,
			Margin:        contracts.PairwiseMarginDecisive,
			Justification: "pass2 won local comparison",
		}},
	}, nil, resolvedAt)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, contracts.PairwiseWinnerTie, entries[0].Winner)
	assert.Equal(t, contracts.PairwiseMarginSlight, entries[0].Margin)
}

func TestRun_PairwiseModesControlJudgeFanout(t *testing.T) {
	tests := []struct {
		name            string
		mode            judges.PairwiseMode
		wantComparisons int
		wantOrders      []string
	}{
		{
			name:            "single",
			mode:            judges.PairwiseModeSingle,
			wantComparisons: 0,
		},
		{
			name:            "basic",
			mode:            judges.PairwiseModeBasic,
			wantComparisons: 3,
			wantOrders:      []string{"a1:AB", "a2:AB", "a3:AB"},
		},
		{
			name:            "strict",
			mode:            judges.PairwiseModeStrict,
			wantComparisons: 6,
			wantOrders:      []string{"a1:AB", "a1:BA", "a2:AB", "a2:BA", "a3:AB", "a3:BA"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runIO, pkg := seedStep60Fixture(t, fixtureOptions{
				agents:          []contracts.AgentID{"a1", "a2", "a3"},
				writePass1Score: true,
			})
			pairwiseJudge := &recordingPairwiseJudge{}
			decisionJudge := &recordingPairwiseDecisionJudge{}

			require.NoError(t, Run(context.Background(), Input{
				IO:                    runIO,
				TaskPackage:           &pkg,
				PairwiseMode:          tt.mode,
				PairwiseJudge:         pairwiseJudge,
				PairwiseDecisionJudge: decisionJudge,
				Now:                   func() time.Time { return time.Date(2026, 4, 21, 15, 4, 5, 0, time.UTC) },
			}))

			assert.Equal(t, tt.wantOrders, pairwiseJudge.orders)
			assert.Equal(t, 1, decisionJudge.calls)
			assert.Equal(t, tt.mode, decisionJudge.mode)
			assert.Equal(t, 3, decisionJudge.pairCount)
			assert.Equal(t, tt.wantComparisons, decisionJudge.comparisonCount)

			pairwise := mustReadJSONL[contracts.PairwiseEntry](t, runIO, "60/pairwise.jsonl")
			require.Len(t, pairwise, 3)
			for _, entry := range pairwise {
				assert.Equal(t, contracts.PairwiseWinnerB, entry.Winner)
				assert.Equal(t, contracts.PairwiseMarginClear, entry.Margin)
				assert.Contains(t, entry.Justification, "mode="+string(tt.mode)+" decision=adopt")
				assert.Contains(t, entry.Justification, "final=recorded final decision")
			}
		})
	}
}

func TestRun_Pass2JudgeInputIncludesCandidateRules(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})

	candidateRules := []judges.CandidateRule{{
		ID:    "cand-1",
		Kind:  "new",
		Title: "Candidate rule",
		Body:  "When message changes, details must change too.",
	}}
	primary := &echoExpectedComplianceJudge{score: 90}
	secondary := &echoExpectedComplianceJudge{score: 90}
	require.NoError(t, Run(context.Background(), Input{
		IO:             runIO,
		TaskPackage:    &pkg,
		Primary:        primary,
		Secondary:      secondary,
		Arbiter:        judges.NewArbiterStub(),
		CandidateRules: candidateRules,
		Now:            func() time.Time { return time.Date(2026, 4, 21, 15, 4, 5, 0, time.UTC) },
	}))

	require.NotEmpty(t, primary.inputs)
	require.NotEmpty(t, secondary.inputs)
	assert.Equal(t, candidateRules, primary.inputs[0].CandidateRules)
	assert.Contains(t, primary.inputs[0].ExpectedComplianceRuleIDs, "cand-1")
	assert.Contains(t, secondary.inputs[0].ExpectedComplianceRuleIDs, "cand-1")

	finalCompliance := mustReadJSONL[contracts.ComplianceEntry](t, runIO, "60/compliance-B.jsonl")
	finalRuleIDs := make([]string, 0, len(finalCompliance))
	var found bool
	for _, row := range finalCompliance {
		finalRuleIDs = append(finalRuleIDs, row.RuleID)
		if row.RuleID == "cand-1" {
			found = true
			assert.Equal(t, contracts.ComplianceVerdictCompliant, row.Verdict)
		}
	}
	sort.Strings(finalRuleIDs)
	assert.Equal(t, []string{"cand-1", "cand-1", "cand-1"}, finalRuleIDs)
	assert.True(t, found)
}

func TestRun_RejectsMissingExpectedCandidateComplianceRule(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})

	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary: scriptedJudge{
			score:            80,
			reasonPrefix:     "primary",
			compliance:       map[string]contracts.ComplianceVerdict{},
			strictCompliance: true,
		},
		Secondary: judges.NewSecondaryStub(),
		Arbiter:   judges.NewArbiterStub(),
		CandidateRules: []judges.CandidateRule{{
			ID:    "cand-1",
			Kind:  "new",
			Title: "Candidate rule",
			Body:  "Rule body",
		}},
		Now: func() time.Time { return time.Date(2026, 4, 21, 15, 4, 5, 0, time.UTC) },
	})
	require.ErrorIs(t, err, judges.ErrJudgeOutputMissingCompliance)
	assert.ErrorContains(t, err, "cand-1")
}

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

func TestRun_RejectsMissingExpectedActiveAndPass1ComplianceRules(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})
	writePass1Compliance(t, runIO, pkg.RunID, "a1", map[string]contracts.ComplianceVerdict{
		"pass1-rule": contracts.ComplianceVerdictCompliant,
	})
	rubricPath := filepath.Join(t.TempDir(), "rubric.md")
	require.NoError(t, os.WriteFile(rubricPath, []byte("## Active Rule IDs\n- active-rule\n"), 0o644))

	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		RubricPath:  rubricPath,
		Primary: scriptedJudge{
			score:        80,
			reasonPrefix: "primary",
			compliance: map[string]contracts.ComplianceVerdict{
				"active-rule": contracts.ComplianceVerdictCompliant,
			},
			strictCompliance: true,
		},
		Secondary: judges.NewSecondaryStub(),
		Arbiter:   judges.NewArbiterStub(),
		Now:       func() time.Time { return time.Date(2026, 4, 21, 15, 4, 5, 0, time.UTC) },
	})
	require.ErrorIs(t, err, judges.ErrJudgeOutputMissingCompliance)
	assert.ErrorContains(t, err, "pass1-rule")
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

// TestExpectedComplianceRuleIDsForAgent_IgnoresRawRuleIDs is the F18
// contract: raw rows may contain historical rule IDs that no longer appear
// in pass1. They MUST NOT authorize themselves during reuse — the expected
// set is derived purely from the current pass1 rules (falling back to the
// rubric default when pass1 is silent).
func TestExpectedComplianceRuleIDsForAgent_IgnoresRawRuleIDs(t *testing.T) {
	t.Run("pass1 drives coverage", func(t *testing.T) {
		got := expectedComplianceRuleIDsForAgent(
			"a1",
			map[contracts.AgentID]map[string]struct{}{
				"a1": {"pass1-rule": {}},
			},
			nil,
			[]string{"fallback-rule"},
			nil,
		)
		assert.Equal(t, map[string]struct{}{"pass1-rule": {}}, got)
	})

	t.Run("fallback used when pass1 silent", func(t *testing.T) {
		got := expectedComplianceRuleIDsForAgent(
			"a1",
			map[contracts.AgentID]map[string]struct{}{},
			nil,
			[]string{"fallback-rule"},
			nil,
		)
		assert.Equal(t, map[string]struct{}{"fallback-rule": {}}, got)
	})

	t.Run("empty pass1 and empty fallback yields nil", func(t *testing.T) {
		got := expectedComplianceRuleIDsForAgent(
			"a1",
			map[contracts.AgentID]map[string]struct{}{},
			nil,
			nil,
			nil,
		)
		assert.Nil(t, got)
	})

	t.Run("candidate rules are always included", func(t *testing.T) {
		got := expectedComplianceRuleIDsForAgent(
			"a1",
			map[contracts.AgentID]map[string]struct{}{
				"a1": {"pass1-rule": {}},
			},
			nil,
			[]string{"fallback-rule"},
			[]judges.CandidateRule{{ID: "cand-1"}},
		)
		assert.Equal(t, map[string]struct{}{"pass1-rule": {}, "cand-1": {}}, got)
	})

	t.Run("active rules are included with pass1 and candidate rules", func(t *testing.T) {
		got := expectedComplianceRuleIDsForAgent(
			"a1",
			map[contracts.AgentID]map[string]struct{}{
				"a1": {"pass1-rule": {}},
			},
			[]string{"active-rule"},
			[]string{"fallback-rule"},
			[]judges.CandidateRule{{ID: "cand-1"}},
		)
		assert.Equal(t, map[string]struct{}{"active-rule": {}, "pass1-rule": {}, "cand-1": {}}, got)
	})
}

// TestRun_RerunRejectsRawOnlyRuleIDsAfterPass1Shrink exercises F18 end-to-end:
// step60 first writes raw rows for rule "stale-rule", then pass1 no longer
// declares any compliance rule. On a marker-less resume the raw-only rule
// must not self-authorize the reuse path; judges must be re-invoked so the
// stale compliance evidence is refreshed.
func TestRun_RerunRejectsRawOnlyRuleIDsAfterPass1Shrink(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	writePass1Compliance(t, runIO, pkg.RunID, "a1", map[string]contracts.ComplianceVerdict{"stale-rule": contracts.ComplianceVerdictCompliant})
	now := time.Date(2026, 4, 21, 11, 45, 0, 0, time.UTC)

	// First run: emit raw rows for "stale-rule".
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     scriptedJudge{score: 80, reasonPrefix: "primary", compliance: map[string]contracts.ComplianceVerdict{"stale-rule": contracts.ComplianceVerdictCompliant}},
		Secondary:   scriptedJudge{score: 79, reasonPrefix: "secondary", compliance: map[string]contracts.ComplianceVerdict{"stale-rule": contracts.ComplianceVerdictCompliant}},
		Arbiter:     scriptedJudge{score: 78, reasonPrefix: "arbiter", compliance: map[string]contracts.ComplianceVerdict{"stale-rule": contracts.ComplianceVerdictCompliant}},
		Now:         func() time.Time { return now },
	}))

	// Simulate pass1 shrinking: delete the pass1 compliance file entirely.
	// The raw rows for "stale-rule" still sit under 60/.
	require.NoError(t, os.Remove(mustResolve(t, runIO, "30/compliance-A.jsonl")))
	require.NoError(t, os.Remove(mustResolve(t, runIO, "60/done.marker")))

	// A rubric fallback provides coverage for stubRuleID, which is not in
	// the raw rows. So the rerun must re-invoke the judges to regenerate
	// coverage for the new expected rule set rather than trusting
	// "stale-rule" raw rows.
	var called bool
	callTracker := unexpectedCallJudge{called: &called}
	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     callTracker,
		Secondary:   callTracker,
		Arbiter:     callTracker,
		Now:         func() time.Time { return now.Add(time.Minute) },
	})
	// The test asserts the judges are re-invoked — unexpectedCallJudge
	// fails the run, which confirms F18 forced a rejudge instead of
	// silently reusing stale raw compliance.
	require.Error(t, err)
	assert.True(t, called, "F18: raw-only rule IDs must not authorize themselves; judges must run")
}

func TestRun_RerunWithoutMarker_IgnoresStaleFinalComplianceRows(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})
	now := time.Date(2026, 4, 21, 11, 45, 0, 0, time.UTC)

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return now },
	}))
	require.NoError(t, os.Remove(mustResolve(t, runIO, "60/done.marker")))
	require.NoError(t, internalio.AppendJSONL(mustResolve(t, runIO, "60/compliance-B.jsonl"), contracts.ComplianceEntry{
		SchemaVersion: "1",
		RunID:         pkg.RunID,
		Pass:          2,
		Agent:         "a1",
		RuleID:        "stale-only",
		Verdict:       contracts.ComplianceVerdictViolated,
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "default",
		PromptVersion: "phase0-stub",
		ResolvedAt:    now,
	}))

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

func TestRun_RerunsWhenRubricVersionChanges(t *testing.T) {
	// Pass1 must already share step60's scoring versions; otherwise F8 fails
	// closed before rerun logic is exercised.
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:    true,
		pass1RubricVersion: "rubric-v1",
		pass1PromptVersion: "prompt-v1",
	})
	now := time.Date(2026, 4, 21, 11, 30, 0, 0, time.UTC)
	agents := []contracts.AgentID{"a1", "a2", "a3"}

	require.NoError(t, Run(context.Background(), Input{
		IO:            runIO,
		TaskPackage:   &pkg,
		RubricVersion: "rubric-v1",
		PromptVersion: "prompt-v1",
		Primary:       judges.NewPrimaryStub(),
		Secondary:     judges.NewSecondaryStub(),
		Arbiter:       judges.NewArbiterStub(),
		Now:           func() time.Time { return now },
	}))

	// Simulate step30 being rerun at the new rubric version before step60
	// reruns — F8 demands matching pass1 version metadata.
	rewritePass1ScoresAt(t, runIO, pkg.RunID, agents, "rubric-v2", "prompt-v1")

	var called bool
	noJudge := unexpectedCallJudge{called: &called}
	err := Run(context.Background(), Input{
		IO:            runIO,
		TaskPackage:   &pkg,
		RubricVersion: "rubric-v2",
		PromptVersion: "prompt-v1",
		Primary:       noJudge,
		Secondary:     noJudge,
		Arbiter:       noJudge,
		Now:           func() time.Time { return now },
	})
	require.ErrorContains(t, err, "unexpected judge call")
	assert.True(t, called)
}

func TestRun_DerivesMissingRubricVersionFromPass1Scores(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:    true,
		pass1RubricVersion: "rubric-hash-v1",
		pass1PromptVersion: "prompt-hash-v1",
	})
	now := time.Date(2026, 4, 21, 11, 35, 0, 0, time.UTC)

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return now },
	}))

	scores := mustReadJSONL[contracts.ScoreEntry](t, runIO, "60/scores-B.jsonl")
	require.NotEmpty(t, scores)
	for _, score := range scores {
		assert.Equal(t, "rubric-hash-v1", score.RubricVersion)
		assert.Equal(t, "prompt-hash-v1", score.PromptVersion)
	}
}

// TestRun_FailsClosedOnPass1VersionMismatch is the F8 contract: when pass1
// scores were generated under a different rubric/prompt version than step60
// is running, pairwise winner classification is meaningless, so Run must
// abort with ErrPass1VersionMismatch before invoking any judge.
func TestRun_FailsClosedOnPass1VersionMismatch(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:    true,
		pass1RubricVersion: "rubric-v1",
		pass1PromptVersion: "prompt-v1",
	})
	now := time.Date(2026, 4, 21, 11, 30, 0, 0, time.UTC)

	var called bool
	noJudge := unexpectedCallJudge{called: &called}
	err := Run(context.Background(), Input{
		IO:            runIO,
		TaskPackage:   &pkg,
		RubricVersion: "rubric-v2",
		PromptVersion: "prompt-v1",
		Primary:       noJudge,
		Secondary:     noJudge,
		Arbiter:       noJudge,
		Now:           func() time.Time { return now },
	})
	require.ErrorIs(t, err, ErrPass1VersionMismatch)
	assert.False(t, called, "judges must not run before pass1 version gate passes")
}

func TestRun_FailsClosedWhenJudgeProviderPromptVersionChanges(t *testing.T) {
	promptV1 := judges.PanelPromptVersion(
		"phase0-stub",
		versionedJudge{delegate: judges.NewPrimaryStub(), version: "provider-a"},
		versionedJudge{delegate: judges.NewSecondaryStub(), version: "provider-a"},
		versionedJudge{delegate: judges.NewArbiterStub(), version: "provider-a"},
	)
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:    true,
		pass1PromptVersion: promptV1,
	})
	now := time.Date(2026, 4, 21, 11, 30, 0, 0, time.UTC)

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     versionedJudge{delegate: judges.NewPrimaryStub(), version: "provider-a"},
		Secondary:   versionedJudge{delegate: judges.NewSecondaryStub(), version: "provider-a"},
		Arbiter:     versionedJudge{delegate: judges.NewArbiterStub(), version: "provider-a"},
		Now:         func() time.Time { return now },
	}))

	var called bool
	noJudge := versionedJudge{delegate: unexpectedCallJudge{called: &called}, version: "provider-b"}
	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     noJudge,
		Secondary:   noJudge,
		Arbiter:     noJudge,
		Now:         func() time.Time { return now },
	})
	require.ErrorIs(t, err, ErrPass1VersionMismatch)
	assert.False(t, called)
}

func TestRun_IgnoresHistoricalRawVersionsAfterMigration(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:    true,
		pass1RubricVersion: "rubric-v1",
		pass1PromptVersion: "prompt-v1",
	})
	now := time.Date(2026, 4, 21, 11, 30, 0, 0, time.UTC)
	agents := []contracts.AgentID{"a1", "a2", "a3"}

	require.NoError(t, Run(context.Background(), Input{
		IO:            runIO,
		TaskPackage:   &pkg,
		RubricVersion: "rubric-v1",
		PromptVersion: "prompt-v1",
		Primary:       judges.NewPrimaryStub(),
		Secondary:     judges.NewSecondaryStub(),
		Arbiter:       judges.NewArbiterStub(),
		Now:           func() time.Time { return now },
	}))

	// Simulate step30 being rerun under the new rubric/prompt before step60
	// migrates forward. Without this step, F8 fails closed.
	rewritePass1ScoresAt(t, runIO, pkg.RunID, agents, "rubric-v2", "prompt-v2")

	require.NoError(t, Run(context.Background(), Input{
		IO:            runIO,
		TaskPackage:   &pkg,
		RubricVersion: "rubric-v2",
		PromptVersion: "prompt-v2",
		Primary:       judges.NewPrimaryStub(),
		Secondary:     judges.NewSecondaryStub(),
		Arbiter:       judges.NewArbiterStub(),
		Now:           func() time.Time { return now.Add(time.Hour) },
	}))

	var called bool
	noJudge := unexpectedCallJudge{called: &called}
	require.NoError(t, Run(context.Background(), Input{
		IO:            runIO,
		TaskPackage:   &pkg,
		RubricVersion: "rubric-v2",
		PromptVersion: "prompt-v2",
		Primary:       noJudge,
		Secondary:     noJudge,
		Arbiter:       noJudge,
		Now:           func() time.Time { return now.Add(2 * time.Hour) },
	}))
	assert.False(t, called)
}

// step30 appends a fresh versioned pass1 row set without truncating the
// historical rows. step60 must check the collapsed effective pass1 rows so
// a valid resume after that bump does not fail closed on the superseded
// old-version entries that still remain on disk.
func TestRun_AcceptsPass1AppendOnlyAfterVersionBump(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:    true,
		pass1RubricVersion: "rubric-v1",
		pass1PromptVersion: "prompt-v1",
	})
	agents := []contracts.AgentID{"a1", "a2", "a3"}

	scoresPath := mustResolve(t, runIO, "30/scores-A.jsonl")
	for _, agent := range agents {
		for _, entry := range primaryStubScores(pkg.RunID, 1, agent) {
			entry.RubricVersion = "rubric-v2"
			entry.PromptVersion = "prompt-v2"
			require.NoError(t, internalio.AppendJSONL(scoresPath, entry))
		}
	}

	rows, err := internalio.ReadJSONL[contracts.ScoreEntry](scoresPath)
	require.NoError(t, err)
	assert.Equal(t, 2*len(agents)*len(canonicalDimensions), len(rows))

	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	require.NoError(t, Run(context.Background(), Input{
		IO:            runIO,
		TaskPackage:   &pkg,
		RubricVersion: "rubric-v2",
		PromptVersion: "prompt-v2",
		Primary:       judges.NewPrimaryStub(),
		Secondary:     judges.NewSecondaryStub(),
		Arbiter:       judges.NewArbiterStub(),
		Now:           func() time.Time { return now },
	}))
}

func TestRun_RebuildsWhenRawComplianceCoverageIsMissing(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	now := time.Date(2026, 4, 21, 11, 30, 0, 0, time.UTC)
	primary := scriptedJudge{score: 80, reasonPrefix: "primary", compliance: map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant}}
	secondary := scriptedJudge{score: 79, reasonPrefix: "secondary", compliance: map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant}}
	arbiter := scriptedJudge{score: 78, reasonPrefix: "arbiter", compliance: map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant}}
	writePass1Compliance(t, runIO, pkg.RunID, "a1", primary.compliance)

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     primary,
		Secondary:   secondary,
		Arbiter:     arbiter,
		Now:         func() time.Time { return now },
	}))
	require.NoError(t, os.Remove(mustResolve(t, runIO, "60/compliance-B-raw.jsonl")))
	require.NoError(t, os.Remove(mustResolve(t, runIO, "60/done.marker")))

	counter := &countingJudge{delegate: primary}
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     counter,
		Secondary:   secondary,
		Arbiter:     arbiter,
		Now:         func() time.Time { return now },
	}))
	assert.Greater(t, counter.callCount(), int32(0))
}

func TestRun_RebuildsWhenRawComplianceCoverageIsPartial(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	now := time.Date(2026, 4, 21, 11, 45, 0, 0, time.UTC)
	compliance := map[string]contracts.ComplianceVerdict{
		"rule-a": contracts.ComplianceVerdictCompliant,
		"rule-b": contracts.ComplianceVerdictCompliant,
		"rule-c": contracts.ComplianceVerdictCompliant,
		"rule-d": contracts.ComplianceVerdictCompliant,
		"rule-e": contracts.ComplianceVerdictCompliant,
	}
	primary := scriptedJudge{score: 80, reasonPrefix: "primary", compliance: compliance}
	secondary := scriptedJudge{score: 79, reasonPrefix: "secondary", compliance: compliance}
	arbiter := scriptedJudge{score: 78, reasonPrefix: "arbiter", compliance: compliance}
	writePass1Compliance(t, runIO, pkg.RunID, "a1", compliance)

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     primary,
		Secondary:   secondary,
		Arbiter:     arbiter,
		Now:         func() time.Time { return now },
	}))

	rawCompliancePath := mustResolve(t, runIO, "60/compliance-B-raw.jsonl")
	rawCompliance := mustReadJSONL[contracts.RawComplianceEntry](t, runIO, "60/compliance-B-raw.jsonl")
	truncated := make([]contracts.RawComplianceEntry, 0, 4)
	for _, row := range rawCompliance {
		if row.RuleID == "rule-a" || row.RuleID == "rule-b" {
			truncated = append(truncated, row)
		}
	}
	require.Len(t, truncated, 4)
	rewriteRawCompliance(t, rawCompliancePath, truncated)
	require.NoError(t, os.Remove(mustResolve(t, runIO, "60/done.marker")))

	counter := &countingJudge{delegate: primary}
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     counter,
		Secondary:   secondary,
		Arbiter:     arbiter,
		Now:         func() time.Time { return now },
	}))

	assert.Greater(t, counter.callCount(), int32(0))
	assert.Len(t, mustReadJSONL[contracts.ComplianceEntry](t, runIO, "60/compliance-B.jsonl"), 5)
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
	rubricPath := writeEmptyRubric(t)

	primary := scriptedJudge{
		score:            80,
		reasonPrefix:     "primary",
		strictCompliance: true,
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

	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		RubricPath:  rubricPath,
		Primary:     primary,
		Secondary:   secondary,
		Arbiter:     arbiter,
		Now:         func() time.Time { return time.Date(2026, 4, 21, 16, 0, 0, 0, time.UTC) },
	})
	require.ErrorIs(t, err, judges.ErrJudgeOutputUnexpectedCompliance)
	assert.NoFileExists(t, mustResolve(t, runIO, "60/done.marker"))
}

func TestRun_ComplianceArbiterMayCoverOnlyDisputedRules(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	writePass1Compliance(t, runIO, pkg.RunID, "a1", map[string]contracts.ComplianceVerdict{
		"agreed":   contracts.ComplianceVerdictCompliant,
		"disputed": contracts.ComplianceVerdictCompliant,
	})

	primary := scriptedJudge{
		score:        80,
		reasonPrefix: "primary",
		compliance: map[string]contracts.ComplianceVerdict{
			"agreed":   contracts.ComplianceVerdictCompliant,
			"disputed": contracts.ComplianceVerdictViolated,
		},
	}
	secondary := scriptedJudge{
		score:        80,
		reasonPrefix: "secondary",
		compliance: map[string]contracts.ComplianceVerdict{
			"agreed":   contracts.ComplianceVerdictCompliant,
			"disputed": contracts.ComplianceVerdictValidException,
		},
	}
	arbiter := scriptedJudge{
		score:        80,
		reasonPrefix: "arbiter",
		compliance: map[string]contracts.ComplianceVerdict{
			"disputed": contracts.ComplianceVerdictCompliant,
		},
	}

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     primary,
		Secondary:   secondary,
		Arbiter:     arbiter,
		Now:         func() time.Time { return time.Date(2026, 4, 21, 16, 15, 0, 0, time.UTC) },
	}))

	rows := mustReadJSONL[contracts.RawComplianceEntry](t, runIO, "60/compliance-B-raw.jsonl")
	var arbiterRuleIDs []string
	for _, row := range rows {
		if row.JudgeRole == contracts.JudgeRoleArbiter {
			arbiterRuleIDs = append(arbiterRuleIDs, row.RuleID)
		}
	}
	assert.Equal(t, []string{"disputed"}, arbiterRuleIDs)
}

func TestRun_ComplianceArbiterOnlyRuleFinalizesAsSingleSource(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	rubricPath := writeEmptyRubric(t)

	primary := scriptedJudge{
		score:            80,
		reasonPrefix:     "primary",
		strictCompliance: true,
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

	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		RubricPath:  rubricPath,
		Primary:     primary,
		Secondary:   secondary,
		Arbiter:     arbiter,
		Now:         func() time.Time { return time.Date(2026, 4, 21, 16, 30, 0, 0, time.UTC) },
	})
	require.ErrorIs(t, err, judges.ErrJudgeOutputUnexpectedCompliance)
	assert.NoFileExists(t, mustResolve(t, runIO, "60/done.marker"))
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

func TestRun_FailsClosedOnComplianceRuleSetMismatch(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	rubricPath := writeEmptyRubric(t)

	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		RubricPath:  rubricPath,
		Primary: scriptedJudge{
			score:            80,
			reasonPrefix:     "primary",
			compliance:       map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictViolated},
			strictCompliance: true,
		},
		Secondary: scriptedJudge{
			score:        80,
			reasonPrefix: "secondary",
			compliance:   map[string]contracts.ComplianceVerdict{},
		},
		Arbiter: scriptedJudge{
			score:        80,
			reasonPrefix: "arbiter",
			compliance:   map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant},
		},
		Now: func() time.Time { return time.Date(2026, 4, 21, 17, 45, 0, 0, time.UTC) },
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, judges.ErrJudgeOutputUnexpectedCompliance)
	assert.NoFileExists(t, mustResolve(t, runIO, "60/done.marker"))
}

func TestRun_RejectsDuplicateComplianceRuleIDsFromJudgeOutput(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     duplicateComplianceJudge{},
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return time.Date(2026, 4, 21, 17, 50, 0, 0, time.UTC) },
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, judges.ErrJudgeOutputDuplicateCompliance)
}

func TestNormalizeCompliance_RejectsDuplicateRuleIDs(t *testing.T) {
	runIO, _ := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})
	_, err := normalizeCompliance(runIO, []contracts.ComplianceEntry{
		{
			SchemaVersion: "1",
			RunID:         runIO.RunID,
			Pass:          2,
			Agent:         "a1",
			RuleID:        "rule-x",
			Verdict:       contracts.ComplianceVerdictViolated,
			Rationale:     "first",
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "r1",
			PromptVersion: "p1",
			ResolvedAt:    time.Date(2026, 4, 21, 17, 51, 0, 0, time.UTC),
		},
		{
			SchemaVersion: "1",
			RunID:         runIO.RunID,
			Pass:          2,
			Agent:         "a1",
			RuleID:        "rule-x",
			Verdict:       contracts.ComplianceVerdictCompliant,
			Rationale:     "second",
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "r1",
			PromptVersion: "p1",
			ResolvedAt:    time.Date(2026, 4, 21, 17, 52, 0, 0, time.UTC),
		},
	}, "rubric-v1", "prompt-v1")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDuplicateComplianceRuleID)
}

func TestRun_RejectsArbiterOnlyComplianceRowsOutsideDisputedSet(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary: scriptedJudge{
			score:        80,
			reasonPrefix: "primary",
			compliance:   map[string]contracts.ComplianceVerdict{},
		},
		Secondary: scriptedJudge{
			score:        70,
			reasonPrefix: "secondary",
			compliance:   map[string]contracts.ComplianceVerdict{},
		},
		Arbiter: scriptedJudge{
			score:            75,
			reasonPrefix:     "arbiter",
			strictCompliance: true,
			compliance: map[string]contracts.ComplianceVerdict{
				"rule-x": contracts.ComplianceVerdictViolated,
			},
		},
		Now: func() time.Time { return time.Date(2026, 4, 21, 17, 55, 0, 0, time.UTC) },
	})
	require.ErrorIs(t, err, judges.ErrJudgeOutputUnexpectedCompliance)
	assert.NoFileExists(t, mustResolve(t, runIO, "60/done.marker"))
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
			Secondary: scriptedJudge{
				score:        80,
				reasonPrefix: "secondary",
				compliance:   map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant},
			},
			Arbiter: scriptedJudge{
				score:        80,
				reasonPrefix: "arbiter",
				compliance:   map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant},
			},
		})
	}()

	<-started

	go func() {
		errCh <- Run(context.Background(), Input{
			IO:          runIO,
			TaskPackage: &pkg,
			Primary:     primary,
			Secondary: scriptedJudge{
				score:        80,
				reasonPrefix: "secondary",
				compliance:   map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant},
			},
			Arbiter: scriptedJudge{
				score:        80,
				reasonPrefix: "arbiter",
				compliance:   map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant},
			},
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
	// pass1RubricVersion / pass1PromptVersion override the scoring-version
	// metadata stamped on the pass1 scores-A.jsonl fixture rows. Leaving them
	// empty keeps the historical stub defaults (default / phase0-stub).
	pass1RubricVersion string
	pass1PromptVersion string
}

type scriptedJudge struct {
	score            int
	reasonPrefix     string
	compliance       map[string]contracts.ComplianceVerdict
	resolvedAt       time.Time
	strictCompliance bool
}

type mutatingReadJudge struct {
	delegate    scriptedJudge
	targetAgent contracts.AgentID
	mutatePath  string
	mutateBytes []byte
	seenPath    string
	seenBytes   []byte
}

type echoExpectedComplianceJudge struct {
	score  int
	inputs []judges.JudgeInput
}

type overflowRefJudge struct{}
type duplicateComplianceJudge struct{}
type versionedJudge struct {
	delegate judges.Judge
	version  string
}
type recordingPairwiseJudge struct {
	orders []string
}
type recordingPairwiseDecisionJudge struct {
	calls           int
	mode            judges.PairwiseMode
	pairCount       int
	comparisonCount int
}

func (j *recordingPairwiseJudge) ComparePairwise(_ context.Context, input judges.PairwiseInput) (judges.PairwiseComparison, error) {
	j.orders = append(j.orders, fmt.Sprintf("%s:%s", input.Agent, input.Order))
	winner := contracts.PairwiseWinnerB
	if input.Order == "BA" {
		// The step normalizes BA back to the canonical pass1/pass2 labels.
		winner = contracts.PairwiseWinnerA
	}
	return judges.PairwiseComparison{
		Agent:         input.Agent,
		Order:         input.Order,
		Winner:        winner,
		Margin:        contracts.PairwiseMarginClear,
		Justification: fmt.Sprintf("recorded %s comparison", input.Order),
		DimensionVotes: []judges.PairwiseDimensionVote{{
			Dimension: contracts.DimensionCorrectness,
			Winner:    winner,
			Reason:    "recorded dimension vote",
		}},
	}, nil
}

func (j *recordingPairwiseDecisionJudge) DecidePairwise(_ context.Context, input judges.PairwiseDecisionInput) (judges.PairwiseDecision, error) {
	j.calls++
	j.mode = input.Mode
	j.pairCount = len(input.Pairs)
	j.comparisonCount = len(input.Comparisons)

	decisions := make([]judges.PairwiseAgentDecision, 0, len(input.Pairs))
	for _, pair := range input.Pairs {
		decisions = append(decisions, judges.PairwiseAgentDecision{
			Agent:         pair.Agent,
			Winner:        contracts.PairwiseWinnerB,
			Margin:        contracts.PairwiseMarginClear,
			Justification: "recorded agent decision",
		})
	}
	return judges.PairwiseDecision{
		Action:         judges.PairwiseDecisionAdopt,
		Justification:  "recorded final decision",
		AgentDecisions: decisions,
	}, nil
}

func (j *echoExpectedComplianceJudge) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	j.inputs = append(j.inputs, input)
	if err := input.Validate(); err != nil {
		return judges.JudgeOutput{}, err
	}
	select {
	case <-ctx.Done():
		return judges.JudgeOutput{}, ctx.Err()
	default:
	}

	scores := make([]contracts.ScoreEntry, 0, len(canonicalDimensions))
	for _, dimension := range canonicalDimensions {
		scores = append(scores, contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			Dimension:     dimension,
			Score:         j.score,
			Reasons:       fmt.Sprintf("echo-%s", dimension),
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "echo-rubric",
			PromptVersion: "echo-prompt",
			ResolvedAt:    time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		})
	}
	compliance := make([]contracts.ComplianceEntry, 0, len(input.ExpectedComplianceRuleIDs))
	for _, ruleID := range input.ExpectedComplianceRuleIDs {
		compliance = append(compliance, contracts.ComplianceEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			RuleID:        ruleID,
			Verdict:       contracts.ComplianceVerdictCompliant,
			Rationale:     "echo compliant",
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "echo-rubric",
			PromptVersion: "echo-prompt",
			ResolvedAt:    time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		})
	}
	output := judges.JudgeOutput{Scores: scores, Compliance: compliance}
	return output, output.ValidateFor(input)
}

func (j *mutatingReadJudge) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	if input.Agent != j.targetAgent {
		return j.delegate.ScoreOutput(ctx, input)
	}
	if err := os.WriteFile(j.mutatePath, j.mutateBytes, 0o644); err != nil {
		return judges.JudgeOutput{}, err
	}
	data, err := os.ReadFile(input.OutputPath)
	if err != nil {
		return judges.JudgeOutput{}, err
	}
	j.seenPath = input.OutputPath
	j.seenBytes = append([]byte(nil), data...)
	return j.delegate.ScoreOutput(ctx, input)
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
	if !j.strictCompliance && len(input.ExpectedComplianceRuleIDs) > 0 {
		ruleIDs = append([]string(nil), input.ExpectedComplianceRuleIDs...)
	} else if !j.strictCompliance && input.EnforceExpectedCompliance {
		ruleIDs = nil
	} else if !j.strictCompliance {
		for _, ruleID := range input.ExpectedComplianceRuleIDs {
			if _, ok := j.compliance[ruleID]; !ok {
				ruleIDs = append(ruleIDs, ruleID)
			}
		}
	}
	sort.Strings(ruleIDs)

	compliance := make([]contracts.ComplianceEntry, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		verdict, ok := j.compliance[ruleID]
		if !ok {
			verdict = contracts.ComplianceVerdictCompliant
		}
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

func (overflowRefJudge) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	if err := input.Validate(); err != nil {
		return judges.JudgeOutput{}, err
	}
	select {
	case <-ctx.Done():
		return judges.JudgeOutput{}, ctx.Err()
	default:
	}

	inlineText := "judge supplied inline text"
	bogus := &contracts.OverflowRef{Path: "60/reasons/bogus.txt", Sha256: strings.Repeat("f", 64)}
	scores := make([]contracts.ScoreEntry, 0, len(canonicalDimensions))
	for _, dimension := range canonicalDimensions {
		scores = append(scores, contracts.ScoreEntry{
			SchemaVersion:      "1",
			RunID:              input.RunID,
			Pass:               input.Pass,
			Agent:              input.Agent,
			Dimension:          dimension,
			Score:              80,
			Reasons:            inlineText,
			ReasonsOverflowRef: bogus,
			VerdictPath:        contracts.VerdictPathSingle,
			RubricVersion:      "overflow-rubric",
			PromptVersion:      "overflow-prompt",
			ResolvedAt:         time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		})
	}
	ruleIDs := append([]string(nil), input.ExpectedComplianceRuleIDs...)
	if len(ruleIDs) == 0 {
		ruleIDs = append(ruleIDs, "shared")
	}
	sort.Strings(ruleIDs)
	compliance := make([]contracts.ComplianceEntry, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		compliance = append(compliance, contracts.ComplianceEntry{
			SchemaVersion:        "1",
			RunID:                input.RunID,
			Pass:                 input.Pass,
			Agent:                input.Agent,
			RuleID:               ruleID,
			Verdict:              contracts.ComplianceVerdictCompliant,
			Rationale:            inlineText,
			RationaleOverflowRef: bogus,
			VerdictPath:          contracts.VerdictPathSingle,
			RubricVersion:        "overflow-rubric",
			PromptVersion:        "overflow-prompt",
			ResolvedAt:           time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		})
	}
	output := judges.JudgeOutput{Scores: scores, Compliance: compliance}
	return output, output.ValidateFor(input)
}

func (duplicateComplianceJudge) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	output, err := scriptedJudge{
		score:        80,
		reasonPrefix: "duplicate",
		compliance:   map[string]contracts.ComplianceVerdict{"rule-x": contracts.ComplianceVerdictViolated},
	}.ScoreOutput(ctx, input)
	if err != nil {
		return judges.JudgeOutput{}, err
	}
	duplicate := output.Compliance[0]
	duplicate.Verdict = contracts.ComplianceVerdictCompliant
	duplicate.ResolvedAt = duplicate.ResolvedAt.Add(time.Second)
	output.Compliance = append(output.Compliance, duplicate)
	return output, output.ValidateFor(input)
}

func (j versionedJudge) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	return j.delegate.ScoreOutput(ctx, input)
}

func (j versionedJudge) JudgePromptVersion() string {
	return j.version
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

type countingJudge struct {
	delegate judges.Judge
	calls    int32
}

func (j *countingJudge) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	atomic.AddInt32(&j.calls, 1)
	return j.delegate.ScoreOutput(ctx, input)
}

func (j *countingJudge) callCount() int32 {
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
		for _, agent := range agents {
			path := filepath.Join(worktreeBase, fmt.Sprintf("%s-pass%d-%s", runID, pass, agent))
			require.NoError(t, os.MkdirAll(path, 0o755))
			worktrees = append(worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    path,
				Branch:  filepath.ToSlash(filepath.Join("auto-improve", string(runID), fmt.Sprintf("pass%d", pass), string(agent))),
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
		rubricVersion := opts.pass1RubricVersion
		if rubricVersion == "" {
			rubricVersion = "default"
		}
		promptVersion := opts.pass1PromptVersion
		if promptVersion == "" {
			promptVersion = "phase0-stub"
		}
		writePass1ScoresAt(t, runIO, runID, agents, rubricVersion, promptVersion)
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

// writePass1ScoresAt lets tests seed step30 scores-A.jsonl with a specific
// rubric/prompt version so F8's fail-closed version check exercises the
// matching path.
func writePass1ScoresAt(t *testing.T, runIO internalio.RunContext, runID contracts.RunID, agents []contracts.AgentID, rubricVersion, promptVersion string) {
	t.Helper()

	path := mustResolve(t, runIO, "30/scores-A.jsonl")
	for _, agent := range agents {
		for _, entry := range primaryStubScores(runID, 1, agent) {
			entry.RubricVersion = rubricVersion
			entry.PromptVersion = promptVersion
			require.NoError(t, internalio.AppendJSONL(path, entry))
		}
	}
}

func appendPass1ScoresWithScore(t *testing.T, runIO internalio.RunContext, runID contracts.RunID, agents []contracts.AgentID, score int) {
	t.Helper()

	path := mustResolve(t, runIO, "30/scores-A.jsonl")
	for _, agent := range agents {
		for _, entry := range primaryStubScores(runID, 1, agent) {
			entry.Score = score
			entry.Reasons = fmt.Sprintf("pass1 score changed to %d", score)
			require.NoError(t, internalio.AppendJSONL(path, entry))
		}
	}
}

// rewritePass1ScoresAt clobbers the existing pass1 scores-A.jsonl with rows
// stamped at the supplied version — used by tests that simulate step30 being
// rerun after step60 picked a new rubric/prompt version.
func rewritePass1ScoresAt(t *testing.T, runIO internalio.RunContext, runID contracts.RunID, agents []contracts.AgentID, rubricVersion, promptVersion string) {
	t.Helper()
	path := mustResolve(t, runIO, "30/scores-A.jsonl")
	require.NoError(t, os.Remove(path))
	writePass1ScoresAt(t, runIO, runID, agents, rubricVersion, promptVersion)
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

func writeStep60ReasonsSidecar(t *testing.T, runIO internalio.RunContext, content string) (contracts.OverflowRef, string) {
	t.Helper()
	reasonsDir := mustResolve(t, runIO, "60/reasons")
	sum := sha256Hex([]byte(content))
	sidecarPath, err := internalio.WriteSidecar(reasonsDir, sum, content)
	require.NoError(t, err)
	refPath, err := internalio.SidecarRefPath(runIO.RunDir(), sidecarPath)
	require.NoError(t, err)
	return contracts.OverflowRef{Path: refPath, Sha256: sum}, sidecarPath
}

func rewriteRawScores(t *testing.T, path string, rows []contracts.RawScoreEntry) {
	t.Helper()
	require.NoError(t, os.Remove(path))
	for _, row := range rows {
		require.NoError(t, internalio.AppendJSONL(path, row))
	}
}

func rewriteRawCompliance(t *testing.T, path string, rows []contracts.RawComplianceEntry) {
	t.Helper()
	require.NoError(t, os.Remove(path))
	for _, row := range rows {
		require.NoError(t, internalio.AppendJSONL(path, row))
	}
}

func writeEmptyRubric(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rubric.md")
	require.NoError(t, os.WriteFile(path, []byte("# rubric\n"), 0o644))
	return path
}

func writePass1Compliance(t *testing.T, runIO internalio.RunContext, runID contracts.RunID, agent contracts.AgentID, verdicts map[string]contracts.ComplianceVerdict) {
	t.Helper()
	path := mustResolve(t, runIO, "30/compliance-A.jsonl")
	ruleIDs := make([]string, 0, len(verdicts))
	for ruleID := range verdicts {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)
	for _, ruleID := range ruleIDs {
		require.NoError(t, internalio.AppendJSONL(path, contracts.ComplianceEntry{
			SchemaVersion: "1",
			RunID:         runID,
			Pass:          1,
			Agent:         agent,
			RuleID:        ruleID,
			Verdict:       verdicts[ruleID],
			Rationale:     fmt.Sprintf("pass1-%s-%s", agent, ruleID),
			VerdictPath:   contracts.VerdictPathAgreement,
			RubricVersion: "default",
			PromptVersion: "phase0-stub",
			ResolvedAt:    time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		}))
	}
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
	hash, err := hashReducedRawScoresFile(runIO, mustResolve(t, runIO, "60/scores-B-raw.jsonl"))
	require.NoError(t, err)
	return hash
}

func mustHashReducedRawCompliance(t *testing.T, runIO internalio.RunContext) string {
	t.Helper()
	hash, err := hashReducedRawComplianceFile(runIO, mustResolve(t, runIO, "60/compliance-B-raw.jsonl"))
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
