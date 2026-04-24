package agentrunner

import (
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProcessStartTime_UsesCollisionResistantIdentity(t *testing.T) {
	startTime, err := processStartTime(os.Getpid())
	if err != nil || startTime == "" || isProcessInspectionUnavailableStartTime(startTime) {
		t.Skipf("process inspection unavailable in this sandbox: %v", err)
	}

	switch runtime.GOOS {
	case "darwin":
		require.True(t, strings.HasPrefix(startTime, "darwin-sysctl:v1:"), startTime)
	case "linux":
		require.True(t, strings.HasPrefix(startTime, "linux-proc:v1:"), startTime)
	default:
		t.Skipf("no strong process identity assertion for %s", runtime.GOOS)
	}
}

func TestInspectProcessIdentity_TreatsSameSecondStrongIdentityCollisionAsMismatch(t *testing.T) {
	originalLookup := lookupProcessStartTime
	t.Cleanup(func() { lookupProcessStartTime = originalLookup })

	lookupProcessStartTime = func(int) (string, error) {
		return "linux-proc:v1:boot_id=same-boot:start_ticks=1001", nil
	}

	status, err := inspectProcessIdentity(4242, "linux-proc:v1:boot_id=same-boot:start_ticks=1000")
	require.NoError(t, err)
	require.Equal(t, processIdentityMismatch, status)
}

func TestInspectProcessIdentity_FailsClosedWhenStrongIdentityUnavailable(t *testing.T) {
	originalKill := killPIDSignal
	t.Cleanup(func() { killPIDSignal = originalKill })

	var signals []syscall.Signal
	killPIDSignal = func(pid int, sig syscall.Signal) error {
		require.Equal(t, 4242, pid)
		signals = append(signals, sig)
		return nil
	}

	status, err := inspectProcessIdentity(4242, processInspectionUnavailableStartTime(4242))
	require.Error(t, err)
	require.ErrorIs(t, err, ErrCleanupInspectionUnavailable)
	require.Equal(t, processIdentityMismatch, status)
	require.Equal(t, []syscall.Signal{0}, signals)
}
