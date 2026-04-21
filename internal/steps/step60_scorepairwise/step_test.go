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
	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return now },
	})
	require.NoError(t, err)

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
		assert.Equal(t, contracts.VerdictPathArbitrated, score.VerdictPath)
	}

	compliance := mustReadJSONL[contracts.ComplianceEntry](t, runIO, "60/compliance-B.jsonl")
	require.Len(t, compliance, 3)
	for _, entry := range compliance {
		assert.Equal(t, contracts.VerdictPathAgreement, entry.VerdictPath)
	}

	pairwise := mustReadJSONL[contracts.PairwiseEntry](t, runIO, "60/pairwise.jsonl")
	require.Len(t, pairwise, 3)
	for _, entry := range pairwise {
		assert.Equal(t, entry.AgentA, entry.AgentB)
		assert.Equal(t, contracts.PairwiseWinnerB, entry.Winner)
		assert.Equal(t, contracts.PairwiseMarginSlight, entry.Margin)
	}

	rawScores := mustReadJSONL[contracts.RawScoreEntry](t, runIO, "60/scores-B-raw.jsonl")
	require.Len(t, rawScores, 45)
	rawCompliance := mustReadJSONL[contracts.RawComplianceEntry](t, runIO, "60/compliance-B-raw.jsonl")
	require.Len(t, rawCompliance, 6)

	assert.Len(t, marker.ContentHashes.ScoresFinal, 64)
	assert.Len(t, marker.ContentHashes.ComplianceFinal, 64)
	assert.Len(t, marker.ContentHashes.PairwiseFinal, 64)
	assert.Len(t, marker.RawHashes.ScoresRaw, 64)
	assert.Len(t, marker.RawHashes.ComplianceRaw, 64)

	assert.Equal(t, marker.ContentHashes.ScoresFinal, hashFinalScores(scores))
	assert.Equal(t, marker.ContentHashes.ComplianceFinal, hashFinalCompliance(compliance))
	assert.Equal(t, marker.ContentHashes.PairwiseFinal, hashFinalPairwise(pairwise))
	assert.Equal(t, marker.RawHashes.ScoresRaw, mustHashFileOrEmpty(t, runIO, "60/scores-B-raw.jsonl"))
	assert.Equal(t, marker.RawHashes.ComplianceRaw, mustHashFileOrEmpty(t, runIO, "60/compliance-B-raw.jsonl"))
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

func TestRun_SkipsAgentWhenPass2ManifestMissing(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		agents:             []contracts.AgentID{"a1", "a2", "a3"},
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
	require.NoError(t, err)

	marker := mustReadJSON[contracts.Step60DoneMarker](t, mustResolve(t, runIO, "60/done.marker"))
	assert.Equal(t, []contracts.AgentID{"a1", "a2"}, marker.CompletedAgents)
	assert.EqualValues(t, 10, marker.ExpectedCounts.Scores)
	assert.EqualValues(t, 2, marker.ExpectedCounts.Compliance)
	assert.EqualValues(t, 2, marker.ExpectedCounts.Pairwise)

	assert.Len(t, mustReadJSONL[contracts.ScoreEntry](t, runIO, "60/scores-B.jsonl"), 10)
	assert.Len(t, mustReadJSONL[contracts.PairwiseEntry](t, runIO, "60/pairwise.jsonl"), 2)
}

type fixtureOptions struct {
	agents             []contracts.AgentID
	writePass1Score    bool
	missingPass2Agents map[contracts.AgentID]bool
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
		if !opts.missingPass2Agents[agent] {
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

func mustHashFileOrEmpty(t *testing.T, runIO internalio.RunContext, rel string) string {
	t.Helper()
	hash, err := hashFileOrEmpty(mustResolve(t, runIO, rel))
	require.NoError(t, err)
	return hash
}
