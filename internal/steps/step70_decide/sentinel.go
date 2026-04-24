package step70_decide

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
)

// needsRecoveryDir is the directory holding durable needs-recovery sentinels.
const (
	needsRecoveryDir   = "needs-recovery"
	sunsetMarkerFile   = "sunset-running.marker"
	sunsetDivergedFile = sunsetMarkerFile + ".diverged"
)

// SentinelExists reports whether any `.json` or `.aborted.json` sentinel is
// present under <runs_base>/needs-recovery/. A single sentinel anywhere blocks
// every step70/sunset run.
func SentinelExists(runsBase string) (bool, error) {
	blocked, _, err := globalBlockReason(runsBase, "")
	return blocked, err
}

func SentinelExistsExceptRun(runsBase string, runID contracts.RunID) (bool, error) {
	blocked, _, err := globalBlockReason(runsBase, runID)
	return blocked, err
}

func sentinelExistsExceptRun(runsBase string, ignoreRunID contracts.RunID) (bool, error) {
	blocked, _, err := globalBlockReason(runsBase, ignoreRunID)
	return blocked, err
}

func globalBlockReason(runsBase string, ignoreRunID contracts.RunID) (bool, string, error) {
	dir := filepath.Join(runsBase, needsRecoveryDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, "", err
		}
	} else {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if ignoreRunID != "" {
				if name == contracts.NeedsRecoverySentinelFilename(ignoreRunID) ||
					name == contracts.NeedsRecoverySentinelAbortedFilename(ignoreRunID) {
					continue
				}
			}
			if contracts.IsNeedsRecoverySentinelFilename(name) {
				return true, filepath.Join(needsRecoveryDir, name), nil
			}
		}
	}
	if _, err := os.Stat(filepath.Join(runsBase, sunsetDivergedFile)); err == nil {
		return true, sunsetDivergedFile, nil
	} else if err != nil && !os.IsNotExist(err) {
		return false, "", err
	}
	if _, err := os.Stat(filepath.Join(runsBase, sunsetMarkerFile)); err == nil {
		return true, sunsetMarkerFile, nil
	} else if err != nil && !os.IsNotExist(err) {
		return false, "", err
	}
	return false, "", nil
}

// writeSentinel atomically writes <runs_base>/needs-recovery/<run_id>.json with
// the durable needs-recovery block contract.
func writeSentinel(runsBase string, runID contracts.RunID, pr int, reason contracts.RollbackReason, failedStep contracts.FailedStep, at time.Time) error {
	sentinel := contracts.NeedsRecoverySentinel{
		RunID:      runID,
		PR:         pr,
		Reason:     reason,
		FailedStep: failedStep,
		CreatedAt:  at,
	}
	if err := sentinel.Validate(); err != nil {
		return fmt.Errorf("step70: invalid sentinel: %w", err)
	}
	path := filepath.Join(runsBase, needsRecoveryDir, string(runID)+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(runsBase, needsRecoveryDir, contracts.NeedsRecoverySentinelClearedFilename(runID))); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := internalio.WriteJSONAtomic(path, sentinel); err != nil {
		return err
	}
	return nil
}

type clearedNeedsRecoveryMarker struct {
	RunID contracts.RunID `json:"run_id"`
	State string          `json:"state"`
}

func writeClearedMarker(runsBase string, runID contracts.RunID) error {
	path := filepath.Join(runsBase, needsRecoveryDir, contracts.NeedsRecoverySentinelClearedFilename(runID))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return internalio.WriteJSONAtomic(path, clearedNeedsRecoveryMarker{
		RunID: runID,
		State: "cleared",
	})
}

// FinalizeCleanup clears the durable manual-recovery block for a run after an
// operator has explicitly reconciled branch/registry state out of band.
func FinalizeCleanup(runCtx internalio.RunContext, store IntentionWriter) error {
	return withRecoverPromotionLock(context.Background(), runCtx, func() error {
		return finalizeCleanupUnlocked(runCtx, store)
	})
}

func finalizeCleanupUnlocked(runCtx internalio.RunContext, store IntentionWriter) error {
	if store != nil {
		if err := store.Delete(); err != nil {
			return err
		}
	}
	if err := removeNeedsRecoverySentinels(runCtx); err != nil {
		return err
	}
	if err := writeClearedMarker(runCtx.RunsBase, runCtx.RunID); err != nil {
		return err
	}
	return appendFinalizeCleanupCompleted(runCtx)
}

func removeNeedsRecoverySentinels(runCtx internalio.RunContext) error {
	for _, path := range []string{
		filepath.Join(runCtx.RunsBase, needsRecoveryDir, contracts.NeedsRecoverySentinelFilename(runCtx.RunID)),
		filepath.Join(runCtx.RunsBase, needsRecoveryDir, contracts.NeedsRecoverySentinelAbortedFilename(runCtx.RunID)),
	} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func renameNeedsRecoverySentinelToAborted(runCtx internalio.RunContext) error {
	src := filepath.Join(runCtx.RunsBase, needsRecoveryDir, contracts.NeedsRecoverySentinelFilename(runCtx.RunID))
	dst := filepath.Join(runCtx.RunsBase, needsRecoveryDir, contracts.NeedsRecoverySentinelAbortedFilename(runCtx.RunID))
	if _, err := os.Stat(dst); err == nil {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("step70: %s not found for manual abort", contracts.NeedsRecoverySentinelFilename(runCtx.RunID))
		}
		return err
	}
	return nil
}

func writeAbortedSentinel(runsBase string, runID contracts.RunID, pr int, reason contracts.RollbackReason, failedStep contracts.FailedStep, at time.Time) error {
	sentinel := contracts.NeedsRecoverySentinel{
		RunID:      runID,
		PR:         pr,
		Reason:     reason,
		FailedStep: failedStep,
		CreatedAt:  at,
	}
	path := filepath.Join(runsBase, needsRecoveryDir, contracts.NeedsRecoverySentinelAbortedFilename(runID))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(runsBase, needsRecoveryDir, contracts.NeedsRecoverySentinelClearedFilename(runID))); err != nil && !os.IsNotExist(err) {
		return err
	}
	return internalio.WriteJSONAtomic(path, sentinel)
}

func writeClearedSentinelMarker(runCtx internalio.RunContext) error {
	return writeClearedMarker(runCtx.RunsBase, runCtx.RunID)
}

func appendFinalizeCleanupCompleted(runCtx internalio.RunContext) error {
	events, err := state.ScanEventsForRun(runCtx, runCtx.RunID)
	if err != nil {
		return err
	}
	if hasManualCleanupCompleted(events) {
		return nil
	}
	pr, ok := latestRunPR(events)
	if !ok {
		pr, ok = runIDPR(runCtx.RunID)
	}
	if !ok {
		return nil
	}
	return state.NewWriter(runCtx).Append(contracts.StateEntry{
		Kind: contracts.StateKindCompleted,
		Value: contracts.StateEntryCompleted{
			Kind:   contracts.StateKindCompleted,
			PR:     pr,
			RunID:  runCtx.RunID,
			Step:   contracts.FailedStep70,
			Detail: "manual_cleanup_finalized",
			At:     time.Now().UTC(),
		},
	})
}

func hasManualCleanupCompleted(events []contracts.StateEntry) bool {
	for _, event := range events {
		completed, ok := stateEntryCompleted(event)
		if !ok {
			continue
		}
		if completed.Detail == "manual_cleanup_finalized" {
			return true
		}
	}
	return false
}

func latestRunPR(events []contracts.StateEntry) (int, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if pr, ok := stateEntryPR(events[i]); ok {
			return pr, true
		}
	}
	return 0, false
}

func runIDPR(runID contracts.RunID) (int, bool) {
	value := string(runID)
	parts := strings.Split(value, "-PR")
	if len(parts) != 2 {
		return 0, false
	}
	prPart := parts[1]
	dash := strings.IndexByte(prPart, '-')
	if dash <= 0 {
		return 0, false
	}
	pr, err := strconv.Atoi(prPart[:dash])
	if err != nil || pr <= 0 {
		return 0, false
	}
	return pr, true
}

func stateEntryPR(entry contracts.StateEntry) (int, bool) {
	switch value := entry.Value.(type) {
	case contracts.StateEntryStarted:
		return value.PR, true
	case *contracts.StateEntryStarted:
		if value == nil {
			return 0, false
		}
		return value.PR, true
	case contracts.StateEntryStepDone:
		return value.PR, true
	case *contracts.StateEntryStepDone:
		if value == nil {
			return 0, false
		}
		return value.PR, true
	case contracts.StateEntryInterrupted:
		return value.PR, true
	case *contracts.StateEntryInterrupted:
		if value == nil {
			return 0, false
		}
		return value.PR, true
	case contracts.StateEntryPromoting:
		return value.PR, true
	case *contracts.StateEntryPromoting:
		if value == nil {
			return 0, false
		}
		return value.PR, true
	case contracts.StateEntryCompleted:
		return value.PR, true
	case *contracts.StateEntryCompleted:
		if value == nil {
			return 0, false
		}
		return value.PR, true
	case contracts.StateEntryFailed:
		return value.PR, true
	case *contracts.StateEntryFailed:
		if value == nil {
			return 0, false
		}
		return value.PR, true
	case contracts.StateEntryPromoted:
		return value.PR, true
	case *contracts.StateEntryPromoted:
		if value == nil {
			return 0, false
		}
		return value.PR, true
	case contracts.StateEntryRollback:
		return value.PR, true
	case *contracts.StateEntryRollback:
		if value == nil {
			return 0, false
		}
		return value.PR, true
	case contracts.StateEntrySkipped:
		return value.PR, true
	case *contracts.StateEntrySkipped:
		if value == nil {
			return 0, false
		}
		return value.PR, true
	case contracts.StateEntryTimeout:
		return value.PR, true
	case *contracts.StateEntryTimeout:
		if value == nil {
			return 0, false
		}
		return value.PR, true
	case contracts.StateEntryNeedsManualRecovery:
		return value.PR, true
	case *contracts.StateEntryNeedsManualRecovery:
		if value == nil {
			return 0, false
		}
		return value.PR, true
	case contracts.StateEntryWarning:
		if value.PR == nil {
			return 0, false
		}
		return *value.PR, true
	case *contracts.StateEntryWarning:
		if value == nil || value.PR == nil {
			return 0, false
		}
		return *value.PR, true
	default:
		return 0, false
	}
}

func stateEntryCompleted(entry contracts.StateEntry) (contracts.StateEntryCompleted, bool) {
	switch value := entry.Value.(type) {
	case contracts.StateEntryCompleted:
		return value, true
	case *contracts.StateEntryCompleted:
		if value == nil {
			return contracts.StateEntryCompleted{}, false
		}
		return *value, true
	default:
		return contracts.StateEntryCompleted{}, false
	}
}
