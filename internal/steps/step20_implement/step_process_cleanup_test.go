package step20_implement

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorktreeProcessIDs_RequiresLsof(t *testing.T) {
	originalLookPath := rescueExecLookPath
	rescueExecLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() {
		rescueExecLookPath = originalLookPath
	})

	_, err := worktreeProcessIDs(context.Background(), t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lsof is required")
}

func TestWorktreeProcessIDs_DoesNotMatchArgvOnlyReferences(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	fx := newTestFixture(t, 5)
	fakeLsof := filepath.Join(t.TempDir(), "fake-lsof.sh")
	require.NoError(t, os.WriteFile(fakeLsof, []byte("#!/bin/sh\nexit 1\n"), 0o755))

	originalLookPath := rescueExecLookPath
	rescueExecLookPath = func(string) (string, error) { return fakeLsof, nil }
	t.Cleanup(func() {
		rescueExecLookPath = originalLookPath
	})

	cmd := exec.Command(python, "-c", "import time; time.sleep(60)", fx.worktree)
	require.NoError(t, cmd.Start())
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	pids, err := worktreeProcessIDs(context.Background(), fx.worktree)
	require.NoError(t, err)
	assert.Empty(t, pids)
	assert.True(t, pidAlive(cmd.Process.Pid))
}

func TestStepRunCancelsChildProcessGroupOnContextCancellation(t *testing.T) {
	fx := newTestFixture(t, 5)
	t.Setenv("FAKE_CLAUDE_FORK_SESSION_WRITER", "1")
	t.Setenv("FAKE_CLAUDE_SLEEP_SECONDS", "5")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- fx.step.Run(ctx, fx.run)
	}()

	require.Eventually(t, func() bool {
		sessionBytes, err := os.ReadFile(fx.sessionPath())
		if err != nil {
			return false
		}
		return bytes.Count(sessionBytes, []byte("{\"event\":\"child-process\"}\n")) >= 2
	}, time.Second, 10*time.Millisecond)

	cancel()

	err := <-errCh
	require.ErrorIs(t, err, context.Canceled)

	before, readErr := os.ReadFile(fx.sessionPath())
	require.NoError(t, readErr)

	time.Sleep(250 * time.Millisecond)

	after, readErr := os.ReadFile(fx.sessionPath())
	require.NoError(t, readErr)
	require.Equal(t, before, after)

	_, statErr := os.Stat(fx.manifestPath())
	require.Error(t, statErr)
	require.True(t, os.IsNotExist(statErr))
}

func TestStepRunSweepsGrandchildrenAfterSuccessfulExit(t *testing.T) {
	fx := newTestFixture(t, 5)
	helperPath := writeBackgroundSentinelHelper(t, t.TempDir())
	sentinelPath := filepath.Join(t.TempDir(), "background-child.txt")
	pidPath := sentinelPath + ".pid"
	t.Setenv("FAKE_CLAUDE_WRITE_FILE", filepath.Join(fx.worktree, "background.txt"))
	t.Setenv("FAKE_CLAUDE_BACKGROUND_SENTINEL_HELPER", helperPath)
	t.Setenv("FAKE_CLAUDE_BACKGROUND_SENTINEL_PATH", sentinelPath)
	t.Setenv("FAKE_CLAUDE_BACKGROUND_SENTINEL_DELAY", "200ms")

	err := fx.step.Run(context.Background(), fx.run)
	require.NoError(t, err)

	pidBytes, readErr := os.ReadFile(pidPath)
	require.NoError(t, readErr)
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	require.NoError(t, parseErr)
	require.Eventually(t, func() bool {
		return !pidAlive(pid)
	}, 2*time.Second, 20*time.Millisecond)

	manifest := fx.readManifest(t)
	_, ok := manifest.Value.(contracts.ManifestSuccess)
	require.True(t, ok)
}

func TestStepRunKillsDetachedSetsidChildAfterSuccessfulExit(t *testing.T) {
	requireProcessInspection(t)
	fx := newTestFixture(t, 5)
	helperPath := writeDetachedSleepHelper(t, t.TempDir())
	pidPath := filepath.Join(t.TempDir(), "detached-child.pid")
	t.Setenv("FAKE_CLAUDE_WRITE_FILE", filepath.Join(fx.worktree, "detached.txt"))
	t.Setenv("FAKE_CLAUDE_DETACH_HELPER", helperPath)
	t.Setenv("FAKE_CLAUDE_DETACHED_PID_PATH", pidPath)
	t.Setenv("FAKE_CLAUDE_DETACH_DELAY", "250ms")

	require.NoError(t, fx.step.Run(context.Background(), fx.run))

	pidBytes, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return !pidAlive(pid)
	}, 2*time.Second, 20*time.Millisecond)
}

func TestPidAliveTreatsEPERMAsAlive(t *testing.T) {
	originalKill := killProcess
	killProcess = func(pid int, sig syscall.Signal) error {
		return syscall.EPERM
	}
	t.Cleanup(func() {
		killProcess = originalKill
	})

	require.True(t, pidAlive(12345))
}
