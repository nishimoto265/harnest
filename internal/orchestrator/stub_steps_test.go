package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSeedStubPass1Scores_RebuildsPartialFile(t *testing.T) {
	cfg := testConfig(t)
	runCtx, stepRun := seededStubStepRun(t, cfg, "2026-04-21-PR91-abcdef0", 91)

	agents, err := pass1Agents(stepRun.TaskPackage)
	require.NoError(t, err)
	rows := stubPass1ScoreRows(t, runCtx, agents)
	require.Greater(t, len(rows), 1)

	partialPayload, err := marshalScoreJSONL(rows[:len(rows)-1])
	require.NoError(t, err)
	scoresPath, err := runCtx.ResolveRunRelative("30/scores-A.jsonl")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteAtomic(scoresPath, partialPayload))

	require.NoError(t, seedStubPass1Scores(context.Background(), stepRun))

	seeded, err := internalio.ReadJSONL[contracts.ScoreEntry](scoresPath)
	require.NoError(t, err)
	require.Len(t, seeded, len(agents)*len(stubPass1Dimensions))

	complete, err := stubPass1ScoresComplete(scoresPath, agents)
	require.NoError(t, err)
	assert.True(t, complete)
}

func TestStep60StepRun_SelfHealsMissingPass1SeedForLegacyRuns(t *testing.T) {
	cfg := testConfig(t)
	runCtx, stepRun := seededStubStepRun(t, cfg, "2026-04-21-PR92-abcdef0", 92)

	scoresPath, err := runCtx.ResolveRunRelative("30/scores-A.jsonl")
	require.NoError(t, err)
	if err := os.Remove(scoresPath); err != nil && !os.IsNotExist(err) {
		require.NoError(t, err)
	}

	done60Path, err := runCtx.ResolveRunRelative("60/done.marker")
	require.NoError(t, err)
	require.NoError(t, os.Remove(done60Path))

	require.NoError(t, step60Step{}.Run(context.Background(), stepRun))

	seeded, err := internalio.ReadJSONL[contracts.ScoreEntry](scoresPath)
	require.NoError(t, err)
	assert.Len(t, seeded, len(defaultAgents)*len(stubPass1Dimensions))
	assert.FileExists(t, done60Path)
}

func seededStubStepRun(t *testing.T, cfg *config.Config, runID contracts.RunID, pr int) (internalio.RunContext, *StepRunContext) {
	t.Helper()

	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, seedResumeRun(t, runCtx, pr))

	pkg, err := internalio.ReadJSON[contracts.TaskPackage](runCtx.TaskPackagePath())
	require.NoError(t, err)

	return runCtx, &StepRunContext{
		Config:      cfg,
		IO:          runCtx,
		TaskPackage: &pkg,
		PR:          pr,
	}
}

func stubPass1ScoreRows(t *testing.T, runIO internalio.RunContext, agents []contracts.AgentID) []contracts.ScoreEntry {
	t.Helper()

	judge := judges.NewPrimaryStub()
	rows := make([]contracts.ScoreEntry, 0, len(agents)*len(stubPass1Dimensions))
	for _, agent := range agents {
		manifest, err := internalio.LoadScorableManifest(runIO, 1, agent)
		require.NoError(t, err)
		outputPath, err := runIO.ResolveRunRelative(manifest.DiffPath)
		require.NoError(t, err)
		output, err := judge.ScoreOutput(context.Background(), judges.JudgeInput{
			RunID:      runIO.RunID,
			Pass:       1,
			Agent:      agent,
			OutputPath: outputPath,
			RubricPath: filepath.Join(runIO.RunDir(), "rubrics", "default.md"),
		})
		require.NoError(t, err)
		rows = append(rows, output.Scores...)
	}
	return rows
}
