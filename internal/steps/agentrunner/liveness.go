package agentrunner

import (
	"errors"
	"syscall"
)

type ProcessLease struct {
	PID  int `json:"pid" validate:"required,gt=0"`
	PGID int `json:"pgid" validate:"gte=0"`
}

func ResolveProcessLease(pid int) (ProcessLease, error) {
	if pid <= 0 {
		return ProcessLease{}, syscall.ESRCH
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return ProcessLease{}, err
	}
	return ProcessLease{
		PID:  pid,
		PGID: pgid,
	}, nil
}

func ShouldAttemptRescue(stale bool, pidAlive func(int) bool, pid int) bool {
	if !stale {
		return false
	}
	if pidAlive == nil {
		return true
	}
	return !pidAlive(pid)
}

func KillProcessGroup(pgid int) error {
	if pgid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
