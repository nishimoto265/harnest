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

	require.Eventually(t, func() bool {
		return processDead(pid)
	}, 10*time.Second, 20*time.Millisecond)
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

func TestKillSessionProcessesUntilGone_ReturnsTimeoutWhenSurvivorsRemain(t *testing.T) {
	originalNow := cleanupNow
	originalSleep := cleanupSleep
	originalList := sessionProcessesUntilGoneList
	originalKill := killSessionProcessesUntilGoneKill
	t.Cleanup(func() {
		cleanupNow = originalNow
		cleanupSleep = originalSleep
		sessionProcessesUntilGoneList = originalList
		killSessionProcessesUntilGoneKill = originalKill
	})

	now := time.Unix(0, 0)
	cleanupNow = func() time.Time {
		current := now
		now = now.Add(time.Millisecond)
		return current
	}
	cleanupSleep = func(time.Duration) {}
	sessionProcessesUntilGoneList = func(int) ([]int, error) {
		return []int{111, 222}, nil
	}
	killSessionProcessesUntilGoneKill = func([]int) error { return nil }

	err := KillSessionProcessesUntilGone(42, time.Microsecond, 0)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrCleanupTimeout)
}

func TestKillProcessGroupUntilGone_ReturnsTimeoutWhenMembersRemain(t *testing.T) {
	originalNow := cleanupNow
	originalSleep := cleanupSleep
	originalMembers := processGroupMembersUntilGoneList
	originalKill := killProcessGroupUntilGoneSignal
	t.Cleanup(func() {
		cleanupNow = originalNow
		cleanupSleep = originalSleep
		processGroupMembersUntilGoneList = originalMembers
		killProcessGroupUntilGoneSignal = originalKill
	})

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
	killProcessGroupUntilGoneSignal = func(int) error { return nil }

	err := KillProcessGroupUntilGone(99, time.Microsecond, 0)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrCleanupTimeout)
}

func TestKillProcessGroupUntilGone_JoinsKillErrorWithTimeout(t *testing.T) {
	originalNow := cleanupNow
	originalSleep := cleanupSleep
	originalMembers := processGroupMembersUntilGoneList
	originalKill := killProcessGroupUntilGoneSignal
	t.Cleanup(func() {
		cleanupNow = originalNow
		cleanupSleep = originalSleep
		processGroupMembersUntilGoneList = originalMembers
		killProcessGroupUntilGoneSignal = originalKill
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

	err := KillProcessGroupUntilGone(99, time.Microsecond, 0)
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
