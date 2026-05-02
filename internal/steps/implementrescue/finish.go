package implementrescue

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
)

func FinishState(agentDir string, state State, nextRetry int, heartbeatPath func(string) string, saveState func(string, State) error) (int, error) {
	state.RetryCount = nextRetry
	state.StartedAt = time.Time{}
	state.LastHeartbeat = time.Time{}
	state.Pid = 0
	state.Pgid = 0
	state.LeaderStartTime = ""
	if err := os.Remove(heartbeatPath(agentDir)); err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	if err := saveState(agentDir, state); err != nil {
		return 0, err
	}
	return nextRetry, nil
}

func MapCaptureError(stepName string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, agentrunner.ErrRescueDiffOverLimit) || errors.Is(err, agentrunner.ErrRescueStorageOverLimit) {
		return errors.Join(
			&agentrunner.ManualRecoveryRequiredError{
				Reason: contracts.RollbackReasonLeaseFailure,
				Detail: fmt.Sprintf("%s: rescue capture exceeded storage limits: %v", stepName, err),
			},
			err,
		)
	}
	return err
}

func ToExhaustedResult(agent contracts.AgentID, retryCount int) stepio.RescueExhausted {
	return stepio.RescueExhausted{
		Agent:      agent,
		RetryCount: retryCount,
	}
}

func recordArtifact(budget *agentrunner.RescueArtifactBudget, path, logicalPath string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return budget.RecordFile(logicalPath, info.Size())
}
