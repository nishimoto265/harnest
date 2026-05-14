package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/state"
	"github.com/nishimoto265/harnest/internal/steps/step70_decide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoverRollbackTimesOutRemoteGitOps(t *testing.T) {
	root, runsBase, worktreeBase, runID := seedRecoverActionRun(t)
	runDir := filepath.Join(runsBase, string(runID))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))
	intention := seedRecoverIntention(runID, contracts.IntentionStagePlanning, strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 64))
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "70", "intention.json"), intention))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalGitFactory := recoverGitOpsForRepo
	originalTimeout := recoverMutationTimeout
	recoverMutationTimeout = 25 * time.Millisecond
	recoverGitOpsForRepo = func(string) step70_decide.GitOps {
		return &blockingRecoverGit{}
	}
	t.Cleanup(func() {
		recoverGitOpsForRepo = originalGitFactory
		recoverMutationTimeout = originalTimeout
	})

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--run", string(runID), "--rollback"})
	err = cmd.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestRecoverRollbackWritesRollbackDecisionAndClearsSentinel(t *testing.T) {
	root, runsBase, worktreeBase, runID := seedRecoverActionRun(t)
	runDir := filepath.Join(runsBase, string(runID))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))
	intention := seedRecoverIntention(runID, contracts.IntentionStagePlanning, strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 64))
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "70", "intention.json"), intention))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalGitFactory := recoverGitOpsForRepo
	recoverGitOpsForRepo = func(string) step70_decide.GitOps {
		return &recoverTestGit{head: strings.Repeat("a", 40)}
	}
	t.Cleanup(func() { recoverGitOpsForRepo = originalGitFactory })

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	var stdout bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"recover", "--run", string(runID), "--rollback"})
	require.NoError(t, cmd.Execute())

	assert.NoFileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)))
	assert.NoFileExists(t, filepath.Join(runDir, "70", "intention.json"))
	decision, err := internalio.ReadJSON[contracts.Decision](filepath.Join(runDir, "70", "decision.json"))
	require.NoError(t, err)
	assert.Equal(t, contracts.DecisionActionRollback, decision.Action)
	events, err := state.ScanEventsForRun(mustNewRunCtx(t, runID, runsBase, worktreeBase), runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindRollback, events[len(events)-1].Kind)
}

func TestRecoverRollbackAllowsParkedNeedsManualRecoveryWhenBranchAlreadyAtBase(t *testing.T) {
	root, runsBase, worktreeBase, runID := seedRecoverActionRun(t)
	runDir := filepath.Join(runsBase, string(runID))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))
	intention := seedRecoverIntention(runID, contracts.IntentionStageNeedsManualRecovery, strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 64))
	intention.RecoveryReason = contracts.RollbackReasonTransactionalFailure
	intention.FailedStep = contracts.FailedStep70
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "70", "intention.json"), intention))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalGitFactory := recoverGitOpsForRepo
	recoverGitOpsForRepo = func(string) step70_decide.GitOps {
		return &recoverTestGit{head: strings.Repeat("a", 40)}
	}
	t.Cleanup(func() { recoverGitOpsForRepo = originalGitFactory })

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--run", string(runID), "--rollback"})
	require.NoError(t, cmd.Execute())

	assert.NoFileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)))
	assert.NoFileExists(t, filepath.Join(runDir, "70", "intention.json"))
	events, err := state.ScanEventsForRun(mustNewRunCtx(t, runID, runsBase, worktreeBase), runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindRollback, events[len(events)-1].Kind)
}

func TestRecoverRollbackAndAdoptAnywayRefuseAbortedSentinel(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "rollback", args: []string{"--rollback"}},
		{name: "adopt anyway", args: []string{"--adopt-anyway"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, runsBase, worktreeBase, runID := seedRecoverActionRun(t)
			runDir := filepath.Join(runsBase, string(runID))
			abortedName := contracts.NeedsRecoverySentinelAbortedFilename(runID)
			require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", abortedName), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"manual_abort_pending_cleanup","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))
			intention := seedRecoverIntention(runID, contracts.IntentionStageNeedsManualRecovery, strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 64))
			intention.RecoveryReason = contracts.RollbackReasonManualAbortPendingCleanup
			intention.FailedStep = contracts.FailedStep70
			require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "70", "intention.json"), intention))

			writeTestConfig(t, root, runsBase, worktreeBase)
			originalWD, err := os.Getwd()
			require.NoError(t, err)
			require.NoError(t, os.Chdir(root))
			t.Cleanup(func() { _ = os.Chdir(originalWD) })

			cmd := newRootCmd()
			cmd.SetArgs(append([]string{"recover", "--run", string(runID)}, tc.args...))
			err = cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "aborted sentinel")
			assert.Contains(t, err.Error(), "--finalize-cleanup")
			assert.FileExists(t, filepath.Join(runsBase, "needs-recovery", abortedName))
			assert.FileExists(t, filepath.Join(runDir, "70", "intention.json"))
		})
	}
}

func TestRecoverRollbackRejectsCrossRunTaskPackage(t *testing.T) {
	root, runsBase, worktreeBase, runID := seedRecoverActionRun(t)
	runDir := filepath.Join(runsBase, string(runID))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))
	intention := seedRecoverIntention(runID, contracts.IntentionStagePlanning, strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 64))
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "70", "intention.json"), intention))

	pkg, err := internalio.ReadJSON[contracts.TaskPackage](filepath.Join(runDir, "task-package.json"))
	require.NoError(t, err)
	pkg.RunID = contracts.RunID("2026-04-21-PR99-deadbee")
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "task-package.json"), pkg))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--run", string(runID), "--rollback"})
	err = cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "task-package run_id mismatch")
}
