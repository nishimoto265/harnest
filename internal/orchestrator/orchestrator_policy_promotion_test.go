package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/nishimoto265/harnest/internal/contracts/stepio"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/state"
	"github.com/nishimoto265/harnest/internal/steps/step70_decide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
