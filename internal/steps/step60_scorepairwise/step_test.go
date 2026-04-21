package step60_scorepairwise

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_HappyPath(t *testing.T) {
	fixedNow := time.Date(2026, 4, 21, 12, 34, 56, 0, time.UTC)
	fixture := newFixture(t, []contracts.AgentID{"a1", "a2", "a3"})
	fixture.writePass1Manifests(t, "a1", "a2", "a3")
	fixture.writePass2Manifests(t, "a1", "a2", "a3")

	err := Run(context.Background(), Input{
		IO:          fixture.runCtx,
		TaskPackage: fixture.pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return fixedNow },
	})
	require.NoError(t, err)

	stepPaths, err := resolvePaths(fixture.runCtx)
	require.NoError(t, err)
	assert.FileExists(t, stepPaths.doneMarker)
	assert.FileExists(t, stepPaths.scoresRaw)
	assert.FileExists(t, stepPaths.scoresFinal)
	assert.FileExists(t, stepPaths.complianceRaw)
	assert.FileExists(t, stepPaths.complianceFinal)
	assert.FileExists(t, stepPaths.pairwiseFinal)

	scores, err := internalio.ReadJSONL[contracts.ScoreEntry](stepPaths.scoresFinal)
	require.NoError(t, err)
	require.Len(t, scores, 15)
	for _, score := range scores {
		assert.Equal(t, contracts.VerdictPathArbitrated, score.VerdictPath)
		assert.Equal(t, fixedNow, score.ResolvedAt)
	}

	rawScores, err := internalio.ReadJSONL[contracts.RawScoreEntry](stepPaths.scoresRaw)
	require.NoError(t, err)
	require.Len(t, rawScores, 45)

	compliance, err := internalio.ReadJSONL[contracts.ComplianceEntry](stepPaths.complianceFinal)
	require.NoError(t, err)
	require.Len(t, compliance, 3)
	for _, row := range compliance {
		assert.Equal(t, contracts.VerdictPathAgreement, row.VerdictPath)
	}

	rawCompliance, err := internalio.ReadJSONL[contracts.RawComplianceEntry](stepPaths.complianceRaw)
	require.NoError(t, err)
	require.Len(t, rawCompliance, 6)

	pairwiseRows, err := internalio.ReadJSONL[contracts.PairwiseEntry](stepPaths.pairwiseFinal)
	require.NoError(t, err)
	require.Len(t, pairwiseRows, 3)
	for _, row := range pairwiseRows {
		assert.Equal(t, row.AgentA, row.AgentB)
		assert.Equal(t, contracts.PairwiseWinnerB, row.Winner)
		assert.Equal(t, contracts.PairwiseMarginSlight, row.Margin)
		assert.Equal(t, contracts.VerdictPathAgreement, row.VerdictPath)
		assert.Equal(t, fixedNow, row.ResolvedAt)
	}

	marker, err := internalio.ReadJSON[contracts.Step60DoneMarker](stepPaths.doneMarker)
	require.NoError(t, err)
	require.NoError(t, marker.Validate())
	assert.Equal(t, []contracts.AgentID{"a1", "a2", "a3"}, marker.CompletedAgents)
	assert.EqualValues(t, 15, marker.ExpectedCounts.Scores)
	assert.EqualValues(t, 3, marker.ExpectedCounts.Compliance)
	assert.EqualValues(t, 3, marker.ExpectedCounts.Pairwise)
	assert.Equal(t, fixedNow, marker.ResolvedAt)
	assert.Len(t, marker.RawHashes.ScoresRaw, 64)
	assert.Len(t, marker.RawHashes.ComplianceRaw, 64)
	assert.Len(t, marker.ContentHashes.ScoresFinal, 64)
	assert.Len(t, marker.ContentHashes.ComplianceFinal, 64)
	assert.Len(t, marker.ContentHashes.PairwiseFinal, 64)
	assert.Equal(t, marker.RawHashes.ScoresRaw, hashFileForTest(t, stepPaths.scoresRaw))
	assert.Equal(t, marker.RawHashes.ComplianceRaw, hashFileForTest(t, stepPaths.complianceRaw))
	assert.Equal(t, marker.ContentHashes.ScoresFinal, hashScoreRowsForTest(t, stepPaths.scoresFinal))
	assert.Equal(t, marker.ContentHashes.ComplianceFinal, hashComplianceRowsForTest(t, stepPaths.complianceFinal))
	assert.Equal(t, marker.ContentHashes.PairwiseFinal, hashPairwiseRowsForTest(t, stepPaths.pairwiseFinal))
}

func TestRun_IdempotentWhenDoneMarkerExists(t *testing.T) {
	firstNow := time.Date(2026, 4, 21, 13, 0, 0, 0, time.UTC)
	secondNow := firstNow.Add(2 * time.Hour)
	fixture := newFixture(t, []contracts.AgentID{"a1", "a2", "a3"})
	fixture.writePass1Manifests(t, "a1", "a2", "a3")
	fixture.writePass2Manifests(t, "a1", "a2", "a3")

	require.NoError(t, Run(context.Background(), Input{
		IO:          fixture.runCtx,
		TaskPackage: fixture.pkg,
		Now:         func() time.Time { return firstNow },
	}))

	stepPaths, err := resolvePaths(fixture.runCtx)
	require.NoError(t, err)
	beforeMarker, err := os.ReadFile(stepPaths.doneMarker)
	require.NoError(t, err)
	beforeScores, err := os.ReadFile(stepPaths.scoresFinal)
	require.NoError(t, err)

	require.NoError(t, Run(context.Background(), Input{
		IO:          fixture.runCtx,
		TaskPackage: fixture.pkg,
		Now:         func() time.Time { return secondNow },
	}))

	afterMarker, err := os.ReadFile(stepPaths.doneMarker)
	require.NoError(t, err)
	afterScores, err := os.ReadFile(stepPaths.scoresFinal)
	require.NoError(t, err)
	assert.Equal(t, beforeMarker, afterMarker)
	assert.Equal(t, beforeScores, afterScores)
}

func TestRun_SkipsAgentWhenPass2ManifestMissing(t *testing.T) {
	fixedNow := time.Date(2026, 4, 21, 14, 0, 0, 0, time.UTC)
	fixture := newFixture(t, []contracts.AgentID{"a1", "a2", "a3"})
	fixture.writePass1Manifests(t, "a1", "a2", "a3")
	fixture.writePass2Manifests(t, "a1", "a3")

	require.NoError(t, Run(context.Background(), Input{
		IO:          fixture.runCtx,
		TaskPackage: fixture.pkg,
		Now:         func() time.Time { return fixedNow },
	}))

	stepPaths, err := resolvePaths(fixture.runCtx)
	require.NoError(t, err)

	scores, err := internalio.ReadJSONL[contracts.ScoreEntry](stepPaths.scoresFinal)
	require.NoError(t, err)
	require.Len(t, scores, 10)

	pairwiseRows, err := internalio.ReadJSONL[contracts.PairwiseEntry](stepPaths.pairwiseFinal)
	require.NoError(t, err)
	require.Len(t, pairwiseRows, 2)

	marker, err := internalio.ReadJSON[contracts.Step60DoneMarker](stepPaths.doneMarker)
	require.NoError(t, err)
	assert.Equal(t, []contracts.AgentID{"a1", "a3"}, marker.CompletedAgents)
	assert.EqualValues(t, 10, marker.ExpectedCounts.Scores)
	assert.EqualValues(t, 2, marker.ExpectedCounts.Compliance)
	assert.EqualValues(t, 2, marker.ExpectedCounts.Pairwise)
}

type fixture struct {
	runCtx internalio.RunContext
	pkg    *contracts.TaskPackage
}

func newFixture(t *testing.T, agents []contracts.AgentID) fixture {
	t.Helper()

	root := t.TempDir()
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	runID := contracts.RunID("2026-04-21-PR60-abcdef0")
	runCtx, err := internalio.NewRunContext(runID, runsBase, worktreeBase)
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(runCtx.RunDir(), 0o755))
	worktrees := make([]contracts.WorktreeAllocation, 0, len(agents)*2)
	for pass := 1; pass <= 2; pass++ {
		for _, agent := range agents {
			path := filepath.Join(worktreeBase, fmt.Sprintf("%s-pass%d-%s", runID, pass, agent))
			require.NoError(t, os.MkdirAll(path, 0o755))
			worktrees = append(worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    path,
				Branch:  fmt.Sprintf("auto-improve/%s/pass%d/%s", runID, pass, agent),
				BaseSHA: fixtureSHA('a'),
				HeadSHA: fixtureSHA('b'),
			})
		}
	}

	pkg := &contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runID,
		PR:                      60,
		Title:                   "step60 fixture",
		BaseSHA:                 fixtureSHA('a'),
		BestBranch:              "best",
		ReconstructedTaskPrompt: "fixture prompt",
		Worktrees:               worktrees,
		CreatedAt:               time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}
	require.NoError(t, internalio.WriteJSONAtomic(runCtx.TaskPackagePath(), pkg))

	return fixture{
		runCtx: runCtx,
		pkg:    pkg,
	}
}

func (f fixture) writePass1Manifests(t *testing.T, agents ...contracts.AgentID) {
	t.Helper()
	f.writeManifests(t, 1, agents...)
}

func (f fixture) writePass2Manifests(t *testing.T, agents ...contracts.AgentID) {
	t.Helper()
	f.writeManifests(t, 2, agents...)
}

func (f fixture) writeManifests(t *testing.T, pass int, agents ...contracts.AgentID) {
	t.Helper()
	sort.Slice(agents, func(i, j int) bool {
		return string(agents[i]) < string(agents[j])
	})
	for _, agent := range agents {
		prefix := manifestPrefixForTest(pass, agent)
		diffPath, err := f.runCtx.ResolveRunRelative(filepath.Join(prefix, "diff.patch"))
		require.NoError(t, err)
		sessionPath, err := f.runCtx.ResolveRunRelative(filepath.Join(prefix, "session.jsonl"))
		require.NoError(t, err)
		checklistPath, err := f.runCtx.ResolveRunRelative(filepath.Join(prefix, "checklist-result.json"))
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Dir(diffPath), 0o755))
		require.NoError(t, os.WriteFile(diffPath, []byte(fmt.Sprintf("diff for %s pass %d\n", agent, pass)), 0o644))
		require.NoError(t, os.WriteFile(sessionPath, []byte("[]\n"), 0o644))
		require.NoError(t, os.WriteFile(checklistPath, []byte("{}\n"), 0o644))

		manifest := contracts.Manifest{
			Kind: contracts.ManifestKindSuccess,
			Value: contracts.ManifestSuccess{
				Kind:          contracts.ManifestKindSuccess,
				SchemaVersion: "1",
				RunID:         f.runCtx.RunID,
				Pass:          pass,
				Agent:         agent,
				BranchName:    fmt.Sprintf("auto-improve/%s/pass%d/%s", f.runCtx.RunID, pass, agent),
				HeadSHA:       fixtureSHA('b'),
				BaseSHA:       fixtureSHA('a'),
				DiffPath:      filepath.Join(prefix, "diff.patch"),
				SessionPath:   filepath.Join(prefix, "session.jsonl"),
				ChecklistPath: filepath.Join(prefix, "checklist-result.json"),
				PromptVersion: "stub-prompt-v1",
				StartedAt:     time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
				FinishedAt:    time.Date(2026, 4, 21, 9, 1, 0, 0, time.UTC),
			},
		}
		manifestPath, err := f.runCtx.ManifestPath(pass, agent)
		require.NoError(t, err)
		require.NoError(t, internalio.WriteJSONAtomic(manifestPath, manifest))
	}
}

func manifestPrefixForTest(pass int, agent contracts.AgentID) string {
	if pass == 2 {
		return filepath.Join("50-pass2", string(agent))
	}
	return filepath.Join("20-pass1", string(agent))
}

func fixtureSHA(ch byte) string {
	return string(bytesOf(ch, 40))
}

func bytesOf(ch byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = ch
	}
	return out
}

func hashFileForTest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hashScoreRowsForTest(t *testing.T, path string) string {
	t.Helper()
	rows, err := internalio.ReadJSONL[contracts.ScoreEntry](path)
	require.NoError(t, err)
	rows = internalio.CollapseByKey(rows, func(row contracts.ScoreEntry) scoreKey {
		return scoreKey{Agent: row.Agent, Dimension: row.Dimension}
	})
	return hashRowsForTest(t, rows)
}

func hashComplianceRowsForTest(t *testing.T, path string) string {
	t.Helper()
	rows, err := internalio.ReadJSONL[contracts.ComplianceEntry](path)
	require.NoError(t, err)
	rows = internalio.CollapseByKey(rows, func(row contracts.ComplianceEntry) complianceKey {
		return complianceKey{Agent: row.Agent, RuleID: row.RuleID}
	})
	return hashRowsForTest(t, rows)
}

func hashPairwiseRowsForTest(t *testing.T, path string) string {
	t.Helper()
	rows, err := internalio.ReadJSONL[contracts.PairwiseEntry](path)
	require.NoError(t, err)
	rows = internalio.CollapseByKey(rows, func(row contracts.PairwiseEntry) pairwiseKey {
		return pairwiseKey{AgentA: row.AgentA, AgentB: row.AgentB}
	})
	return hashRowsForTest(t, rows)
}

func hashRowsForTest[T any](t *testing.T, rows []T) string {
	t.Helper()
	if len(rows) == 0 {
		rows = []T{}
	}
	data, err := contracts.CanonicalMarshal(rows)
	require.NoError(t, err)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
