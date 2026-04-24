package agentrunner

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCleanupProcessTree_KillsDetachedGrandchildSpawnedAfterRootExit(t *testing.T) {
	requireProcessInspection(t)

	helperPath := writeDetachedGrandchildHelper(t, t.TempDir())
	pidPath := filepath.Join(t.TempDir(), "detached-grandchild.pid")

	cmd := exec.Command(helperPath, pidPath, "60ms", "30ms")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: 0}
	require.NoError(t, cmd.Start())

	lease, err := ResolveProcessLease(cmd.Process.Pid)
	require.NoError(t, err)
	tracker := StartDescendantTracker(lease.PID, 25*time.Millisecond)
	if tracker != nil {
		tracker.CaptureBurst(15 * time.Millisecond)
	}

	require.NoError(t, cmd.Wait())
	if tracker != nil {
		require.Eventually(t, func() bool {
			_, err := os.Stat(pidPath)
			return err == nil
		}, 5*time.Second, 20*time.Millisecond)
		tracker.CaptureBurst(200 * time.Millisecond)
		tracker.Stop()
	}
	require.NoError(t, CleanupProcessTree(lease, 0, tracker))

	pidBytes, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	require.NoError(t, err)
	t.Cleanup(func() {
		if !processDead(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	require.Eventually(t, func() bool {
		return processDead(pid)
	}, 10*time.Second, 20*time.Millisecond)
}

func requireProcessInspection(t *testing.T) {
	t.Helper()
	startTime, err := processStartTime(os.Getpid())
	if err != nil || startTime == "" || isProcessInspectionUnavailableStartTime(startTime) {
		t.Skipf("process inspection unavailable in this sandbox: %v", err)
	}
}

func TestKillTrackedPIDs_SkipsRecycledPIDWhenStartTimeDiffers(t *testing.T) {
	originalLookup := lookupProcessStartTime
	originalKill := killPIDSignal
	t.Cleanup(func() {
		lookupProcessStartTime = originalLookup
		killPIDSignal = originalKill
	})

	var killed []int
	lookupProcessStartTime = func(pid int) (string, error) {
		if pid == 4242 {
			return "Tue Apr 22 10:00:01 2026", nil
		}
		return "", syscall.ESRCH
	}
	killPIDSignal = func(pid int, sig syscall.Signal) error {
		killed = append(killed, pid)
		return nil
	}

	err := killTrackedPIDs([]processIdentity{{pid: 4242, startTime: "Tue Apr 22 10:00:00 2026"}})
	require.NoError(t, err)
	require.Empty(t, killed)
}

func TestKillTrackedPIDs_FailsClosedForInspectionUnavailableMarker(t *testing.T) {
	originalKill := killPIDSignal
	t.Cleanup(func() {
		killPIDSignal = originalKill
	})

	var signals []syscall.Signal
	killPIDSignal = func(pid int, sig syscall.Signal) error {
		require.Equal(t, 4242, pid)
		signals = append(signals, sig)
		return nil
	}

	err := killTrackedPIDs([]processIdentity{{pid: 4242, startTime: processInspectionUnavailableStartTime(4242)}})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrCleanupInspectionUnavailable)
	require.Equal(t, []syscall.Signal{0}, signals)
}

func TestKillTrackedPIDs_FailsClosedWhenLookupInspectionUnavailable(t *testing.T) {
	originalLookup := lookupProcessStartTime
	originalKill := killPIDSignal
	t.Cleanup(func() {
		lookupProcessStartTime = originalLookup
		killPIDSignal = originalKill
	})

	lookupProcessStartTime = func(int) (string, error) {
		return "", exec.ErrNotFound
	}
	killed := false
	killPIDSignal = func(int, syscall.Signal) error {
		killed = true
		return nil
	}

	err := killTrackedPIDs([]processIdentity{{pid: 4242, startTime: "Tue Apr 22 10:00:00 2026"}})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrCleanupInspectionUnavailable)
	require.False(t, killed)
}

func TestCleanupProcessTree_SkipsRecycledGroupAndSessionButKillsTrackedDescendants(t *testing.T) {
	originalLookup := lookupProcessStartTime
	originalGroupKill := killProcessGroupUntilGoneSignal
	originalGroupMembers := processGroupMembersUntilGoneList
	originalSessionList := sessionProcessesUntilGoneList
	originalSessionKill := killSessionProcessesUntilGoneKill
	originalPIDKill := killPIDSignal
	t.Cleanup(func() {
		lookupProcessStartTime = originalLookup
		killProcessGroupUntilGoneSignal = originalGroupKill
		processGroupMembersUntilGoneList = originalGroupMembers
		sessionProcessesUntilGoneList = originalSessionList
		killSessionProcessesUntilGoneKill = originalSessionKill
		killPIDSignal = originalPIDKill
	})

	groupKillCount := 0
	sessionKillCount := 0
	var killed []int
	lookupProcessStartTime = func(pid int) (string, error) {
		switch pid {
		case 4242:
			return "Tue Apr 22 10:00:01 2026", nil
		case 777:
			return "Tue Apr 22 09:59:59 2026", nil
		default:
			return "", syscall.ESRCH
		}
	}
	killProcessGroupUntilGoneSignal = func(int) error {
		groupKillCount++
		return nil
	}
	processGroupMembersUntilGoneList = func(int) ([]int, error) {
		return []int{9001}, nil
	}
	sessionProcessesUntilGoneList = func(int) ([]int, error) {
		return []int{9002}, nil
	}
	killSessionProcessesUntilGoneKill = func([]int) error {
		sessionKillCount++
		return nil
	}
	killPIDSignal = func(pid int, sig syscall.Signal) error {
		killed = append(killed, pid)
		return nil
	}

	tracker := &DescendantTracker{
		seen: map[int]string{
			777: "Tue Apr 22 09:59:59 2026",
		},
	}
	err := CleanupProcessTree(ProcessLease{
		PID:       4242,
		PGID:      4242,
		StartTime: "Tue Apr 22 10:00:00 2026",
	}, 4242, tracker)
	require.NoError(t, err)
	require.Zero(t, groupKillCount)
	require.Zero(t, sessionKillCount)
	require.Equal(t, []int{777}, killed)
}

func TestCleanupProcessTree_CleansKnownGroupAndSessionAfterLeaderExit(t *testing.T) {
	originalLookup := lookupProcessStartTime
	originalGroupKill := killProcessGroupUntilGoneSignal
	originalGroupMembers := processGroupMembersUntilGoneList
	originalSessionList := sessionProcessesUntilGoneList
	originalSessionKill := killSessionProcessesUntilGoneKill
	t.Cleanup(func() {
		lookupProcessStartTime = originalLookup
		killProcessGroupUntilGoneSignal = originalGroupKill
		processGroupMembersUntilGoneList = originalGroupMembers
		sessionProcessesUntilGoneList = originalSessionList
		killSessionProcessesUntilGoneKill = originalSessionKill
	})

	lookupProcessStartTime = func(int) (string, error) {
		return "", syscall.ESRCH
	}
	groupMembers := []int{9001}
	sessionMembers := []int{9002}
	groupKills := 0
	sessionKills := 0
	killProcessGroupUntilGoneSignal = func(int) error {
		groupKills++
		groupMembers = nil
		return nil
	}
	processGroupMembersUntilGoneList = func(int) ([]int, error) {
		return append([]int(nil), groupMembers...), nil
	}
	sessionProcessesUntilGoneList = func(int) ([]int, error) {
		return append([]int(nil), sessionMembers...), nil
	}
	killSessionProcessesUntilGoneKill = func([]int) error {
		sessionKills++
		sessionMembers = nil
		return nil
	}

	err := CleanupProcessTree(ProcessLease{
		PID:       4242,
		PGID:      4242,
		StartTime: "Tue Apr 22 10:00:00 2026",
	}, 4242, nil)
	require.NoError(t, err)
	require.Equal(t, 1, groupKills)
	require.Equal(t, 1, sessionKills)
}

func TestCleanupProcessTree_KillsBackgroundDescendantAfterRootExit(t *testing.T) {
	requireProcessInspection(t)

	pidPath := filepath.Join(t.TempDir(), "background.pid")
	cmd := exec.Command("sh", "-c", "sleep 60 & echo $! > \"$1\"; exit 0", "sh", pidPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	require.NoError(t, cmd.Start())

	lease, err := ResolveProcessLease(cmd.Process.Pid)
	require.NoError(t, err)
	require.NoError(t, cmd.Wait())

	pidBytes, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	require.NoError(t, err)
	t.Cleanup(func() {
		if !processDead(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	err = CleanupProcessTree(lease, lease.PID, nil)
	if err != nil {
		require.ErrorIs(t, err, ErrCleanupOwnershipUnverified)
		return
	}
	require.Eventually(t, func() bool {
		return processDead(pid)
	}, 10*time.Second, 20*time.Millisecond)
}

func TestCleanupProcessTree_FailsClosedWhenLeaderExitedAndGroupOwnershipUnsafe(t *testing.T) {
	originalLookup := lookupProcessStartTime
	originalGroupKill := killProcessGroupUntilGoneSignal
	originalGroupMembers := processGroupMembersUntilGoneList
	t.Cleanup(func() {
		lookupProcessStartTime = originalLookup
		killProcessGroupUntilGoneSignal = originalGroupKill
		processGroupMembersUntilGoneList = originalGroupMembers
	})

	lookupProcessStartTime = func(int) (string, error) {
		return "", syscall.ESRCH
	}
	killProcessGroupUntilGoneSignal = func(int) error {
		t.Fatal("must not kill group with unverified ownership")
		return nil
	}
	processGroupMembersUntilGoneList = func(int) ([]int, error) {
		t.Fatal("must not inspect group with unverified ownership")
		return nil, nil
	}

	err := CleanupProcessTree(ProcessLease{
		PID:       4242,
		PGID:      7777,
		StartTime: "Tue Apr 22 10:00:00 2026",
	}, 0, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrCleanupOwnershipUnverified)
}

func TestCleanupProcessTree_FailsClosedWhenProcessInspectionUnavailableForActiveLease(t *testing.T) {
	originalLookup := lookupProcessStartTime
	originalGroupKill := killProcessGroupUntilGoneSignal
	originalGroupMembers := processGroupMembersUntilGoneList
	originalSessionList := sessionProcessesUntilGoneList
	originalSessionKill := killSessionProcessesUntilGoneKill
	originalPIDKill := killPIDSignal
	t.Cleanup(func() {
		lookupProcessStartTime = originalLookup
		killProcessGroupUntilGoneSignal = originalGroupKill
		processGroupMembersUntilGoneList = originalGroupMembers
		sessionProcessesUntilGoneList = originalSessionList
		killSessionProcessesUntilGoneKill = originalSessionKill
		killPIDSignal = originalPIDKill
	})

	lookupProcessStartTime = func(int) (string, error) {
		return "", exec.ErrNotFound
	}
	killProcessGroupUntilGoneSignal = func(int) error {
		return nil
	}
	processGroupMembersUntilGoneList = func(int) ([]int, error) {
		return nil, exec.ErrNotFound
	}
	sessionProcessesUntilGoneList = func(int) ([]int, error) {
		return nil, exec.ErrNotFound
	}
	killed := false
	killPIDSignal = func(int, syscall.Signal) error {
		killed = true
		return nil
	}

	tracker := &DescendantTracker{
		seen: map[int]string{
			777: "Tue Apr 22 09:59:59 2026",
		},
	}
	err := CleanupProcessTree(ProcessLease{
		PID:       4242,
		PGID:      4242,
		StartTime: "Tue Apr 22 10:00:00 2026",
	}, 4242, tracker)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrCleanupInspectionUnavailable)
	require.False(t, killed)
}

func TestCleanupProcessTree_JoinsGroupKillErrorWithTimeout(t *testing.T) {
	originalNow := cleanupNow
	originalSleep := cleanupSleep
	originalMembers := processGroupMembersUntilGoneList
	originalKill := killProcessGroupUntilGoneSignal
	originalLookup := lookupProcessStartTime
	t.Cleanup(func() {
		cleanupNow = originalNow
		cleanupSleep = originalSleep
		processGroupMembersUntilGoneList = originalMembers
		killProcessGroupUntilGoneSignal = originalKill
		lookupProcessStartTime = originalLookup
	})

	want := errors.New("kill failed")
	now := time.Unix(0, 0)
	cleanupNow = func() time.Time {
		current := now
		now = now.Add(time.Millisecond)
		return current
	}
	cleanupSleep = func(time.Duration) {}
	processGroupMembersUntilGoneList = func(int) ([]int, error) {
		return []int{333}, nil
	}
	killProcessGroupUntilGoneSignal = func(int) error { return want }
	lookupProcessStartTime = func(int) (string, error) { return "saved-start", nil }

	err := CleanupProcessTree(ProcessLease{
		PID:       99,
		PGID:      99,
		StartTime: "saved-start",
	}, 0, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrCleanupTimeout)
	require.ErrorIs(t, err, want)
}

func writeDetachedGrandchildHelper(t *testing.T, dir string) string {
	t.Helper()
	sourcePath := filepath.Join(dir, "detached_grandchild_helper.go")
	binaryPath := filepath.Join(dir, "detached-grandchild-helper")
	source := `package main

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 4 {
		os.Exit(2)
	}
	pidPath := os.Args[1]
	grandchildDelay, err := time.ParseDuration(os.Args[2])
	if err != nil {
		os.Exit(2)
	}
	rootExitDelay, err := time.ParseDuration(os.Args[3])
	if err != nil {
		os.Exit(2)
	}

	switch os.Getenv("DETACH_STAGE") {
	case "grandchild":
		if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
			os.Exit(1)
		}
		time.Sleep(60 * time.Second)
		return
	case "intermediate":
		time.Sleep(grandchildDelay)
		cmd := exec.Command(os.Args[0], os.Args[1], os.Args[2], os.Args[3])
		cmd.Env = append(os.Environ(), "DETACH_STAGE=grandchild")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			os.Exit(1)
		}
		time.Sleep(250 * time.Millisecond)
		return
	default:
		cmd := exec.Command(os.Args[0], os.Args[1], os.Args[2], os.Args[3])
		cmd.Env = append(os.Environ(), "DETACH_STAGE=intermediate")
		if err := cmd.Start(); err != nil {
			os.Exit(1)
		}
		time.Sleep(rootExitDelay)
	}
}
`
	require.NoError(t, os.WriteFile(sourcePath, []byte(source), 0o644))

	cmd := exec.Command("go", "build", "-o", binaryPath, sourcePath)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	return binaryPath
}

func processDead(pid int) bool {
	if pid <= 0 {
		return true
	}
	err := syscall.Kill(pid, 0)
	return err == syscall.ESRCH
}
