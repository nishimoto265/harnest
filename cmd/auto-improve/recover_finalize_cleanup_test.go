package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoverRejectsRunWithoutInspect(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--run", "2026-04-21-PR42-abcdef0"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "recover: not implemented")
}

func TestRecoverFinalizeCleanupRequiresRunAndHeadFlags(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--finalize-cleanup", "--run", "2026-04-21-PR42-abcdef0"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--remote-head and --registry-head")
}

func TestRecoverFinalizeCleanupVerifiesHeadsAndClearsAbortedSentinel(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))

	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	runDir := filepath.Join(runsBase, string(runID))
	require.NoError(t, os.MkdirAll(filepath.Join(runDir, "70"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(runsBase, "needs-recovery"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "config.snapshot.yaml"), []byte(
		"repo:\n"+
			"  root: "+root+"\n"+
			"  default_branch: main\n"+
			"  best_branch: auto-improve/best\n"+
			"  policy_branch: auto-improve/policy\n"+
			"paths:\n"+
			"  runs: "+runsBase+"\n"+
			"worktree:\n"+
			"  base: "+worktreeBase+"\n",
	), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelAbortedFilename(runID)), []byte(`{"run_id":"2026-04-21-PR42-abcdef0","pr":42,"reason":"manual_abort_pending_cleanup","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))

	pkg := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runID,
		PR:                      42,
		Title:                   "cleanup",
		BaseSHA:                 strings.Repeat("1", 40),
		BestBranch:              "auto-improve/best",
		ReconstructedTaskPrompt: "prompt",
		CreatedAt:               time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		Worktrees: []contracts.WorktreeAllocation{
			{Agent: "a1", Pass: 1, Path: filepath.Join(worktreeBase, string(runID)+"-pass1-a1"), Branch: "test/pass1/a1", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a2", Pass: 1, Path: filepath.Join(worktreeBase, string(runID)+"-pass1-a2"), Branch: "test/pass1/a2", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a3", Pass: 1, Path: filepath.Join(worktreeBase, string(runID)+"-pass1-a3"), Branch: "test/pass1/a3", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a1", Pass: 2, Path: filepath.Join(worktreeBase, string(runID)+"-pass2-a1"), Branch: "test/pass2/a1", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a2", Pass: 2, Path: filepath.Join(worktreeBase, string(runID)+"-pass2-a2"), Branch: "test/pass2/a2", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a3", Pass: 2, Path: filepath.Join(worktreeBase, string(runID)+"-pass2-a3"), Branch: "test/pass2/a3", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
		},
	}
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "task-package.json"), pkg))
	bestShaBefore := strings.Repeat("a", 40)
	targetSha := strings.Repeat("b", 40)
	candidatesHash := strings.Repeat("c", 64)
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "70", "intention.json"), contracts.IntentionRecord{
		SchemaVersion:  "1",
		Stage:          contracts.IntentionStageNeedsManualRecovery,
		IdempotencyKey: contracts.ComputeAdoptIdempotencyKey(string(runID), targetSha, bestShaBefore, candidatesHash),
		RunID:          runID,
		BestShaBefore:  bestShaBefore,
		TargetSha:      targetSha,
		CandidatesHash: candidatesHash,
		StartedAt:      time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		RecoveryReason: contracts.RollbackReasonManualAbortPendingCleanup,
		FailedStep:     contracts.FailedStep70,
	}))
	ctx, err := internalio.NewRunContext(runID, runsBase, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, state.NewWriter(ctx).Append(contracts.StateEntry{
		Kind: contracts.StateKindNeedsManualRecovery,
		Value: contracts.StateEntryNeedsManualRecovery{
			Kind:       contracts.StateKindNeedsManualRecovery,
			PR:         42,
			RunID:      runID,
			Step:       contracts.FailedStep70,
			Reason:     contracts.RollbackReasonManualAbortPendingCleanup,
			FailedStep: contracts.FailedStep70,
			At:         time.Date(2026, 4, 21, 12, 5, 0, 0, time.UTC),
		},
	}))

	require.NoError(t, os.WriteFile(filepath.Join(root, "config.yaml"), []byte(
		"repo:\n"+
			"  root: "+root+"\n"+
			"  default_branch: main\n"+
			"  best_branch: auto-improve/best\n"+
			"  policy_branch: auto-improve/policy\n"+
			"paths:\n"+
			"  runs: "+runsBase+"\n"+
			"worktree:\n"+
			"  base: "+worktreeBase+"\n",
	), 0o644))
	originalRemoteHead := recoverRemoteHead
	originalRegistryHead := recoverRegistryHead
	recoverRemoteHead = func(_ context.Context, repoRoot, branch string) (string, error) {
		assert.Equal(t, root, repoRoot)
		switch branch {
		case "auto-improve/best":
			return strings.Repeat("d", 40), nil
		case "auto-improve/policy":
			return strings.Repeat("f", 40), nil
		default:
			t.Fatalf("unexpected branch: %s", branch)
			return "", nil
		}
	}
	recoverRegistryHead = func(string) (string, error) {
		return strings.Repeat("e", 64), nil
	}
	t.Cleanup(func() {
		recoverRemoteHead = originalRemoteHead
		recoverRegistryHead = originalRegistryHead
	})

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	var stdout bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{
		"recover",
		"--run", string(runID),
		"--finalize-cleanup",
		"--remote-head", strings.Repeat("d", 40),
		"--registry-head", strings.Repeat("e", 64),
		"--policy-head", strings.Repeat("f", 40),
	})
	require.NoError(t, cmd.Execute())

	assert.NoFileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelAbortedFilename(runID)))
	assert.FileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelClearedFilename(runID)))
	assert.NoFileExists(t, filepath.Join(runDir, "70", "intention.json"))
}
