package step20_implement

import (
	"strings"
	"testing"
	"time"

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
