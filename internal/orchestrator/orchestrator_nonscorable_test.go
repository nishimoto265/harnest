package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_AllNonScorablePass1StopsBeforeStep30(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	recorder := &callRecorder{}
	orch.steps = Steps{
		Step10:  stubStep10{},
		Step20:  noChangeAgentSteps(),
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

func TestRun_AllTimeoutPass1RecordsTimeout(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	recorder := &callRecorder{}
	orch.steps = Steps{
		Step10:  stubStep10{},
		Step20:  nonScorableAgentSteps(contracts.ManifestKindTimeout),
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
	assert.Equal(t, contracts.StateKindTimeout, last.Kind)
	timeout, ok := last.Value.(contracts.StateEntryTimeout)
	require.True(t, ok)
	assert.Equal(t, contracts.FailedStep20, timeout.Step)
	assert.Empty(t, recorder.snapshot())
	assert.NoFileExists(t, filepath.Join(cfg.Paths.Runs, string(runID), "30", "done.marker"))
}

func TestRun_ResumeAllTimeoutPass1RecordsTimeout(t *testing.T) {
	cfg := testConfig(t)
	runID := contracts.RunID("2026-04-21-PR454-abcdef0")
	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, seedResumeRun(t, runCtx, 454))
	require.NoError(t, os.RemoveAll(filepath.Join(runCtx.RunDir(), "30")))
	require.NoError(t, os.RemoveAll(filepath.Join(runCtx.RunDir(), "40")))
	require.NoError(t, os.RemoveAll(filepath.Join(runCtx.RunDir(), "50-pass2")))
	require.NoError(t, os.RemoveAll(filepath.Join(runCtx.RunDir(), "60")))
	overwriteTimeoutManifests(t, runCtx, 1)

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

	require.NoError(t, orch.Run(context.Background(), 454, RunOptions{RunID: runID}))

	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	assert.Equal(t, contracts.StateKindTimeout, last.Kind)
	timeout, ok := last.Value.(contracts.StateEntryTimeout)
	require.True(t, ok)
	assert.Equal(t, contracts.FailedStep20, timeout.Step)
	assert.Empty(t, recorder.snapshot())
}

func TestRun_ResumeAllTimeoutPass1BeatsStaleStep30Marker(t *testing.T) {
	cfg := testConfig(t)
	runID := contracts.RunID("2026-04-21-PR456-abcdef0")
	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, seedResumeRun(t, runCtx, 456))
	require.NoError(t, os.RemoveAll(filepath.Join(runCtx.RunDir(), "40")))
	require.NoError(t, os.RemoveAll(filepath.Join(runCtx.RunDir(), "50-pass2")))
	require.NoError(t, os.RemoveAll(filepath.Join(runCtx.RunDir(), "60")))
	overwriteTimeoutManifests(t, runCtx, 1)

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

	require.NoError(t, orch.Run(context.Background(), 456, RunOptions{RunID: runID}))

	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	assert.Equal(t, contracts.StateKindTimeout, last.Kind)
	timeout, ok := last.Value.(contracts.StateEntryTimeout)
	require.True(t, ok)
	assert.Equal(t, contracts.FailedStep20, timeout.Step)
	assert.Empty(t, recorder.snapshot())
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
		Step50:  noChangeAgentSteps(),
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
	assert.Equal(t, contracts.FailedStep50, failed.Step)
	assert.NotContains(t, recorder.snapshot(), "70")
}

func TestRun_AllTimeoutPass2RecordsTimeout(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	recorder := &callRecorder{}
	orch.steps = Steps{
		Step10:  stubStep10{},
		Step20:  stubAgentSteps(),
		Step30:  stubMarkerStep{path: "30/done.marker"},
		Step40:  forcedCandidateStep{},
		Step50:  nonScorableAgentSteps(contracts.ManifestKindTimeout),
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
	assert.Equal(t, contracts.StateKindTimeout, last.Kind)
	timeout, ok := last.Value.(contracts.StateEntryTimeout)
	require.True(t, ok)
	assert.Equal(t, contracts.FailedStep50, timeout.Step)
	assert.Empty(t, recorder.snapshot())
}

func TestRun_ResumeAllTimeoutPass2RecordsTimeout(t *testing.T) {
	cfg := testConfig(t)
	runID := contracts.RunID("2026-04-21-PR455-abcdef0")
	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, seedResumeRun(t, runCtx, 455))
	require.NoError(t, os.RemoveAll(filepath.Join(runCtx.RunDir(), "60")))
	overwriteTimeoutManifests(t, runCtx, 2)

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

	require.NoError(t, orch.Run(context.Background(), 455, RunOptions{RunID: runID}))

	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	assert.Equal(t, contracts.StateKindTimeout, last.Kind)
	timeout, ok := last.Value.(contracts.StateEntryTimeout)
	require.True(t, ok)
	assert.Equal(t, contracts.FailedStep50, timeout.Step)
	assert.Empty(t, recorder.snapshot())
}

func TestRun_ResumeAllTimeoutPass2BeatsStaleStep60Marker(t *testing.T) {
	cfg := testConfig(t)
	runID := contracts.RunID("2026-04-21-PR457-abcdef0")
	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, seedResumeRun(t, runCtx, 457))
	overwriteTimeoutManifests(t, runCtx, 2)

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

	require.NoError(t, orch.Run(context.Background(), 457, RunOptions{RunID: runID}))

	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	assert.Equal(t, contracts.StateKindTimeout, last.Kind)
	timeout, ok := last.Value.(contracts.StateEntryTimeout)
	require.True(t, ok)
	assert.Equal(t, contracts.FailedStep50, timeout.Step)
	assert.Empty(t, recorder.snapshot())
}

func TestRun_ProviderInterruptedPass1IsNonTerminalInterrupted(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	recorder := &callRecorder{}
	orch.steps = Steps{
		Step10:  stubStep10{},
		Step20:  providerInterruptedAgentSteps("unknown"),
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
	assert.Equal(t, contracts.InterruptedReasonUnknown, interrupted.Reason)
	assert.Empty(t, recorder.snapshot())
	for _, event := range events {
		if event.Kind != contracts.StateKindStepDone {
			continue
		}
		done := event.Value.(contracts.StateEntryStepDone)
		assert.NotEqual(t, contracts.FailedStep20, done.Step)
	}
}

func TestRun_ProviderInterruptedPass1RerunsInterruptedAgentsOnResume(t *testing.T) {
	for _, reason := range []string{
		string(contracts.InterruptedReasonRateLimit),
		string(contracts.InterruptedReasonBudget),
		string(contracts.InterruptedReasonContext),
		string(contracts.InterruptedReasonSignal),
		string(contracts.InterruptedReasonUnknown),
	} {
		t.Run(reason, func(t *testing.T) {
			cfg := testConfig(t)
			first, err := NewOrchestrator(cfg)
			require.NoError(t, err)
			first.steps = Steps{
				Step10:  stubStep10{},
				Step20:  providerInterruptedAgentSteps(reason),
				Step30:  stubMarkerStep{path: "30/done.marker"},
				Step40:  stubStep40{},
				Step50:  stubAgentSteps(),
				Step60:  stubMarkerStep{path: "60/done.marker"},
				Step70:  stubStep70{},
				Archive: stubArchiveStep{},
			}

			runID := contracts.RunID("2026-04-21-PR462-" + map[string]string{
				string(contracts.InterruptedReasonRateLimit): "aaaaaaa",
				string(contracts.InterruptedReasonBudget):    "bbbbbbb",
				string(contracts.InterruptedReasonContext):   "ccccccc",
				string(contracts.InterruptedReasonSignal):    "ddddddd",
				string(contracts.InterruptedReasonUnknown):   "eeeeeee",
			}[reason])
			require.NoError(t, first.Run(context.Background(), 462, RunOptions{RunID: runID}))

			runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
			require.NoError(t, err)
			firstEvents, err := state.ScanEventsForRun(runCtx, runID)
			require.NoError(t, err)
			require.NotEmpty(t, firstEvents)
			assert.Equal(t, contracts.StateKindInterrupted, firstEvents[len(firstEvents)-1].Kind)
			for _, event := range firstEvents {
				if event.Kind != contracts.StateKindStepDone {
					continue
				}
				done := event.Value.(contracts.StateEntryStepDone)
				assert.NotEqual(t, contracts.FailedStep20, done.Step)
			}

			recorder := &callRecorder{}
			second, err := NewOrchestrator(cfg)
			require.NoError(t, err)
			second.steps = Steps{
				Step10:  recordingStep{label: "10", recorder: recorder},
				Step20:  recordingAgentSteps("20", recorder),
				Step30:  recordingStep{label: "30", recorder: recorder},
				Step40:  recordingStep{label: "40", recorder: recorder},
				Step50:  recordingAgentSteps("50", recorder),
				Step60:  recordingStep{label: "60", recorder: recorder},
				Step70:  recordingStep{label: "70", recorder: recorder},
				Archive: recordingStep{label: "archive", recorder: recorder},
			}
			require.NoError(t, second.Run(context.Background(), 462, RunOptions{}))

			calls := recorder.snapshot()
			assert.NotContains(t, calls, "10")
			assert.Contains(t, calls, "20:a1")
			assert.Contains(t, calls, "20:a2")
			assert.Contains(t, calls, "20:a3")
			assert.Contains(t, calls, "30")
			assert.Contains(t, calls, "70")

			events, err := state.ScanEventsForRun(runCtx, runID)
			require.NoError(t, err)
			require.NotEmpty(t, events)
			assert.Equal(t, contracts.StateKindCompleted, events[len(events)-1].Kind)
		})
	}
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
	for _, event := range events {
		if event.Kind != contracts.StateKindStepDone {
			continue
		}
		done := event.Value.(contracts.StateEntryStepDone)
		assert.NotEqual(t, contracts.FailedStep50, done.Step)
	}
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

func TestRun_ResumeStep30WithNoChangeManifestsFailsImmediately(t *testing.T) {
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
				ExitCode:      0,
				Reason:        "unknown",
				Detail:        "agent produced no diff",
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

func TestRun_ResumeStep30WithUnknownNonzeroManifestsRecordsInterrupted(t *testing.T) {
	cfg := testConfig(t)
	runID := contracts.RunID("2026-04-21-PR79-bbcdef0")
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

	pkg := stubTaskPackageForRun(runCtx, 79)
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
				Detail:        "fixture provider error manifest",
				StartedAt:     time.Now().UTC(),
				FinishedAt:    time.Now().UTC(),
			},
		}))
	}
	require.NoError(t, writeRunText(runCtx, "30/done.marker", "stub\n"))
	require.NoError(t, state.NewWriter(runCtx).Append(startedEntry(79, runID, time.Now().UTC())))

	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	recorder := &callRecorder{}
	orch.steps.Step30 = recordingStep{label: "30", recorder: recorder}

	require.NoError(t, orch.Run(context.Background(), 79, RunOptions{RunID: runID}))
	assert.NotContains(t, recorder.snapshot(), "30")

	events, err := state.ScanEventsForRun(runCtx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	assert.Equal(t, contracts.StateKindInterrupted, last.Kind)
	interrupted, ok := last.Value.(contracts.StateEntryInterrupted)
	require.True(t, ok)
	assert.Equal(t, contracts.FailedStep20, interrupted.Step)
	assert.Equal(t, contracts.InterruptedReasonUnknown, interrupted.Reason)
}
