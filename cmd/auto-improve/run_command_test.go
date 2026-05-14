package main

import (
	"bytes"
	"context"
	"github.com/nishimoto265/harnest/internal/config"
	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/nishimoto265/harnest/internal/detect"
	"github.com/nishimoto265/harnest/internal/orchestrator"
	"github.com/nishimoto265/harnest/internal/preflight"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

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
