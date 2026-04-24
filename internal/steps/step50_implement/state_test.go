package step50_implement

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
)

func TestResumeStateValidate_RejectsActiveLeaseWithoutLeaderStartTime(t *testing.T) {
	state := resumeState{
		ExpectedBaseSHA: strings.Repeat("a", 40),
		StartedAt:       time.Now().UTC(),
		Pid:             1234,
		Pgid:            1234,
		RetryCount:      1,
		LastHeartbeat:   time.Now().UTC(),
	}

	err := state.Validate()
	require.ErrorContains(t, err, "leader_start_time")
}

func TestLoadResumeState_MigratesLegacyActiveLeaseWithoutLeaderStartTime(t *testing.T) {
	originalKillProcess := killProcess
	killProcess = func(int, syscall.Signal) error { return syscall.ESRCH }
	t.Cleanup(func() {
		killProcess = originalKillProcess
	})

	agentDir := t.TempDir()
	oldTime := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339Nano)
	baseSHA := strings.Repeat("a", 40)
	legacy := `{"expected_base_sha":"` + baseSHA + `","started_at":"` + oldTime + `","pid":1234,"pgid":1234,"retry_count":2,"last_heartbeat":"` + oldTime + `"}`
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, resumeStateFileName), []byte(legacy), 0o644))

	state, ok, err := loadResumeState(agentDir)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, baseSHA, state.ExpectedBaseSHA)
	assert.Zero(t, state.Pid, "legacy active lease without leader_start_time should migrate to inactive")
	assert.Zero(t, state.Pgid)
	assert.True(t, state.StartedAt.IsZero())
	assert.True(t, state.LastHeartbeat.IsZero())
	assert.Equal(t, 2, state.RetryCount, "retry_count must survive migration")
}

func TestLoadResumeState_RejectsLiveLegacyLeaseWithoutLeaderStartTime(t *testing.T) {
	originalKillProcess := killProcess
	var gotPID int
	var gotSignal syscall.Signal
	killProcess = func(pid int, sig syscall.Signal) error {
		gotPID = pid
		gotSignal = sig
		return nil
	}
	t.Cleanup(func() {
		killProcess = originalKillProcess
	})

	agentDir := t.TempDir()
	oldTime := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339Nano)
	baseSHA := strings.Repeat("a", 40)
	legacy := `{"expected_base_sha":"` + baseSHA + `","started_at":"` + oldTime + `","pid":1234,"pgid":1234,"retry_count":2,"last_heartbeat":"` + oldTime + `"}`
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, resumeStateFileName), []byte(legacy), 0o644))

	_, ok, err := loadResumeState(agentDir)
	require.ErrorIs(t, err, ErrLegacyResumeStateLiveLease)
	var manual *agentrunner.ManualRecoveryRequiredError
	require.ErrorAs(t, err, &manual)
	assert.Equal(t, contracts.RollbackReasonWorktreeRescueLoop, manual.Reason)
	assert.False(t, ok)
	assert.Equal(t, 1234, gotPID)
	assert.Equal(t, syscall.Signal(0), gotSignal)
}

func TestLoadResumeState_RejectsMalformedCurrentResumeState(t *testing.T) {
	agentDir := t.TempDir()
	baseSHA := strings.Repeat("a", 40)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	current := `{"expected_base_sha":"` + baseSHA + `","started_at":"` + now + `","pid":-1,"pgid":1234,"leader_start_time":"saved-start","retry_count":2,"last_heartbeat":"` + now + `"}`
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, resumeStateFileName), []byte(current), 0o644))

	_, _, err := loadResumeState(agentDir)
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrLegacyResumeStateLiveLease), "malformed current JSON must not fall back to legacy migration")
}

func TestStartHeartbeat_CancelsOnTickFailure(t *testing.T) {
	agentDir := t.TempDir()
	ctx, cancel := context.WithCancelCause(context.Background())
	t.Cleanup(func() { cancel(nil) })

	handle, err := startHeartbeat(ctx, heartbeatConfig{
		agentDir:  agentDir,
		interval:  10 * time.Millisecond,
		now:       time.Now,
		baseState: resumeState{ExpectedBaseSHA: strings.Repeat("a", 40), StartedAt: time.Now().UTC(), Pid: 1234, Pgid: 1234, LeaderStartTime: "saved-start", RetryCount: 1, LastHeartbeat: time.Now().UTC()},
		cancel:    cancel,
		prefix:    "step50",
	})
	require.NoError(t, err)
	t.Cleanup(handle.Stop)
	t.Cleanup(func() { _ = os.Chmod(agentDir, 0o700) })

	require.NoError(t, os.Chmod(agentDir, 0o500))
	require.Eventually(t, func() bool {
		return context.Cause(ctx) != nil
	}, time.Second, 10*time.Millisecond)
	assert.ErrorContains(t, context.Cause(ctx), "step50: heartbeat update failed")
}
