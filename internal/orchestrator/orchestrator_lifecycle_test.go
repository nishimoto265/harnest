package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_ArchiveFailureDoesNotAppendTerminalState(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	archive := &failOnceStep{err: fmt.Errorf("archive failed")}
	orch.steps.Step10 = stubStep10{}
	orch.steps.Step20 = stubAgentSteps()
	orch.steps.Step30 = stubMarkerStep{path: "30/done.marker"}
	orch.steps.Step40 = duplicateOnlyCandidateStep{}
	orch.steps.Step50 = stubAgentSteps()
	orch.steps.Step60 = scriptedStep60Step{decode: orch.decoders.Step60}
	orch.steps.Step70 = stubStep70{}
	orch.steps.Archive = archive

	runID := contracts.RunID("2026-04-21-PR81-abcdef0")
	err = orch.Run(context.Background(), 81, RunOptions{RunID: runID})
	require.ErrorContains(t, err, "archive failed")

	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	latest, err := state.LatestRunForPR(runCtx, 81)
	require.NoError(t, err)
	require.NotNil(t, latest.LastEvent)
	assert.Equal(t, contracts.StateKindStepDone, latest.LastEvent.Kind)
	assert.Equal(t, state.NextActionResume, latest.Action)

	require.NoError(t, orch.Run(context.Background(), 81, RunOptions{}))
	latest, err = state.LatestRunForPR(runCtx, 81)
	require.NoError(t, err)
	require.NotNil(t, latest.LastEvent)
	assert.Equal(t, contracts.StateKindCompleted, latest.LastEvent.Kind)
	assert.Equal(t, state.NextActionFreshStart, latest.Action)
}

func TestRun_CleanupFailureDoesNotAppendTerminalState(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	orch.steps.Step10 = stubStep10{}
	orch.steps.Step20 = stubAgentSteps()
	orch.steps.Step30 = stubMarkerStep{path: "30/done.marker"}
	orch.steps.Step40 = duplicateOnlyCandidateStep{}
	orch.steps.Step50 = stubAgentSteps()
	orch.steps.Step60 = scriptedStep60Step{decode: orch.decoders.Step60}
	orch.steps.Step70 = corruptCleanupStep70{}

	runID := contracts.RunID("2026-04-21-PR82-abcdef0")
	err = orch.Run(context.Background(), 82, RunOptions{RunID: runID})
	require.ErrorContains(t, err, "worktree path escapes")

	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	latest, err := state.LatestRunForPR(runCtx, 82)
	require.NoError(t, err)
	require.NotNil(t, latest.LastEvent)
	assert.Equal(t, contracts.StateKindStepDone, latest.LastEvent.Kind)
	assert.Equal(t, state.NextActionResume, latest.Action)

	orch.steps.Step70 = stubStep70{}
	require.NoError(t, orch.Run(context.Background(), 82, RunOptions{}))
	latest, err = state.LatestRunForPR(runCtx, 82)
	require.NoError(t, err)
	require.NotNil(t, latest.LastEvent)
	assert.Equal(t, contracts.StateKindCompleted, latest.LastEvent.Kind)
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

func TestRun_CanceledAfterStep70TerminalDoesNotAppendInterrupted(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	cancelStep := &cancelAfterTerminalPromoteStep{}
	orch.steps = stubPipelineSteps(nil, cancelStep)

	ctx, cancel := context.WithCancel(context.Background())
	cancelStep.cancel = cancel

	runID := contracts.RunID("2026-04-21-PR104-abcdef0")
	require.NoError(t, orch.Run(ctx, 104, RunOptions{RunID: runID}))

	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindPromoted, events[len(events)-1].Kind)
	assert.NotContains(t, eventKinds(events), contracts.StateKindInterrupted)
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
