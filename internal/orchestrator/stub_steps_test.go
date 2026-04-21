package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStubMarkerStepSeedsPass1ScoresFromTaskPackageWorktrees(t *testing.T) {
	runsBase := filepath.Join(t.TempDir(), "runs")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")
	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	agents := []contracts.AgentID{"a1", "a2", "a3"}

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
				Branch:  fmt.Sprintf("auto-improve/fixture/%s/%s", passDir, agent),
				BaseSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				HeadSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			})
		}
	}

	pkg := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runID,
		PR:                      42,
		Title:                   "stub step30 seed",
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
		require.NoError(t, stubImplementStep{}.Run(context.Background(), &StepRunContext{
			IO:          runIO,
			TaskPackage: &pkg,
			Pass:        1,
			Agent:       agent,
		}))
	}

	stepRun := &StepRunContext{
		IO:          runIO,
		TaskPackage: &pkg,
	}
	require.NoError(t, stubMarkerStep{path: "30/done.marker"}.Run(context.Background(), stepRun))

	scoresPath, err := runIO.ResolveRunRelative("30/scores-A.jsonl")
	require.NoError(t, err)
	scores, err := internalio.ReadJSONL[contracts.ScoreEntry](scoresPath)
	require.NoError(t, err)
	require.Len(t, scores, 15)
	seenAgents := make(map[contracts.AgentID]struct{}, len(agents))
	for _, score := range scores {
		assert.Equal(t, 1, score.Pass)
		seenAgents[score.Agent] = struct{}{}
	}
	assert.Len(t, seenAgents, len(agents))
	for _, agent := range agents {
		_, ok := seenAgents[agent]
		assert.True(t, ok)
	}

	before, err := os.ReadFile(scoresPath)
	require.NoError(t, err)
	require.NoError(t, stubMarkerStep{path: "30/done.marker"}.Run(context.Background(), stepRun))
	after, err := os.ReadFile(scoresPath)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}
