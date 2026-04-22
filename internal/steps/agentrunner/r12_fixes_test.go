package agentrunner

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnsureRescueLeaseQuiesced_EscalatesWhenKillFailsAndLeaderAlive covers H6.
// Before the fix, KillProcessGroupUntilGone's error was silently swallowed
// (`_ = opts.KillProcessGroupUntilGone(...)`). After the fix, we recheck the
// saved leader identity; if it is still alive we escalate.
func TestEnsureRescueLeaseQuiesced_EscalatesWhenKillFailsAndLeaderAlive(t *testing.T) {
	state := RescueLeaseState{PID: 1234, PGID: 1234, LeaderStartTime: "saved-start"}
	killErr := errors.New("EPERM")

	err := EnsureRescueLeaseQuiesced(context.Background(), t.TempDir(), state, RescueLeaseQuiesceOptions{
		KillProcessGroupUntilGone: func(int, time.Duration, time.Duration) error {
			return killErr
		},
		WorktreeProcessIDs: func(context.Context, string) ([]int, error) {
			return nil, nil
		},
		KillPID:                func(int, syscall.Signal) error { return nil },
		Sleep:                  func(time.Duration) {},
		Now:                    func() time.Time { return time.Unix(0, 0) },
		PIDAlive:               func(int) bool { return true },
		LookupProcessStartTime: func(int) (string, error) { return "saved-start", nil },
	})
	require.Error(t, err, "kill failure with leader still alive must surface")
	require.True(t, errors.Is(err, ErrRescueLeaseQuiesceKillFailed),
		"expected ErrRescueLeaseQuiesceKillFailed, got: %v", err)
}

// TestEnsureRescueLeaseQuiesced_ContinuesWhenKillFailsButLeaderGone is the
// complement: the kill returned an error but the saved leader has since
// disappeared (process exited on its own). We must proceed to enumeration.
func TestEnsureRescueLeaseQuiesced_ContinuesWhenKillFailsButLeaderGone(t *testing.T) {
	state := RescueLeaseState{PID: 1234, PGID: 1234, LeaderStartTime: "saved-start"}
	callCount := 0
	err := EnsureRescueLeaseQuiesced(context.Background(), t.TempDir(), state, RescueLeaseQuiesceOptions{
		KillProcessGroupUntilGone: func(int, time.Duration, time.Duration) error {
			return errors.New("transient-eperm")
		},
		WorktreeProcessIDs: func(context.Context, string) ([]int, error) {
			callCount++
			return nil, nil
		},
		KillPID:  func(int, syscall.Signal) error { return nil },
		Sleep:    func(time.Duration) {},
		Now:      func() time.Time { return time.Unix(0, 0) },
		PIDAlive: func(int) bool { return false }, // leader is gone
		LookupProcessStartTime: func(int) (string, error) {
			return "", syscall.ESRCH
		},
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, callCount, 1, "must reach enumeration phase")
}

// TestWorktreeProcessIDs_DistinguishesNoMatchesFromRealFailure covers H7.
// lsof -t +D exits 1 when nothing matches (stderr empty) and also exits 1 on
// real failures like "permission denied" (stderr populated). Before the fix
// we treated every exit=1 as success-with-no-pids; after the fix we inspect
// stderr and return ErrRescueLeaseQuiesceEnumerate on real failures.
func TestWorktreeProcessIDs_DistinguishesNoMatchesFromRealFailure(t *testing.T) {
	// Real failure: lsof exits 1 with a stderr diagnostic.
	fakeCmd := fakeExitOneCommand(t, "lsof: permission denied on worktree\n", "")
	_, err := WorktreeProcessIDs(context.Background(), t.TempDir(), WorktreeProcessIDsOptions{
		LookPath:       func(string) (string, error) { return "/usr/bin/lsof", nil },
		CommandContext: func(context.Context, string, ...string) *exec.Cmd { return fakeCmd },
	})
	require.Error(t, err, "lsof exit=1 with stderr must surface as enumerate error")
	require.True(t, errors.Is(err, ErrRescueLeaseQuiesceEnumerate),
		"expected ErrRescueLeaseQuiesceEnumerate, got: %v", err)

	// Empty stdout AND empty stderr: the "no matches" case — no error.
	fakeCmdClean := fakeExitOneCommand(t, "", "")
	pids, err := WorktreeProcessIDs(context.Background(), t.TempDir(), WorktreeProcessIDsOptions{
		LookPath:       func(string) (string, error) { return "/usr/bin/lsof", nil },
		CommandContext: func(context.Context, string, ...string) *exec.Cmd { return fakeCmdClean },
	})
	require.NoError(t, err, "lsof exit=1 with empty output must be 'no pids'")
	assert.Empty(t, pids)
}

// TestKillProcessGroupUntilGone_ReturnsCleanupTimeoutWhenSurvivors covers M4.
// Before the fix, the timeout path returned nil (or only lastErr), letting
// the caller believe the group was cleaned up even though survivors remained.
func TestKillProcessGroupUntilGone_ReturnsCleanupTimeoutWhenSurvivors(t *testing.T) {
	// Spawn a real long-lived child in its own process group so we can
	// exercise the timeout without depending on test-environment processes.
	cmd := exec.Command("/bin/sh", "-c", "trap '' TERM; sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})

	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	require.NoError(t, err)

	// Use a tiny deadline so the loop runs out before the child dies.
	killErr := KillProcessGroupUntilGone(pgid, 10*time.Millisecond, 1*time.Millisecond)
	// The SIGTERM was trapped; the child is still alive. We need the error
	// to carry ErrCleanupTimeout.
	require.Error(t, killErr)
	require.ErrorIs(t, killErr, ErrCleanupTimeout,
		"expected ErrCleanupTimeout wrap, got: %v", killErr)
}

// fakeExitOneCommand builds an *exec.Cmd wrapping /bin/sh that prints the
// requested strings to stdout/stderr and exits 1. Using /bin/sh keeps this
// test platform-agnostic for darwin/linux CI.
func fakeExitOneCommand(t *testing.T, stderr, stdout string) *exec.Cmd {
	t.Helper()
	script := fmt.Sprintf("printf %%s %q; printf %%s %q 1>&2; exit 1", stdout, stderr)
	return exec.Command("/bin/sh", "-c", script)
}
