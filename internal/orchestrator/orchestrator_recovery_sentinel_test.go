package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/nishimoto265/auto-improve/internal/steps/step70_decide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
