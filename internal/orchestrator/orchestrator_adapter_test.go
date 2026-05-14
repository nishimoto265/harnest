package orchestrator

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/nishimoto265/harnest/internal/contracts/stepio"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/steps/step30_score"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStubStep40_UsesRequestBoundDecoder(t *testing.T) {
	cfg := testConfig(t)
	runID := contracts.RunID("2026-04-21-PR79-abcdef0")
	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunDir(), 0o755))

	pkg := stubTaskPackageForRun(runCtx, 79)
	require.NoError(t, internalio.WriteJSONAtomic(runCtx.TaskPackagePath(), pkg))
	require.NoError(t, appendJSONLForTest(runCtx, "30/scores-A.jsonl", contracts.ScoreEntry{
		SchemaVersion: "1",
		RunID:         runID,
		Pass:          1,
		Agent:         "a1",
		Dimension:     contracts.DimensionFidelity,
		Score:         80,
		Reasons:       "Missing the guard lets regressions slip into the changed code path.",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "default",
		PromptVersion: "phase0",
		ResolvedAt:    time.Now().UTC(),
	}))
	require.NoError(t, appendJSONLForTest(runCtx, "30/compliance-A.jsonl", contracts.ComplianceEntry{
		SchemaVersion: "1",
		RunID:         runID,
		Pass:          1,
		Agent:         "a1",
		RuleID:        "rule-a",
		Verdict:       contracts.ComplianceVerdictViolated,
		Rationale:     "Rule rule-a was skipped when the implementation touched the guarded path.",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "default",
		PromptVersion: "phase0",
		ResolvedAt:    time.Now().UTC(),
	}))
	require.NoError(t, writeValidStep30ArtifactsForTest(runCtx))
	manifestPath, err := runCtx.ManifestPath(1, "a1")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(manifestPath, contracts.Manifest{
		Kind: contracts.ManifestKindSuccess,
		Value: contracts.ManifestSuccess{
			Kind:          contracts.ManifestKindSuccess,
			SchemaVersion: "1",
			RunID:         runID,
			Pass:          1,
			Agent:         "a1",
			BranchName:    "auto-improve/fixture",
			HeadSHA:       strings.Repeat("b", 40),
			BaseSHA:       strings.Repeat("a", 40),
			DiffPath:      "20-pass1/a1/diff.patch",
			SessionPath:   "20-pass1/a1/session.jsonl",
			ChecklistPath: "20-pass1/a1/checklist-result.json",
			PromptVersion: "phase0",
			StartedAt:     time.Now().UTC(),
			FinishedAt:    time.Now().UTC(),
		},
	}))
	require.NoError(t, internalio.WriteAtomic(runCtx.RulesRegistryPath(), nil))

	called := false
	step := stubStep40{
		decode: func(data []byte, req any) (any, error) {
			called = true
			return stepio.DecodeAndValidateStep40Response(data, req.(stepio.Step40Request))
		},
	}
	run := &StepRunContext{
		Config:      cfg,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		PR:          79,
		IO:          runCtx,
		TaskPackage: &pkg,
	}

	require.NoError(t, step.Run(context.Background(), run))
	assert.True(t, called)
	require.NotNil(t, run.Candidates)
	assert.Len(t, run.Candidates.Candidates, 1)
}

func TestStep30AdapterDecoderRequestUsesArtifactPromptVersionFromConfigJudge(t *testing.T) {
	constructionCfg := testConfig(t)
	cfg := testConfigWithCLIJudge(t)
	runID := contracts.RunID("2026-04-21-PR120-abcdef0")
	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	pkg := stubTaskPackageForRun(runCtx, 120)
	for _, agent := range defaultAgents {
		require.NoError(t, stubImplementStep{}.Run(context.Background(), &StepRunContext{
			IO:          runCtx,
			TaskPackage: &pkg,
			Pass:        1,
			Agent:       agent,
		}))
	}

	var captured stepio.Step30Request
	adapter := newStep30ScoreAdapter(
		step30_score.New(step30_score.WithPanelProvider(step30_score.ConfigPanelProvider(constructionCfg))),
		func(data []byte, req any) (any, error) {
			captured = req.(stepio.Step30Request)
			return stepio.DecodeAndValidateStep30Response(data, captured)
		},
	)
	require.NoError(t, adapter.Run(context.Background(), &StepRunContext{
		Config:      cfg,
		IO:          runCtx,
		TaskPackage: &pkg,
	}))

	rows := mustReadJSONL[contracts.ScoreEntry](t, runCtx, "30/scores-A.jsonl")
	require.NotEmpty(t, rows)
	assert.Equal(t, rows[0].RubricVersion, captured.RubricVersion)
	assert.Equal(t, rows[0].PromptVersion, captured.PromptVersion)
	assert.NotEqual(t, "phase0-stub", captured.PromptVersion)
	assert.Contains(t, captured.PromptVersion, "cli-judge-v1-codex")
}

func TestStep60AdapterDecoderRequestUsesArtifactPromptVersionFromConfigJudge(t *testing.T) {
	cfg := testConfigWithCLIJudge(t)
	runID := contracts.RunID("2026-04-21-PR121-abcdef0")
	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	pkg := stubTaskPackageForRun(runCtx, 121)
	for _, agent := range defaultAgents {
		require.NoError(t, stubImplementStep{}.Run(context.Background(), &StepRunContext{
			IO:          runCtx,
			TaskPackage: &pkg,
			Pass:        1,
			Agent:       agent,
		}))
		require.NoError(t, stubImplementStep{}.Run(context.Background(), &StepRunContext{
			IO:          runCtx,
			TaskPackage: &pkg,
			Pass:        2,
			Agent:       agent,
		}))
	}
	promptVersion := configJudgePanelPromptVersion(t, cfg)
	for _, agent := range defaultAgents {
		writePass1ScoringRowsForAdapterTest(t, runCtx, pkg.RunID, agent, "default", promptVersion)
	}

	var captured stepio.Step60Request
	step := step60Step{
		cfg: cfg,
		decode: func(data []byte, req any) (any, error) {
			captured = req.(stepio.Step60Request)
			return stepio.DecodeAndValidateStep60Response(data, captured)
		},
	}
	require.NoError(t, step.Run(context.Background(), &StepRunContext{
		Config:      cfg,
		IO:          runCtx,
		TaskPackage: &pkg,
	}))

	rows := mustReadJSONL[contracts.ScoreEntry](t, runCtx, "60/scores-B.jsonl")
	require.NotEmpty(t, rows)
	assert.Equal(t, rows[0].RubricVersion, captured.RubricVersion)
	assert.Equal(t, rows[0].PromptVersion, captured.PromptVersion)
	assert.Equal(t, promptVersion, captured.PromptVersion)
	assert.NotEqual(t, "phase0-stub", captured.PromptVersion)
}

func TestStubMarkerStep_SeedsPass1ScoresFromTaskPackageWorktrees(t *testing.T) {
	cfg := testConfig(t)
	agents := []contracts.AgentID{"a2", "a4", "a7"}
	runID := contracts.RunID("2026-04-21-PR66-abcdef0")

	worktrees := make([]contracts.WorktreeAllocation, 0, len(agents)*2)
	for pass := 1; pass <= 2; pass++ {
		for _, agent := range agents {
			worktrees = append(worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    filepath.Join(cfg.Worktree.Base, fmt.Sprintf("%s-pass%d-%s", runID, pass, agent)),
				Branch:  fmt.Sprintf("stub/%s/pass%d/%s", runID, pass, agent),
				BaseSHA: strings.Repeat("a", 40),
				HeadSHA: strings.Repeat("b", 40),
			})
		}
	}
	pkg := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runID,
		PR:                      66,
		Title:                   "stub seed",
		BaseSHA:                 strings.Repeat("a", 40),
		BestBranch:              "best",
		ReconstructedTaskPrompt: "stub seed prompt",
		Worktrees:               worktrees,
		CreatedAt:               time.Now().UTC(),
	}
	require.NoError(t, pkg.Validate())

	runCtx, err := internalio.RunContextFromTaskPackage(pkg, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunDir(), 0o755))

	stepRun := &StepRunContext{Config: cfg, IO: runCtx, TaskPackage: &pkg}
	for _, agent := range agents {
		require.NoError(t, stubImplementStep{}.Run(context.Background(), &StepRunContext{
			Config:      cfg,
			IO:          runCtx,
			TaskPackage: &pkg,
			Pass:        1,
			Agent:       agent,
		}))
	}

	require.NoError(t, stubMarkerStep{path: "30/done.marker"}.Run(context.Background(), stepRun))

	scoresPath, err := runCtx.ResolveRunRelative("30/scores-A.jsonl")
	require.NoError(t, err)
	scores, err := internalio.ReadJSONL[contracts.ScoreEntry](scoresPath)
	require.NoError(t, err)
	require.Len(t, scores, len(agents)*5)

	seenAgents := make(map[contracts.AgentID]int, len(agents))
	for _, score := range scores {
		seenAgents[score.Agent]++
	}
	assert.Equal(t, map[contracts.AgentID]int{"a2": 5, "a4": 5, "a7": 5}, seenAgents)

	before := mustReadFile(t, scoresPath)
	require.NoError(t, stubMarkerStep{path: "30/done.marker"}.Run(context.Background(), stepRun))
	assert.Equal(t, before, mustReadFile(t, scoresPath))
}
