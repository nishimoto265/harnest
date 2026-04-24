package agentrunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunCommand_TimeoutGroupKillArmedBeforeCaptureBurstAndOnStart(t *testing.T) {
	killCalled := make(chan struct{})
	var killOnce sync.Once

	result, err := RunCommand(context.Background(), CommandRequest{
		Binary:      "sh",
		Args:        []string{"-c", "sleep 10"},
		Workdir:     t.TempDir(),
		SessionPath: filepath.Join(t.TempDir(), "session.log"),
		Timeout:     50 * time.Millisecond,
		StartDescendantTracker: func(pid int, interval time.Duration) *DescendantTracker {
			return StartDescendantTracker(pid, interval)
		},
		KillProcessGroup: func(pgid int) error {
			if pgid <= 0 {
				return fmt.Errorf("invalid pgid %d", pgid)
			}
			killOnce.Do(func() {
				close(killCalled)
			})
			return KillProcessGroup(pgid)
		},
		OnStart: func(ProcessLease, time.Time) error {
			select {
			case <-killCalled:
				return nil
			default:
				return errors.New("timeout group kill was not armed before OnStart")
			}
		},
	})

	require.NoError(t, err)
	require.True(t, result.TimedOut)
}

func TestRunCommand_StartsDescendantTrackerBeforeLeaseResolution(t *testing.T) {
	trackerStarted := make(chan struct{})
	var trackerOnce sync.Once

	result, err := RunCommand(context.Background(), CommandRequest{
		Binary:      "sh",
		Args:        []string{"-c", "sleep 0.05"},
		Workdir:     t.TempDir(),
		SessionPath: filepath.Join(t.TempDir(), "session.log"),
		Timeout:     time.Second,
		StartDescendantTracker: func(int, time.Duration) *DescendantTracker {
			trackerOnce.Do(func() {
				close(trackerStarted)
			})
			return nil
		},
		ResolveProcessLease: func(pid int) (ProcessLease, error) {
			select {
			case <-trackerStarted:
			default:
				return ProcessLease{}, errors.New("lease resolved before descendant tracker started")
			}
			return ResolveProcessLease(pid)
		},
	})

	require.NoError(t, err)
	require.False(t, result.TimedOut)
}

func TestRunCommand_OnStartFailureStillRunsCleanup(t *testing.T) {
	onStartErr := errors.New("on start failed")
	cleanupCalled := make(chan struct{})
	var cleanupOnce sync.Once

	_, err := RunCommand(context.Background(), CommandRequest{
		Binary:      "sh",
		Args:        []string{"-c", "sleep 10"},
		Workdir:     t.TempDir(),
		SessionPath: filepath.Join(t.TempDir(), "session.log"),
		Timeout:     time.Second,
		StartDescendantTracker: func(int, time.Duration) *DescendantTracker {
			return nil
		},
		CleanupProcessTree: func(lease ProcessLease, sessionID int, tracker *DescendantTracker) error {
			cleanupOnce.Do(func() {
				close(cleanupCalled)
			})
			return KillProcessGroup(lease.PGID)
		},
		OnStart: func(ProcessLease, time.Time) error {
			return onStartErr
		},
	})

	require.ErrorIs(t, err, onStartErr)
	select {
	case <-cleanupCalled:
	default:
		t.Fatal("cleanup was not called after OnStart failure")
	}
}

func TestRunCommand_IgnoresParentPathShadow(t *testing.T) {
	shadowDir := t.TempDir()
	markerPath := filepath.Join(shadowDir, "shadow-ran")
	shadowPath := filepath.Join(shadowDir, "sh")
	require.NoError(t, os.WriteFile(shadowPath, []byte(fmt.Sprintf("#!/bin/sh\ntouch %q\nexit 97\n", markerPath)), 0o755))
	t.Setenv("PATH", shadowDir)

	sessionPath := filepath.Join(t.TempDir(), "session.log")
	result, err := RunCommand(context.Background(), CommandRequest{
		Binary:      "sh",
		Args:        []string{"-c", "printf trusted"},
		Workdir:     t.TempDir(),
		SessionPath: sessionPath,
		Timeout:     time.Second,
		ErrPrefix:   "test",
	})

	require.NoError(t, err)
	require.False(t, result.TimedOut)
	assertFileContains(t, sessionPath, "trusted")
	_, statErr := os.Stat(markerPath)
	require.True(t, os.IsNotExist(statErr), "parent PATH shadow binary was executed")
}

func TestRunCommand_AppliesSafeGitProfileToAgentEnv(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/tmp/malicious-gitconfig")
	t.Setenv("GIT_SSH_COMMAND", "ssh -F /tmp/malicious-ssh-config")
	t.Setenv("GIT_ASKPASS", "/tmp/malicious-askpass")

	sessionPath := filepath.Join(t.TempDir(), "session.log")
	result, err := RunCommand(context.Background(), CommandRequest{
		Binary: "sh",
		Args: []string{"-c", `printf '%s
%s
%s
%s
' "$GIT_CONFIG_GLOBAL" "$GIT_CONFIG_NOSYSTEM" "$GIT_SSH_COMMAND" "$GIT_ASKPASS"`},
		Workdir:     t.TempDir(),
		SessionPath: sessionPath,
		Timeout:     time.Second,
		ErrPrefix:   "test",
	})

	require.NoError(t, err)
	require.False(t, result.TimedOut)
	data, err := os.ReadFile(sessionPath)
	require.NoError(t, err)
	output := string(data)
	require.Contains(t, output, os.DevNull)
	require.Contains(t, output, "\n1\n")
	require.Contains(t, output, "\nssh -F "+os.DevNull+"\n")
	require.Contains(t, output, "\n/bin/false\n")
	require.NotContains(t, output, "/tmp/malicious-gitconfig")
	require.NotContains(t, output, "ssh -F /tmp/malicious-ssh-config")
	require.NotContains(t, output, "/tmp/malicious-askpass")
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), want)
}
