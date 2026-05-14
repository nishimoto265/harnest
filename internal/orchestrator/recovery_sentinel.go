package orchestrator

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/nishimoto265/harnest/internal/contracts/stepio"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/state"
)

func (o *Orchestrator) appendInterrupted(pr int, runID contracts.RunID, step contracts.FailedStep, reason contracts.InterruptedReason, detail string) error {
	value := contracts.StateEntryInterrupted{
		Kind:   contracts.StateKindInterrupted,
		PR:     pr,
		RunID:  runID,
		Step:   step,
		Reason: reason,
		Detail: detail,
		At:     time.Now().UTC(),
	}
	return o.appendState(contracts.StateEntry{Kind: value.Kind, Value: value})
}

func (o *Orchestrator) handleRescueExhausted(run *StepRunContext, step contracts.FailedStep, exhausted []stepio.RescueExhausted) error {
	now := time.Now().UTC()
	manual := contracts.StateEntryNeedsManualRecovery{
		Kind:       contracts.StateKindNeedsManualRecovery,
		PR:         run.PR,
		RunID:      run.IO.RunID,
		Step:       step,
		Reason:     contracts.RollbackReasonWorktreeRescueLoop,
		FailedStep: step,
		At:         now,
	}
	if err := ensureNeedsRecoverySentinel(run.IO, run.PR, run.IO.RunID, manual.Reason, manual.FailedStep); err != nil {
		return err
	}
	if err := o.appendState(contracts.StateEntry{Kind: manual.Kind, Value: manual}); err != nil {
		return err
	}
	for _, item := range exhausted {
		pr := run.PR
		runID := run.IO.RunID
		failedStep := step
		detail := fmt.Sprintf("agent=%s retry_count=%d", item.Agent, item.RetryCount)
		warning := contracts.StateEntryWarning{
			Kind:   contracts.StateKindWarningRescueRetry,
			PR:     &pr,
			RunID:  &runID,
			Step:   &failedStep,
			Detail: detail,
			At:     now,
		}
		if err := o.appendState(contracts.StateEntry{Kind: warning.Kind, Value: warning}); err != nil {
			return err
		}
	}
	return nil
}

func (o *Orchestrator) handleManualRecovery(
	run *StepRunContext,
	step contracts.FailedStep,
	reason contracts.RollbackReason,
	agent contracts.AgentID,
	detail string,
) error {
	entries, err := state.ScanEventsForRun(run.IO, run.IO.RunID)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Kind == contracts.StateKindNeedsManualRecovery {
			return ensureNeedsRecoverySentinelFromState(run.IO, &entry)
		}
	}
	if detail != "" {
		run.Logger.Warn("orchestrator: implementation rescue requires manual recovery", slog.String("agent", string(agent)), slog.String("detail", detail))
	}
	if step != contracts.FailedStep70 && reason != contracts.RollbackReasonWorktreeRescueLoop {
		reason = contracts.RollbackReasonWorktreeRescueLoop
	}
	value := contracts.StateEntryNeedsManualRecovery{
		Kind:       contracts.StateKindNeedsManualRecovery,
		PR:         run.PR,
		RunID:      run.IO.RunID,
		Step:       step,
		Reason:     reason,
		FailedStep: step,
		At:         time.Now().UTC(),
	}
	if err := ensureNeedsRecoverySentinel(run.IO, run.PR, run.IO.RunID, value.Reason, value.FailedStep); err != nil {
		return err
	}
	if err := o.appendState(contracts.StateEntry{Kind: value.Kind, Value: value}); err != nil {
		return err
	}
	return nil
}

func (o *Orchestrator) ensureNoGlobalSentinel(runCtx internalio.RunContext) error {
	return CheckGlobalRecoveryGate(runCtx.RunsBase)
}

func (o *Orchestrator) ensureStep70NeedsManualRecoveryState(run *StepRunContext) error {
	reason := contracts.RollbackReasonTransactionalFailure
	failedStep := contracts.FailedStep70
	if run.Intention != nil {
		if run.Intention.RecoveryReason != "" {
			reason = run.Intention.RecoveryReason
		}
		if run.Intention.FailedStep != "" {
			failedStep = run.Intention.FailedStep
		}
	}
	if err := ensureNeedsRecoverySentinel(run.IO, run.PR, run.IO.RunID, reason, failedStep); err != nil {
		return err
	}
	entries, err := state.ScanEventsForRun(run.IO, run.IO.RunID)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Kind == contracts.StateKindNeedsManualRecovery {
			return nil
		}
	}
	value := contracts.StateEntryNeedsManualRecovery{
		Kind:       contracts.StateKindNeedsManualRecovery,
		PR:         run.PR,
		RunID:      run.IO.RunID,
		Step:       contracts.FailedStep70,
		Reason:     reason,
		FailedStep: failedStep,
		At:         time.Now().UTC(),
	}
	return o.appendState(contracts.StateEntry{Kind: value.Kind, Value: value})
}

func ensureNeedsRecoverySentinelFromState(runCtx internalio.RunContext, entry *contracts.StateEntry) error {
	if entry == nil {
		return nil
	}
	suppress, err := shouldSuppressNeedsRecoveryReconstruction(runCtx.RunsBase, runCtx.RunID)
	if err != nil {
		return err
	}
	if suppress {
		return nil
	}
	if _, exists, err := existingNeedsRecoverySentinelPath(runCtx.RunsBase, runCtx.RunID); err != nil {
		return err
	} else if exists {
		return nil
	}
	switch value := entry.Value.(type) {
	case contracts.StateEntryNeedsManualRecovery:
		return ensureNeedsRecoverySentinel(runCtx, value.PR, value.RunID, value.Reason, value.FailedStep)
	case *contracts.StateEntryNeedsManualRecovery:
		if value == nil {
			return nil
		}
		return ensureNeedsRecoverySentinel(runCtx, value.PR, value.RunID, value.Reason, value.FailedStep)
	default:
		return nil
	}
}

func ensureNeedsRecoverySentinel(runCtx internalio.RunContext, pr int, runID contracts.RunID, reason contracts.RollbackReason, failedStep contracts.FailedStep) error {
	path := filepath.Join(runCtx.RunsBase, "needs-recovery", string(runID)+".json")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	sentinel := contracts.NeedsRecoverySentinel{
		RunID:      runID,
		PR:         pr,
		Reason:     reason,
		FailedStep: failedStep,
		CreatedAt:  time.Now().UTC(),
	}
	if err := sentinel.Validate(); err != nil {
		return err
	}
	return internalio.WriteJSONAtomic(path, sentinel)
}

func firstNeedsRecoverySentinel(runsBase string) (contracts.NeedsRecoverySentinel, bool, error) {
	processedPath := filepath.Join(runsBase, "processed.jsonl")
	manualRuns, err := state.NeedsManualRecoveryRunsPath(processedPath)
	if err != nil {
		return contracts.NeedsRecoverySentinel{}, false, err
	}
	for _, run := range manualRuns {
		sentinel, ok, err := ensureNeedsRecoverySentinelFromLatestRun(runsBase, run)
		if err != nil {
			return contracts.NeedsRecoverySentinel{}, false, err
		}
		if ok {
			return sentinel, true, nil
		}
	}

	dir := filepath.Join(runsBase, "needs-recovery")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return contracts.NeedsRecoverySentinel{}, false, nil
		}
		return contracts.NeedsRecoverySentinel{}, false, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if contracts.IsNeedsRecoverySentinelFilename(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		sentinel, err := internalio.ReadJSON[contracts.NeedsRecoverySentinel](filepath.Join(dir, name))
		if err != nil {
			return contracts.NeedsRecoverySentinel{RunID: sentinelRunIDFromFilename(name)}, true, nil
		}
		return sentinel, true, nil
	}
	return contracts.NeedsRecoverySentinel{}, false, nil
}

func ensureNeedsRecoverySentinelFromLatestRun(runsBase string, latest state.LatestRun) (contracts.NeedsRecoverySentinel, bool, error) {
	if latest.LastEvent == nil || latest.Action != state.NextActionNeedsManualRecovery {
		return contracts.NeedsRecoverySentinel{}, false, nil
	}
	var sentinel contracts.NeedsRecoverySentinel
	switch value := latest.LastEvent.Value.(type) {
	case contracts.StateEntryNeedsManualRecovery:
		sentinel = contracts.NeedsRecoverySentinel{
			RunID:      value.RunID,
			PR:         value.PR,
			Reason:     value.Reason,
			FailedStep: value.FailedStep,
			CreatedAt:  value.At,
		}
	case *contracts.StateEntryNeedsManualRecovery:
		if value == nil {
			return contracts.NeedsRecoverySentinel{}, false, nil
		}
		sentinel = contracts.NeedsRecoverySentinel{
			RunID:      value.RunID,
			PR:         value.PR,
			Reason:     value.Reason,
			FailedStep: value.FailedStep,
			CreatedAt:  value.At,
		}
	default:
		return contracts.NeedsRecoverySentinel{}, false, nil
	}
	suppress, err := shouldSuppressNeedsRecoveryReconstruction(runsBase, sentinel.RunID)
	if err != nil {
		return contracts.NeedsRecoverySentinel{}, false, err
	}
	if suppress {
		return contracts.NeedsRecoverySentinel{}, false, nil
	}
	if err := sentinel.Validate(); err != nil {
		return contracts.NeedsRecoverySentinel{}, false, err
	}
	if _, exists, err := existingNeedsRecoverySentinelPath(runsBase, sentinel.RunID); err != nil {
		return contracts.NeedsRecoverySentinel{}, false, err
	} else if !exists {
		path := needsRecoverySentinelPath(runsBase, sentinel.RunID)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return contracts.NeedsRecoverySentinel{}, false, err
		}
		if err := internalio.WriteJSONAtomic(path, sentinel); err != nil {
			return contracts.NeedsRecoverySentinel{}, false, err
		}
	}
	return sentinel, true, nil
}

func firstSunsetSentinel(runsBase string) (string, bool, error) {
	for _, name := range []string{"sunset-running.marker.diverged", "sunset-running.marker"} {
		path := filepath.Join(runsBase, name)
		if _, err := os.Stat(path); err == nil {
			return name, true, nil
		} else if err != nil && !os.IsNotExist(err) {
			return "", false, err
		}
	}
	return "", false, nil
}

func hasTerminalEvent(runCtx internalio.RunContext, runID contracts.RunID) (bool, error) {
	events, err := state.ScanEventsForRun(runCtx, runID)
	if err != nil {
		return false, err
	}
	for _, entry := range events {
		if entry.Kind.IsTerminal() {
			return true, nil
		}
	}
	return false, nil
}

func sentinelRunIDFromFilename(name string) contracts.RunID {
	return contracts.SentinelRunIDFromFilename(name)
}

func needsRecoverySentinelPath(runsBase string, runID contracts.RunID) string {
	return filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID))
}

func needsRecoverySentinelAbortedPath(runsBase string, runID contracts.RunID) string {
	return filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelAbortedFilename(runID))
}

func needsRecoverySentinelClearedPath(runsBase string, runID contracts.RunID) string {
	return filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelClearedFilename(runID))
}

func existingNeedsRecoverySentinelPath(runsBase string, runID contracts.RunID) (string, bool, error) {
	for _, path := range []string{
		needsRecoverySentinelPath(runsBase, runID),
		needsRecoverySentinelAbortedPath(runsBase, runID),
	} {
		if _, err := os.Stat(path); err == nil {
			return path, true, nil
		} else if err != nil && !os.IsNotExist(err) {
			return "", false, err
		}
	}
	return "", false, nil
}

func shouldSuppressNeedsRecoveryReconstruction(runsBase string, runID contracts.RunID) (bool, error) {
	_, exists, err := existingNeedsRecoveryClearedMarker(runsBase, runID)
	return exists, err
}

func existingNeedsRecoveryClearedMarker(runsBase string, runID contracts.RunID) (string, bool, error) {
	path := needsRecoverySentinelClearedPath(runsBase, runID)
	if _, err := os.Stat(path); err == nil {
		return path, true, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", false, err
	}
	return "", false, nil
}
