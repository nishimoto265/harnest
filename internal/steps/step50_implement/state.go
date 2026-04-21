package step50_implement

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
)

var killProcess = syscall.Kill

type resumeState struct {
	ExpectedBaseSHA string    `json:"expected_base_sha" validate:"required,sha1_hex"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	Pid             int       `json:"pid,omitempty" validate:"gte=0"`
	Pgid            int       `json:"pgid" validate:"gte=0"`
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
	return writeJSONAtomicImpl(resumeStatePath(agentDir), state)
}

func loadResumeState(agentDir string) (resumeState, bool, error) {
	path := resumeStatePath(agentDir)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return resumeState{}, false, nil
		}
		return resumeState{}, false, err
	}
	state, err := readJSON[resumeState](path)
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
	if err := writeAtomicImpl(path, nil); err != nil {
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

func shouldAttemptRescue(stale bool, pid int) bool {
	return agentrunner.ShouldAttemptRescue(stale, pidAlive, pid)
}

func (s resumeState) Validate() error {
	if s.ExpectedBaseSHA == "" {
		return errors.New("step50: resume state: expected_base_sha is required")
	}
	if s.Pid == 0 {
		if s.Pgid != 0 {
			return errors.New("step50: resume state: pgid requires pid")
		}
		if !s.StartedAt.IsZero() || !s.LastHeartbeat.IsZero() {
			return errors.New("step50: resume state: inactive lease must not persist heartbeat timestamps")
		}
		return nil
	}
	if s.Pid < 0 {
		return errors.New("step50: resume state: pid must be >= 0")
	}
	if s.StartedAt.IsZero() || s.LastHeartbeat.IsZero() {
		return errors.New("step50: resume state: active lease requires started_at and last_heartbeat")
	}
	return nil
}
