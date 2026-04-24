package main

import (
	"errors"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/orchestrator"
)

func checkCLIRecoveryGate(cfg config.Config) error {
	runsBase, err := cfg.RunsBase()
	if err != nil {
		return commandExitError{code: 2, msg: err.Error()}
	}
	return checkCLIRecoveryGateForRunsBase(runsBase)
}

func checkCLIRecoveryGateForRunsBase(runsBase string) error {
	if err := orchestrator.CheckGlobalRecoveryGate(runsBase); err != nil {
		if commandErr := recoveryGateExitError(err); commandErr != nil {
			return commandErr
		}
		return err
	}
	return nil
}

func recoveryGateExitError(err error) error {
	var needsRecovery *orchestrator.GlobalNeedsRecoveryError
	var sunset *orchestrator.GlobalSunsetSentinelError
	if errors.As(err, &needsRecovery) || errors.As(err, &sunset) {
		return commandExitError{code: 10, msg: err.Error()}
	}
	return nil
}
