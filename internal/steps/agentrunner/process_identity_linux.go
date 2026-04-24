//go:build linux

package agentrunner

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func platformProcessIdentity(pid int) (string, error) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		if os.IsNotExist(err) {
			return "", syscall.ESRCH
		}
		return "", err
	}
	startTicks, err := linuxProcStatStartTicks(string(stat))
	if err != nil {
		return "", err
	}
	bootIDBytes, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		if os.IsNotExist(err) {
			return "", errors.New("linux boot_id unavailable")
		}
		return "", err
	}
	bootID := strings.TrimSpace(string(bootIDBytes))
	if bootID == "" {
		return "", errors.New("linux boot_id empty")
	}
	return "linux-proc:v1:boot_id=" + bootID + ":start_ticks=" + startTicks, nil
}

func linuxProcStatStartTicks(stat string) (string, error) {
	endComm := strings.LastIndex(stat, ")")
	if endComm < 0 {
		return "", malformedProcessIdentity(0, "missing comm terminator")
	}
	fields := strings.Fields(stat[endComm+1:])
	if len(fields) <= 19 {
		return "", malformedProcessIdentity(0, "missing starttime field")
	}
	startTicks := fields[19]
	if _, err := strconv.ParseUint(startTicks, 10, 64); err != nil {
		return "", malformedProcessIdentity(0, "invalid starttime field")
	}
	return startTicks, nil
}
