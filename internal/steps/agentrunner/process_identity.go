package agentrunner

import (
	"errors"
	"fmt"
	"syscall"
)

func processStartTime(pid int) (string, error) {
	if pid <= 0 {
		return "", syscall.ESRCH
	}
	identity, err := platformProcessIdentity(pid)
	switch {
	case errors.Is(err, syscall.ESRCH):
		return "", syscall.ESRCH
	case err != nil:
		if killErr := syscall.Kill(pid, 0); errors.Is(killErr, syscall.ESRCH) {
			return "", syscall.ESRCH
		}
		if isProcessInspectionUnavailable(err) {
			return processInspectionUnavailableStartTime(pid), nil
		}
		return "", err
	case identity == "":
		return "", syscall.ESRCH
	default:
		return identity, nil
	}
}

func malformedProcessIdentity(pid int, detail string) error {
	return fmt.Errorf("agentrunner: malformed process identity for pid=%d: %s", pid, detail)
}
