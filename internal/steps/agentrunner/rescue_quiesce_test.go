package agentrunner

import (
	"context"
	"errors"
	"os/exec"
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
