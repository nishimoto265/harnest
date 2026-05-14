package agentrunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/validation"
)

var ErrHeartbeatUpdateFailed = errors.New("agentrunner: heartbeat update failed")

type HeartbeatConfig struct {
	AgentDir string
	Interval time.Duration
	Now      func() time.Time
	OnTick   func(now time.Time) error
	Cancel   context.CancelCauseFunc
	Prefix   string
}

type HeartbeatHandle struct {
	stop chan struct{}
	done chan struct{}
}

func StartHeartbeat(ctx context.Context, cfg HeartbeatConfig) (*HeartbeatHandle, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Second
	}
	if err := TouchHeartbeat(cfg.AgentDir, cfg.Now()); err != nil {
		return nil, err
	}
	handle := &HeartbeatHandle{
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go func() {
		defer close(handle.done)
		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-handle.stop:
				return
			case <-ticker.C:
				now := cfg.Now().UTC()
				if cfg.OnTick != nil {
					if err := cfg.OnTick(now); err != nil {
						if cfg.Cancel != nil {
							cfg.Cancel(fmt.Errorf("%s: heartbeat update failed: %w: %v", cfg.Prefix, ErrHeartbeatUpdateFailed, err))
						}
						return
					}
				}
			}
		}
	}()
	return handle, nil
}

func (h *HeartbeatHandle) Stop() {
	if h == nil {
		return
	}
	close(h.stop)
	<-h.done
}

func TouchHeartbeat(agentDir string, at time.Time) error {
	path := HeartbeatPath(agentDir)
	if err := internalio.WriteAtomic(path, nil); err != nil {
		return err
	}
	return os.Chtimes(path, at, at)
}

func HeartbeatPath(agentDir string) string {
	return filepath.Join(agentDir, ".heartbeat")
}

func HeartbeatStale(agentDir string, staleAfter time.Duration, now time.Time) (bool, time.Time, error) {
	info, err := os.Stat(HeartbeatPath(agentDir))
	if err != nil {
		if os.IsNotExist(err) {
			return true, time.Time{}, nil
		}
		return false, time.Time{}, err
	}
	modTime := info.ModTime()
	return now.Sub(modTime) > staleAfter, modTime, nil
}

func PidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	switch {
	case err == nil:
		return true
	case errors.Is(err, syscall.ESRCH):
		return false
	default:
		return true
	}
}

func ProcessLeaseAlive(pid, expectedPGID int, expectedStartTime string) bool {
	if !PidAlive(pid) {
		return false
	}
	if expectedStartTime == "" {
		return false
	}
	if expectedPGID <= 0 {
		actualStartTime, err := LookupProcessStartTime(pid)
		if err != nil {
			return !errors.Is(err, syscall.ESRCH)
		}
		return actualStartTime == expectedStartTime
	}
	actualPGID, err := syscall.Getpgid(pid)
	if err != nil {
		return !errors.Is(err, syscall.ESRCH)
	}
	if actualPGID != expectedPGID {
		return false
	}
	actualStartTime, err := LookupProcessStartTime(pid)
	if err != nil {
		return !errors.Is(err, syscall.ESRCH)
	}
	return actualStartTime == expectedStartTime
}

func ValidateLeaseState(prefix string, expectedBaseSHA string, startedAt time.Time, pid, pgid, retryCount int, leaderStartTime string, lastHeartbeat time.Time) error {
	if err := validation.Instance().Var(expectedBaseSHA, "required,sha1_hex"); err != nil {
		return err
	}
	if pid == 0 {
		if pgid != 0 {
			return errors.New(prefix + ": resume state: pgid requires pid")
		}
		if leaderStartTime != "" {
			return errors.New(prefix + ": resume state: inactive lease must not persist leader_start_time")
		}
		if retryCount < 0 {
			return errors.New(prefix + ": resume state: retry_count must be >= 0")
		}
		if !startedAt.IsZero() || !lastHeartbeat.IsZero() {
			return errors.New(prefix + ": resume state: inactive lease must not persist heartbeat timestamps")
		}
		return nil
	}
	if pid < 0 {
		return errors.New(prefix + ": resume state: pid must be >= 0")
	}
	if pgid < 0 {
		return errors.New(prefix + ": resume state: pgid must be >= 0")
	}
	if retryCount < 0 {
		return errors.New(prefix + ": resume state: retry_count must be >= 0")
	}
	if leaderStartTime == "" {
		return fmt.Errorf(prefix+": resume state: active lease requires leader_start_time: %w", ErrResumeStateMissingLeaderStartTime)
	}
	if startedAt.IsZero() || lastHeartbeat.IsZero() {
		return errors.New(prefix + ": resume state: active lease requires started_at and last_heartbeat")
	}
	return nil
}

var ErrResumeStateMissingLeaderStartTime = errors.New("agentrunner: resume state: active lease requires leader_start_time")
