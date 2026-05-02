package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_DefaultSteps_RealWiringWithFakeCLIs(t *testing.T) {
	cfg := testConfig(t)
	cfg.Repo.Root = repoRootFromTestFile(t)
	binDir := installFakeCLI(t)
	cfg.ClaudeCLIPath = filepath.Join(binDir, "claude")

	t.Setenv("AUTO_IMPROVE_TEST_BASE_SHA", strings.Repeat("a", 40))
	t.Setenv("AUTO_IMPROVE_TEST_TARGET_SHA", strings.Repeat("b", 40))
	t.Setenv("AUTO_IMPROVE_TEST_MERGE_SHA", strings.Repeat("c", 40))
	t.Setenv("AUTO_IMPROVE_TEST_BEST_SHA", strings.Repeat("d", 40))

	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	runID := contracts.RunID("2026-04-21-PR42-abcdeff")
	require.NoError(t, orch.Run(context.Background(), 42, RunOptions{RunID: runID}))

	runDir := filepath.Join(cfg.Paths.Runs, string(runID))
	taskPackagePath := filepath.Join(runDir, "task-package.json")
	decisionPath := filepath.Join(runDir, "70", "decision.json")
	assert.FileExists(t, taskPackagePath)
	assert.FileExists(t, decisionPath)
	assert.FileExists(t, filepath.Join(runDir, "30", "done.marker"))
	assert.NoFileExists(t, filepath.Join(runDir, "60", "done.marker"))

	decision, err := internalio.ReadJSON[contracts.Decision](decisionPath)
	require.NoError(t, err)
	assert.Equal(t, contracts.DecisionActionNoop, decision.Action)

	_, err = internalio.ReadJSON[contracts.TaskPackage](taskPackagePath)
	require.NoError(t, err)
	for _, agent := range []contracts.AgentID{"a1", "a2", "a3"} {
		manifest, manifestErr := internalio.LoadFinalizedManifest(internalio.RunContext{
			RunID:        runID,
			RunsBase:     cfg.Paths.Runs,
			WorktreeBase: cfg.Worktree.Base,
		}, 1, agent)
		require.NoError(t, manifestErr)
		require.NotNil(t, manifest)
		assert.Equal(t, contracts.ManifestKindSuccess, manifest.Kind)
	}
}

func TestRun_DefaultSteps_RealWiringWithFakeCLIs_AdoptFlow(t *testing.T) {
	cfg := testConfig(t)
	cfg.Repo.Root = repoRootFromTestFile(t)
	binDir := installFakeCLI(t)
	cfg.ClaudeCLIPath = filepath.Join(binDir, "claude")

	t.Setenv("AUTO_IMPROVE_TEST_BASE_SHA", strings.Repeat("a", 40))
	t.Setenv("AUTO_IMPROVE_TEST_TARGET_SHA", strings.Repeat("b", 40))
	t.Setenv("AUTO_IMPROVE_TEST_MERGE_SHA", strings.Repeat("c", 40))
	t.Setenv("AUTO_IMPROVE_TEST_BEST_SHA", strings.Repeat("d", 40))

	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orch.steps.Step40 = forcedCandidateStep{}
	orch.steps.Step60 = scriptedStep60Step{decode: orch.decoders.Step60}

	runID := contracts.RunID("2026-04-21-PR43-abcdeff")
	require.NoError(t, orch.Run(context.Background(), 43, RunOptions{RunID: runID}))

	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(cfg.Paths.Runs, string(runID), "60", "done.marker"))
	got, err := buildCycleArtifactBundle(runCtx)
	require.NoError(t, err)
	fixturePath := filepath.Join(repoRootFromTestFile(t), "testdata", "golden_run", "full_cycle_adopt_real", "bundle.json")
	if update := strings.TrimSpace(os.Getenv("UPDATE_GOLDEN_BUNDLE_REAL")); update == "1" {
		data, err := json.MarshalIndent(got, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Dir(fixturePath), 0o755))
		require.NoError(t, os.WriteFile(fixturePath, append(data, '\n'), 0o644))
		return
	}
	fixtureBytes, err := os.ReadFile(fixturePath)
	require.NoError(t, err)
	var want cycleArtifactBundle
	require.NoError(t, json.Unmarshal(fixtureBytes, &want))
	assert.Equal(t, want, got)
}

func TestRun_DefaultSteps_RealWiringWithFakeCLIs_BlockedBySentinel(t *testing.T) {
	cfg := testConfig(t)
	cfg.Repo.Root = repoRootFromTestFile(t)
	binDir := installFakeCLI(t)
	cfg.ClaudeCLIPath = filepath.Join(binDir, "claude")

	t.Setenv("AUTO_IMPROVE_TEST_BASE_SHA", strings.Repeat("a", 40))
	t.Setenv("AUTO_IMPROVE_TEST_TARGET_SHA", strings.Repeat("b", 40))
	t.Setenv("AUTO_IMPROVE_TEST_MERGE_SHA", strings.Repeat("c", 40))
	t.Setenv("AUTO_IMPROVE_TEST_BEST_SHA", strings.Repeat("d", 40))

	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	blockedRunID := contracts.RunID("2026-04-21-PR99-deadbee")
	blockedRunCtx, err := internalio.NewRunContext(blockedRunID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, ensureNeedsRecoverySentinel(blockedRunCtx, 99, blockedRunID, contracts.RollbackReasonTransactionalFailure, contracts.FailedStep70))

	runID := contracts.RunID("2026-04-21-PR44-abcdeff")
	var blockedErr *GlobalNeedsRecoveryError
	err = orch.Run(context.Background(), 44, RunOptions{RunID: runID})
	require.ErrorAs(t, err, &blockedErr)
	require.Equal(t, blockedRunID, blockedErr.Sentinel.RunID)

	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	decisionPath := filepath.Join(cfg.Paths.Runs, string(runID), "70", "decision.json")
	assert.NoFileExists(t, decisionPath)

	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	assert.Empty(t, events)
}

func TestRun_DefaultSteps_RealWiringWithFakeCLIs_PolicyOnlyAdoptIgnoresBestBranchPushFailure(t *testing.T) {
	cfg := testConfig(t)
	cfg.Repo.Root = repoRootFromTestFile(t)
	binDir := installFakeCLI(t)
	overwriteFakeGitScript(t, binDir, manualRecoveryGitScript())
	stateDir := t.TempDir()
	t.Setenv("AUTO_IMPROVE_GIT_STATE_DIR", stateDir)
	cfg.ClaudeCLIPath = filepath.Join(binDir, "claude")

	t.Setenv("AUTO_IMPROVE_TEST_BASE_SHA", strings.Repeat("a", 40))
	t.Setenv("AUTO_IMPROVE_TEST_TARGET_SHA", strings.Repeat("b", 40))
	t.Setenv("AUTO_IMPROVE_TEST_MERGE_SHA", strings.Repeat("c", 40))
	t.Setenv("AUTO_IMPROVE_TEST_BEST_SHA", strings.Repeat("d", 40))

	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orch.steps.Step40 = forcedCandidateStep{}
	orch.steps.Step60 = scriptedStep60Step{decode: orch.decoders.Step60}

	runID := contracts.RunID("2026-04-21-PR45-abcdeff")
	require.NoError(t, orch.Run(context.Background(), 45, RunOptions{RunID: runID}))

	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(cfg.Paths.Runs, "needs-recovery", string(runID)+".json"))
	assert.NoFileExists(t, filepath.Join(stateDir, "after-push"))
	assert.FileExists(t, filepath.Join(cfg.Paths.Runs, string(runID), "70", "decision.json"))

	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindPromoted, events[len(events)-1].Kind)
}

func TestRun_DefaultSteps_RealWiringWithFakeCLIs_PolicyOnlyAdoptIgnoresPostPushSentinelScript(t *testing.T) {
	cfg := testConfig(t)
	cfg.Repo.Root = repoRootFromTestFile(t)
	binDir := installFakeCLI(t)
	sentinelPath := filepath.Join(cfg.Paths.Runs, "needs-recovery", "other-run.json")
	overwriteFakeGitScript(t, binDir, postPushRollbackGitScript())
	stateDir := t.TempDir()
	t.Setenv("AUTO_IMPROVE_GIT_STATE_DIR", stateDir)
	t.Setenv("AUTO_IMPROVE_TEST_SENTINEL_PATH", sentinelPath)
	cfg.ClaudeCLIPath = filepath.Join(binDir, "claude")

	t.Setenv("AUTO_IMPROVE_TEST_BASE_SHA", strings.Repeat("a", 40))
	t.Setenv("AUTO_IMPROVE_TEST_TARGET_SHA", strings.Repeat("b", 40))
	t.Setenv("AUTO_IMPROVE_TEST_MERGE_SHA", strings.Repeat("c", 40))
	t.Setenv("AUTO_IMPROVE_TEST_BEST_SHA", strings.Repeat("d", 40))

	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orch.steps.Step40 = forcedCandidateStep{}
	orch.steps.Step60 = scriptedStep60Step{decode: orch.decoders.Step60}

	runID := contracts.RunID("2026-04-21-PR46-abcdeff")
	require.NoError(t, orch.Run(context.Background(), 46, RunOptions{RunID: runID}))

	decision, err := internalio.ReadJSON[contracts.Decision](filepath.Join(cfg.Paths.Runs, string(runID), "70", "decision.json"))
	require.NoError(t, err)
	assert.Equal(t, contracts.DecisionActionAdopt, decision.Action)

	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	assert.FileExists(t, runCtx.RulesRegistryPath())
	assert.NoFileExists(t, sentinelPath)
	assert.NoFileExists(t, filepath.Join(stateDir, "pushed-target"))

	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	assert.Contains(t, eventKinds(events), contracts.StateKindPromoted)
	assert.NotContains(t, eventKinds(events), contracts.StateKindRollback)
}

func TestRun_DefaultSteps_RealWiringWithFakeCLIs_ResumesBranchPushedIntention(t *testing.T) {
	cfg := testConfig(t)
	cfg.Repo.Root = repoRootFromTestFile(t)
	binDir := installFakeCLI(t)
	cfg.ClaudeCLIPath = filepath.Join(binDir, "claude")

	t.Setenv("AUTO_IMPROVE_TEST_BASE_SHA", strings.Repeat("a", 40))
	t.Setenv("AUTO_IMPROVE_TEST_TARGET_SHA", strings.Repeat("b", 40))
	t.Setenv("AUTO_IMPROVE_TEST_MERGE_SHA", strings.Repeat("c", 40))
	t.Setenv("AUTO_IMPROVE_TEST_BEST_SHA", strings.Repeat("d", 40))

	runID := contracts.RunID("2026-04-21-PR47-abcdeff")

	first, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	first.steps.Step40 = forcedCandidateStep{}
	first.steps.Step60 = scriptedStep60Step{decode: first.decoders.Step60}
	first.steps.Step70 = branchPushedCrashStep{t: t}
	require.ErrorContains(t, first.Run(context.Background(), 47, RunOptions{RunID: runID}), "simulated branch_pushed crash")

	second, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	second.steps.Step40 = forcedCandidateStep{}
	second.steps.Step60 = scriptedStep60Step{decode: second.decoders.Step60}
	require.NoError(t, second.Run(context.Background(), 47, RunOptions{RunID: runID}))

	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	decision, err := internalio.ReadJSON[contracts.Decision](filepath.Join(cfg.Paths.Runs, string(runID), "70", "decision.json"))
	require.NoError(t, err)
	assert.Equal(t, contracts.DecisionActionAdopt, decision.Action)

	lines, err := internalio.ReadJSONL[contracts.RuleRegistryEntry](runCtx.RulesRegistryPath())
	require.NoError(t, err)
	require.Len(t, lines, 1)

	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	assert.Contains(t, eventKinds(events), contracts.StateKindPromoted)
}
