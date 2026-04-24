package agentrunner

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
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

func TestEnsureRescueLeaseQuiesced_PropagatesSavedProcessGroupKillError(t *testing.T) {
	want := errors.New("kill group failed")
	err := EnsureRescueLeaseQuiesced(context.Background(), t.TempDir(), RescueLeaseState{
		PID:             4242,
		PGID:            4242,
		LeaderStartTime: "Tue Apr 22 10:00:00 2026",
	}, RescueLeaseQuiesceOptions{
		KillLeasedProcessGroup: func(context.Context, RescueLeaseState, RescueLeaseQuiesceOptions) error { return want },
		WorktreeProcessIDs:     func(context.Context, string) ([]int, error) { return nil, nil },
		KillPID:                func(int, syscall.Signal) error { return nil },
		Sleep:                  func(time.Duration) {},
		Now:                    func() time.Time { return time.Unix(0, 0) },
		PIDAlive:               func(int) bool { return true },
		LookupProcessStartTime: func(int) (string, error) { return "Tue Apr 22 10:00:00 2026", nil },
	})
	require.ErrorIs(t, err, want)
}

func TestEnsureRescueLeaseQuiesced_SkipsPGIDKillWhenOwnerStartTimeDiffers(t *testing.T) {
	// Simulates PGID reuse: saved state has start time "saved-start" but the
	// live PID now reports "recycled-start". No SIGKILL must be sent to the
	// saved PGID — otherwise we would kill the unrelated process group that
	// happens to own the recycled PGID.
	var killedTargets []int
	err := EnsureRescueLeaseQuiesced(context.Background(), t.TempDir(), RescueLeaseState{
		PID:             4242,
		PGID:            4242,
		LeaderStartTime: "saved-start",
	}, RescueLeaseQuiesceOptions{
		KillLeasedProcessGroup: func(ctx context.Context, state RescueLeaseState, opts RescueLeaseQuiesceOptions) error {
			return defaultKillLeasedProcessGroup(ctx, state, opts)
		},
		WorktreeProcessIDs: func(context.Context, string) ([]int, error) { return nil, nil },
		KillPID: func(pid int, _ syscall.Signal) error {
			killedTargets = append(killedTargets, pid)
			return nil
		},
		Sleep:                  func(time.Duration) {},
		Now:                    func() time.Time { return time.Unix(0, 0) },
		PIDAlive:               func(int) bool { return true },
		LookupProcessStartTime: func(int) (string, error) { return "recycled-start", nil },
	})
	require.NoError(t, err)
	assert.Empty(t, killedTargets, "must not SIGKILL a recycled PGID whose owner start time differs")
}

func TestEnsureRescueLeaseQuiesced_RevalidatesIdentityBeforeEachKill(t *testing.T) {
	// Owner is initially alive with saved-start, but exits (start time flips to
	// recycled-start) after the first SIGKILL. The helper must NOT send a
	// second SIGKILL once identity stops matching.
	var mu sync.Mutex
	lookups := 0
	killCalls := 0
	lookup := func(int) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		lookups++
		if lookups <= 2 {
			return "saved-start", nil
		}
		return "recycled-start", nil
	}
	var pgMembers = [][]int{{4242, 4243}, {4242}, {}}
	origMembers := processGroupMembersUntilGoneList
	t.Cleanup(func() { processGroupMembersUntilGoneList = origMembers })
	processGroupMembersUntilGoneList = func(int) ([]int, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(pgMembers) == 0 {
			return nil, nil
		}
		out := pgMembers[0]
		pgMembers = pgMembers[1:]
		return out, nil
	}

	err := EnsureRescueLeaseQuiesced(context.Background(), t.TempDir(), RescueLeaseState{
		PID:             4242,
		PGID:            4242,
		LeaderStartTime: "saved-start",
	}, RescueLeaseQuiesceOptions{
		KillLeasedProcessGroup: defaultKillLeasedProcessGroup,
		WorktreeProcessIDs:     func(context.Context, string) ([]int, error) { return nil, nil },
		KillPID: func(pid int, _ syscall.Signal) error {
			mu.Lock()
			defer mu.Unlock()
			killCalls++
			return nil
		},
		Sleep:                  func(time.Duration) {},
		Now:                    func() time.Time { return time.Unix(0, 0) },
		PIDAlive:               func(int) bool { return true },
		LookupProcessStartTime: lookup,
	})
	require.NoError(t, err)
	mu.Lock()
	defer mu.Unlock()
	assert.LessOrEqual(t, killCalls, 1, "must stop killing once lease identity no longer matches")
}

func TestEnsureRescueLeaseQuiesced_MapsCleanupTimeoutToQuiesceTimeout(t *testing.T) {
	err := EnsureRescueLeaseQuiesced(context.Background(), t.TempDir(), RescueLeaseState{
		PID:             4242,
		PGID:            4242,
		LeaderStartTime: "saved-start",
	}, RescueLeaseQuiesceOptions{
		KillLeasedProcessGroup: func(context.Context, RescueLeaseState, RescueLeaseQuiesceOptions) error {
			return fmt.Errorf("%w: pgid=4242 survivors=[4242]", ErrCleanupTimeout)
		},
		WorktreeProcessIDs:     func(context.Context, string) ([]int, error) { return nil, nil },
		KillPID:                func(int, syscall.Signal) error { return nil },
		Sleep:                  func(time.Duration) {},
		Now:                    func() time.Time { return time.Unix(0, 0) },
		PIDAlive:               func(int) bool { return true },
		LookupProcessStartTime: func(int) (string, error) { return "saved-start", nil },
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRescueLeaseQuiesceTimedOut, "ErrCleanupTimeout from saved-group kill must map to ErrRescueLeaseQuiesceTimedOut")
}

func TestWorktreeProcessIDs_PromotesLsofWarningsToEnumerationError(t *testing.T) {
	_, err := WorktreeProcessIDs(context.Background(), t.TempDir(), WorktreeProcessIDsOptions{
		LookPath: func(string) (string, error) { return "lsof", nil },
		CommandContext: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "sh", "-c", "printf \"lsof: WARNING: can't stat (permission denied)\\n\" >&2; exit 1")
		},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRescueLeaseQuiesceEnumerate)
	assert.Contains(t, err.Error(), "permission denied")
}
