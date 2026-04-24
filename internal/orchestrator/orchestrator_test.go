package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/agents"
	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
	"github.com/nishimoto265/auto-improve/internal/processenv"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
	"github.com/nishimoto265/auto-improve/internal/steps/step20_implement"
	"github.com/nishimoto265/auto-improve/internal/steps/step30_score"
	"github.com/nishimoto265/auto-improve/internal/steps/step60_scorepairwise"
	"github.com/nishimoto265/auto-improve/internal/steps/step70_decide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_OrdersStepsAcrossParallelPasses(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	recorder := &callRecorder{}
	orch.steps = Steps{
		Step10:  recordingStep{label: "10", recorder: recorder},
		Step20:  recordingAgentSteps("20", recorder),
		Step30:  recordingStep{label: "30", recorder: recorder},
		Step40:  recordingStep{label: "40", recorder: recorder},
		Step50:  recordingAgentSteps("50", recorder),
		Step60:  recordingStep{label: "60", recorder: recorder},
		Step70:  recordingStep{label: "70", recorder: recorder},
		Archive: recordingStep{label: "archive", recorder: recorder},
	}

	err = orch.Run(context.Background(), 42, RunOptions{
		RunID: "2026-04-21-PR42-abcdef0",
	})
	require.NoError(t, err)

	calls := recorder.snapshot()
	require.Len(t, calls, 12)
	assert.Equal(t, "10", calls[0])
	assert.Contains(t, calls[1:4], "20:a1")
	assert.Contains(t, calls[1:4], "20:a2")
	assert.Contains(t, calls[1:4], "20:a3")
	assert.Equal(t, "30", calls[4])
	assert.Equal(t, "40", calls[5])
	assert.Contains(t, calls[6:9], "50:a1")
	assert.Contains(t, calls[6:9], "50:a2")
	assert.Contains(t, calls[6:9], "50:a3")
	assert.Equal(t, "60", calls[9])
	assert.Equal(t, "70", calls[10])
	assert.Equal(t, "archive", calls[11])
}

func TestRun_ResumesFromIntentionStage(t *testing.T) {
	cfg := testConfig(t)
	runID := contracts.RunID("2026-04-21-PR55-abcdef0")
	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)

	require.NoError(t, seedResumeRun(t, runCtx, 55))
	writer := state.NewWriter(runCtx)
	require.NoError(t, writer.Append(contracts.StateEntry{
		Kind: contracts.StateKindInterrupted,
		Value: contracts.StateEntryInterrupted{
			Kind:   contracts.StateKindInterrupted,
			PR:     55,
			RunID:  runID,
			Step:   contracts.FailedStep70,
			Reason: contracts.InterruptedReasonUnknown,
			At:     time.Now().UTC(),
		},
	}))

	store := NewIntentionStore(runCtx)
	intention := validPlanningIntention(runID)
	require.NoError(t, store.Save(intention))

	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	recorder := &callRecorder{}
	orch.steps = Steps{
		Step10:  recordingStep{label: "10", recorder: recorder},
		Step20:  recordingAgentSteps("20", recorder),
		Step30:  recordingStep{label: "30", recorder: recorder},
		Step40:  recordingStep{label: "40", recorder: recorder},
		Step50:  recordingAgentSteps("50", recorder),
		Step60:  recordingStep{label: "60", recorder: recorder},
		Step70:  stubStep70{},
		Archive: recordingStep{label: "archive", recorder: recorder},
	}

	err = orch.Run(context.Background(), 55, RunOptions{})
	require.NoError(t, err)

	assert.Equal(t, []string{"archive"}, recorder.snapshot())
	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	assert.FileExists(t, decisionPath)
	loaded, err := store.Load()
	require.NoError(t, err)
	assert.Nil(t, loaded)
}

func TestRun_ResumeUsesConfigSnapshotPolicyBranch(t *testing.T) {
	cfg := testConfig(t)
	cfg.Repo.PolicyBranch = "current-policy"
	snapshotWorktreeBase := cfg.Worktree.Base
	runID := contracts.RunID("2026-04-21-PR56-abcdef0")
	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, snapshotWorktreeBase)
	require.NoError(t, err)
	require.NoError(t, seedResumeRun(t, runCtx, 56))
	require.NoError(t, os.WriteFile(filepath.Join(runCtx.RunDir(), "config.snapshot.yaml"), []byte(
		"repo:\n"+
			"  root: "+cfg.Repo.Root+"\n"+
			"  default_branch: main\n"+
			"  best_branch: best\n"+
			"  policy_branch: snapshot-policy\n"+
			"paths:\n"+
			"  runs: "+cfg.Paths.Runs+"\n"+
			"worktree:\n"+
			"  base: "+snapshotWorktreeBase+"\n",
	), 0o644))
	currentWorktreeBase := filepath.Join(t.TempDir(), "current-worktrees")
	require.NoError(t, os.MkdirAll(currentWorktreeBase, 0o755))
	cfg.Worktree.Base = currentWorktreeBase

	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orch.steps = stubPipelineSteps(nil, assertPolicyBranchStep{t: t, want: "snapshot-policy"})

	require.NoError(t, orch.Run(context.Background(), 56, RunOptions{}))
}

func TestRun_ResumeKeepsAgentProfileFromSnapshotWhenLiveAgentsFileChanges(t *testing.T) {
	for name, mutateAgentsFile := range map[string]func(t *testing.T, path string){
		"modified": func(t *testing.T, path string) {
			t.Helper()
			require.NoError(t, os.WriteFile(path, []byte(`
profiles:
  claude_impl:
    provider: claude
    binary: claude
  stub:
    provider: stub
roles:
  implementer: claude_impl
  judge_primary: stub
  judge_secondary: stub
  judge_arbiter: stub
`), 0o644))
		},
		"deleted": func(t *testing.T, path string) {
			t.Helper()
			require.NoError(t, os.Remove(path))
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			repoRoot := filepath.Join(root, "repo")
			runsBase := filepath.Join(root, "runs")
			worktreeBase := filepath.Join(root, "worktrees")
			agentsPath := filepath.Join(repoRoot, "agents.yaml")
			require.NoError(t, os.MkdirAll(repoRoot, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "config.yaml"), []byte(fmt.Sprintf(`
repo:
  root: %q
  default_branch: "main"
  best_branch: "best"
paths:
  runs: %q
worktree:
  base: %q
agent_config_path: "./agents.yaml"
`, repoRoot, runsBase, worktreeBase)), 0o644))
			require.NoError(t, os.WriteFile(agentsPath, []byte(`
profiles:
  codex_impl:
    provider: codex
    binary: codex
  stub:
    provider: stub
roles:
  implementer: codex_impl
  judge_primary: stub
  judge_secondary: stub
  judge_arbiter: stub
`), 0o644))
			cfg, err := config.LoadConfig(filepath.Join(repoRoot, "config.yaml"))
			require.NoError(t, err)

			runID := contracts.RunID("2026-04-21-PR57-abcdef0")
			runCtx, err := internalio.NewRunContext(runID, runsBase, worktreeBase)
			require.NoError(t, err)
			require.NoError(t, seedResumeRun(t, runCtx, 57))
			snapshotPath := filepath.Join(runCtx.RunDir(), "config.snapshot.yaml")
			require.NoError(t, os.Remove(snapshotPath))
			require.NoError(t, writeConfigSnapshot(snapshotPath, cfg))
			mutateAgentsFile(t, agentsPath)

			orch, err := NewOrchestrator(cfg)
			require.NoError(t, err)
			orch.steps = stubPipelineSteps(nil, assertImplementerProviderStep{
				t:    t,
				want: agents.ProviderCodex,
			})

			require.NoError(t, orch.Run(context.Background(), 57, RunOptions{}))
		})
	}
}

func TestRun_DefaultStub_EndToEnd(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orch.steps.Step10 = stubStep10{}
	orch.steps.Step20 = stubAgentSteps()
	orch.steps.Step50 = stubAgentSteps()
	orch.steps.Step70 = stubStep70{}

	err = orch.Run(context.Background(), 77, RunOptions{
		RunID: "2026-04-21-PR77-abcdef0",
	})
	require.NoError(t, err)

	taskPackagePath := filepath.Join(cfg.Paths.Runs, "2026-04-21-PR77-abcdef0", "task-package.json")
	decisionPath := filepath.Join(cfg.Paths.Runs, "2026-04-21-PR77-abcdef0", "70", "decision.json")
	assert.FileExists(t, taskPackagePath)
	assert.FileExists(t, decisionPath)

	pkg, err := internalio.ReadJSON[contracts.TaskPackage](taskPackagePath)
	require.NoError(t, err)
	for _, worktree := range pkg.Worktrees {
		assert.NoDirExists(t, worktree.Path)
	}

	runCtx, err := internalio.RunContextFromTaskPackage(pkg, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	events, err := state.ScanEventsForRun(runCtx, pkg.RunID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindCompleted, events[len(events)-1].Kind)
}

func TestRun_DefaultStub_EndToEnd_UsesNamespacedStateWhenRepoGitHubIsSet(t *testing.T) {
	cfg := testConfig(t)
	cfg.Repo.GitHub = "owner/repo"
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orch.steps.Step10 = stubStep10{}
	orch.steps.Step20 = stubAgentSteps()
	orch.steps.Step50 = stubAgentSteps()
	orch.steps.Step70 = stubStep70{}

	err = orch.Run(context.Background(), 78, RunOptions{
		RunID: "2026-04-21-PR78-abcdef0",
	})
	require.NoError(t, err)

	runsBase, err := cfg.RunsBase()
	require.NoError(t, err)
	worktreeBase, err := cfg.WorktreeBase()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(filepath.Dir(cfg.Paths.Runs), "owner__repo", "runs"), runsBase)
	assert.Equal(t, filepath.Join(filepath.Dir(cfg.Worktree.Base), "owner__repo", "worktrees"), worktreeBase)
	assert.FileExists(t, filepath.Join(runsBase, "2026-04-21-PR78-abcdef0", "70", "decision.json"))
}

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

func TestRun_DefaultSteps_RealWiringWithFakeCLIs_NeedsManualRecovery(t *testing.T) {
	cfg := testConfig(t)
	cfg.Repo.Root = repoRootFromTestFile(t)
	binDir := installFakeCLI(t)
	overwriteFakeGitScript(t, binDir, manualRecoveryGitScript())
	t.Setenv("AUTO_IMPROVE_GIT_STATE_DIR", t.TempDir())
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
	assert.FileExists(t, filepath.Join(cfg.Paths.Runs, "needs-recovery", string(runID)+".json"))

	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindNeedsManualRecovery, events[len(events)-1].Kind)
	assert.NoFileExists(t, filepath.Join(cfg.Paths.Runs, string(runID), "70", "decision.json"))
}

func TestRun_DefaultSteps_RealWiringWithFakeCLIs_PostPushRollback(t *testing.T) {
	cfg := testConfig(t)
	cfg.Repo.Root = repoRootFromTestFile(t)
	binDir := installFakeCLI(t)
	sentinelPath := filepath.Join(cfg.Paths.Runs, "needs-recovery", "other-run.json")
	overwriteFakeGitScript(t, binDir, postPushRollbackGitScript())
	t.Setenv("AUTO_IMPROVE_GIT_STATE_DIR", t.TempDir())
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
	assert.Equal(t, contracts.DecisionActionRollback, decision.Action)

	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	assert.NoFileExists(t, runCtx.RulesRegistryPath())

	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	assert.Contains(t, eventKinds(events), contracts.StateKindRollback)
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

func TestRun_AllNonScorablePass1StopsBeforeStep30(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	recorder := &callRecorder{}
	orch.steps = Steps{
		Step10:  stubStep10{},
		Step20:  nonScorableAgentSteps(contracts.ManifestKindError),
		Step30:  recordingStep{label: "30", recorder: recorder},
		Step40:  recordingStep{label: "40", recorder: recorder},
		Step50:  stubAgentSteps(),
		Step60:  recordingStep{label: "60", recorder: recorder},
		Step70:  stubStep70{},
		Archive: recordingStep{label: "archive", recorder: recorder},
	}

	runID := contracts.RunID("2026-04-21-PR45-abcdef0")
	require.NoError(t, orch.Run(context.Background(), 45, RunOptions{RunID: runID}))

	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindFailed, events[len(events)-1].Kind)
	assert.Empty(t, recorder.snapshot())
	assert.NoFileExists(t, filepath.Join(cfg.Paths.Runs, string(runID), "30", "done.marker"))
}

func TestRun_AllNonScorablePass2StopsBeforeStep70(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	recorder := &callRecorder{}
	orch.steps = Steps{
		Step10:  stubStep10{},
		Step20:  stubAgentSteps(),
		Step30:  stubMarkerStep{path: "30/done.marker"},
		Step40:  forcedCandidateStep{},
		Step50:  nonScorableAgentSteps(contracts.ManifestKindError),
		Step60:  step60Step{cfg: cfg},
		Step70:  recordingStep{label: "70", recorder: recorder},
		Archive: recordingStep{label: "archive", recorder: recorder},
	}

	runID := contracts.RunID("2026-04-21-PR451-abcdef0")
	require.NoError(t, orch.Run(context.Background(), 451, RunOptions{RunID: runID}))

	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	assert.Equal(t, contracts.StateKindFailed, last.Kind)
	failed, ok := last.Value.(contracts.StateEntryFailed)
	require.True(t, ok)
	assert.Equal(t, "no_scorable_agents", failed.Reason)
	assert.NotContains(t, recorder.snapshot(), "70")
}

func TestRun_ProviderInterruptedPass1IsNonTerminalInterrupted(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	recorder := &callRecorder{}
	orch.steps = Steps{
		Step10:  stubStep10{},
		Step20:  providerInterruptedAgentSteps("rate_limit"),
		Step30:  recordingStep{label: "30", recorder: recorder},
		Step40:  recordingStep{label: "40", recorder: recorder},
		Step50:  stubAgentSteps(),
		Step60:  recordingStep{label: "60", recorder: recorder},
		Step70:  stubStep70{},
		Archive: recordingStep{label: "archive", recorder: recorder},
	}

	runID := contracts.RunID("2026-04-21-PR452-abcdef0")
	require.NoError(t, orch.Run(context.Background(), 452, RunOptions{RunID: runID}))

	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	assert.Equal(t, contracts.StateKindInterrupted, last.Kind)
	interrupted, ok := last.Value.(contracts.StateEntryInterrupted)
	require.True(t, ok)
	assert.Equal(t, contracts.FailedStep20, interrupted.Step)
	assert.Equal(t, contracts.InterruptedReasonRateLimit, interrupted.Reason)
	assert.Empty(t, recorder.snapshot())
}

func TestRun_ProviderInterruptedPass2IsNonTerminalInterrupted(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	recorder := &callRecorder{}
	orch.steps = Steps{
		Step10:  stubStep10{},
		Step20:  stubAgentSteps(),
		Step30:  stubMarkerStep{path: "30/done.marker"},
		Step40:  forcedCandidateStep{},
		Step50:  providerInterruptedAgentSteps("budget"),
		Step60:  recordingStep{label: "60", recorder: recorder},
		Step70:  recordingStep{label: "70", recorder: recorder},
		Archive: recordingStep{label: "archive", recorder: recorder},
	}

	runID := contracts.RunID("2026-04-21-PR453-abcdef0")
	require.NoError(t, orch.Run(context.Background(), 453, RunOptions{RunID: runID}))

	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	assert.Equal(t, contracts.StateKindInterrupted, last.Kind)
	interrupted, ok := last.Value.(contracts.StateEntryInterrupted)
	require.True(t, ok)
	assert.Equal(t, contracts.FailedStep50, interrupted.Step)
	assert.Equal(t, contracts.InterruptedReasonBudget, interrupted.Reason)
	assert.NotContains(t, recorder.snapshot(), "60")
	assert.NotContains(t, recorder.snapshot(), "70")
}

func TestRun_ContextCancellationDuringStepRecordsInterrupted(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	orch.steps = Steps{
		Step10: cancelingStep{cancel: cancel},
		Step20: stubAgentSteps(),
		Step70: stubStep70{},
	}

	runID := contracts.RunID("2026-04-21-PR454-abcdef0")
	require.NoError(t, orch.Run(ctx, 454, RunOptions{RunID: runID}))

	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	assert.Equal(t, contracts.StateKindInterrupted, last.Kind)
	interrupted, ok := last.Value.(contracts.StateEntryInterrupted)
	require.True(t, ok)
	assert.Equal(t, contracts.FailedStep10, interrupted.Step)
	assert.Equal(t, contracts.InterruptedReasonContext, interrupted.Reason)
}

func TestRun_FromScratchSupersedesNonTerminalRunAndPrunesWorktrees(t *testing.T) {
	cfg := testConfig(t)
	oldRunID := contracts.RunID("2026-04-21-PR455-abcdef0")
	oldRunCtx, err := internalio.NewRunContext(oldRunID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(oldRunCtx.RunDir(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(oldRunCtx.RunDir(), "config.snapshot.yaml"), []byte(
		"repo:\n"+
			"  root: "+cfg.Repo.Root+"\n"+
			"  default_branch: main\n"+
			"  best_branch: best\n"+
			"paths:\n"+
			"  runs: "+cfg.Paths.Runs+"\n"+
			"worktree:\n"+
			"  base: "+cfg.Worktree.Base+"\n",
	), 0o644))
	pkg := stubTaskPackageForRun(oldRunCtx, 455)
	require.NoError(t, internalio.WriteJSONAtomic(oldRunCtx.TaskPackagePath(), pkg))
	for _, wt := range pkg.Worktrees {
		require.NoError(t, os.MkdirAll(wt.Path, 0o755))
	}
	require.NoError(t, state.NewWriter(oldRunCtx).Append(startedEntry(455, oldRunID, time.Now().UTC())))
	require.NoError(t, state.NewWriter(oldRunCtx).Append(contracts.StateEntry{
		Kind: contracts.StateKindInterrupted,
		Value: contracts.StateEntryInterrupted{
			Kind:   contracts.StateKindInterrupted,
			PR:     455,
			RunID:  oldRunID,
			Step:   contracts.FailedStep20,
			Reason: contracts.InterruptedReasonUnknown,
			At:     time.Now().UTC(),
		},
	}))

	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orch.steps = stubPipelineSteps(nil, stubStep70{})

	require.NoError(t, orch.Run(context.Background(), 455, RunOptions{FromScratch: true}))

	oldEvents, err := state.ScanEventsForRun(oldRunCtx, oldRunID)
	require.NoError(t, err)
	require.NotEmpty(t, oldEvents)
	lastOld := oldEvents[len(oldEvents)-1]
	assert.Equal(t, contracts.StateKindSkipped, lastOld.Kind)
	skipped, ok := lastOld.Value.(contracts.StateEntrySkipped)
	require.True(t, ok)
	assert.Equal(t, "superseded_by_from_scratch", skipped.Detail)
	for _, wt := range pkg.Worktrees {
		assert.NoDirExists(t, wt.Path)
	}

	latest, err := state.LatestRunForPR(oldRunCtx, 455)
	require.NoError(t, err)
	require.NotNil(t, latest.LastEvent)
	assert.NotEqual(t, oldRunID, latest.RunID)
	assert.Equal(t, state.NextActionFreshStart, latest.Action)
}

func TestRun_Step70NeedsManualRecovery_EndToEnd(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	orch.steps.Step10 = stubStep10{}
	orch.steps.Step20 = stubAgentSteps()
	orch.steps.Step40 = forcedCandidateStep{}
	orch.steps.Step50 = stubAgentSteps()
	orch.steps.Step60 = scriptedStep60Step{decode: orch.decoders.Step60}
	orch.steps.Step70 = orchestratorStep70{
		git: testStep70Git{
			pushErr: step70_decide.ErrRemoteDivergence,
			state: &testStep70GitState{
				head: strings.Repeat("d", 40),
				onPush: func(s *testStep70GitState) {
					s.head = strings.Repeat("9", 40)
				},
			},
		},
		resolver: testStep70Resolver{},
	}

	runID := contracts.RunID("2026-04-21-PR46-abcdef0")
	require.NoError(t, orch.Run(context.Background(), 46, RunOptions{RunID: runID}))

	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(cfg.Paths.Runs, "needs-recovery", string(runID)+".json"))
	assert.NoFileExists(t, filepath.Join(cfg.Paths.Runs, string(runID), "70", "decision.json"))

	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Contains(t, eventKinds(events), contracts.StateKindNeedsManualRecovery)
}

func TestRun_Step70PolicySnapshotStaleStartsFreshNextRun(t *testing.T) {
	cfg := testConfig(t)
	first, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	first.steps = stubPipelineSteps(nil, policySnapshotStaleStep{})

	runID := contracts.RunID("2026-04-21-PR410-abcdef0")
	require.NoError(t, first.Run(context.Background(), 410, RunOptions{RunID: runID}))

	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	assert.Equal(t, contracts.StateKindInterrupted, last.Kind)
	interrupted, ok := last.Value.(contracts.StateEntryInterrupted)
	require.True(t, ok)
	assert.Contains(t, interrupted.Detail, "policy_snapshot_stale")

	second, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	recorder := &callRecorder{}
	second.steps = stubPipelineSteps(recordingStep{label: "10", recorder: recorder}, stubStep70{})
	require.NoError(t, second.Run(context.Background(), 410, RunOptions{RunID: runID}))

	assert.Contains(t, recorder.snapshot(), "10")
}

func TestRun_DuplicateOnlyCandidatesSkipPass2AndStep60(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	recorder := &callRecorder{}
	orch.steps.Step10 = stubStep10{}
	orch.steps.Step20 = stubAgentSteps()
	orch.steps.Step30 = stubMarkerStep{path: "30/done.marker"}
	orch.steps.Step40 = duplicateOnlyCandidateStep{}
	orch.steps.Step50 = recordingAgentSteps("50", recorder)
	orch.steps.Step60 = recordingStep{label: "60", recorder: recorder}
	orch.steps.Step70 = stubStep70{}
	orch.steps.Archive = recordingStep{label: "archive", recorder: recorder}

	runID := contracts.RunID("2026-04-21-PR50-abcdef0")
	require.NoError(t, orch.Run(context.Background(), 50, RunOptions{RunID: runID}))

	assert.NotContains(t, recorder.snapshot(), "50:a1")
	assert.NotContains(t, recorder.snapshot(), "50:a2")
	assert.NotContains(t, recorder.snapshot(), "50:a3")
	assert.NotContains(t, recorder.snapshot(), "60")
}

func TestRun_RescueExhaustedWritesGlobalSentinel(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	orch.steps.Step10 = stubStep10{}
	orch.steps.Step20 = map[contracts.AgentID]Step{
		"a1": rescueExhaustedStep{},
		"a2": rescueExhaustedStep{},
		"a3": rescueExhaustedStep{},
	}

	runID := contracts.RunID("2026-04-21-PR77-abcdef0")
	require.NoError(t, orch.Run(context.Background(), 77, RunOptions{RunID: runID}))

	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(cfg.Paths.Runs, "needs-recovery", string(runID)+".json"))

	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Contains(t, eventKinds(events), contracts.StateKindNeedsManualRecovery)
	manualIndex := -1
	warningIndex := -1
	for idx, event := range events {
		if event.Kind == contracts.StateKindNeedsManualRecovery && manualIndex == -1 {
			manualIndex = idx
		}
		if event.Kind == contracts.StateKindWarningRescueRetry && warningIndex == -1 {
			warningIndex = idx
		}
	}
	require.NotEqual(t, -1, manualIndex)
	require.NotEqual(t, -1, warningIndex)
	assert.Less(t, manualIndex, warningIndex)
}

func TestRun_RescueExhaustedBlocksOtherPRs(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	orch.steps.Step10 = stubStep10{}
	orch.steps.Step20 = map[contracts.AgentID]Step{
		"a1": rescueExhaustedStep{},
		"a2": rescueExhaustedStep{},
		"a3": rescueExhaustedStep{},
	}

	require.NoError(t, orch.Run(context.Background(), 77, RunOptions{RunID: "2026-04-21-PR77-abcdef0"}))

	other, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	other.steps.Step10 = stubStep10{}
	other.steps.Step20 = stubAgentSteps()
	other.steps.Step70 = stubStep70{}
	err = other.Run(context.Background(), 78, RunOptions{RunID: "2026-04-21-PR78-abcdef0"})
	require.Error(t, err)
	var blocked *GlobalNeedsRecoveryError
	require.ErrorAs(t, err, &blocked)
	assert.Equal(t, contracts.RunID("2026-04-21-PR77-abcdef0"), blocked.Sentinel.RunID)
}

func TestRun_ResumeStep30WithNoScorableAgentsFailsImmediately(t *testing.T) {
	cfg := testConfig(t)
	runID := contracts.RunID("2026-04-21-PR78-abcdef0")
	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunDir(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runCtx.RunDir(), "config.snapshot.yaml"), []byte(
		"repo:\n"+
			"  root: "+cfg.Repo.Root+"\n"+
			"  default_branch: main\n"+
			"  best_branch: best\n"+
			"paths:\n"+
			"  runs: "+cfg.Paths.Runs+"\n"+
			"worktree:\n"+
			"  base: "+cfg.Worktree.Base+"\n",
	), 0o644))

	pkg := stubTaskPackageForRun(runCtx, 78)
	require.NoError(t, internalio.WriteJSONAtomic(runCtx.TaskPackagePath(), pkg))
	for _, agent := range defaultAgents {
		manifestPath, err := runCtx.ManifestPath(1, agent)
		require.NoError(t, err)
		require.NoError(t, internalio.WriteJSONAtomic(manifestPath, contracts.Manifest{
			Kind: contracts.ManifestKindError,
			Value: contracts.ManifestError{
				Kind:          contracts.ManifestKindError,
				SchemaVersion: "1",
				RunID:         runID,
				Pass:          1,
				Agent:         agent,
				ExitCode:      1,
				Reason:        "unknown",
				Detail:        "fixture non-scorable manifest",
				StartedAt:     time.Now().UTC(),
				FinishedAt:    time.Now().UTC(),
			},
		}))
	}
	require.NoError(t, writeRunText(runCtx, "30/done.marker", "stub\n"))
	require.NoError(t, state.NewWriter(runCtx).Append(startedEntry(78, runID, time.Now().UTC())))

	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	recorder := &callRecorder{}
	orch.steps.Step30 = recordingStep{label: "30", recorder: recorder}

	require.NoError(t, orch.Run(context.Background(), 78, RunOptions{RunID: runID}))
	assert.NotContains(t, recorder.snapshot(), "30")

	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	assert.Equal(t, contracts.StateKindFailed, last.Kind)
	failed, ok := last.Value.(contracts.StateEntryFailed)
	require.True(t, ok)
	assert.Equal(t, "no_scorable_agents", failed.Reason)
}

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

func TestRun_Step70TerminalEventSkipsStepDoneAndNextTickDoesNotResume(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	orch.steps.Step10 = stubStep10{}
	orch.steps.Step20 = stubAgentSteps()
	orch.steps.Step30 = stubMarkerStep{path: "30/done.marker"}
	orch.steps.Step40 = duplicateOnlyCandidateStep{}
	orch.steps.Step50 = stubAgentSteps()
	orch.steps.Step60 = scriptedStep60Step{decode: orch.decoders.Step60}
	orch.steps.Step70 = terminalPromoteStep{}

	runID := contracts.RunID("2026-04-21-PR80-abcdef0")
	require.NoError(t, orch.Run(context.Background(), 80, RunOptions{RunID: runID}))

	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	latest, err := state.LatestRunForPR(runCtx, 80)
	require.NoError(t, err)
	require.NotNil(t, latest.LastEvent)
	assert.Equal(t, contracts.StateKindPromoted, latest.LastEvent.Kind)
	assert.Equal(t, state.NextActionFreshStart, latest.Action)
}

func TestRun_LeaseContentionBecomesInterruptedAndResumeSucceeds(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	leaseStep := &leaseContendedOnceStep{}
	orch.steps.Step10 = stubStep10{}
	orch.steps.Step20 = map[contracts.AgentID]Step{
		"a1": leaseStep,
		"a2": stubImplementStep{},
		"a3": stubImplementStep{},
	}
	orch.steps.Step30 = stubMarkerStep{path: "30/done.marker"}
	orch.steps.Step40 = duplicateOnlyCandidateStep{}
	orch.steps.Step70 = stubStep70{}

	runID := contracts.RunID("2026-04-21-PR81-abcdef0")
	require.NoError(t, orch.Run(context.Background(), 81, RunOptions{RunID: runID}))

	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	assert.Contains(t, eventKinds(events), contracts.StateKindInterrupted)

	require.NoError(t, orch.Run(context.Background(), 81, RunOptions{RunID: runID}))
	events, err = state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	assert.Contains(t, eventKinds(events), contracts.StateKindCompleted)
}

func TestRealStep70_RejectsTamperedFreshDecision(t *testing.T) {
	cfg := testConfig(t)
	runCtx, err := internalio.NewRunContext("2026-04-21-PR82-abcdef0", cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunDir(), 0o755))

	pkg := stubTaskPackageForRun(runCtx, 82)
	candidates := forcedCandidate(runCtx.RunID)
	run := &StepRunContext{
		Config:        cfg,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		PR:            82,
		IO:            runCtx,
		TaskPackage:   &pkg,
		Candidates:    candidates,
		IntentionFile: NewIntentionStore(runCtx),
	}
	step := realStep70{
		cfg: cfg,
		decode: func(data []byte, req any) (any, error) {
			return stepio.DecodeAndValidateStep70Response(data, req.(stepio.Step70Request))
		},
		runFn: func(ctx context.Context, pr int, runCtx internalio.RunContext, pkg *contracts.TaskPackage, candidates *contracts.Candidates, store step70_decide.IntentionWriter, deps step70_decide.Deps) error {
			_ = ctx
			_ = pr
			_ = store
			_ = deps
			path, err := runCtx.ResolveRunRelative("70/decision.json")
			if err != nil {
				return err
			}
			return internalio.WriteJSONAtomic(path, contracts.Decision{
				Action: contracts.DecisionActionNoop,
				Value: contracts.DecisionNoop{
					Action:        contracts.DecisionActionNoop,
					SchemaVersion: "1",
					RunID:         "2026-04-21-PR999-deadbee",
					Reason:        "tampered",
					DecidedAt:     time.Now().UTC(),
				},
			})
		},
	}

	err = step.Run(context.Background(), run)
	require.ErrorContains(t, err, "run_id")
}

func TestRun_FreshDecisionRunIDMismatchRejectedOnTerminalAppend(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orch.steps = stubPipelineSteps(nil, tamperedDecisionStep70{runID: "2026-04-21-PR999-deadbee"})

	err = orch.Run(context.Background(), 82, RunOptions{RunID: "2026-04-21-PR82-abcdef0"})
	require.ErrorContains(t, err, "decision run_id mismatch")
}

func TestLoadRunContext_RejectsTaskPackageRunIDMismatch(t *testing.T) {
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	runCtx, err := internalio.NewRunContext("2026-04-21-PR83-abcdef0", runsBase, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunDir(), 0o755))

	pkg := stubTaskPackageForRun(runCtx, 83)
	pkg.RunID = "2026-04-21-PR84-badcafe"
	require.NoError(t, internalio.WriteJSONAtomic(runCtx.TaskPackagePath(), pkg))

	_, err = loadRunContext(runCtx.RunID, runsBase, worktreeBase)
	require.ErrorContains(t, err, "task package run_id mismatch")
}

func TestLoadRunContext_RejectsCandidatesRunIDMismatch(t *testing.T) {
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	runCtx, err := internalio.NewRunContext("2026-04-21-PR84-abcdef0", runsBase, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunDir(), 0o755))

	pkg := stubTaskPackageForRun(runCtx, 84)
	require.NoError(t, internalio.WriteJSONAtomic(runCtx.TaskPackagePath(), pkg))

	candidatesPath, err := runCtx.ResolveRunRelative("40/candidates.json")
	require.NoError(t, err)
	candidates := forcedCandidate("2026-04-21-PR85-badcafe")
	require.NoError(t, internalio.WriteJSONAtomic(candidatesPath, candidates))

	_, err = loadRunContext(runCtx.RunID, runsBase, worktreeBase)
	require.ErrorContains(t, err, "candidates run_id mismatch")
}

func TestLoadRunContext_RejectsIntentionRunIDMismatch(t *testing.T) {
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	runCtx, err := internalio.NewRunContext("2026-04-21-PR86-abcdef0", runsBase, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunDir(), 0o755))

	pkg := stubTaskPackageForRun(runCtx, 86)
	require.NoError(t, internalio.WriteJSONAtomic(runCtx.TaskPackagePath(), pkg))

	store := NewIntentionStore(runCtx)
	intention := validPlanningIntention("2026-04-21-PR87-badcafe")
	intention.RunID = "2026-04-21-PR87-badcafe"
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runCtx.RunDir(), "70", "intention.json"), intention))

	_, err = loadRunContext(runCtx.RunID, runsBase, worktreeBase)
	require.ErrorContains(t, err, "intention run_id mismatch")

	loaded, loadErr := store.Load()
	require.ErrorContains(t, loadErr, "run_id mismatch")
	assert.Nil(t, loaded)
}

func TestNewFreshSelection_RejectsRunIDPRMismatch(t *testing.T) {
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()

	_, err := newFreshSelection(105, RunOptions{RunID: "2026-04-21-PR104-abcdef0"}, runsBase, worktreeBase)
	require.ErrorContains(t, err, "run_id PR mismatch")
}

func TestNewFreshSelection_RejectsCompletedRunIDReuse(t *testing.T) {
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	runCtx, err := internalio.NewRunContext("2026-04-21-PR105-abcdef0", runsBase, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, state.Append(runCtx, completedEntry(105, runCtx.RunID, contracts.FailedStep70, time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC))))

	_, err = newFreshSelection(105, RunOptions{RunID: runCtx.RunID}, runsBase, worktreeBase)
	require.ErrorContains(t, err, "terminal state")
}

func TestNewFreshSelection_RejectsNonEmptyRunDir(t *testing.T) {
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	runCtx, err := internalio.NewRunContext("2026-04-21-PR110-abcdef0", runsBase, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunDir(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runCtx.RunDir(), "stale.txt"), []byte("stale\n"), 0o644))

	_, err = newFreshSelection(110, RunOptions{RunID: runCtx.RunID}, runsBase, worktreeBase)
	require.ErrorContains(t, err, "empty run dir")
}

func TestRun_RejectsSymlinkedRunStepDir(t *testing.T) {
	cfg := testConfig(t)
	runID := contracts.RunID("2026-04-21-PR111-abcdef0")
	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, seedResumeRun(t, runCtx, 111))

	escapeDir := filepath.Join(t.TempDir(), "escape")
	require.NoError(t, os.MkdirAll(escapeDir, 0o755))
	require.NoError(t, os.RemoveAll(filepath.Join(runCtx.RunDir(), "70")))
	require.NoError(t, os.Symlink(escapeDir, filepath.Join(runCtx.RunDir(), "70")))

	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orch.steps = stubPipelineSteps(nil, nil)

	err = orch.Run(context.Background(), 111, RunOptions{RunID: runID})
	require.Error(t, err)
	assert.NoFileExists(t, filepath.Join(escapeDir, "decision.json"))
}

func TestFirstNeedsRecoverySentinel_MalformedJSONMaintainsBlockedState(t *testing.T) {
	runsBase := t.TempDir()
	path := filepath.Join(runsBase, "needs-recovery", "2026-04-21-PR99-deadbee.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("{not-json"), 0o644))

	sentinel, blocked, err := firstNeedsRecoverySentinel(runsBase)
	require.NoError(t, err)
	assert.True(t, blocked)
	assert.Equal(t, contracts.RunID("2026-04-21-PR99-deadbee"), sentinel.RunID)
}

func TestFirstNeedsRecoverySentinel_RecreatesMissingSentinelFromProcessedState(t *testing.T) {
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	runCtx, err := internalio.NewRunContext("2026-04-21-PR99-deadbee", runsBase, worktreeBase)
	require.NoError(t, err)

	require.NoError(t, state.Append(runCtx, contracts.StateEntry{
		Kind: contracts.StateKindNeedsManualRecovery,
		Value: contracts.StateEntryNeedsManualRecovery{
			Kind:       contracts.StateKindNeedsManualRecovery,
			PR:         99,
			RunID:      runCtx.RunID,
			Step:       contracts.FailedStep70,
			Reason:     contracts.RollbackReasonTransactionalFailure,
			FailedStep: contracts.FailedStep70,
			At:         time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}))

	sentinel, blocked, err := firstNeedsRecoverySentinel(runsBase)
	require.NoError(t, err)
	assert.True(t, blocked)
	assert.Equal(t, runCtx.RunID, sentinel.RunID)
	assert.FileExists(t, filepath.Join(runsBase, "needs-recovery", string(runCtx.RunID)+".json"))
}

func TestFirstNeedsRecoverySentinel_RecreatesWorktreeRescueLoopSentinel(t *testing.T) {
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	runCtx, err := internalio.NewRunContext("2026-04-21-PR98-deadbee", runsBase, worktreeBase)
	require.NoError(t, err)

	require.NoError(t, state.Append(runCtx, contracts.StateEntry{
		Kind: contracts.StateKindNeedsManualRecovery,
		Value: contracts.StateEntryNeedsManualRecovery{
			Kind:       contracts.StateKindNeedsManualRecovery,
			PR:         98,
			RunID:      runCtx.RunID,
			Step:       contracts.FailedStep20,
			Reason:     contracts.RollbackReasonWorktreeRescueLoop,
			FailedStep: contracts.FailedStep20,
			At:         time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}))

	sentinel, blocked, err := firstNeedsRecoverySentinel(runsBase)
	require.NoError(t, err)
	assert.True(t, blocked)
	assert.Equal(t, runCtx.RunID, sentinel.RunID)
	assert.Equal(t, contracts.RollbackReasonWorktreeRescueLoop, sentinel.Reason)
	assert.Equal(t, contracts.FailedStep20, sentinel.FailedStep)
	assert.FileExists(t, filepath.Join(runsBase, "needs-recovery", string(runCtx.RunID)+".json"))
}

func TestRun_ClearedNeedsRecoveryMarkerSuppressesSentinelRehydration(t *testing.T) {
	cfg := testConfig(t)
	blockedRunID := contracts.RunID("2026-04-21-PR100-deadbee")
	blockedRunCtx, err := internalio.NewRunContext(blockedRunID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, state.Append(blockedRunCtx, contracts.StateEntry{
		Kind: contracts.StateKindNeedsManualRecovery,
		Value: contracts.StateEntryNeedsManualRecovery{
			Kind:       contracts.StateKindNeedsManualRecovery,
			PR:         100,
			RunID:      blockedRunID,
			Step:       contracts.FailedStep70,
			Reason:     contracts.RollbackReasonTransactionalFailure,
			FailedStep: contracts.FailedStep70,
			At:         time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}))
	require.NoError(t, internalio.WriteAtomic(
		needsRecoverySentinelClearedPath(cfg.Paths.Runs, blockedRunID),
		[]byte(fmt.Sprintf("{\"run_id\":%q,\"state\":\"cleared\"}\n", blockedRunID)),
	))

	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orch.steps = stubPipelineSteps(nil, nil)
	runID := contracts.RunID("2026-04-21-PR101-abcdef0")
	require.NoError(t, orch.Run(context.Background(), 101, RunOptions{RunID: runID}))
	assert.NoFileExists(t, needsRecoverySentinelPath(cfg.Paths.Runs, blockedRunID))
}

func TestFirstNeedsRecoverySentinel_PreservesAbortedSentinelDuringRehydration(t *testing.T) {
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	runCtx, err := internalio.NewRunContext("2026-04-21-PR102-deadbee", runsBase, worktreeBase)
	require.NoError(t, err)
	sentinel := contracts.NeedsRecoverySentinel{
		RunID:      runCtx.RunID,
		PR:         102,
		Reason:     contracts.RollbackReasonTransactionalFailure,
		FailedStep: contracts.FailedStep70,
		CreatedAt:  time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
	}
	require.NoError(t, internalio.WriteJSONAtomic(needsRecoverySentinelAbortedPath(runsBase, runCtx.RunID), sentinel))
	require.NoError(t, state.Append(runCtx, contracts.StateEntry{
		Kind: contracts.StateKindNeedsManualRecovery,
		Value: contracts.StateEntryNeedsManualRecovery{
			Kind:       contracts.StateKindNeedsManualRecovery,
			PR:         102,
			RunID:      runCtx.RunID,
			Step:       contracts.FailedStep70,
			Reason:     contracts.RollbackReasonTransactionalFailure,
			FailedStep: contracts.FailedStep70,
			At:         sentinel.CreatedAt,
		},
	}))

	got, blocked, err := firstNeedsRecoverySentinel(runsBase)
	require.NoError(t, err)
	assert.True(t, blocked)
	assert.Equal(t, runCtx.RunID, got.RunID)
	assert.FileExists(t, needsRecoverySentinelAbortedPath(runsBase, runCtx.RunID))
	assert.NoFileExists(t, needsRecoverySentinelPath(runsBase, runCtx.RunID))
}

func TestRun_CanceledAfterStep70NoopAppendsInterruptedInsteadOfCompleted(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	cancelStep := &cancelAfterNoopStep{}
	orch.steps = stubPipelineSteps(nil, cancelStep)

	ctx, cancel := context.WithCancel(context.Background())
	cancelStep.cancel = cancel

	runID := contracts.RunID("2026-04-21-PR103-abcdef0")
	require.NoError(t, orch.Run(ctx, 103, RunOptions{RunID: runID}))

	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindInterrupted, events[len(events)-1].Kind)
	assert.NotContains(t, eventKinds(events), contracts.StateKindCompleted)
}

func TestRun_FreshSecondSentinelGatePreventsScaffoldWrites(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	originalHook := beforeFreshRunGateHook
	beforeFreshRunGateHook = func(run *StepRunContext) error {
		blockedRunCtx, err := internalio.NewRunContext("2026-04-21-PR104-abcdef0", run.IO.RunsBase, run.IO.WorktreeBase)
		if err != nil {
			return err
		}
		return ensureNeedsRecoverySentinel(blockedRunCtx, 104, blockedRunCtx.RunID, contracts.RollbackReasonTransactionalFailure, contracts.FailedStep70)
	}
	t.Cleanup(func() {
		beforeFreshRunGateHook = originalHook
	})

	runID := contracts.RunID("2026-04-21-PR105-abcdef0")
	err = orch.Run(context.Background(), 105, RunOptions{RunID: runID})
	var blockedErr *GlobalNeedsRecoveryError
	require.ErrorAs(t, err, &blockedErr)

	runCtx, ctxErr := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, ctxErr)
	_, statErr := os.Stat(runCtx.RunDir())
	require.Error(t, statErr)
	assert.True(t, os.IsNotExist(statErr))
	assert.NoFileExists(t, filepath.Join(runCtx.RunDir(), "config.snapshot.yaml"))
}

func TestRun_ResumeSecondSentinelGatePreventsResumeExecution(t *testing.T) {
	cfg := testConfig(t)
	runID := contracts.RunID("2026-04-21-PR107-abcdef0")
	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, seedResumeRun(t, runCtx, 107))

	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orch.steps = stubPipelineSteps(nil, nil)

	originalHook := beforeRunScaffoldHook
	beforeRunScaffoldHook = func(run *StepRunContext) error {
		blockedRunCtx, err := internalio.NewRunContext("2026-04-21-PR999-deadbee", run.IO.RunsBase, run.IO.WorktreeBase)
		if err != nil {
			return err
		}
		return ensureNeedsRecoverySentinel(blockedRunCtx, 999, blockedRunCtx.RunID, contracts.RollbackReasonTransactionalFailure, contracts.FailedStep70)
	}
	t.Cleanup(func() {
		beforeRunScaffoldHook = originalHook
	})

	err = orch.Run(context.Background(), 107, RunOptions{RunID: runID})
	var blockedErr *GlobalNeedsRecoveryError
	require.ErrorAs(t, err, &blockedErr)

	events, scanErr := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, scanErr)
	assert.NotContains(t, eventKinds(events), contracts.StateKindCompleted)
}

func TestRun_FreshStartedAppendGateRechecksSentinel(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orch.steps = stubPipelineSteps(nil, nil)

	originalHook := beforeStartedAppendHook
	beforeStartedAppendHook = func(run *StepRunContext) error {
		blockedRunCtx, err := internalio.NewRunContext("2026-04-21-PR108-deadbee", run.IO.RunsBase, run.IO.WorktreeBase)
		if err != nil {
			return err
		}
		return ensureNeedsRecoverySentinel(blockedRunCtx, 108, blockedRunCtx.RunID, contracts.RollbackReasonTransactionalFailure, contracts.FailedStep70)
	}
	t.Cleanup(func() {
		beforeStartedAppendHook = originalHook
	})

	runID := contracts.RunID("2026-04-21-PR109-abcdef0")
	err = orch.Run(context.Background(), 109, RunOptions{RunID: runID})
	var blockedErr *GlobalNeedsRecoveryError
	require.ErrorAs(t, err, &blockedErr)

	runCtx, ctxErr := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, ctxErr)
	assert.FileExists(t, filepath.Join(runCtx.RunDir(), "config.snapshot.yaml"))
	events, scanErr := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, scanErr)
	assert.NotContains(t, eventKinds(events), contracts.StateKindStarted)
}

func TestRun_RejectsConcurrentUseOnSameInstance(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	blocker := newBlockingStartStep()
	orch.steps = stubPipelineSteps(blocker, nil)

	firstErrCh := make(chan error, 1)
	go func() {
		firstErrCh <- orch.Run(context.Background(), 106, RunOptions{RunID: "2026-04-21-PR106-abcdef0"})
	}()

	<-blocker.started
	err = orch.Run(context.Background(), 107, RunOptions{RunID: "2026-04-21-PR107-abcdef0"})
	require.ErrorIs(t, err, errConcurrentRun)

	close(blocker.release)
	require.NoError(t, <-firstErrCh)
}

func TestRun_RejectsConcurrentUseAcrossInstancesForSamePR(t *testing.T) {
	cfg := testConfig(t)
	blocker := newBlockingStartStep()

	orchA, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orchA.steps = stubPipelineSteps(blocker, nil)

	orchB, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orchB.steps = stubPipelineSteps(nil, nil)

	firstErrCh := make(chan error, 1)
	go func() {
		firstErrCh <- orchA.Run(context.Background(), 106, RunOptions{RunID: "2026-04-21-PR106-abcdef0"})
	}()

	<-blocker.started
	err = orchB.Run(context.Background(), 106, RunOptions{RunID: "2026-04-21-PR106-bcdef01"})
	require.ErrorIs(t, err, errConcurrentPRRun)

	close(blocker.release)
	require.NoError(t, <-firstErrCh)
}

func TestRun_Step20ManualRecoveryAppendsNeedsManualRecovery(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orch.steps = stubPipelineSteps(nil, nil)
	orch.steps.Step20["a1"] = manualRecoveryStep{
		reason: contracts.RollbackReasonLeaseFailure,
		detail: "quiesce timed out",
	}

	runID := contracts.RunID("2026-04-21-PR112-abcdef0")
	require.NoError(t, orch.Run(context.Background(), 112, RunOptions{RunID: runID}))

	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindNeedsManualRecovery, events[len(events)-1].Kind)
}

func TestRun_ResumeFromBranchPushed_EndToEnd(t *testing.T) {
	cfg := testConfig(t)
	runID := contracts.RunID("2026-04-21-PR47-abcdef0")
	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, seedResumeRun(t, runCtx, 47))

	store := NewIntentionStore(runCtx)
	intention := validPlanningIntention(runID)
	intention.Stage = contracts.IntentionStageBranchPushed
	require.NoError(t, store.Save(intention))
	require.NoError(t, internalio.WriteAtomic(filepath.Join(runCtx.RunDir(), "staging", "rules", "r-0001.md"), []byte("r-0001 body\n")))

	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orch.steps.Step70 = orchestratorStep70{
		git:      testStep70Git{head: intention.TargetSha},
		resolver: testStep70Resolver{},
	}

	require.NoError(t, orch.Run(context.Background(), 47, RunOptions{RunID: runID}))

	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	kinds := make([]contracts.StateKind, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	assert.Contains(t, kinds, contracts.StateKindPromoted)
	assert.FileExists(t, filepath.Join(cfg.Paths.Runs, string(runID), "70", "decision.json"))

	lines, err := internalio.ReadJSONL[contracts.RuleRegistryEntry](runCtx.RulesRegistryPath())
	require.NoError(t, err)
	assert.Len(t, lines, 1)
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

func TestIntentionStore_RoundTrip(t *testing.T) {
	cfg := testConfig(t)
	runCtx, err := internalio.NewRunContext("2026-04-21-PR88-abcdef0", cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)

	store := NewIntentionStore(runCtx)
	record := validPlanningIntention(runCtx.RunID)
	require.NoError(t, store.Save(record))

	loaded, err := store.Load()
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, contracts.IntentionStagePlanning, loaded.Stage)

	require.NoError(t, store.Transition(contracts.IntentionStageNeedsManualRecovery, func(r *contracts.IntentionRecord) error {
		r.RecoveryReason = contracts.RollbackReasonRemoteDivergence
		r.FailedStep = contracts.FailedStep70
		return nil
	}))
	loaded, err = store.Load()
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, contracts.IntentionStageNeedsManualRecovery, loaded.Stage)

	require.NoError(t, store.Delete())
	loaded, err = store.Load()
	require.NoError(t, err)
	assert.Nil(t, loaded)
}

func TestRealArchiveStep_NoOpLeavesSunsetStateUntouched(t *testing.T) {
	cfg := testConfig(t)
	runCtx, err := internalio.NewRunContext("2026-04-21-PR89-abcdef0", cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunsBase, 0o755))

	markerPath := filepath.Join(runCtx.RunsBase, "sunset-running.marker")
	lastSunsetPath := filepath.Join(runCtx.RunsBase, "last-sunset-at")
	require.NoError(t, os.WriteFile(markerPath, []byte("2026-04-21T09:00:00Z\nstale-run\n"), 0o644))
	require.NoError(t, os.WriteFile(lastSunsetPath, []byte("2026-04-21T08:00:00Z\n"), 0o644))

	step := realArchiveStep{}
	require.NoError(t, step.Run(context.Background(), &StepRunContext{IO: runCtx}))
	require.NoError(t, step.Run(context.Background(), &StepRunContext{IO: runCtx}))

	markerBody, err := os.ReadFile(markerPath)
	require.NoError(t, err)
	assert.Equal(t, "2026-04-21T09:00:00Z\nstale-run\n", string(markerBody))

	lastSunsetBody, err := os.ReadFile(lastSunsetPath)
	require.NoError(t, err)
	assert.Equal(t, "2026-04-21T08:00:00Z\n", string(lastSunsetBody))
}

type callRecorder struct {
	mu    sync.Mutex
	calls []string
}

type cancelAfterNoopStep struct {
	cancel context.CancelFunc
}

func (s *cancelAfterNoopStep) Run(ctx context.Context, run *StepRunContext) error {
	if err := (stubStep70{}).Run(ctx, run); err != nil {
		return err
	}
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

type policySnapshotStaleStep struct{}

func (policySnapshotStaleStep) Run(context.Context, *StepRunContext) error {
	return &step70_decide.PolicySnapshotStaleError{Reason: "policy_branch_stale"}
}

type blockingStartStep struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type manualRecoveryStep struct {
	reason contracts.RollbackReason
	detail string
}

func (s manualRecoveryStep) Run(context.Context, *StepRunContext) error {
	return &agentrunner.ManualRecoveryRequiredError{
		Reason: s.reason,
		Detail: s.detail,
	}
}

type tamperedDecisionStep70 struct {
	runID contracts.RunID
}

type assertPolicyBranchStep struct {
	t    *testing.T
	want string
}

func (s assertPolicyBranchStep) Run(_ context.Context, run *StepRunContext) error {
	s.t.Helper()
	require.NotNil(s.t, run.Config)
	assert.Equal(s.t, s.want, run.Config.Repo.PolicyBranch)
	return stubStep70{}.Run(context.Background(), run)
}

type assertImplementerProviderStep struct {
	t    *testing.T
	want agents.Provider
}

func (s assertImplementerProviderStep) Run(_ context.Context, run *StepRunContext) error {
	s.t.Helper()
	require.NotNil(s.t, run.Config)
	profile, err := run.Config.AgentProfile(agents.RoleImplementer)
	require.NoError(s.t, err)
	assert.Equal(s.t, s.want, profile.Provider)
	return stubStep70{}.Run(context.Background(), run)
}

func (s tamperedDecisionStep70) Run(_ context.Context, run *StepRunContext) error {
	path, err := run.IO.ResolveRunRelative("70/decision.json")
	if err != nil {
		return err
	}
	return internalio.WriteJSONAtomic(path, contracts.Decision{
		Action: contracts.DecisionActionNoop,
		Value: contracts.DecisionNoop{
			Action:        contracts.DecisionActionNoop,
			SchemaVersion: "1",
			RunID:         s.runID,
			Reason:        "tampered",
			DecidedAt:     time.Now().UTC(),
		},
	})
}

func newBlockingStartStep() *blockingStartStep {
	return &blockingStartStep{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *blockingStartStep) Run(ctx context.Context, run *StepRunContext) error {
	s.once.Do(func() {
		close(s.started)
	})
	<-s.release
	return stubStep10{}.Run(ctx, run)
}

func stubPipelineSteps(step10 Step, step70 Step) Steps {
	if step10 == nil {
		step10 = stubStep10{}
	}
	if step70 == nil {
		step70 = stubStep70{}
	}
	return Steps{
		Step10: step10,
		Step20: map[contracts.AgentID]Step{
			"a1": stubImplementStep{},
			"a2": stubImplementStep{},
			"a3": stubImplementStep{},
		},
		Step30: stubMarkerStep{path: "30/done.marker"},
		Step40: duplicateOnlyCandidateStep{},
		Step50: map[contracts.AgentID]Step{
			"a1": stubImplementStep{},
			"a2": stubImplementStep{},
			"a3": stubImplementStep{},
		},
		Step60:  step60Step{},
		Step70:  step70,
		Archive: stubArchiveStep{},
	}
}

type forcedCandidateStep struct{}

func (forcedCandidateStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	body := "# Forced rule\n\nUse explicit resource cleanup.\n"
	candidate := contracts.Candidate{
		CandidateID:        "cand-forced-001",
		Kind:               contracts.CandidateKindNew,
		Title:              "Forced rule",
		Problem:            "problem",
		Rationale:          "rationale",
		ProposedBodyPath:   "40/candidates/cand-forced-001.md",
		ProposedBodySha256: sha256String(body),
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	candidates := &contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          run.IO.RunID,
		Candidates:     []contracts.Candidate{candidate},
		CandidatesHash: contracts.CanonicalCandidatesHash([]contracts.Candidate{candidate}),
		CreatedAt:      time.Now().UTC(),
	}
	sidecarPath, err := run.IO.ResolveRunRelative(candidate.ProposedBodyPath)
	if err != nil {
		return err
	}
	if err := internalio.WriteAtomic(sidecarPath, []byte(body)); err != nil {
		return err
	}
	candidatesPath, err := run.IO.ResolveRunRelative("40/candidates.json")
	if err != nil {
		return err
	}
	if err := internalio.WriteJSONAtomic(candidatesPath, candidates); err != nil {
		return err
	}
	run.Candidates = candidates
	return nil
}

type duplicateOnlyCandidateStep struct{}

func (duplicateOnlyCandidateStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	body := "# Duplicate rule\n\n- source_rule_id: rule-existing\n- classification: duplicate\n"
	candidate := contracts.Candidate{
		CandidateID:        "cand-dup-only",
		Kind:               contracts.CandidateKindDuplicate,
		TargetRuleID:       "rule-existing",
		Title:              "Duplicate rule",
		Problem:            "problem",
		Rationale:          "rationale",
		ProposedBodyPath:   "40/candidates/cand-dup-only.md",
		ProposedBodySha256: sha256String(body),
	}
	candidates := &contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          run.IO.RunID,
		Candidates:     []contracts.Candidate{candidate},
		CandidatesHash: contracts.CanonicalCandidatesHash([]contracts.Candidate{candidate}),
		CreatedAt:      time.Now().UTC(),
	}
	sidecarPath, err := run.IO.ResolveRunRelative(candidate.ProposedBodyPath)
	if err != nil {
		return err
	}
	if err := internalio.WriteAtomic(sidecarPath, []byte(body)); err != nil {
		return err
	}
	candidatesPath, err := run.IO.ResolveRunRelative("40/candidates.json")
	if err != nil {
		return err
	}
	if err := internalio.WriteJSONAtomic(candidatesPath, candidates); err != nil {
		return err
	}
	run.Candidates = candidates
	return nil
}

type rescueExhaustedStep struct{}

func (rescueExhaustedStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	return &step20_implement.RescueExhaustedError{
		Rescue: stepio.RescueExhausted{
			Agent:      run.Agent,
			RetryCount: 3,
		},
	}
}

type leaseContendedOnceStep struct {
	mu   sync.Mutex
	seen bool
}

func (s *leaseContendedOnceStep) Run(ctx context.Context, run *StepRunContext) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.seen {
		s.seen = true
		return fmt.Errorf("%w: agent %s", step20_implement.ErrAgentLeaseContended, run.Agent)
	}
	return stubImplementStep{}.Run(ctx, run)
}

type terminalPromoteStep struct{}

func (terminalPromoteStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	path, err := run.IO.ResolveRunRelative("70/decision.json")
	if err != nil {
		return err
	}
	bestShaBefore := strings.Repeat("1", 40)
	targetSha := strings.Repeat("2", 40)
	candidatesHash := strings.Repeat("3", 64)
	decision := contracts.Decision{
		Action: contracts.DecisionActionAdopt,
		Value: contracts.DecisionAdopt{
			Action:        contracts.DecisionActionAdopt,
			SchemaVersion: "1",
			RunID:         run.IO.RunID,
			IdempotencyKey: contracts.ComputeAdoptIdempotencyKey(
				string(run.IO.RunID),
				targetSha,
				bestShaBefore,
				candidatesHash,
			),
			BestShaBefore:  bestShaBefore,
			TargetSha:      targetSha,
			CandidatesHash: candidatesHash,
			RegistryAppendResult: contracts.RegistryAppendResult{
				Offset: 0,
				Sha256: strings.Repeat("a", 64),
			},
			DecidedAt: time.Now().UTC(),
		},
	}
	if err := internalio.WriteJSONAtomic(path, decision); err != nil {
		return err
	}
	writer := state.NewWriter(run.IO)
	if err := writer.Append(promotedEntry(run.PR, run.IO.RunID, time.Now().UTC())); err != nil {
		return err
	}
	run.Decision = &decision
	return nil
}

func forcedCandidate(runID contracts.RunID) *contracts.Candidates {
	body := "# Forced rule\n\nUse explicit resource cleanup.\n"
	candidate := contracts.Candidate{
		CandidateID:        "cand-forced-001",
		Kind:               contracts.CandidateKindNew,
		Title:              "Forced rule",
		Problem:            "problem",
		Rationale:          "rationale",
		ProposedBodyPath:   "40/candidates/cand-forced-001.md",
		ProposedBodySha256: sha256String(body),
	}
	return &contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          runID,
		Candidates:     []contracts.Candidate{candidate},
		CandidatesHash: contracts.CanonicalCandidatesHash([]contracts.Candidate{candidate}),
		CreatedAt:      time.Now().UTC(),
	}
}

type scriptedStep60Step struct {
	decode func([]byte, any) (any, error)
}

func (s scriptedStep60Step) Run(ctx context.Context, run *StepRunContext) error {
	pass1RubricVersion := step30RubricVersionForStep60(run.IO)
	if err := step60_scorepairwise.Run(ctx, step60_scorepairwise.Input{
		IO:            run.IO,
		TaskPackage:   run.TaskPackage,
		RubricVersion: pass1RubricVersion,
		Primary:       orchestratorJudge{score: 95},
		Secondary:     orchestratorJudge{score: 94},
		Arbiter:       orchestratorJudge{score: 95},
	}); err != nil {
		return err
	}
	if s.decode == nil {
		return nil
	}
	scorableAgents, err := step60ScorableAgents(run.IO, run.TaskPackage)
	if err != nil {
		return err
	}
	versions, err := step60ScoringVersions(run.IO)
	if err != nil {
		return err
	}
	req := stepio.Step60Request{
		TaskPackage:    *run.TaskPackage,
		ScorableAgents: scorableAgents,
		RubricVersion:  versions.RubricVersion,
		PromptVersion:  versions.PromptVersion,
	}
	markerPath, err := run.IO.ResolveRunRelative("60/done.marker")
	if err != nil {
		return err
	}
	marker, err := internalio.ReadJSON[contracts.Step60DoneMarker](markerPath)
	if err != nil {
		return err
	}
	resp := stepio.Step60Response{
		RunID:           run.IO.RunID,
		ScoresCount:     int(marker.ExpectedCounts.Scores),
		ComplianceCount: int(marker.ExpectedCounts.Compliance),
		PairwiseCount:   int(marker.ExpectedCounts.Pairwise),
		ResolvedAt:      marker.ResolvedAt,
	}
	payload, err := contracts.MarshalStrict(resp)
	if err != nil {
		return err
	}
	_, err = s.decode(payload, req)
	return err
}

type orchestratorJudge struct {
	score int
}

type branchPushedCrashStep struct {
	t *testing.T
}

func (s branchPushedCrashStep) Run(ctx context.Context, run *StepRunContext) error {
	s.t.Helper()
	repoRoot, err := run.Config.RepoRoot()
	require.NoError(s.t, err)
	resolver := step70_decide.FilesystemResolver{RepoDir: repoRoot}
	target, ok, err := resolver.Resolve(run.IO, run.TaskPackage, run.Candidates)
	require.NoError(s.t, err)
	require.True(s.t, ok)

	bestSHA := os.Getenv("AUTO_IMPROVE_TEST_BEST_SHA")
	idempotencyKey := contracts.ComputeAdoptIdempotencyKey(string(run.IO.RunID), target.TargetSHA, bestSHA, run.Candidates.CandidatesHash)
	plannedEntries := make([]contracts.PlannedAdoptionEntry, 0, len(target.RulesToAppend))
	for idx, entry := range target.RulesToAppend {
		var (
			ruleID   string
			rulePath string
			sha256   string
		)
		switch value := entry.Value.(type) {
		case contracts.RuleRegistryAdded:
			ruleID = value.RuleID
			rulePath = value.RulePath
			sha256 = value.Sha256
		case contracts.RuleRegistryUpdated:
			ruleID = value.RuleID
			rulePath = value.RulePath
			sha256 = value.Sha256
		default:
			s.t.Fatalf("unexpected planned registry entry type %T", entry.Value)
		}
		plannedEntries = append(plannedEntries, contracts.PlannedAdoptionEntry{
			OpID:     contracts.ComputePlannedAdoptionEntryOpID(idempotencyKey, idx, ruleID),
			Kind:     entry.Kind,
			RuleID:   ruleID,
			RulePath: rulePath,
			Sha256:   sha256,
		})
	}

	intention := contracts.IntentionRecord{
		SchemaVersion:      "1",
		Stage:              contracts.IntentionStageBranchPushed,
		IdempotencyKey:     idempotencyKey,
		RunID:              run.IO.RunID,
		BestShaBefore:      bestSHA,
		TargetSha:          target.TargetSHA,
		CandidatesHash:     run.Candidates.CandidatesHash,
		RegistryHeadBefore: "",
		PlannedAdoption: &contracts.PlannedAdoption{
			IdempotencyKey: idempotencyKey,
			Entries:        plannedEntries,
		},
		StartedAt: time.Now().UTC(),
	}
	require.NoError(s.t, run.IntentionFile.Save(intention))
	return fmt.Errorf("simulated branch_pushed crash")
}

func (j orchestratorJudge) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	if err := input.Validate(); err != nil {
		return judges.JudgeOutput{}, err
	}
	select {
	case <-ctx.Done():
		return judges.JudgeOutput{}, ctx.Err()
	default:
	}
	scores := make([]contracts.ScoreEntry, 0, 5)
	dimensions := []contracts.Dimension{
		contracts.DimensionFidelity,
		contracts.DimensionCorrectness,
		contracts.DimensionMaintainability,
		contracts.DimensionDiscipline,
		contracts.DimensionCommunication,
	}
	for _, dimension := range dimensions {
		scores = append(scores, contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			Dimension:     dimension,
			Score:         j.score,
			Reasons:       "scripted adopt score",
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "default",
			PromptVersion: "phase0-stub",
			ResolvedAt:    time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		})
	}
	ruleIDs := input.ExpectedComplianceRuleIDs
	if len(ruleIDs) == 0 {
		ruleIDs = []string{"shared"}
	}
	compliance := make([]contracts.ComplianceEntry, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		compliance = append(compliance, contracts.ComplianceEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			RuleID:        ruleID,
			Verdict:       contracts.ComplianceVerdictCompliant,
			Rationale:     "scripted adopt compliance",
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "default",
			PromptVersion: "phase0-stub",
			ResolvedAt:    time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		})
	}
	output := judges.JudgeOutput{
		Scores:     scores,
		Compliance: compliance,
	}
	return output, output.ValidateFor(input)
}

func sha256String(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func overwriteFakeGitScript(t *testing.T, binDir, script string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755))
}

func manualRecoveryGitScript() string {
	return `#!/bin/sh
set -eu

state_dir="${AUTO_IMPROVE_GIT_STATE_DIR}"
mkdir -p "$state_dir"

if [ "${1:-}" = "-C" ]; then
  repo_dir="$2"
  shift 2
else
  repo_dir="$(pwd)"
fi

subcmd="$1"
shift

case "$subcmd" in
  rev-parse)
    if [ "${1:-}" = "--verify" ]; then
      echo "${AUTO_IMPROVE_TEST_BEST_SHA}"
      exit 0
    fi
    case "${1:-}" in
      *^1) echo "${AUTO_IMPROVE_TEST_BASE_SHA}" ;;
      HEAD) echo "${AUTO_IMPROVE_TEST_TARGET_SHA}" ;;
      refs/heads/*) echo "${AUTO_IMPROVE_TEST_TARGET_SHA}" ;;
      *) echo "${AUTO_IMPROVE_TEST_BEST_SHA}" ;;
    esac
    ;;
  fetch)
    exit 0
    ;;
  remote)
    case "${1:-} ${2:-}" in
      "get-url origin")
        printf '%s\n' "git@github.com:owner/repo.git"
        ;;
      *)
        echo "unsupported remote args: $*" >&2
        exit 1
        ;;
    esac
    ;;
  merge-base)
    if [ "${1:-}" = "--is-ancestor" ]; then
      exit 0
    fi
    ;;
  worktree)
    case "${1:-}" in
      add)
        if [ "${2:-}" = "-b" ]; then
          path="$4"
        else
          path="$2"
        fi
        mkdir -p "$path"
        { grep -Fqx "$path" "$state_dir/worktrees.list" 2>/dev/null || printf '%s\n' "$path" >> "$state_dir/worktrees.list"; } || true
        ;;
      remove)
        rm -rf "$3"
        ;;
      list)
        while IFS= read -r path; do
          [ -n "$path" ] || continue
          printf 'worktree %s\n\n' "$path"
        done < "$state_dir/worktrees.list"
        ;;
    esac
    ;;
  diff)
    for file in "$repo_dir"/generated-*; do
      [ -e "$file" ] || continue
      name="$(basename "$file")"
      printf 'diff --git a/%s b/%s\n' "$name" "$name"
      printf 'new file mode 100644\n'
      printf 'index 0000000..1111111\n'
      printf -- '--- /dev/null\n'
      printf '+++ b/%s\n' "$name"
      printf '@@ -0,0 +1 @@\n'
      printf '+generated change\n'
      exit 0
    done
    exit 0
    ;;
  ls-files)
    for file in "$repo_dir"/generated-*; do
      [ -e "$file" ] || continue
      printf '%s\0' "$(basename "$file")"
      exit 0
    done
    exit 0
    ;;
  status)
    exit 0
    ;;
  branch)
    case "$(basename "$repo_dir")" in
      *-pass1-a1) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass1-a1$//')/pass1/a1" ;;
      *-pass1-a2) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass1-a2$//')/pass1/a2" ;;
      *-pass1-a3) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass1-a3$//')/pass1/a3" ;;
      *-pass2-a1) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass2-a1$//')/pass2/a1" ;;
      *-pass2-a2) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass2-a2$//')/pass2/a2" ;;
      *-pass2-a3) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass2-a3$//')/pass2/a3" ;;
      *) echo "stub-branch" ;;
    esac
    ;;
  ls-remote)
    if [ -f "$state_dir/after-push" ]; then
      exit 0
    fi
    branch="${4:-best}"
    printf '%s\trefs/heads/%s\n' "${AUTO_IMPROVE_TEST_BEST_SHA}" "$branch"
    ;;
  push)
    touch "$state_dir/after-push"
    echo "non-fast-forward" >&2
    exit 1
    ;;
esac

exit 0
`
}

func postPushRollbackGitScript() string {
	return `#!/bin/sh
set -eu

state_dir="${AUTO_IMPROVE_GIT_STATE_DIR}"
mkdir -p "$state_dir"

if [ "${1:-}" = "-C" ]; then
  repo_dir="$2"
  shift 2
else
  repo_dir="$(pwd)"
fi

subcmd="$1"
shift

case "$subcmd" in
  rev-parse)
    if [ "${1:-}" = "--verify" ]; then
      echo "${AUTO_IMPROVE_TEST_BEST_SHA}"
      exit 0
    fi
    case "${1:-}" in
      *^1) echo "${AUTO_IMPROVE_TEST_BASE_SHA}" ;;
      HEAD) echo "${AUTO_IMPROVE_TEST_TARGET_SHA}" ;;
      refs/heads/*) echo "${AUTO_IMPROVE_TEST_TARGET_SHA}" ;;
      *) echo "${AUTO_IMPROVE_TEST_BEST_SHA}" ;;
    esac
    ;;
  fetch)
    exit 0
    ;;
  remote)
    case "${1:-} ${2:-}" in
      "get-url origin")
        printf '%s\n' "git@github.com:owner/repo.git"
        ;;
      *)
        echo "unsupported remote args: $*" >&2
        exit 1
        ;;
    esac
    ;;
  merge-base)
    if [ "${1:-}" = "--is-ancestor" ]; then
      exit 0
    fi
    ;;
  worktree)
    case "${1:-}" in
      add)
        if [ "${2:-}" = "-b" ]; then
          path="$4"
        else
          path="$2"
        fi
        mkdir -p "$path"
        { grep -Fqx "$path" "$state_dir/worktrees.list" 2>/dev/null || printf '%s\n' "$path" >> "$state_dir/worktrees.list"; } || true
        ;;
      remove)
        rm -rf "$3"
        ;;
      list)
        while IFS= read -r path; do
          [ -n "$path" ] || continue
          printf 'worktree %s\n\n' "$path"
        done < "$state_dir/worktrees.list"
        ;;
    esac
    ;;
  diff)
    for file in "$repo_dir"/generated-*; do
      [ -e "$file" ] || continue
      name="$(basename "$file")"
      printf 'diff --git a/%s b/%s\n' "$name" "$name"
      printf 'new file mode 100644\n'
      printf 'index 0000000..1111111\n'
      printf -- '--- /dev/null\n'
      printf '+++ b/%s\n' "$name"
      printf '@@ -0,0 +1 @@\n'
      printf '+generated change\n'
      exit 0
    done
    exit 0
    ;;
  ls-files)
    for file in "$repo_dir"/generated-*; do
      [ -e "$file" ] || continue
      printf '%s\0' "$(basename "$file")"
      exit 0
    done
    exit 0
    ;;
  status)
    exit 0
    ;;
  branch)
    case "$(basename "$repo_dir")" in
      *-pass1-a1) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass1-a1$//')/pass1/a1" ;;
      *-pass1-a2) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass1-a2$//')/pass1/a2" ;;
      *-pass1-a3) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass1-a3$//')/pass1/a3" ;;
      *-pass2-a1) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass2-a1$//')/pass2/a1" ;;
      *-pass2-a2) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass2-a2$//')/pass2/a2" ;;
      *-pass2-a3) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass2-a3$//')/pass2/a3" ;;
      *) echo "stub-branch" ;;
    esac
    ;;
  ls-remote)
    branch="${4:-best}"
    if [ -f "$state_dir/pushed-target" ]; then
      printf '%s\trefs/heads/%s\n' "${AUTO_IMPROVE_TEST_TARGET_SHA}" "$branch"
    else
      printf '%s\trefs/heads/%s\n' "${AUTO_IMPROVE_TEST_BEST_SHA}" "$branch"
    fi
    ;;
  push)
    refspec="${2:-}"
    if [ "$refspec" = "${AUTO_IMPROVE_TEST_TARGET_SHA}:best" ]; then
      mkdir -p "$(dirname "$AUTO_IMPROVE_TEST_SENTINEL_PATH")"
      cat > "$AUTO_IMPROVE_TEST_SENTINEL_PATH" <<EOF
{"run_id":"2026-04-21-PR99-deadbee","pr":99,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T00:00:00Z"}
EOF
      touch "$state_dir/pushed-target"
      exit 0
    fi
    exit 0
    ;;
esac

exit 0
`
}

func (r *callRecorder) add(call string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
}

func (r *callRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func eventKinds(events []contracts.StateEntry) []contracts.StateKind {
	kinds := make([]contracts.StateKind, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

type recordingStep struct {
	label    string
	recorder *callRecorder
}

func (s recordingStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	s.recorder.add(s.label)
	switch s.label {
	case "10":
		return stubStep10{}.Run(ctx, run)
	case "30":
		return stubMarkerStep{path: "30/done.marker"}.Run(ctx, run)
	case "40":
		if err := (stubStep40{}).Run(ctx, run); err != nil {
			return err
		}
		if run.Candidates == nil || len(run.Candidates.Candidates) == 0 {
			candidates := []contracts.Candidate{{
				CandidateID:        "cand-test-001",
				Kind:               contracts.CandidateKindNew,
				Title:              "stub candidate",
				Problem:            "problem",
				Rationale:          "rationale",
				ProposedBodyPath:   "40/candidates/cand-test-001.md",
				ProposedBodySha256: strings.Repeat("a", 64),
			}}
			run.Candidates = &contracts.Candidates{
				SchemaVersion:  "1",
				RunID:          run.IO.RunID,
				Candidates:     candidates,
				CandidatesHash: contracts.CanonicalCandidatesHash(candidates),
				CreatedAt:      time.Now().UTC(),
			}
			candidatesPath, err := run.IO.ResolveRunRelative("40/candidates.json")
			if err != nil {
				return err
			}
			if err := internalio.WriteJSONAtomic(candidatesPath, run.Candidates); err != nil {
				return err
			}
		}
		return nil
	case "70":
		return stubStep70{}.Run(ctx, run)
	default:
		return nil
	}
}

type recordingAgentStep struct {
	prefix   string
	recorder *callRecorder
}

func (s recordingAgentStep) Run(ctx context.Context, run *StepRunContext) error {
	s.recorder.add(s.prefix + ":" + string(run.Agent))
	return stubImplementStep{}.Run(ctx, run)
}

func recordingAgentSteps(prefix string, recorder *callRecorder) map[contracts.AgentID]Step {
	steps := make(map[contracts.AgentID]Step, len(defaultAgents))
	for _, agent := range defaultAgents {
		steps[agent] = recordingAgentStep{
			prefix:   prefix,
			recorder: recorder,
		}
	}
	return steps
}

func stubAgentSteps() map[contracts.AgentID]Step {
	steps := make(map[contracts.AgentID]Step, len(defaultAgents))
	for _, agent := range defaultAgents {
		steps[agent] = stubImplementStep{}
	}
	return steps
}

type nonScorableImplementStep struct {
	kind   contracts.ManifestKind
	reason string
}

type cancelingStep struct {
	cancel context.CancelFunc
}

func (s cancelingStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = run
	s.cancel()
	<-ctx.Done()
	return ctx.Err()
}

func (s nonScorableImplementStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	manifestPath, err := run.IO.ManifestPath(run.Pass, run.Agent)
	if err != nil {
		return err
	}
	startedAt := time.Now().UTC()
	switch s.kind {
	case contracts.ManifestKindTimeout:
		return internalio.WriteJSONAtomic(manifestPath, contracts.Manifest{
			Kind: contracts.ManifestKindTimeout,
			Value: contracts.ManifestTimeout{
				Kind:           contracts.ManifestKindTimeout,
				SchemaVersion:  "1",
				RunID:          run.IO.RunID,
				Pass:           run.Pass,
				Agent:          run.Agent,
				TimeoutSeconds: 300,
				StartedAt:      startedAt,
				FinishedAt:     startedAt,
			},
		})
	default:
		reason := s.reason
		if reason == "" {
			reason = "unknown"
		}
		return internalio.WriteJSONAtomic(manifestPath, contracts.Manifest{
			Kind: contracts.ManifestKindError,
			Value: contracts.ManifestError{
				Kind:          contracts.ManifestKindError,
				SchemaVersion: "1",
				RunID:         run.IO.RunID,
				Pass:          run.Pass,
				Agent:         run.Agent,
				ExitCode:      1,
				Reason:        reason,
				Detail:        "fixture non-scorable manifest",
				StartedAt:     startedAt,
				FinishedAt:    startedAt,
			},
		})
	}
}

func nonScorableAgentSteps(kind contracts.ManifestKind) map[contracts.AgentID]Step {
	steps := make(map[contracts.AgentID]Step, len(defaultAgents))
	for _, agent := range defaultAgents {
		steps[agent] = nonScorableImplementStep{kind: kind}
	}
	return steps
}

func providerInterruptedAgentSteps(reason string) map[contracts.AgentID]Step {
	steps := make(map[contracts.AgentID]Step, len(defaultAgents))
	for _, agent := range defaultAgents {
		steps[agent] = nonScorableImplementStep{kind: contracts.ManifestKindError, reason: reason}
	}
	return steps
}

type orchestratorStep70 struct {
	git      step70_decide.GitOps
	resolver step70_decide.TargetResolver
}

func (s orchestratorStep70) Run(ctx context.Context, run *StepRunContext) error {
	if err := step70_decide.Run(ctx, run.PR, run.IO, run.TaskPackage, run.Candidates, run.IntentionFile, step70_decide.Deps{
		Git:      s.git,
		Resolver: s.resolver,
		Now: func() time.Time {
			return time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
		},
	}); err != nil {
		return err
	}
	decisionPath, err := run.IO.ResolveRunRelative("70/decision.json")
	if err != nil {
		return err
	}
	if !fileExists(decisionPath) {
		return nil
	}
	decision, err := internalio.ReadJSON[contracts.Decision](decisionPath)
	if err != nil {
		return err
	}
	run.Decision = &decision
	return nil
}

type testStep70Resolver struct{}

func (testStep70Resolver) Resolve(runCtx internalio.RunContext, pkg *contracts.TaskPackage, candidates *contracts.Candidates) (step70_decide.Target, bool, error) {
	_ = pkg
	if candidates == nil || len(candidates.Candidates) == 0 {
		return step70_decide.Target{}, false, nil
	}
	ruleID := "r-bf1d22bf4a85"
	ruleBodyPath, err := runCtx.ResolveRunRelative("staging/rules/" + ruleID + ".md")
	if err != nil {
		return step70_decide.Target{}, false, err
	}
	bodyPath, err := runCtx.ResolveRunRelative(candidates.Candidates[0].ProposedBodyPath)
	if err != nil {
		return step70_decide.Target{}, false, err
	}
	body, err := os.ReadFile(bodyPath)
	if err != nil {
		return step70_decide.Target{}, false, err
	}
	if err := internalio.WriteAtomic(ruleBodyPath, body); err != nil {
		return step70_decide.Target{}, false, err
	}
	return step70_decide.Target{
		BestBranch:    "best",
		BestShaBefore: strings.Repeat("1", 40),
		TargetSHA:     strings.Repeat("2", 40),
		RulesToAppend: []contracts.RuleRegistryEntry{{
			Kind: contracts.RegistryKindAdded,
			Value: contracts.RuleRegistryAdded{
				Kind:          contracts.RegistryKindAdded,
				SchemaVersion: "1",
				RuleID:        ruleID,
				RulePath:      "rules/" + ruleID + ".md",
				Sha256:        candidates.Candidates[0].ProposedBodySha256,
			},
		}},
	}, true, nil
}

type testStep70Git struct {
	head    string
	pushErr error
	state   *testStep70GitState
}

func (g testStep70Git) RemoteHead(context.Context, string) (string, error) {
	if g.state != nil {
		return g.state.head, nil
	}
	return g.head, nil
}

func (g testStep70Git) PushForceWithLease(context.Context, string, string, string) error {
	if g.state != nil && g.state.onPush != nil {
		g.state.onPush(g.state)
	}
	return g.pushErr
}

func (g testStep70Git) RemoveWorktree(context.Context, string) error {
	return nil
}

type testStep70GitState struct {
	head   string
	onPush func(*testStep70GitState)
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	runsBase := filepath.Join(t.TempDir(), "runs")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")
	return &config.Config{
		Repo: config.RepoConfig{
			Root:          t.TempDir(),
			DefaultBranch: "main",
			BestBranch:    "best",
		},
		Worktree: config.WorktreeConfig{
			Base: worktreeBase,
		},
		Paths: config.PathsConfig{
			Runs: runsBase,
		},
		RegistryHighThreshold:     config.DefaultRegistryHighThreshold,
		RegistryCriticalThreshold: config.DefaultRegistryCriticalThreshold,
		StepTimeouts: map[string]int{
			"step10": 300,
			"step20": 300,
			"step30": 300,
			"step40": 300,
			"step50": 300,
			"step60": 300,
			"step70": 300,
		},
	}
}

func testConfigWithCLIJudge(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	runsBase := filepath.Join(dir, "runs")
	worktreeBase := filepath.Join(dir, "worktrees")
	repoRoot := filepath.Join(dir, "repo")
	require.NoError(t, os.MkdirAll(repoRoot, 0o755))
	codexPath := writeFakeCodexJudge(t, dir)
	configPath := filepath.Join(dir, "config.yaml")
	agentsPath := filepath.Join(dir, "agents.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(fmt.Sprintf(`
repo:
  root: %q
  default_branch: "main"
  best_branch: "best"
paths:
  runs: %q
worktree:
  base: %q
agent_config_path: %q
`, repoRoot, runsBase, worktreeBase, agentsPath)), 0o644))
	require.NoError(t, os.WriteFile(agentsPath, []byte(fmt.Sprintf(`
profiles:
  codex-judge:
    provider: codex
    binary: %q
  stub:
    provider: stub
roles:
  implementer: stub
  judge_primary: codex-judge
  judge_secondary: codex-judge
  judge_arbiter: codex-judge
`, codexPath)), 0o644))
	cfg, err := config.LoadConfig(configPath)
	require.NoError(t, err)
	return cfg
}

func writeFakeCodexJudge(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "codex-judge")
	require.NoError(t, os.WriteFile(path, []byte(`#!/bin/sh
set -eu
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    out="$2"
    shift 2
    continue
  fi
  shift
done
cat > /dev/null
cat > "$out" <<'EOF'
{"scores":[
  {"dimension":"fidelity","score":80,"reason":"r1"},
  {"dimension":"correctness","score":81,"reason":"r2"},
  {"dimension":"maintainability","score":82,"reason":"r3"},
  {"dimension":"discipline","score":83,"reason":"r4"},
  {"dimension":"communication","score":84,"reason":"r5"}
],"compliance":[
  {"rule_id":"stub-rubric-rule","verdict":"compliant","rationale":"ok"}
]}
EOF
`), 0o755))
	return path
}

func configJudgePanelPromptVersion(t *testing.T, cfg *config.Config) string {
	t.Helper()
	primary, err := judges.NewJudgeFromConfig(cfg, contracts.JudgeRolePrimary)
	require.NoError(t, err)
	secondary, err := judges.NewJudgeFromConfig(cfg, contracts.JudgeRoleSecondary)
	require.NoError(t, err)
	arbiter, err := judges.NewJudgeFromConfig(cfg, contracts.JudgeRoleArbiter)
	require.NoError(t, err)
	return judges.PanelPromptVersion("phase0-stub", primary, secondary, arbiter)
}

func repoRootFromTestFile(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func installFakeCLI(t *testing.T) string {
	t.Helper()
	sourceDir := filepath.Join(repoRootFromTestFile(t), "internal", "orchestrator", "testdata", "bin")
	destDir := t.TempDir()
	for _, name := range []string{"gh", "git", "claude"} {
		src := filepath.Join(sourceDir, name)
		data, err := os.ReadFile(src)
		require.NoError(t, err)
		dst := filepath.Join(destDir, name)
		require.NoError(t, os.WriteFile(dst, data, 0o755))
	}
	restoreTrustedPath := processenv.SetTrustedPathForTest(destDir + string(os.PathListSeparator) + processenv.TrustedPath())
	t.Cleanup(restoreTrustedPath)
	return destDir
}

func stubTaskPackageForRun(runCtx internalio.RunContext, pr int) contracts.TaskPackage {
	worktrees := make([]contracts.WorktreeAllocation, 0, len(defaultAgents)*2)
	for pass := 1; pass <= 2; pass++ {
		for _, agent := range defaultAgents {
			worktrees = append(worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    filepath.Join(runCtx.WorktreeBase, fmt.Sprintf("%s-pass%d-%s", runCtx.RunID, pass, agent)),
				Branch:  fmt.Sprintf("stub/%s/pass%d/%s", runCtx.RunID, pass, agent),
				BaseSHA: strings.Repeat("a", 40),
				HeadSHA: strings.Repeat("a", 40),
			})
		}
	}
	return contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runCtx.RunID,
		PR:                      pr,
		Title:                   "stub task",
		BaseSHA:                 strings.Repeat("a", 40),
		BestBranch:              "best",
		ReconstructedTaskPrompt: "stub prompt",
		Worktrees:               worktrees,
		CreatedAt:               time.Now().UTC(),
	}
}

func appendJSONLForTest(runCtx internalio.RunContext, rel string, record any) error {
	path, err := runCtx.ResolveRunRelative(rel)
	if err != nil {
		return err
	}
	return internalio.AppendJSONL(path, record)
}

func mustReadJSONL[T any](t *testing.T, runCtx internalio.RunContext, rel string) []T {
	t.Helper()
	path, err := runCtx.ResolveRunRelative(rel)
	require.NoError(t, err)
	rows, err := internalio.ReadJSONL[T](path)
	require.NoError(t, err)
	return rows
}

func writePass1ScoringRowsForAdapterTest(
	t *testing.T,
	runCtx internalio.RunContext,
	runID contracts.RunID,
	agent contracts.AgentID,
	rubricVersion string,
	promptVersion string,
) {
	t.Helper()
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	dimensions := []contracts.Dimension{
		contracts.DimensionFidelity,
		contracts.DimensionCorrectness,
		contracts.DimensionMaintainability,
		contracts.DimensionDiscipline,
		contracts.DimensionCommunication,
	}
	for i, dimension := range dimensions {
		require.NoError(t, appendJSONLForTest(runCtx, "30/scores-A.jsonl", contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         runID,
			Pass:          1,
			Agent:         agent,
			Dimension:     dimension,
			Score:         80 + i,
			Reasons:       "pass1 fixture score",
			VerdictPath:   contracts.VerdictPathAgreement,
			RubricVersion: rubricVersion,
			PromptVersion: promptVersion,
			ResolvedAt:    now,
		}))
	}
	require.NoError(t, appendJSONLForTest(runCtx, "30/compliance-A.jsonl", contracts.ComplianceEntry{
		SchemaVersion: "1",
		RunID:         runID,
		Pass:          1,
		Agent:         agent,
		RuleID:        "stub-rubric-rule",
		Verdict:       contracts.ComplianceVerdictCompliant,
		Rationale:     "pass1 fixture compliance",
		VerdictPath:   contracts.VerdictPathAgreement,
		RubricVersion: rubricVersion,
		PromptVersion: promptVersion,
		ResolvedAt:    now,
	}))
}

func writeValidStep30ArtifactsForTest(runCtx internalio.RunContext) error {
	scoreFinalPath, err := runCtx.ResolveRunRelative("30/scores-A.jsonl")
	if err != nil {
		return err
	}
	complianceFinalPath, err := runCtx.ResolveRunRelative("30/compliance-A.jsonl")
	if err != nil {
		return err
	}
	scoreRawPath, err := runCtx.ResolveRunRelative("30/scores-A-raw.jsonl")
	if err != nil {
		return err
	}
	complianceRawPath, err := runCtx.ResolveRunRelative("30/compliance-A-raw.jsonl")
	if err != nil {
		return err
	}
	scoreFinal, err := internalio.ReadJSONL[contracts.ScoreEntry](scoreFinalPath)
	if err != nil {
		return err
	}
	complianceFinal, err := internalio.ReadJSONL[contracts.ComplianceEntry](complianceFinalPath)
	if err != nil {
		return err
	}
	if err := internalio.WriteAtomic(scoreRawPath, nil); err != nil {
		return err
	}
	for _, row := range scoreFinal {
		if err := internalio.AppendJSONL(scoreRawPath, contracts.RawScoreEntry{
			SchemaVersion: "1",
			RunID:         row.RunID,
			Pass:          row.Pass,
			Agent:         row.Agent,
			JudgeRole:     contracts.JudgeRolePrimary,
			Dimension:     row.Dimension,
			Score:         row.Score,
			Reasons:       row.Reasons,
			OutputSha256:  strings.Repeat("a", 64),
			RubricVersion: row.RubricVersion,
			PromptVersion: row.PromptVersion,
			ResolvedAt:    row.ResolvedAt,
		}); err != nil {
			return err
		}
	}
	if err := internalio.WriteAtomic(complianceRawPath, nil); err != nil {
		return err
	}
	for _, row := range complianceFinal {
		if err := internalio.AppendJSONL(complianceRawPath, contracts.RawComplianceEntry{
			SchemaVersion: "1",
			RunID:         row.RunID,
			Pass:          row.Pass,
			Agent:         row.Agent,
			JudgeRole:     contracts.JudgeRolePrimary,
			RuleID:        row.RuleID,
			Verdict:       row.Verdict,
			Rationale:     row.Rationale,
			OutputSha256:  strings.Repeat("a", 64),
			RubricVersion: row.RubricVersion,
			PromptVersion: row.PromptVersion,
			ResolvedAt:    row.ResolvedAt,
		}); err != nil {
			return err
		}
	}
	marker, err := scorecore.BuildStep30DoneMarker(scorecore.Step30MarkerInputs{
		Agents: []contracts.AgentID{"a1"},
		Paths: scorecore.Step30MarkerPaths{
			ScoreFinal:      scoreFinalPath,
			ComplianceFinal: complianceFinalPath,
			ScoreRaw:        scoreRawPath,
			ComplianceRaw:   complianceRawPath,
		},
		ResolvedAt: time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	return scorecore.WriteStep30DoneMarker(runCtx, marker)
}

func seedResumeRun(t *testing.T, runCtx internalio.RunContext, pr int) error {
	t.Helper()
	if err := os.MkdirAll(runCtx.RunDir(), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(runCtx.RunDir(), "config.snapshot.yaml"), []byte(
		"repo:\n"+
			"  root: "+runCtx.RunsBase+"\n"+
			"  default_branch: main\n"+
			"  best_branch: best\n"+
			"paths:\n"+
			"  runs: "+runCtx.RunsBase+"\n"+
			"worktree:\n"+
			"  base: "+runCtx.WorktreeBase+"\n",
	), 0o644); err != nil {
		return err
	}
	pkg := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runCtx.RunID,
		PR:                      pr,
		Title:                   "resume",
		BaseSHA:                 strings.Repeat("a", 40),
		BestBranch:              "best",
		ReconstructedTaskPrompt: "resume prompt",
		CreatedAt:               time.Now().UTC(),
	}
	for pass := 1; pass <= 2; pass++ {
		for _, agent := range defaultAgents {
			pkg.Worktrees = append(pkg.Worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    filepath.Join(runCtx.WorktreeBase, fmt.Sprintf("%s-pass%d-%s", runCtx.RunID, pass, agent)),
				Branch:  fmt.Sprintf("resume/%s/pass%d/%s", runCtx.RunID, pass, agent),
				BaseSHA: strings.Repeat("a", 40),
				HeadSHA: strings.Repeat("a", 40),
			})
		}
	}
	if err := internalio.WriteJSONAtomic(runCtx.TaskPackagePath(), pkg); err != nil {
		return err
	}
	if err := internalio.WriteAtomic(runCtx.BaseSHAPath(), []byte(strings.Repeat("a", 40)+"\n")); err != nil {
		return err
	}
	for pass := 1; pass <= 2; pass++ {
		for _, agent := range defaultAgents {
			prefix := manifestPrefix(pass, agent)
			if err := writeRunText(runCtx, filepath.Join(prefix, "diff.patch"), ""); err != nil {
				return err
			}
			if err := writeRunText(runCtx, filepath.Join(prefix, "session.jsonl"), ""); err != nil {
				return err
			}
			if err := writeRunText(runCtx, filepath.Join(prefix, "checklist-result.json"), "{}\n"); err != nil {
				return err
			}
			manifest := contracts.Manifest{
				Kind: contracts.ManifestKindSuccess,
				Value: contracts.ManifestSuccess{
					Kind:          contracts.ManifestKindSuccess,
					SchemaVersion: "1",
					RunID:         runCtx.RunID,
					Pass:          pass,
					Agent:         agent,
					BranchName:    fmt.Sprintf("resume/%s/pass%d/%s", runCtx.RunID, pass, agent),
					HeadSHA:       strings.Repeat("a", 40),
					BaseSHA:       strings.Repeat("a", 40),
					DiffPath:      filepath.Join(prefix, "diff.patch"),
					SessionPath:   filepath.Join(prefix, "session.jsonl"),
					ChecklistPath: filepath.Join(prefix, "checklist-result.json"),
					PromptVersion: "stub",
					StartedAt:     time.Now().UTC(),
					FinishedAt:    time.Now().UTC(),
				},
			}
			manifestPath, err := runCtx.ManifestPath(pass, agent)
			if err != nil {
				return err
			}
			if err := internalio.WriteJSONAtomic(manifestPath, manifest); err != nil {
				return err
			}
		}
	}
	if err := writeRunText(runCtx, "30/done.marker", "stub\n"); err != nil {
		return err
	}
	candidates := contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          runCtx.RunID,
		Candidates:     []contracts.Candidate{},
		CandidatesHash: contracts.CanonicalCandidatesHash(nil),
		CreatedAt:      time.Now().UTC(),
	}
	candidatesPath, err := runCtx.ResolveRunRelative("40/candidates.json")
	if err != nil {
		return err
	}
	if err := internalio.WriteJSONAtomic(candidatesPath, candidates); err != nil {
		return err
	}
	if err := writeRunText(runCtx, "60/done.marker", "stub\n"); err != nil {
		return err
	}
	return state.Append(runCtx, startedEntry(pr, runCtx.RunID, time.Now().UTC()))
}

func validPlanningIntention(runID contracts.RunID) contracts.IntentionRecord {
	best := strings.Repeat("1", 40)
	target := strings.Repeat("2", 40)
	hash := strings.Repeat("3", 64)
	body := "r-0001 body\n"
	idempotencyKey := contracts.ComputeAdoptIdempotencyKey(string(runID), target, best, hash)
	return contracts.IntentionRecord{
		SchemaVersion:      "1",
		Stage:              contracts.IntentionStagePlanning,
		IdempotencyKey:     idempotencyKey,
		RunID:              runID,
		BestShaBefore:      best,
		TargetSha:          target,
		CandidatesHash:     hash,
		RegistryHeadBefore: "",
		PlannedAdoption: &contracts.PlannedAdoption{
			IdempotencyKey: idempotencyKey,
			Entries: []contracts.PlannedAdoptionEntry{
				{
					OpID:     contracts.ComputePlannedAdoptionEntryOpID(idempotencyKey, 0, "r-0001"),
					Kind:     contracts.RegistryKindAdded,
					RuleID:   "r-0001",
					RulePath: "rules/r-0001.md",
					Sha256:   sha256String(body),
				},
			},
		},
		StartedAt: time.Now().UTC(),
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
