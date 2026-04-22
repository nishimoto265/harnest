package step50_implement

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
