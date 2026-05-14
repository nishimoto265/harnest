package step50_implement

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/steps/agentrunner"
	"github.com/stretchr/testify/require"
)

func TestStep50GitHelpers_ReturnContextCancellation(t *testing.T) {
	wrapperDir := t.TempDir()
	wrapperPath := filepath.Join(wrapperDir, "git")
	wrapper := "#!/bin/sh\nsleep 5\nexit 1\n"
	require.NoError(t, os.WriteFile(wrapperPath, []byte(wrapper), 0o755))
	useFakeGitWrapper(t, wrapperPath)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, err := gitOutputBytesContext(ctx, t.TempDir(), "rev-list", "HEAD")
	require.ErrorIs(t, err, context.Canceled)

	ctx, cancel = context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	err = runGitCommand(ctx, t.TempDir(), "rev-list", "HEAD")
	require.ErrorIs(t, err, context.Canceled)
}

func TestStepRun_KillsDetachedSetsidChildAfterSuccessfulExit(t *testing.T) {
	requireProcessInspection(t)
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	helperPath := writeDetachedSleepHelper(t, t.TempDir())
	pidPath := filepath.Join(t.TempDir(), "detached-child.pid")
	t.Setenv("FAKE_CLAUDE_WRITE_FILE", filepath.Join(env.run.TaskPackage.Worktrees[3].Path, "detached.txt"))
	t.Setenv("FAKE_DETACH_HELPER", helperPath)
	t.Setenv("FAKE_DETACHED_PID_PATH", pidPath)
	t.Setenv("FAKE_DETACH_DELAY", "250ms")

	originalCleanup := cleanupProcessTree
	cleanupCalled := make(chan struct{}, 1)
	cleanupProcessTree = func(lease agentrunner.ProcessLease, sessionID int, tracker *agentrunner.DescendantTracker) error {
		select {
		case cleanupCalled <- struct{}{}:
		default:
		}
		return originalCleanup(lease, sessionID, tracker)
	}
	t.Cleanup(func() {
		cleanupProcessTree = originalCleanup
	})

	require.NoError(t, (Step{}).Run(context.Background(), env.run))
	select {
	case <-cleanupCalled:
	default:
		t.Fatal("expected cleanupProcessTree to run before Step.Run returned")
	}

	pidBytes, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return processDead(pid)
	}, 10*time.Second, 20*time.Millisecond)
}

func TestStepRun_KillsFastDetachedSetsidChildAfterSuccessfulExit(t *testing.T) {
	if raceBuild {
		t.Skip("timing-sensitive detached-child regression is covered in non-race mode")
	}
	requireProcessInspection(t)
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	helperPath := writeDetachedSleepHelper(t, t.TempDir())
	pidPath := filepath.Join(t.TempDir(), "fast-detached-child.pid")
	t.Setenv("FAKE_CLAUDE_WRITE_FILE", filepath.Join(env.run.TaskPackage.Worktrees[3].Path, "fast-detached.txt"))
	t.Setenv("FAKE_DETACH_HELPER", helperPath)
	t.Setenv("FAKE_DETACHED_PID_PATH", pidPath)
	t.Setenv("FAKE_DETACH_DELAY", "50ms")

	originalCleanup := cleanupProcessTree
	cleanupCalled := make(chan struct{}, 1)
	cleanupProcessTree = func(lease agentrunner.ProcessLease, sessionID int, tracker *agentrunner.DescendantTracker) error {
		select {
		case cleanupCalled <- struct{}{}:
		default:
		}
		return originalCleanup(lease, sessionID, tracker)
	}
	t.Cleanup(func() {
		cleanupProcessTree = originalCleanup
	})

	require.NoError(t, (Step{}).Run(context.Background(), env.run))
	select {
	case <-cleanupCalled:
	default:
		t.Fatal("expected cleanupProcessTree to run before Step.Run returned")
	}

	pidBytes, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return processDead(pid)
	}, 10*time.Second, 20*time.Millisecond)
}
