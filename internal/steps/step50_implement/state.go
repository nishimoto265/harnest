package step50_implement

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
)

var killProcess = syscall.Kill
var getProcessGroupID = syscall.Getpgid
var lookupLeaseStartTime = agentrunner.LookupProcessStartTime
var errHeartbeatUpdateFailed = agentrunner.ErrHeartbeatUpdateFailed
var errLegacyResumeStateMissingLeaderStartTime = agentrunner.ErrResumeStateMissingLeaderStartTime

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
	return agentrunner.HeartbeatPath(agentDir)
}

func saveResumeState(agentDir string, state resumeState) error {
	return internalio.WriteJSONAtomic(resumeStatePath(agentDir), state)
}

// ErrLegacyResumeStateLiveLease is returned when a pre-leader_start_time
// resume-state file is decoded and the recorded pid is still alive. Silently
// clearing the lease in this case would downgrade a live writer to "inactive"
// and let resumeIfNeeded fast-path past rescue, yielding a concurrent writer
// on the same worktree. Callers MUST surface this as manual recovery.
var ErrLegacyResumeStateLiveLease = errors.New("step50: legacy resume state without leader_start_time but pid is still alive; refusing to silently downgrade")

func loadResumeState(agentDir string) (resumeState, bool, error) {
	path := resumeStatePath(agentDir)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return resumeState{}, false, nil
		}
		return resumeState{}, false, err
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		return resumeState{}, false, readErr
	}
	state, err := decodeCurrentResumeState(data)
	if err == nil {
		return state, true, nil
	}
	if !errors.Is(err, errLegacyResumeStateMissingLeaderStartTime) {
		return resumeState{}, false, err
	}
	// Backward-compatible load: pre-leader_start_time resume-state files
	// stored an active lease (pid != 0) without a leader_start_time field.
	// We only migrate when the current document is otherwise structurally
	// valid and fails solely because the legacy field is absent. Any other
	// validation or decode error must fail closed so malformed current JSON
	// cannot be mistaken for legacy state.
	legacy, legacyErr := decodeLegacyResumeState(data)
	if legacyErr != nil {
		return resumeState{}, false, legacyErr
	}
	if legacy.Pid != 0 && legacy.LeaderStartTime == "" {
		if legacyLeaseLikelyLive(agentDir, legacy) {
			return resumeState{}, false, &agentrunner.ManualRecoveryRequiredError{
				Reason: contracts.RollbackReasonWorktreeRescueLoop,
				Detail: "legacy resume state still references a live lease; operator must stop the original agent before migration",
				Err:    ErrLegacyResumeStateLiveLease,
			}
		}
		legacy.StartedAt = time.Time{}
		legacy.LastHeartbeat = time.Time{}
		legacy.Pid = 0
		legacy.Pgid = 0
	}
	return legacy, true, nil
}

func legacyLeaseLikelyLive(agentDir string, legacy resumeState) bool {
	if !pidAlive(legacy.Pid) {
		return false
	}
	stale, modTime, err := heartbeatStale(agentDir, defaultStaleAfter, time.Now().UTC())
	if err != nil {
		return true
	}
	if modTime.IsZero() {
		return true
	}
	_ = stale
	return true
}

func decodeCurrentResumeState(data []byte) (resumeState, error) {
	return decodeResumeState(data, true)
}

// decodeLegacyResumeState parses a resume-state JSON document without invoking
// Validate() so older schemas can be loaded and migrated. Duplicate keys /
// unknown fields / trailing tokens are still rejected.
func decodeLegacyResumeState(data []byte) (resumeState, error) {
	return decodeResumeState(data, false)
}

func decodeResumeState(data []byte, validate bool) (resumeState, error) {
	var out resumeState
	if err := contracts.RejectDuplicateJSONKeys(data); err != nil {
		return resumeState{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return resumeState{}, err
	}
	var rest any
	if err := dec.Decode(&rest); err != io.EOF {
		return resumeState{}, contracts.ErrTrailingJSON
	}
	if validate {
		if err := out.Validate(); err != nil {
			return resumeState{}, err
		}
	}
	return out, nil
}

type heartbeatConfig struct {
	agentDir  string
	interval  time.Duration
	now       func() time.Time
	baseState resumeState
	cancel    context.CancelCauseFunc
	prefix    string
}

type heartbeatHandle = agentrunner.HeartbeatHandle

func startHeartbeat(ctx context.Context, cfg heartbeatConfig) (*heartbeatHandle, error) {
	state := cfg.baseState
	return agentrunner.StartHeartbeat(ctx, agentrunner.HeartbeatConfig{
		AgentDir: cfg.agentDir,
		Interval: cfg.interval,
		Now:      cfg.now,
		Cancel:   cfg.cancel,
		Prefix:   cfg.prefix,
		OnTick: func(now time.Time) error {
			state.LastHeartbeat = now
			var tickErr error
			if err := touchHeartbeat(cfg.agentDir, now); err != nil {
				tickErr = err
			}
			if err := saveResumeState(cfg.agentDir, state); err != nil {
				tickErr = errors.Join(tickErr, err)
			}
			return tickErr
		},
	})
}

func touchHeartbeat(agentDir string, at time.Time) error {
	return agentrunner.TouchHeartbeat(agentDir, at)
}

func heartbeatStale(agentDir string, staleAfter time.Duration, now time.Time) (bool, time.Time, error) {
	return agentrunner.HeartbeatStale(agentDir, staleAfter, now)
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
	return agentrunner.ValidateLeaseState("step50", s.ExpectedBaseSHA, s.StartedAt, s.Pid, s.Pgid, s.RetryCount, s.LeaderStartTime, s.LastHeartbeat)
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

func prepareTerminalLeaseFinalize(agentDir string) error {
	state, ok, err := loadResumeState(agentDir)
	if err != nil || !ok {
		return err
	}
	if state.Pid == 0 {
		if err := os.Remove(heartbeatPath(agentDir)); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.Remove(heartbeatPath(agentDir)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return saveResumeState(agentDir, state)
}
