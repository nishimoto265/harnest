//go:build darwin

package agentrunner

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

func platformProcessIdentity(pid int) (string, error) {
	kinfo, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	if kinfo == nil || kinfo.Proc.P_pid != int32(pid) {
		return "", syscall.ESRCH
	}
	start := kinfo.Proc.P_starttime
	if start.Sec == 0 && start.Usec == 0 {
		return "", syscall.ESRCH
	}
	return fmt.Sprintf("darwin-sysctl:v1:start_sec=%d:start_usec=%d", start.Sec, start.Usec), nil
}
