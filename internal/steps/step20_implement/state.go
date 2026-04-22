package step20_implement

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
	"github.com/nishimoto265/auto-improve/internal/validation"
)

var killProcess = syscall.Kill
var getProcessGroupID = syscall.Getpgid
var lookupLeaseStartTime = agentrunner.LookupProcessStartTime

type resumeState struct {
	ExpectedBaseSHA string    `json:"expected_base_sha" validate:"required,sha1_hex"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	Pid             int       `json:"pid,omitempty" validate:"gte=0"`
	Pgid            int       `json:"pgid,omitempty" validate:"gte=0"`
	LeaderStartTime string    `json:"leader_start_time,omitempty"`
	RetryCount      int       `json:"retry_count" validate:"gte=0"`
	LastHeartbeat   time.Time `json:"last_heartbeat,omitempty"`
}

func resumeStatePath(agentDir string) string {
	return filepath.Join(agentDir, resumeStateFileName)
}

func heartbeatPath(agentDir string) string {
	return filepath.Join(agentDir, heartbeatFileName)
}

func saveResumeState(agentDir string, state resumeState) error {
	return internalio.WriteJSONAtomic(resumeStatePath(agentDir), state)
}

func loadResumeState(agentDir string) (resumeState, bool, error) {
	path := resumeStatePath(agentDir)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return resumeState{}, false, nil
		}
		return resumeState{}, false, err
	}
	state, err := internalio.ReadJSON[resumeState](path)
	if err != nil {
		return resumeState{}, false, err
	}
	return state, true, nil
}

type heartbeatConfig struct {
	agentDir  string
	interval  time.Duration
	now       func() time.Time
	baseState resumeState
}

type heartbeatHandle struct {
	stop chan struct{}
	done chan struct{}
}

func startHeartbeat(ctx context.Context, cfg heartbeatConfig) (*heartbeatHandle, error) {
	if err := touchHeartbeat(cfg.agentDir, cfg.now()); err != nil {
		return nil, err
	}
	handle := &heartbeatHandle{
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go func() {
		defer close(handle.done)
		ticker := time.NewTicker(cfg.interval)
		defer ticker.Stop()
		state := cfg.baseState
		for {
			select {
			case <-ctx.Done():
				return
			case <-handle.stop:
				return
			case <-ticker.C:
				now := cfg.now().UTC()
				state.LastHeartbeat = now
				_ = touchHeartbeat(cfg.agentDir, now)
				_ = saveResumeState(cfg.agentDir, state)
			}
		}
	}()
	return handle, nil
}

func (h *heartbeatHandle) Stop() {
	if h == nil {
		return
	}
	close(h.stop)
	<-h.done
}

func touchHeartbeat(agentDir string, at time.Time) error {
	path := heartbeatPath(agentDir)
	if err := internalio.WriteAtomic(path, nil); err != nil {
		return err
	}
	return os.Chtimes(path, at, at)
}

func heartbeatStale(agentDir string, staleAfter time.Duration, now time.Time) (bool, time.Time, error) {
	info, err := os.Stat(heartbeatPath(agentDir))
	if err != nil {
		if os.IsNotExist(err) {
			return true, time.Time{}, nil
		}
		return false, time.Time{}, err
	}
	modTime := info.ModTime()
	return now.Sub(modTime) > staleAfter, modTime, nil
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := killProcess(pid, 0)
	switch {
	case err == nil:
		return true
	case errors.Is(err, syscall.ESRCH):
		return false
	default:
		return true
	}
}

func processLeaseAlive(pid, expectedPGID int, expectedStartTime string) bool {
	if !pidAlive(pid) {
		return false
	}
	if expectedStartTime == "" {
		return false
	}
	if expectedPGID <= 0 {
		actualStartTime, err := lookupLeaseStartTime(pid)
		if err != nil {
			return !errors.Is(err, syscall.ESRCH)
		}
		return actualStartTime == expectedStartTime
	}
	actualPGID, err := getProcessGroupID(pid)
	if err != nil {
		return !errors.Is(err, syscall.ESRCH)
	}
	if actualPGID != expectedPGID {
		return false
	}
	actualStartTime, err := lookupLeaseStartTime(pid)
	if err != nil {
		return !errors.Is(err, syscall.ESRCH)
	}
	return actualStartTime == expectedStartTime
}

func shouldAttemptRescue(stale bool, pid, pgid int, leaderStartTime string) bool {
	return agentrunner.ShouldAttemptRescue(stale, func(pid int) bool {
		return processLeaseAlive(pid, pgid, leaderStartTime)
	}, pid)
}

func (s resumeState) Validate() error {
	if err := validation.Instance().Var(s.ExpectedBaseSHA, "required,sha1_hex"); err != nil {
		return err
	}
	if s.Pid == 0 {
		if s.Pgid != 0 {
			return errors.New("step20: resume state: pgid requires pid")
		}
		if s.LeaderStartTime != "" {
			return errors.New("step20: resume state: inactive lease must not persist leader_start_time")
		}
		if !s.StartedAt.IsZero() || !s.LastHeartbeat.IsZero() {
			return errors.New("step20: resume state: inactive lease must not persist heartbeat timestamps")
		}
		return nil
	}
	if s.Pid < 0 {
		return errors.New("step20: resume state: pid must be >= 0")
	}
	if s.LeaderStartTime == "" {
		return errors.New("step20: resume state: active lease requires leader_start_time")
	}
	if s.StartedAt.IsZero() || s.LastHeartbeat.IsZero() {
		return errors.New("step20: resume state: active lease requires started_at and last_heartbeat")
	}
	return nil
}

func clearActiveLease(agentDir string) error {
	state, ok, err := loadResumeState(agentDir)
	if err != nil || !ok {
		return err
	}
	state.StartedAt = time.Time{}
	state.LastHeartbeat = time.Time{}
	state.Pid = 0
	state.Pgid = 0
	state.LeaderStartTime = ""
	if err := os.Remove(heartbeatPath(agentDir)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return saveResumeState(agentDir, state)
}
