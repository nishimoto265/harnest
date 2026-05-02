package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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

func mustNewRunCtx(t *testing.T, runID contracts.RunID, runsBase, worktreeBase string) internalio.RunContext {
	t.Helper()
	ctx, err := internalio.NewRunContext(runID, runsBase, worktreeBase)
	require.NoError(t, err)
	return ctx
}
