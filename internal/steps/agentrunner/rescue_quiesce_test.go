package agentrunner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorktreeProcessIDs_RequiresLsof(t *testing.T) {
	_, err := WorktreeProcessIDs(context.Background(), t.TempDir(), WorktreeProcessIDsOptions{
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lsof is required")
}

func TestShouldKillSavedProcessGroup_SkipsRecycledPIDWhenLeaderStartTimeDiffers(t *testing.T) {
	state := RescueLeaseState{
		PID:             1234,
		PGID:            1234,
		LeaderStartTime: "saved-start",
	}
	kill := ShouldKillSavedProcessGroup(state,
		func(int) bool { return true },
		func(int) (string, error) { return "current-start", nil },
	)
	assert.False(t, kill)
}

func TestEnsureRescueLeaseQuiesced_FailsClosedOnEnumerationError(t *testing.T) {
	want := errors.New("boom")
	err := EnsureRescueLeaseQuiesced(context.Background(), t.TempDir(), RescueLeaseState{}, RescueLeaseQuiesceOptions{
		Now:                func() time.Time { return time.Unix(0, 0) },
		Sleep:              func(time.Duration) {},
		KillPID:            func(int, syscall.Signal) error { return nil },
		WorktreeProcessIDs: func(context.Context, string) ([]int, error) { return nil, want },
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRescueLeaseQuiesceEnumerate)
	var enumerateErr *RescueLeaseQuiesceEnumerateError
	require.ErrorAs(t, err, &enumerateErr)
	assert.ErrorIs(t, enumerateErr.Err, want)
}

func TestEnsureRescueLeaseQuiesced_PropagatesSavedProcessGroupKillFailure(t *testing.T) {
	want := errors.New("kill failed")
	err := EnsureRescueLeaseQuiesced(context.Background(), t.TempDir(), RescueLeaseState{
		PID:             4242,
		PGID:            4242,
		LeaderStartTime: "saved-start",
	}, RescueLeaseQuiesceOptions{
		Now:                    func() time.Time { return time.Unix(0, 0) },
		Sleep:                  func(time.Duration) {},
		PIDAlive:               func(int) bool { return true },
		LookupProcessStartTime: func(int) (string, error) { return "saved-start", nil },
		KillProcessGroupUntilGone: func(int, time.Duration, time.Duration) error {
			return want
		},
		WorktreeProcessIDs: func(context.Context, string) ([]int, error) { return nil, nil },
		KillPID:            func(int, syscall.Signal) error { return nil },
	})
	require.ErrorIs(t, err, want)
}

func TestWorktreeProcessIDs_ExitErrorWithStderrFailsClosed(t *testing.T) {
	cmdPath := filepath.Join(t.TempDir(), "fake-lsof")
	script := "#!/bin/sh\nprintf 'permission denied\\n' >&2\nexit 1\n"
	require.NoError(t, os.WriteFile(cmdPath, []byte(script), 0o755))

	_, err := WorktreeProcessIDs(context.Background(), t.TempDir(), WorktreeProcessIDsOptions{
		LookPath:       func(string) (string, error) { return cmdPath, nil },
		CommandContext: exec.CommandContext,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRescueLeaseQuiesceEnumerate)
}
