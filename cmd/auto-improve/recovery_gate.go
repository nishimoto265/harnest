package main

import (
	"context"
	"errors"

	"github.com/nishimoto265/harnest/internal/archive"
	"github.com/nishimoto265/harnest/internal/config"
	"github.com/nishimoto265/harnest/internal/orchestrator"
)

const sunsetRunningMarker = "sunset-running.marker"

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

func checkDetectLoopRecoveryGate(ctx context.Context, cfg config.Config) error {
	runsBase, err := cfg.RunsBase()
	if err != nil {
		return commandExitError{code: 2, msg: err.Error()}
	}
	return checkDetectLoopRecoveryGateForRunsBase(ctx, runsBase)
}

func checkDetectLoopRecoveryGateForRunsBase(ctx context.Context, runsBase string) error {
	err := orchestrator.CheckGlobalRecoveryGate(runsBase)
	if err == nil {
		return nil
	}
	var sunset *orchestrator.GlobalSunsetSentinelError
	if !errors.As(err, &sunset) || sunset.Path != sunsetRunningMarker {
		if commandErr := recoveryGateExitError(err); commandErr != nil {
			return commandErr
		}
		return err
	}
	if reconcileErr := archive.ReconcileStaleSunsetMarkerWithLock(ctx, runsBase); reconcileErr != nil &&
		!errors.Is(reconcileErr, archive.ErrSunsetActive) &&
		!errors.Is(reconcileErr, archive.ErrStaleMarkerDiverged) {
		return reconcileErr
	}
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
