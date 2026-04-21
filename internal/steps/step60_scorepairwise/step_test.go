package step60_scorepairwise

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

	donePath, err := runIO.ResolveRunRelative("60/done.marker")
	require.NoError(t, err)
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
		assert.Equal(t, contracts.VerdictPathArbiterOverruled, score.VerdictPath)
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
		assert.Equal(t, contracts.PairwiseWinnerB, entry.Winner)
		assert.Equal(t, contracts.PairwiseMarginSlight, entry.Margin)
		assert.Equal(t, contracts.VerdictPathSingle, entry.VerdictPath)
		assert.Equal(t, now, entry.ResolvedAt)
	}

	rawScores := mustReadJSONL[contracts.RawScoreEntry](t, runIO, "60/scores-B-raw.jsonl")
	require.Len(t, rawScores, 45)
	rawCompliance := mustReadJSONL[contracts.RawComplianceEntry](t, runIO, "60/compliance-B-raw.jsonl")
	require.Len(t, rawCompliance, 9)

	assert.Len(t, marker.ContentHashes.ScoresFinal, 64)
	assert.Len(t, marker.ContentHashes.ComplianceFinal, 64)
	assert.Len(t, marker.ContentHashes.PairwiseFinal, 64)
	assert.Len(t, marker.RawHashes.ScoresRaw, 64)
	assert.Len(t, marker.RawHashes.ComplianceRaw, 64)

	assert.Equal(t, marker.ContentHashes.ScoresFinal, mustHashFinalScores(t, scores))
	assert.Equal(t, marker.ContentHashes.ComplianceFinal, mustHashFinalCompliance(t, compliance))
	assert.Equal(t, marker.ContentHashes.PairwiseFinal, mustHashFinalPairwise(t, pairwise))
	assert.Equal(t, marker.RawHashes.ScoresRaw, mustHashRawScores(t, runIO, "60/scores-B-raw.jsonl"))
	assert.Equal(t, marker.RawHashes.ComplianceRaw, mustHashRawCompliance(t, runIO, "60/compliance-B-raw.jsonl"))
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

	donePath, err := runIO.ResolveRunRelative("60/done.marker")
	require.NoError(t, err)
	beforeStat, err := os.Stat(donePath)
	require.NoError(t, err)
	before := snapshotStep60Artifacts(t, runIO)

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
	after := snapshotStep60Artifacts(t, runIO)
	assert.Equal(t, beforeStat.ModTime(), afterStat.ModTime())
	assert.Equal(t, before, after)
}

func TestRun_SkipsAgentWhenPass2ManifestMissing(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		agents:             []contracts.AgentID{"a1", "a2", "a3"},
		writePass1Score:    true,
		missingPass2Agents: map[contracts.AgentID]bool{"a3": true},
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

	assert.Len(t, mustReadJSONL[contracts.ScoreEntry](t, runIO, "60/scores-B.jsonl"), 10)
	assert.Len(t, mustReadJSONL[contracts.PairwiseEntry](t, runIO, "60/pairwise.jsonl"), 2)
}

func TestRun_FreshRunsAreByteIdentical(t *testing.T) {
	now := time.Date(2026, 4, 21, 9, 30, 0, 0, time.UTC)
	firstIO, firstPkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})
	secondIO, secondPkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})

	require.NoError(t, Run(context.Background(), Input{
		IO:             firstIO,
		TaskPackage:    &firstPkg,
		ScorableAgents: []contracts.AgentID{"a1", "a2"},
		Primary:        judges.NewPrimaryStub(),
		Secondary:      judges.NewSecondaryStub(),
		Arbiter:        judges.NewArbiterStub(),
		Now:            func() time.Time { return now },
	}))
	require.NoError(t, Run(context.Background(), Input{
		IO:             secondIO,
		TaskPackage:    &secondPkg,
		ScorableAgents: []contracts.AgentID{"a1", "a2"},
		Primary:        judges.NewPrimaryStub(),
		Secondary:      judges.NewSecondaryStub(),
		Arbiter:        judges.NewArbiterStub(),
		Now:            func() time.Time { return now },
	}))

	assert.Equal(t, snapshotStep60Artifacts(t, firstIO), snapshotStep60Artifacts(t, secondIO))
}

func TestRun_RerunWithoutMarkerTruncatesArtifacts(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	input := Input{
		IO:             runIO,
		TaskPackage:    &pkg,
		ScorableAgents: []contracts.AgentID{"a1", "a2"},
		Primary:        judges.NewPrimaryStub(),
		Secondary:      judges.NewSecondaryStub(),
		Arbiter:        judges.NewArbiterStub(),
		Now:            func() time.Time { return now },
	}

	require.NoError(t, Run(context.Background(), input))
	before := snapshotStep60Artifacts(t, runIO)
	beforeMarker := mustReadJSON[contracts.Step60DoneMarker](t, mustResolve(t, runIO, "60/done.marker"))
	require.NoError(t, os.Remove(mustResolve(t, runIO, "60/done.marker")))

	require.NoError(t, Run(context.Background(), input))

	after := snapshotStep60Artifacts(t, runIO)
	afterMarker := mustReadJSON[contracts.Step60DoneMarker](t, mustResolve(t, runIO, "60/done.marker"))
	assert.Equal(t, before, after)
	assert.Equal(t, beforeMarker.RawHashes, afterMarker.RawHashes)
}

func TestRun_NoScorableAgents(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		agents:                 []contracts.AgentID{"a1", "a2", "a3"},
		writePass1Score:        true,
		missingPass2Agents:     map[contracts.AgentID]bool{"a1": true, "a2": true},
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a3": true},
	})

	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC) },
	})
	require.ErrorIs(t, err, ErrNoScorablePass2Agents)
	assert.NoFileExists(t, mustResolve(t, runIO, "60/done.marker"))
	assert.NoFileExists(t, mustResolve(t, runIO, "60/scores-B.jsonl"))
	assert.NoFileExists(t, mustResolve(t, runIO, "60/scores-B-raw.jsonl"))
}

func TestRun_ArbiterVerdictPaths(t *testing.T) {
	t.Run("arbitrated", func(t *testing.T) {
		runIO, pkg := seedStep60Fixture(t, fixtureOptions{
			writePass1Score: true,
		})
		now := time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC)
		require.NoError(t, Run(context.Background(), Input{
			IO:             runIO,
			TaskPackage:    &pkg,
			ScorableAgents: []contracts.AgentID{"a1"},
			Primary:        panelJudge(80, 80, contracts.ComplianceVerdictCompliant),
			Secondary:      panelJudge(81, 81, contracts.ComplianceVerdictViolated),
			Arbiter:        panelJudge(80, 80, contracts.ComplianceVerdictCompliant),
			Now:            func() time.Time { return now },
		}))

		scores := mustReadJSONL[contracts.ScoreEntry](t, runIO, "60/scores-B.jsonl")
		assert.Equal(t, contracts.VerdictPathArbitrated, scoreByDimension(scores, contracts.DimensionFidelity).VerdictPath)
		compliance := mustReadJSONL[contracts.ComplianceEntry](t, runIO, "60/compliance-B.jsonl")
		require.Len(t, compliance, 1)
		assert.Equal(t, contracts.VerdictPathArbitrated, compliance[0].VerdictPath)
	})

	t.Run("arbiter_overruled", func(t *testing.T) {
		runIO, pkg := seedStep60Fixture(t, fixtureOptions{
			writePass1Score: true,
		})
		now := time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC)
		require.NoError(t, Run(context.Background(), Input{
			IO:             runIO,
			TaskPackage:    &pkg,
			ScorableAgents: []contracts.AgentID{"a1"},
			Primary:        panelJudge(80, 80, contracts.ComplianceVerdictCompliant),
			Secondary:      panelJudge(81, 81, contracts.ComplianceVerdictViolated),
			Arbiter:        panelJudge(82, 82, contracts.ComplianceVerdictMissed),
			Now:            func() time.Time { return now },
		}))

		scores := mustReadJSONL[contracts.ScoreEntry](t, runIO, "60/scores-B.jsonl")
		assert.Equal(t, contracts.VerdictPathArbiterOverruled, scoreByDimension(scores, contracts.DimensionFidelity).VerdictPath)
		compliance := mustReadJSONL[contracts.ComplianceEntry](t, runIO, "60/compliance-B.jsonl")
		require.Len(t, compliance, 1)
		assert.Equal(t, contracts.VerdictPathArbiterOverruled, compliance[0].VerdictPath)
	})
}

func TestRun_ScoresFinalHashGolden(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})
	now := time.Date(2026, 4, 21, 8, 0, 0, 0, time.UTC)
	require.NoError(t, Run(context.Background(), Input{
		IO:             runIO,
		TaskPackage:    &pkg,
		ScorableAgents: []contracts.AgentID{"a1"},
		Primary:        judges.NewPrimaryStub(),
		Secondary:      judges.NewSecondaryStub(),
		Arbiter:        judges.NewArbiterStub(),
		Now:            func() time.Time { return now },
	}))

	marker := mustReadJSON[contracts.Step60DoneMarker](t, mustResolve(t, runIO, "60/done.marker"))
	assert.Equal(t, "5cc196d0943a0e542ae193d6f25a2c036e5d41d1cd3553582dfedcda93cb45fd", marker.ContentHashes.ScoresFinal)
}

func TestRun_UsesUnionRuleIDsAcrossJudges(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})
	now := time.Date(2026, 4, 21, 13, 0, 0, 0, time.UTC)
	require.NoError(t, Run(context.Background(), Input{
		IO:             runIO,
		TaskPackage:    &pkg,
		ScorableAgents: []contracts.AgentID{"a1"},
		Primary:        panelJudgeWithRules(80, map[string]contracts.ComplianceVerdict{}),
		Secondary:      panelJudgeWithRules(81, map[string]contracts.ComplianceVerdict{"secondary-only": contracts.ComplianceVerdictViolated}),
		Arbiter:        panelJudgeWithRules(81, map[string]contracts.ComplianceVerdict{"secondary-only": contracts.ComplianceVerdictViolated}),
		Now:            func() time.Time { return now },
	}))

	compliance := mustReadJSONL[contracts.ComplianceEntry](t, runIO, "60/compliance-B.jsonl")
	require.Len(t, compliance, 1)
	assert.Equal(t, "secondary-only", compliance[0].RuleID)
	assert.Equal(t, contracts.ComplianceVerdictViolated, compliance[0].Verdict)
	assert.Equal(t, contracts.VerdictPathArbitrated, compliance[0].VerdictPath)
}

type fixtureOptions struct {
	agents                 []contracts.AgentID
	writePass1Score        bool
	missingPass2Agents     map[contracts.AgentID]bool
	nonScorablePass2Agents map[contracts.AgentID]bool
}

type judgeFunc func(context.Context, judges.JudgeInput) (judges.JudgeOutput, error)

func (fn judgeFunc) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	return fn(ctx, input)
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
		writeManifestSuccess(t, runIO, runID, 1, agent)
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

func panelJudge(baseScore, fidelityScore int, verdict contracts.ComplianceVerdict) judges.Judge {
	return panelJudgeWithRulesAndFidelity(baseScore, fidelityScore, map[string]contracts.ComplianceVerdict{"stub-rubric-rule": verdict})
}

func panelJudgeWithRules(baseScore int, rules map[string]contracts.ComplianceVerdict) judges.Judge {
	return panelJudgeWithRulesAndFidelity(baseScore, baseScore, rules)
}

func panelJudgeWithRulesAndFidelity(baseScore, fidelityScore int, rules map[string]contracts.ComplianceVerdict) judges.Judge {
	return judgeFunc(func(_ context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
		scores := make([]contracts.ScoreEntry, 0, len(canonicalDimensions))
		for _, dimension := range canonicalDimensions {
			score := baseScore
			if dimension == contracts.DimensionFidelity {
				score = fidelityScore
			}
			scores = append(scores, contracts.ScoreEntry{
				SchemaVersion: "1",
				RunID:         input.RunID,
				Pass:          input.Pass,
				Agent:         input.Agent,
				Dimension:     dimension,
				Score:         score,
				Reasons:       fmt.Sprintf("score=%d", score),
				VerdictPath:   contracts.VerdictPathSingle,
				RubricVersion: "default",
				PromptVersion: "phase0-stub",
				ResolvedAt:    time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
			})
		}
		compliance := make([]contracts.ComplianceEntry, 0, len(rules))
		for ruleID, ruleVerdict := range rules {
			compliance = append(compliance, contracts.ComplianceEntry{
				SchemaVersion: "1",
				RunID:         input.RunID,
				Pass:          input.Pass,
				Agent:         input.Agent,
				RuleID:        ruleID,
				Verdict:       ruleVerdict,
				Rationale:     string(ruleVerdict),
				VerdictPath:   contracts.VerdictPathSingle,
				RubricVersion: "default",
				PromptVersion: "phase0-stub",
				ResolvedAt:    time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
			})
		}
		return judges.JudgeOutput{Scores: scores, Compliance: compliance}, nil
	})
}

func scoreByDimension(entries []contracts.ScoreEntry, dimension contracts.Dimension) contracts.ScoreEntry {
	for _, entry := range entries {
		if entry.Dimension == dimension {
			return entry
		}
	}
	return contracts.ScoreEntry{}
}

func snapshotStep60Artifacts(t *testing.T, runIO internalio.RunContext) map[string][]byte {
	t.Helper()
	artifacts := map[string][]byte{}
	for _, rel := range []string{
		"60/scores-B.jsonl",
		"60/compliance-B.jsonl",
		"60/pairwise.jsonl",
		"60/scores-B-raw.jsonl",
		"60/compliance-B-raw.jsonl",
		"60/done.marker",
	} {
		artifacts[rel] = mustReadFile(t, mustResolve(t, runIO, rel))
	}
	return artifacts
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

func mustHashRawScores(t *testing.T, runIO internalio.RunContext, rel string) string {
	t.Helper()
	hash, err := hashRawScores(mustResolve(t, runIO, rel))
	require.NoError(t, err)
	return hash
}

func mustHashRawCompliance(t *testing.T, runIO internalio.RunContext, rel string) string {
	t.Helper()
	hash, err := hashRawCompliance(mustResolve(t, runIO, rel))
	require.NoError(t, err)
	return hash
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
