package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/detect"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/orchestrator"
	"github.com/nishimoto265/auto-improve/internal/preflight"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/nishimoto265/auto-improve/internal/steps/step70_decide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func realTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return real
}

func TestRecoverClearDivergedSunsetClearsMarkerAndUnblocksStep70(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "sunset-running.marker.diverged"), []byte("diverged\n"), 0o644))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	blocked, err := step70_decide.SentinelExists(runsBase)
	require.NoError(t, err)
	require.True(t, blocked)

	var stdout bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"recover", "--clear-diverged-sunset"})
	require.NoError(t, cmd.Execute())

	assert.NoFileExists(t, filepath.Join(runsBase, "sunset-running.marker.diverged"))
	blocked, err = step70_decide.SentinelExists(runsBase)
	require.NoError(t, err)
	assert.False(t, blocked)

	var payload map[string]string
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &payload))
	assert.Equal(t, "diverged_sunset_cleared", payload["event"])
	assert.Equal(t, runsBase, payload["runs_base"])
}

func TestRecoverClearDivergedSunsetRefusesWhenSunsetTransactionStillOpen(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "sunset-running.marker"), []byte("running\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "sunset-running.marker.diverged"), []byte("diverged\n"), 0o644))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--clear-diverged-sunset"})
	err = cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sunset-running.marker still exists")
	assert.FileExists(t, filepath.Join(runsBase, "sunset-running.marker.diverged"))
}

func TestRecoverClearDivergedSunsetFailsFastWhenPromotionLockHeld(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	lockPath := filepath.Join(runsBase, "promotion.lock")
	lock, err := internalio.AcquireFileLock(lockPath)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, lock.Unlock())
	}()

	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--clear-diverged-sunset"})
	err = cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "promotion.lock is held by another process")
}

func TestRecoverInspectReportsRegistryIntegrityError(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "rules-registry.jsonl"), []byte("{\"kind\":\"added\"\n"), 0o644))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--inspect"})
	err = cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rules-registry.jsonl integrity check failed")
}

type recoverInspectOutput struct {
	Event      string `json:"event"`
	RunsBase   string `json:"runs_base"`
	RunID      string `json:"run_id,omitempty"`
	RemoteHead string `json:"remote_head,omitempty"`
}

type recoverTestGit struct {
	head      string
	pushCalls int
}

type blockingRecoverGit struct{}

func (g *recoverTestGit) RemoteHead(context.Context, string) (string, error) {
	return g.head, nil
}

func (g *recoverTestGit) PushForceWithLease(_ context.Context, _ string, targetSHA, _ string) error {
	g.pushCalls++
	g.head = targetSHA
	return nil
}

func (*recoverTestGit) RemoveWorktree(context.Context, string) error {
	return nil
}

func (*blockingRecoverGit) RemoteHead(ctx context.Context, branch string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func (*blockingRecoverGit) PushForceWithLease(ctx context.Context, branch, targetSHA, expectedOldSHA string) error {
	<-ctx.Done()
	return ctx.Err()
}

func (*blockingRecoverGit) RemoveWorktree(context.Context, string) error {
	return nil
}

func TestRecoverInspectCreatesPromotionLockWhenAbsent(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	var stdout bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"recover", "--inspect"})
	require.NoError(t, cmd.Execute())

	assert.FileExists(t, filepath.Join(runsBase, "promotion.lock"))

	var payload recoverInspectOutput
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &payload))
	assert.Equal(t, "recover_inspect", payload.Event)
	assert.Equal(t, runsBase, payload.RunsBase)
}

func TestRecoverInspectWithRunIDUsesRunScopedPath(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	runID := "2026-04-21-PR42-abcdef0"
	runDir := filepath.Join(runsBase, runID)
	require.NoError(t, os.MkdirAll(filepath.Join(runDir, "70"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "config.snapshot.yaml"), []byte(
		"repo:\n"+
			"  root: "+root+"\n"+
			"  default_branch: main\n"+
			"  best_branch: auto-improve/best\n"+
			"paths:\n"+
			"  runs: "+runsBase+"\n"+
			"worktree:\n"+
			"  base: "+worktreeBase+"\n",
	), 0o644))
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "task-package.json"), contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   contracts.RunID(runID),
		PR:                      42,
		Title:                   "inspect",
		BaseSHA:                 strings.Repeat("1", 40),
		BestBranch:              "auto-improve/best",
		ReconstructedTaskPrompt: "prompt",
		CreatedAt:               time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		Worktrees: []contracts.WorktreeAllocation{
			{Agent: "a1", Pass: 1, Path: filepath.Join(worktreeBase, runID+"-pass1-a1"), Branch: "test/pass1/a1", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a2", Pass: 1, Path: filepath.Join(worktreeBase, runID+"-pass1-a2"), Branch: "test/pass1/a2", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a3", Pass: 1, Path: filepath.Join(worktreeBase, runID+"-pass1-a3"), Branch: "test/pass1/a3", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a1", Pass: 2, Path: filepath.Join(worktreeBase, runID+"-pass2-a1"), Branch: "test/pass2/a1", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a2", Pass: 2, Path: filepath.Join(worktreeBase, runID+"-pass2-a2"), Branch: "test/pass2/a2", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a3", Pass: 2, Path: filepath.Join(worktreeBase, runID+"-pass2-a3"), Branch: "test/pass2/a3", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
		},
	}))
	originalRemoteHead := recoverRemoteHead
	recoverRemoteHead = func(_ context.Context, repoRoot, branch string) (string, error) {
		assert.Equal(t, root, repoRoot)
		assert.Equal(t, "auto-improve/best", branch)
		return strings.Repeat("d", 40), nil
	}
	t.Cleanup(func() { recoverRemoteHead = originalRemoteHead })

	var stdout bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"recover", "--inspect", "--run", runID})
	require.NoError(t, cmd.Execute())

	var payload recoverInspectOutput
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &payload))
	assert.Equal(t, "recover_inspect", payload.Event)
	assert.Equal(t, runsBase, payload.RunsBase)
	assert.Equal(t, runID, payload.RunID)
	assert.Equal(t, strings.Repeat("d", 40), payload.RemoteHead)
}

func TestRecoverInspectWithRunIDTimesOutRemoteHeadLookup(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	runID := "2026-04-21-PR42-abcdef0"
	runDir := filepath.Join(runsBase, runID)
	require.NoError(t, os.MkdirAll(filepath.Join(runDir, "70"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "config.snapshot.yaml"), []byte(
		"repo:\n"+
			"  root: "+root+"\n"+
			"  default_branch: main\n"+
			"  best_branch: auto-improve/best\n"+
			"paths:\n"+
			"  runs: "+runsBase+"\n"+
			"worktree:\n"+
			"  base: "+worktreeBase+"\n",
	), 0o644))
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "task-package.json"), contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   contracts.RunID(runID),
		PR:                      42,
		Title:                   "inspect-timeout",
		BaseSHA:                 strings.Repeat("1", 40),
		BestBranch:              "auto-improve/best",
		ReconstructedTaskPrompt: "prompt",
		CreatedAt:               time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		Worktrees: []contracts.WorktreeAllocation{
			{Agent: "a1", Pass: 1, Path: filepath.Join(worktreeBase, runID+"-pass1-a1"), Branch: "test/pass1/a1", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a2", Pass: 1, Path: filepath.Join(worktreeBase, runID+"-pass1-a2"), Branch: "test/pass1/a2", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a3", Pass: 1, Path: filepath.Join(worktreeBase, runID+"-pass1-a3"), Branch: "test/pass1/a3", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a1", Pass: 2, Path: filepath.Join(worktreeBase, runID+"-pass2-a1"), Branch: "test/pass2/a1", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a2", Pass: 2, Path: filepath.Join(worktreeBase, runID+"-pass2-a2"), Branch: "test/pass2/a2", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a3", Pass: 2, Path: filepath.Join(worktreeBase, runID+"-pass2-a3"), Branch: "test/pass2/a3", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
		},
	}))
	originalRemoteHead := recoverRemoteHead
	originalTimeout := recoverInspectRemoteHeadTimeout
	recoverInspectRemoteHeadTimeout = 25 * time.Millisecond
	recoverRemoteHead = func(ctx context.Context, repoRoot, branch string) (string, error) {
		assert.Equal(t, root, repoRoot)
		assert.Equal(t, "auto-improve/best", branch)
		<-ctx.Done()
		return "", ctx.Err()
	}
	t.Cleanup(func() {
		recoverRemoteHead = originalRemoteHead
		recoverInspectRemoteHeadTimeout = originalTimeout
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--inspect", "--run", runID})
	err = cmd.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

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

func TestRecoverAdoptAnywayPromotesAndClearsSentinel(t *testing.T) {
	root, runsBase, worktreeBase, runID := seedRecoverActionRun(t)
	runDir := filepath.Join(runsBase, string(runID))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))
	candidatesDoc, err := internalio.ReadJSON[contracts.Candidates](filepath.Join(runDir, "40", "candidates.json"))
	require.NoError(t, err)
	candidatesHash := candidatesDoc.CandidatesHash
	intention := seedRecoverIntention(runID, contracts.IntentionStageDecisionWritten, strings.Repeat("a", 40), strings.Repeat("b", 40), candidatesHash)
	appendResult := appendRecoverRegistryEntry(t, runsBase, runID, intention)
	intention.RegistryAppendResult = &appendResult
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "70", "intention.json"), intention))
	decision := contracts.Decision{
		Action: contracts.DecisionActionAdopt,
		Value: contracts.DecisionAdopt{
			Action:               contracts.DecisionActionAdopt,
			SchemaVersion:        "1",
			RunID:                runID,
			IdempotencyKey:       intention.IdempotencyKey,
			BestShaBefore:        intention.BestShaBefore,
			TargetSha:            intention.TargetSha,
			CandidatesHash:       candidatesHash,
			RegistryAppendResult: appendResult,
			DecidedAt:            time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "70", "decision.json"), decision))
	seedRecoverPublishedRule(t, runsBase)

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalGitFactory := recoverGitOpsForRepo
	recoverGitOpsForRepo = func(string) step70_decide.GitOps {
		return &recoverTestGit{head: strings.Repeat("b", 40)}
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
	cmd.SetArgs([]string{"recover", "--run", string(runID), "--adopt-anyway"})
	require.NoError(t, cmd.Execute())

	assert.NoFileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)))
	assert.NoFileExists(t, filepath.Join(runDir, "70", "intention.json"))
	events, err := state.ScanEventsForRun(mustNewRunCtx(t, runID, runsBase, worktreeBase), runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindPromoted, events[len(events)-1].Kind)
}

func TestRecoverAdoptAnywayAllowsNeedsManualRecoveryStage(t *testing.T) {
	root, runsBase, worktreeBase, runID := seedRecoverActionRun(t)
	runDir := filepath.Join(runsBase, string(runID))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))
	candidatesDoc, err := internalio.ReadJSON[contracts.Candidates](filepath.Join(runDir, "40", "candidates.json"))
	require.NoError(t, err)
	candidatesHash := candidatesDoc.CandidatesHash
	intention := seedRecoverIntention(runID, contracts.IntentionStageNeedsManualRecovery, strings.Repeat("a", 40), strings.Repeat("b", 40), candidatesHash)
	appendResult := appendRecoverRegistryEntry(t, runsBase, runID, intention)
	intention.RegistryAppendResult = &appendResult
	intention.RecoveryReason = contracts.RollbackReasonTransactionalFailure
	intention.FailedStep = contracts.FailedStep70
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "70", "intention.json"), intention))
	seedRecoverPublishedRule(t, runsBase)

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalGitFactory := recoverGitOpsForRepo
	recoverGitOpsForRepo = func(string) step70_decide.GitOps {
		return &recoverTestGit{head: strings.Repeat("b", 40)}
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
	cmd.SetArgs([]string{"recover", "--run", string(runID), "--adopt-anyway"})
	require.NoError(t, cmd.Execute())

	assert.NoFileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)))
	assert.NoFileExists(t, filepath.Join(runDir, "70", "intention.json"))
	events, err := state.ScanEventsForRun(mustNewRunCtx(t, runID, runsBase, worktreeBase), runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindPromoted, events[len(events)-1].Kind)
}

func TestRecoverAdoptAnywayReconstructsMissingRegistryAppendResult(t *testing.T) {
	root, runsBase, worktreeBase, runID := seedRecoverActionRun(t)
	runDir := filepath.Join(runsBase, string(runID))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))
	candidatesDoc, err := internalio.ReadJSON[contracts.Candidates](filepath.Join(runDir, "40", "candidates.json"))
	require.NoError(t, err)
	candidatesHash := candidatesDoc.CandidatesHash
	intention := seedRecoverIntention(runID, contracts.IntentionStageNeedsManualRecovery, strings.Repeat("a", 40), strings.Repeat("b", 40), candidatesHash)
	intention.RegistryAppendResult = nil
	intention.RecoveryReason = contracts.RollbackReasonTransactionalFailure
	intention.FailedStep = contracts.FailedStep70
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "70", "intention.json"), intention))

	_ = appendRecoverRegistryEntry(t, runsBase, runID, intention)
	seedRecoverPublishedRule(t, runsBase)

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalGitFactory := recoverGitOpsForRepo
	recoverGitOpsForRepo = func(string) step70_decide.GitOps {
		return &recoverTestGit{head: strings.Repeat("b", 40)}
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
	cmd.SetArgs([]string{"recover", "--run", string(runID), "--adopt-anyway"})
	require.NoError(t, cmd.Execute())

	decision, err := internalio.ReadJSON[contracts.Decision](filepath.Join(runDir, "70", "decision.json"))
	require.NoError(t, err)
	adopt, ok := decision.Value.(contracts.DecisionAdopt)
	require.True(t, ok)
	assert.NotEmpty(t, adopt.RegistryAppendResult.Sha256)
	assert.NoFileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)))
}

func TestRecoverMarkManualAbortRenamesSentinel(t *testing.T) {
	root, runsBase, worktreeBase, runID := seedRecoverActionRun(t)
	runDir := filepath.Join(runsBase, string(runID))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))
	intention := seedRecoverIntention(runID, contracts.IntentionStagePlanning, strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 64))
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "70", "intention.json"), intention))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--run", string(runID), "--mark-manual-abort"})
	require.NoError(t, cmd.Execute())

	assert.NoFileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)))
	assert.FileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelAbortedFilename(runID)))
	decision, err := internalio.ReadJSON[contracts.Decision](filepath.Join(runDir, "70", "decision.json"))
	require.NoError(t, err)
	assert.Equal(t, contracts.DecisionActionRollback, decision.Action)
}

func TestRecoverMarkManualAbortAllowsPersistedIntentionWithoutSentinel(t *testing.T) {
	root, runsBase, worktreeBase, runID := seedRecoverActionRun(t)
	runDir := filepath.Join(runsBase, string(runID))
	intention := seedRecoverIntention(runID, contracts.IntentionStageNeedsManualRecovery, strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 64))
	intention.RecoveryReason = contracts.RollbackReasonTransactionalFailure
	intention.FailedStep = contracts.FailedStep70
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "70", "intention.json"), intention))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--run", string(runID), "--mark-manual-abort"})
	require.NoError(t, cmd.Execute())

	decision, err := internalio.ReadJSON[contracts.Decision](filepath.Join(runDir, "70", "decision.json"))
	require.NoError(t, err)
	assert.Equal(t, contracts.DecisionActionRollback, decision.Action)
}

func TestRecoverClearSentinelAppendsCompletedForNonTerminalRun(t *testing.T) {
	root, runsBase, worktreeBase, runID := seedRecoverActionRun(t)
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))
	ctx := mustNewRunCtx(t, runID, runsBase, worktreeBase)
	require.NoError(t, state.NewWriter(ctx).Append(contracts.StateEntry{
		Kind: contracts.StateKindStarted,
		Value: contracts.StateEntryStarted{
			Kind:  contracts.StateKindStarted,
			PR:    52,
			RunID: runID,
			Step:  contracts.FailedStep10,
			At:    time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--run", string(runID), "--clear-sentinel"})
	require.NoError(t, cmd.Execute())

	assert.NoFileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)))
	assert.FileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelClearedFilename(runID)))
	events, err := state.ScanEventsForRun(ctx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindCompleted, events[len(events)-1].Kind)
}

func TestRecoverClearSentinelDoesNotAppendCompletedAfterTerminalActionWithTrailingWarning(t *testing.T) {
	root, runsBase, worktreeBase, runID := seedRecoverActionRun(t)
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))
	ctx := mustNewRunCtx(t, runID, runsBase, worktreeBase)
	writer := state.NewWriter(ctx)
	require.NoError(t, writer.Append(contracts.StateEntry{
		Kind: contracts.StateKindCompleted,
		Value: contracts.StateEntryCompleted{
			Kind:   contracts.StateKindCompleted,
			PR:     52,
			RunID:  runID,
			Step:   contracts.FailedStep70,
			Detail: "already_terminal",
			At:     time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}))
	pr := 52
	source := contracts.WarningSourceStep70
	step := contracts.FailedStep70
	count := int64(1501)
	require.NoError(t, writer.Append(contracts.StateEntry{
		Kind: contracts.StateKindWarningRegistrySizeHigh,
		Value: contracts.StateEntryWarning{
			Kind:   contracts.StateKindWarningRegistrySizeHigh,
			PR:     &pr,
			RunID:  &runID,
			Source: &source,
			Step:   &step,
			Count:  &count,
			At:     time.Date(2026, 4, 21, 12, 1, 0, 0, time.UTC),
		},
	}))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--run", string(runID), "--clear-sentinel"})
	require.NoError(t, cmd.Execute())

	events, err := state.ScanEventsForRun(ctx, runID)
	require.NoError(t, err)
	completedCount := 0
	for _, event := range events {
		if event.Kind == contracts.StateKindCompleted {
			completedCount++
			completed, ok := event.Value.(contracts.StateEntryCompleted)
			require.True(t, ok)
			assert.NotEqual(t, "sentinel_manually_cleared", completed.Detail)
		}
	}
	assert.Equal(t, 1, completedCount)
	assert.Equal(t, contracts.StateKindWarningRegistrySizeHigh, events[len(events)-1].Kind)
	assert.NoFileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)))
}

func TestRecoverClearSentinelAllowsMissingTaskPackageAndCandidates(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	runID := contracts.RunID("2026-04-21-PR52-abcdef0")
	require.NoError(t, os.MkdirAll(filepath.Join(runsBase, "needs-recovery"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(runsBase, string(runID), "70"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))

	ctx := mustNewRunCtx(t, runID, runsBase, worktreeBase)
	require.NoError(t, state.NewWriter(ctx).Append(contracts.StateEntry{
		Kind: contracts.StateKindStarted,
		Value: contracts.StateEntryStarted{
			Kind:  contracts.StateKindStarted,
			PR:    52,
			RunID: runID,
			Step:  contracts.FailedStep10,
			At:    time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--run", string(runID), "--clear-sentinel"})
	require.NoError(t, cmd.Execute())

	assert.NoFileExists(t, filepath.Join(runsBase, string(runID), "task-package.json"))
	assert.NoFileExists(t, filepath.Join(runsBase, string(runID), "40", "candidates.json"))
	assert.NoFileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)))
	assert.FileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelClearedFilename(runID)))
}

func TestRecoverClearSentinelAllowsAbortedSentinel(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	runID := contracts.RunID("2026-04-21-PR52-abcdef0")
	require.NoError(t, os.MkdirAll(filepath.Join(runsBase, "needs-recovery"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(runsBase, string(runID), "70"), 0o755))
	abortedName := contracts.NeedsRecoverySentinelAbortedFilename(runID)
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", abortedName), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"manual_abort_pending_cleanup","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))

	ctx := mustNewRunCtx(t, runID, runsBase, worktreeBase)
	require.NoError(t, state.NewWriter(ctx).Append(contracts.StateEntry{
		Kind: contracts.StateKindStarted,
		Value: contracts.StateEntryStarted{
			Kind:  contracts.StateKindStarted,
			PR:    52,
			RunID: runID,
			Step:  contracts.FailedStep10,
			At:    time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--run", string(runID), "--clear-sentinel"})
	require.NoError(t, cmd.Execute())

	assert.NoFileExists(t, filepath.Join(runsBase, "needs-recovery", abortedName))
	assert.FileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelClearedFilename(runID)))
}

type stubPipelineRunner struct {
	prs   []int
	opts  []orchestrator.RunOptions
	onRun func(pr int, opts orchestrator.RunOptions) error
}

func (r *stubPipelineRunner) Run(_ context.Context, pr int, opts orchestrator.RunOptions) error {
	r.prs = append(r.prs, pr)
	r.opts = append(r.opts, opts)
	if r.onRun != nil {
		return r.onRun(pr, opts)
	}
	return nil
}

type pipelineRunnerFunc func(context.Context, int, orchestrator.RunOptions) error

func (f pipelineRunnerFunc) Run(ctx context.Context, pr int, opts orchestrator.RunOptions) error {
	return f(ctx, pr, opts)
}

func assertCommandExitCode(t *testing.T, err error, code int) {
	t.Helper()
	var exitErr interface{ ExitCode() int }
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, code, exitErr.ExitCode())
}

func TestRunExecutesSinglePR(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	writeTestConfig(t, root, runsBase, worktreeBase)

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	stub := &stubPipelineRunner{}
	originalNewPipelineRunner := newPipelineRunner
	newPipelineRunner = func(*config.Config) (pipelineRunner, error) {
		return stub, nil
	}
	t.Cleanup(func() { newPipelineRunner = originalNewPipelineRunner })

	cmd := newRootCmd()
	cmd.SetArgs([]string{"run", "--pr", "42"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, []int{42}, stub.prs)
}

func TestRunFromScratchPassesRunOption(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	writeTestConfig(t, root, runsBase, worktreeBase)

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	stub := &stubPipelineRunner{}
	originalNewPipelineRunner := newPipelineRunner
	newPipelineRunner = func(*config.Config) (pipelineRunner, error) {
		return stub, nil
	}
	t.Cleanup(func() { newPipelineRunner = originalNewPipelineRunner })

	cmd := newRootCmd()
	cmd.SetArgs([]string{"run", "--pr", "42", "--from-scratch"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, []int{42}, stub.prs)
	require.Len(t, stub.opts, 1)
	assert.True(t, stub.opts[0].FromScratch)
}

func TestRunFromScratchRejectsDetectLoop(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"run", "--detect-loop", "--from-scratch"})
	err := cmd.Execute()
	require.Error(t, err)
	assertCommandExitCode(t, err, 2)
	assert.Contains(t, err.Error(), "--from-scratch and --detect-loop are mutually exclusive")
}

func TestRunSignalCancelsPipelineContext(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	writeTestConfig(t, root, runsBase, worktreeBase)

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	originalNewPipelineRunner := newPipelineRunner
	newPipelineRunner = func(*config.Config) (pipelineRunner, error) {
		return pipelineRunnerFunc(func(ctx context.Context, pr int, opts orchestrator.RunOptions) error {
			require.Equal(t, 42, pr)
			require.False(t, opts.FromScratch)
			require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGTERM))
			<-ctx.Done()
			return nil
		}), nil
	}
	t.Cleanup(func() { newPipelineRunner = originalNewPipelineRunner })

	cmd := newRootCmd()
	cmd.SetArgs([]string{"run", "--pr", "42"})
	require.NoError(t, cmd.Execute())
}

func TestRunWithPreflightBlocksBeforePreflightOnNeedsRecovery(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(filepath.Join(runsBase, "needs-recovery"), 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	runID := contracts.RunID("2026-04-21-PR52-abcdef0")
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))
	writeTestConfig(t, root, runsBase, worktreeBase)

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	preflightCalled := false
	runnerCreated := false
	originalRunPreflightCheck := runPreflightCheck
	originalNewPipelineRunner := newPipelineRunner
	runPreflightCheck = func(context.Context, config.Config) preflight.PreflightResult {
		preflightCalled = true
		return preflight.PreflightResult{OK: true}
	}
	newPipelineRunner = func(*config.Config) (pipelineRunner, error) {
		runnerCreated = true
		return &stubPipelineRunner{}, nil
	}
	t.Cleanup(func() {
		runPreflightCheck = originalRunPreflightCheck
		newPipelineRunner = originalNewPipelineRunner
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"run", "--pr", "42", "--with-preflight"})
	err = cmd.Execute()
	require.Error(t, err)
	assertCommandExitCode(t, err, 10)
	assert.Contains(t, err.Error(), "global needs_manual_recovery block")
	assert.False(t, preflightCalled)
	assert.False(t, runnerCreated)
}

func TestPreflightBlocksBeforeChecksOnNeedsRecovery(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(filepath.Join(runsBase, "needs-recovery"), 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	runID := contracts.RunID("2026-04-21-PR52-abcdef0")
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))
	writeTestConfig(t, root, runsBase, worktreeBase)

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	preflightCalled := false
	originalRunPreflightCheck := runPreflightCheck
	runPreflightCheck = func(context.Context, config.Config) preflight.PreflightResult {
		preflightCalled = true
		return preflight.PreflightResult{OK: true}
	}
	t.Cleanup(func() { runPreflightCheck = originalRunPreflightCheck })

	var stdout bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"preflight"})
	err = cmd.Execute()
	require.Error(t, err)
	assertCommandExitCode(t, err, 10)
	assert.Contains(t, err.Error(), "global needs_manual_recovery block")
	assert.False(t, preflightCalled)
	assert.Empty(t, stdout.String())
}

func TestDetectMergedBlocksBeforeDetectionOnNeedsRecovery(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(filepath.Join(runsBase, "needs-recovery"), 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	runID := contracts.RunID("2026-04-21-PR52-abcdef0")
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))
	writeTestConfig(t, root, runsBase, worktreeBase)

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	detectCalled := false
	originalDetectMergedPRs := detectMergedPRs
	detectMergedPRs = func(context.Context, config.Config, string) ([]detect.MergedPR, error) {
		detectCalled = true
		return []detect.MergedPR{{Number: 42}}, nil
	}
	t.Cleanup(func() { detectMergedPRs = originalDetectMergedPRs })

	var stdout bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"detect-merged"})
	err = cmd.Execute()
	require.Error(t, err)
	assertCommandExitCode(t, err, 10)
	assert.Contains(t, err.Error(), "global needs_manual_recovery block")
	assert.False(t, detectCalled)
	assert.Empty(t, stdout.String())
}

func TestRunDetectLoopUsesConfiguredDefaultBranch(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	writeTestConfig(t, root, runsBase, worktreeBase)
	require.NoError(t, os.WriteFile(filepath.Join(root, "config.yaml"), []byte(
		"repo:\n"+
			"  github: owner/repo\n"+
			"  root: "+root+"\n"+
			"  default_branch: develop\n"+
			"  best_branch: auto-improve/best\n"+
			"paths:\n"+
			"  runs: "+runsBase+"\n"+
			"worktree:\n"+
			"  base: "+worktreeBase+"\n",
	), 0o644))

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	stub := &stubPipelineRunner{}
	originalNewPipelineRunner := newPipelineRunner
	originalDetectMergedPRs := detectMergedPRs
	newPipelineRunner = func(*config.Config) (pipelineRunner, error) {
		return stub, nil
	}
	detectMergedPRs = func(_ context.Context, cfg config.Config, processedPath string) ([]detect.MergedPR, error) {
		assert.Equal(t, "develop", cfg.Repo.DefaultBranch)
		assert.Equal(t, filepath.Join(root, "owner__repo", "runs", "processed.jsonl"), processedPath)
		return []detect.MergedPR{{Number: 101}, {Number: 102}}, nil
	}
	t.Cleanup(func() {
		newPipelineRunner = originalNewPipelineRunner
		detectMergedPRs = originalDetectMergedPRs
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"run", "--detect-loop"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, []int{101, 102}, stub.prs)
}

func TestRunDetectLoopUsesNamespacedProcessedPathWhenEnabled(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.WriteFile(filepath.Join(root, "config.yaml"), []byte(
		"repo:\n"+
			"  github: owner/repo\n"+
			"  root: "+root+"\n"+
			"  default_branch: develop\n"+
			"  best_branch: auto-improve/best\n"+
			"paths:\n"+
			"  runs: "+runsBase+"\n"+
			"worktree:\n"+
			"  base: "+worktreeBase+"\n",
	), 0o644))

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	stub := &stubPipelineRunner{}
	originalNewPipelineRunner := newPipelineRunner
	originalDetectMergedPRs := detectMergedPRs
	newPipelineRunner = func(*config.Config) (pipelineRunner, error) {
		return stub, nil
	}
	detectMergedPRs = func(_ context.Context, cfg config.Config, processedPath string) ([]detect.MergedPR, error) {
		assert.Equal(t, filepath.Join(root, "owner__repo", "runs", "processed.jsonl"), processedPath)
		return []detect.MergedPR{{Number: 201}}, nil
	}
	t.Cleanup(func() {
		newPipelineRunner = originalNewPipelineRunner
		detectMergedPRs = originalDetectMergedPRs
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"run", "--detect-loop"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, []int{201}, stub.prs)
}

func TestRunDetectLoopDrainsResumeQueueBeforeFreshDetection(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "owner__repo", "runs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "config.yaml"), []byte(
		"repo:\n"+
			"  github: owner/repo\n"+
			"  root: "+root+"\n"+
			"  default_branch: develop\n"+
			"  best_branch: auto-improve/best\n"+
			"paths:\n"+
			"  runs: "+runsBase+"\n"+
			"worktree:\n"+
			"  base: "+worktreeBase+"\n",
	), 0o644))
	runID := contracts.RunID("2026-04-21-PR301-abcdef0")
	ctx := mustNewRunCtx(t, runID, filepath.Join(root, "owner__repo", "runs"), filepath.Join(root, "owner__repo", "worktrees"))
	require.NoError(t, state.NewWriter(ctx).Append(contracts.StateEntry{
		Kind: contracts.StateKindInterrupted,
		Value: contracts.StateEntryInterrupted{
			Kind:   contracts.StateKindInterrupted,
			PR:     301,
			RunID:  runID,
			Step:   contracts.FailedStep20,
			Reason: contracts.InterruptedReasonUnknown,
			At:     time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}))

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	stub := &stubPipelineRunner{
		onRun: func(pr int, opts orchestrator.RunOptions) error {
			if opts.RunID == "" {
				return nil
			}
			require.Equal(t, 301, pr)
			require.Equal(t, runID, opts.RunID)
			return state.NewWriter(ctx).Append(contracts.StateEntry{
				Kind: contracts.StateKindPromoted,
				Value: contracts.StateEntryPromoted{
					Kind:  contracts.StateKindPromoted,
					PR:    301,
					RunID: runID,
					Step:  contracts.FailedStep70,
					At:    time.Date(2026, 4, 21, 12, 5, 0, 0, time.UTC),
				},
			})
		},
	}
	originalNewPipelineRunner := newPipelineRunner
	originalDetectMergedPRs := detectMergedPRs
	newPipelineRunner = func(*config.Config) (pipelineRunner, error) {
		return stub, nil
	}
	detectMergedPRs = func(_ context.Context, _ config.Config, _ string) ([]detect.MergedPR, error) {
		assert.Equal(t, []int{301}, stub.prs)
		require.Len(t, stub.opts, 1)
		assert.Equal(t, runID, stub.opts[0].RunID)
		return []detect.MergedPR{{Number: 302}}, nil
	}
	t.Cleanup(func() {
		newPipelineRunner = originalNewPipelineRunner
		detectMergedPRs = originalDetectMergedPRs
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"run", "--detect-loop"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, []int{301, 302}, stub.prs)
	require.Len(t, stub.opts, 2)
	assert.Equal(t, runID, stub.opts[0].RunID)
	assert.Empty(t, stub.opts[1].RunID)
}

func TestRunDetectLoopDoesNotFreshReenqueueResumedPRInSameTick(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	writeTestConfig(t, root, runsBase, worktreeBase)

	runID := contracts.RunID("2026-04-21-PR307-abcdef0")
	ctx := mustNewRunCtx(t, runID, runsBase, worktreeBase)
	require.NoError(t, state.NewWriter(ctx).Append(contracts.StateEntry{
		Kind: contracts.StateKindInterrupted,
		Value: contracts.StateEntryInterrupted{
			Kind:   contracts.StateKindInterrupted,
			PR:     307,
			RunID:  runID,
			Step:   contracts.FailedStep20,
			Reason: contracts.InterruptedReasonUnknown,
			At:     time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}))

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	stub := &stubPipelineRunner{
		onRun: func(pr int, opts orchestrator.RunOptions) error {
			if opts.RunID == "" {
				return nil
			}
			require.Equal(t, 307, pr)
			require.Equal(t, runID, opts.RunID)
			return state.NewWriter(ctx).Append(contracts.StateEntry{
				Kind: contracts.StateKindPromoted,
				Value: contracts.StateEntryPromoted{
					Kind:  contracts.StateKindPromoted,
					PR:    307,
					RunID: runID,
					Step:  contracts.FailedStep70,
					At:    time.Date(2026, 4, 21, 12, 5, 0, 0, time.UTC),
				},
			})
		},
	}
	originalNewPipelineRunner := newPipelineRunner
	originalDetectMergedPRs := detectMergedPRs
	newPipelineRunner = func(*config.Config) (pipelineRunner, error) {
		return stub, nil
	}
	detectMergedPRs = func(_ context.Context, _ config.Config, _ string) ([]detect.MergedPR, error) {
		return []detect.MergedPR{{Number: 307}, {Number: 308}}, nil
	}
	t.Cleanup(func() {
		newPipelineRunner = originalNewPipelineRunner
		detectMergedPRs = originalDetectMergedPRs
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"run", "--detect-loop"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, []int{307, 308}, stub.prs)
	require.Len(t, stub.opts, 2)
	assert.Equal(t, runID, stub.opts[0].RunID)
	assert.Empty(t, stub.opts[1].RunID)
}

func TestRunDetectLoopSkipsFreshDetectionWhenResumeRemainsNonTerminal(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	writeTestConfig(t, root, runsBase, worktreeBase)

	runID := contracts.RunID("2026-04-21-PR309-abcdef0")
	ctx := mustNewRunCtx(t, runID, runsBase, worktreeBase)
	require.NoError(t, state.NewWriter(ctx).Append(contracts.StateEntry{
		Kind: contracts.StateKindInterrupted,
		Value: contracts.StateEntryInterrupted{
			Kind:   contracts.StateKindInterrupted,
			PR:     309,
			RunID:  runID,
			Step:   contracts.FailedStep20,
			Reason: contracts.InterruptedReasonUnknown,
			At:     time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}))

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	stub := &stubPipelineRunner{
		onRun: func(pr int, opts orchestrator.RunOptions) error {
			require.Equal(t, 309, pr)
			require.Equal(t, runID, opts.RunID)
			return state.NewWriter(ctx).Append(contracts.StateEntry{
				Kind: contracts.StateKindInterrupted,
				Value: contracts.StateEntryInterrupted{
					Kind:   contracts.StateKindInterrupted,
					PR:     309,
					RunID:  runID,
					Step:   contracts.FailedStep30,
					Reason: contracts.InterruptedReasonUnknown,
					At:     time.Date(2026, 4, 21, 12, 5, 0, 0, time.UTC),
				},
			})
		},
	}
	originalNewPipelineRunner := newPipelineRunner
	originalDetectMergedPRs := detectMergedPRs
	newPipelineRunner = func(*config.Config) (pipelineRunner, error) {
		return stub, nil
	}
	detectMergedPRs = func(context.Context, config.Config, string) ([]detect.MergedPR, error) {
		require.Fail(t, "detect should not run while a resumed run remains non-terminal")
		return nil, nil
	}
	t.Cleanup(func() {
		newPipelineRunner = originalNewPipelineRunner
		detectMergedPRs = originalDetectMergedPRs
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"run", "--detect-loop"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, []int{309}, stub.prs)
	require.Len(t, stub.opts, 1)
	assert.Equal(t, runID, stub.opts[0].RunID)
}

func TestRunDetectLoopStopsBeforeLaterResumeTargetWhenCurrentRemainsNonTerminal(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	writeTestConfig(t, root, runsBase, worktreeBase)

	firstRunID := contracts.RunID("2026-04-21-PR310-abcdef0")
	firstCtx := mustNewRunCtx(t, firstRunID, runsBase, worktreeBase)
	require.NoError(t, state.NewWriter(firstCtx).Append(contracts.StateEntry{
		Kind: contracts.StateKindInterrupted,
		Value: contracts.StateEntryInterrupted{
			Kind:   contracts.StateKindInterrupted,
			PR:     310,
			RunID:  firstRunID,
			Step:   contracts.FailedStep20,
			Reason: contracts.InterruptedReasonUnknown,
			At:     time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}))
	secondRunID := contracts.RunID("2026-04-21-PR311-abcdef0")
	secondCtx := mustNewRunCtx(t, secondRunID, runsBase, worktreeBase)
	require.NoError(t, state.NewWriter(secondCtx).Append(contracts.StateEntry{
		Kind: contracts.StateKindInterrupted,
		Value: contracts.StateEntryInterrupted{
			Kind:   contracts.StateKindInterrupted,
			PR:     311,
			RunID:  secondRunID,
			Step:   contracts.FailedStep20,
			Reason: contracts.InterruptedReasonUnknown,
			At:     time.Date(2026, 4, 21, 12, 1, 0, 0, time.UTC),
		},
	}))

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	stub := &stubPipelineRunner{
		onRun: func(pr int, opts orchestrator.RunOptions) error {
			require.Equal(t, 310, pr)
			require.Equal(t, firstRunID, opts.RunID)
			return state.NewWriter(firstCtx).Append(contracts.StateEntry{
				Kind: contracts.StateKindInterrupted,
				Value: contracts.StateEntryInterrupted{
					Kind:   contracts.StateKindInterrupted,
					PR:     310,
					RunID:  firstRunID,
					Step:   contracts.FailedStep30,
					Reason: contracts.InterruptedReasonUnknown,
					At:     time.Date(2026, 4, 21, 12, 5, 0, 0, time.UTC),
				},
			})
		},
	}
	originalNewPipelineRunner := newPipelineRunner
	originalDetectMergedPRs := detectMergedPRs
	newPipelineRunner = func(*config.Config) (pipelineRunner, error) {
		return stub, nil
	}
	detectMergedPRs = func(context.Context, config.Config, string) ([]detect.MergedPR, error) {
		require.Fail(t, "detect should not run while the first resumed target remains non-terminal")
		return nil, nil
	}
	t.Cleanup(func() {
		newPipelineRunner = originalNewPipelineRunner
		detectMergedPRs = originalDetectMergedPRs
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"run", "--detect-loop"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, []int{310}, stub.prs)
	require.Len(t, stub.opts, 1)
	assert.Equal(t, firstRunID, stub.opts[0].RunID)
}

func TestRunDetectLoopStopsWhenResumeCreatesNeedsRecoverySentinel(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "owner__repo", "runs"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "owner__repo", "runs", "needs-recovery"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "config.yaml"), []byte(
		"repo:\n"+
			"  github: owner/repo\n"+
			"  root: "+root+"\n"+
			"  default_branch: develop\n"+
			"  best_branch: auto-improve/best\n"+
			"paths:\n"+
			"  runs: "+runsBase+"\n"+
			"worktree:\n"+
			"  base: "+worktreeBase+"\n",
	), 0o644))
	runID := contracts.RunID("2026-04-21-PR303-abcdef0")
	namespacedRunsBase := filepath.Join(root, "owner__repo", "runs")
	ctx := mustNewRunCtx(t, runID, namespacedRunsBase, filepath.Join(root, "owner__repo", "worktrees"))
	require.NoError(t, state.NewWriter(ctx).Append(contracts.StateEntry{
		Kind: contracts.StateKindInterrupted,
		Value: contracts.StateEntryInterrupted{
			Kind:   contracts.StateKindInterrupted,
			PR:     303,
			RunID:  runID,
			Step:   contracts.FailedStep20,
			Reason: contracts.InterruptedReasonUnknown,
			At:     time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}))

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	stub := &stubPipelineRunner{
		onRun: func(pr int, opts orchestrator.RunOptions) error {
			require.Equal(t, 303, pr)
			require.Equal(t, runID, opts.RunID)
			return os.WriteFile(
				filepath.Join(namespacedRunsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)),
				[]byte(`{"run_id":"2026-04-21-PR303-abcdef0","pr":303,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`),
				0o644,
			)
		},
	}
	originalNewPipelineRunner := newPipelineRunner
	originalDetectMergedPRs := detectMergedPRs
	newPipelineRunner = func(*config.Config) (pipelineRunner, error) {
		return stub, nil
	}
	detectMergedPRs = func(context.Context, config.Config, string) ([]detect.MergedPR, error) {
		require.Fail(t, "detect should not run after a resumed run creates a recovery sentinel")
		return nil, nil
	}
	t.Cleanup(func() {
		newPipelineRunner = originalNewPipelineRunner
		detectMergedPRs = originalDetectMergedPRs
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"run", "--detect-loop"})
	err = cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "global needs_manual_recovery block")
	assert.Equal(t, []int{303}, stub.prs)
}

func TestRunDetectLoopStopsWhenFreshRunCreatesNeedsRecoverySentinel(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(filepath.Join(runsBase, "needs-recovery"), 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	writeTestConfig(t, root, runsBase, worktreeBase)

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	runID := contracts.RunID("2026-04-21-PR304-abcdef0")
	stub := &stubPipelineRunner{
		onRun: func(pr int, opts orchestrator.RunOptions) error {
			require.Equal(t, 304, pr)
			require.Empty(t, opts.RunID)
			return os.WriteFile(
				filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)),
				[]byte(`{"run_id":"2026-04-21-PR304-abcdef0","pr":304,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`),
				0o644,
			)
		},
	}
	originalNewPipelineRunner := newPipelineRunner
	originalDetectMergedPRs := detectMergedPRs
	newPipelineRunner = func(*config.Config) (pipelineRunner, error) {
		return stub, nil
	}
	detectMergedPRs = func(context.Context, config.Config, string) ([]detect.MergedPR, error) {
		return []detect.MergedPR{{Number: 304}, {Number: 305}}, nil
	}
	t.Cleanup(func() {
		newPipelineRunner = originalNewPipelineRunner
		detectMergedPRs = originalDetectMergedPRs
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"run", "--detect-loop"})
	err = cmd.Execute()
	require.Error(t, err)
	assertCommandExitCode(t, err, 10)
	assert.Contains(t, err.Error(), "global needs_manual_recovery block")
	assert.Equal(t, []int{304}, stub.prs)
}

func TestRunDetectLoopBlocksOnNeedsRecoveryWhenNoWork(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(filepath.Join(runsBase, "needs-recovery"), 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	runID := contracts.RunID("2026-04-21-PR52-abcdef0")
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))
	writeTestConfig(t, root, runsBase, worktreeBase)

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	stub := &stubPipelineRunner{}
	originalNewPipelineRunner := newPipelineRunner
	originalDetectMergedPRs := detectMergedPRs
	newPipelineRunner = func(*config.Config) (pipelineRunner, error) {
		return stub, nil
	}
	detectMergedPRs = func(context.Context, config.Config, string) ([]detect.MergedPR, error) {
		return nil, nil
	}
	t.Cleanup(func() {
		newPipelineRunner = originalNewPipelineRunner
		detectMergedPRs = originalDetectMergedPRs
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"run", "--detect-loop"})
	err = cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "global needs_manual_recovery block")
	assert.Empty(t, stub.prs)
}

func TestRunDetectLoopReconcilesStaleSunsetMarkerBeforeDetect(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "sunset-running.marker"), []byte(`{
  "recorded_start_time": "2026-04-21T09:00:00Z",
  "sunset_run_id": "stale-run",
  "transitions": []
}`), 0o644))
	writeTestConfig(t, root, runsBase, worktreeBase)

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	stub := &stubPipelineRunner{}
	originalNewPipelineRunner := newPipelineRunner
	originalDetectMergedPRs := detectMergedPRs
	newPipelineRunner = func(*config.Config) (pipelineRunner, error) {
		return stub, nil
	}
	detectMergedPRs = func(context.Context, config.Config, string) ([]detect.MergedPR, error) {
		return []detect.MergedPR{{Number: 306}}, nil
	}
	t.Cleanup(func() {
		newPipelineRunner = originalNewPipelineRunner
		detectMergedPRs = originalDetectMergedPRs
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"run", "--detect-loop"})
	require.NoError(t, cmd.Execute())

	assert.Equal(t, []int{306}, stub.prs)
	assert.NoFileExists(t, filepath.Join(runsBase, "sunset-running.marker"))
	lastSunset, err := os.ReadFile(filepath.Join(runsBase, "last-sunset-at"))
	require.NoError(t, err)
	assert.Equal(t, "2026-04-21T09:00:00Z\n", string(lastSunset))
}

func TestRunDetectLoopBlocksOnLiveSunsetMarkerBeforeDetect(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "sunset-running.marker"), []byte(`{
  "recorded_start_time": "2026-04-21T09:00:00Z",
  "sunset_run_id": "live-run",
  "transitions": []
}`), 0o644))
	lock, err := internalio.AcquireFileLock(filepath.Join(runsBase, "promotion.lock"))
	require.NoError(t, err)
	defer func() { _ = lock.Unlock() }()
	writeTestConfig(t, root, runsBase, worktreeBase)

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	runnerCreated := false
	detectCalled := false
	originalNewPipelineRunner := newPipelineRunner
	originalDetectMergedPRs := detectMergedPRs
	newPipelineRunner = func(*config.Config) (pipelineRunner, error) {
		runnerCreated = true
		return &stubPipelineRunner{}, nil
	}
	detectMergedPRs = func(context.Context, config.Config, string) ([]detect.MergedPR, error) {
		detectCalled = true
		return nil, nil
	}
	t.Cleanup(func() {
		newPipelineRunner = originalNewPipelineRunner
		detectMergedPRs = originalDetectMergedPRs
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"run", "--detect-loop"})
	err = cmd.Execute()
	require.Error(t, err)
	assertCommandExitCode(t, err, 10)
	assert.Contains(t, err.Error(), "global sunset block")
	assert.False(t, runnerCreated)
	assert.False(t, detectCalled)
	assert.FileExists(t, filepath.Join(runsBase, "sunset-running.marker"))
}

func TestRunDetectLoopBlocksOnSunsetSentinelWhenNoWork(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "sunset-running.marker.diverged"), []byte("diverged\n"), 0o644))
	writeTestConfig(t, root, runsBase, worktreeBase)

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	runnerCreated := false
	detectCalled := false
	originalNewPipelineRunner := newPipelineRunner
	originalDetectMergedPRs := detectMergedPRs
	newPipelineRunner = func(*config.Config) (pipelineRunner, error) {
		runnerCreated = true
		return &stubPipelineRunner{}, nil
	}
	detectMergedPRs = func(context.Context, config.Config, string) ([]detect.MergedPR, error) {
		detectCalled = true
		return nil, nil
	}
	t.Cleanup(func() {
		newPipelineRunner = originalNewPipelineRunner
		detectMergedPRs = originalDetectMergedPRs
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"run", "--detect-loop"})
	err = cmd.Execute()
	require.Error(t, err)
	assertCommandExitCode(t, err, 10)
	assert.Contains(t, err.Error(), "global sunset block")
	assert.False(t, runnerCreated)
	assert.False(t, detectCalled)
}

func TestSunsetCommandInvokesRunner(t *testing.T) {
	originalRunSunsetTick := runSunsetTick
	called := false
	runSunsetTick = func(context.Context, bool) error {
		called = true
		return nil
	}
	t.Cleanup(func() { runSunsetTick = originalRunSunsetTick })

	cmd := newRootCmd()
	cmd.SetArgs([]string{"sunset"})
	require.NoError(t, cmd.Execute())
	assert.True(t, called)
}

func TestSunsetCommandPassesForce(t *testing.T) {
	originalRunSunsetTick := runSunsetTick
	var gotForce bool
	runSunsetTick = func(_ context.Context, force bool) error {
		gotForce = force
		return nil
	}
	t.Cleanup(func() { runSunsetTick = originalRunSunsetTick })

	cmd := newRootCmd()
	cmd.SetArgs([]string{"sunset", "--force"})
	require.NoError(t, cmd.Execute())
	assert.True(t, gotForce)
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

func writeTestConfig(t *testing.T, root, runsBase, worktreeBase string) {
	t.Helper()
	configPath := filepath.Join(root, "config.yaml")
	content := "repo:\n" +
		"  root: " + root + "\n" +
		"  default_branch: main\n" +
		"  best_branch: auto-improve/best\n" +
		"paths:\n" +
		"  runs: " + runsBase + "\n" +
		"worktree:\n" +
		"  base: " + worktreeBase + "\n"
	require.NoError(t, os.WriteFile(configPath, []byte(content), 0o644))
}

func seedRecoverActionRun(t *testing.T) (string, string, string, contracts.RunID) {
	t.Helper()
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	runID := contracts.RunID("2026-04-21-PR52-abcdef0")
	runDir := filepath.Join(runsBase, string(runID))
	require.NoError(t, os.MkdirAll(filepath.Join(runDir, "70"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(runsBase, "needs-recovery"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "config.snapshot.yaml"), []byte(
		"repo:\n"+
			"  root: "+root+"\n"+
			"  default_branch: main\n"+
			"  best_branch: auto-improve/best\n"+
			"paths:\n"+
			"  runs: "+runsBase+"\n"+
			"worktree:\n"+
			"  base: "+worktreeBase+"\n",
	), 0o644))

	pkg := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runID,
		PR:                      52,
		Title:                   "recover",
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

	candidate := contracts.Candidate{
		CandidateID:        "c-1",
		Kind:               contracts.CandidateKindNew,
		Title:              "rule",
		ProposedBodyPath:   "40/candidates/c-1.md",
		ProposedBodySha256: strings.Repeat("1", 64),
	}
	candidates := contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          runID,
		Candidates:     []contracts.Candidate{candidate},
		CandidatesHash: contracts.CanonicalCandidatesHash([]contracts.Candidate{candidate}),
		CreatedAt:      time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
	}
	require.NoError(t, os.MkdirAll(filepath.Join(runDir, "40"), 0o755))
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "40", "candidates.json"), candidates))
	return root, runsBase, worktreeBase, runID
}

func seedRecoverIntention(runID contracts.RunID, stage contracts.IntentionStage, bestShaBefore, targetSha, candidatesHash string) contracts.IntentionRecord {
	idempotencyKey := contracts.ComputeAdoptIdempotencyKey(string(runID), targetSha, bestShaBefore, candidatesHash)
	return contracts.IntentionRecord{
		SchemaVersion:      "1",
		Stage:              stage,
		IdempotencyKey:     idempotencyKey,
		RunID:              runID,
		BestShaBefore:      bestShaBefore,
		TargetSha:          targetSha,
		CandidatesHash:     candidatesHash,
		RegistryHeadBefore: "",
		StartedAt:          time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		PlannedAdoption: &contracts.PlannedAdoption{
			IdempotencyKey: idempotencyKey,
			Entries: []contracts.PlannedAdoptionEntry{
				{
					Kind:     contracts.RegistryKindAdded,
					OpID:     contracts.ComputePlannedAdoptionEntryOpID(idempotencyKey, 0, "r-0001"),
					RuleID:   "r-0001",
					RulePath: "rules/r-0001.md",
					Sha256:   recoverRuleSHA(),
				},
			},
		},
	}
}

func appendRecoverRegistryEntry(t *testing.T, runsBase string, runID contracts.RunID, intention contracts.IntentionRecord) contracts.RegistryAppendResult {
	t.Helper()
	entry := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         "r-0001",
			RulePath:       "rules/r-0001.md",
			Sha256:         recoverRuleSHA(),
			IdempotencyKey: intention.PlannedAdoption.Entries[0].OpID,
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        runID,
			At:             time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}
	result, err := internalio.AppendRegistryEntry(filepath.Join(runsBase, "rules-registry.jsonl"), entry)
	require.NoError(t, err)
	return result
}

func seedRecoverPublishedRule(t *testing.T, runsBase string) {
	t.Helper()
	require.NoError(t, internalio.WriteAtomic(filepath.Join(runsBase, "rules", "r-0001.md"), []byte(recoverRuleBody())))
}

func recoverRuleBody() string {
	return "recover rule body\n"
}

func recoverRuleSHA() string {
	return sha256String(recoverRuleBody())
}

func mustNewRunCtx(t *testing.T, runID contracts.RunID, runsBase, worktreeBase string) internalio.RunContext {
	t.Helper()
	ctx, err := internalio.NewRunContext(runID, runsBase, worktreeBase)
	require.NoError(t, err)
	return ctx
}
