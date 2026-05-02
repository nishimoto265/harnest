package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/agents"
	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
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
