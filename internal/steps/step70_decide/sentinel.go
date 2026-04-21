package step70_decide

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

// needsRecoveryDir is the directory holding durable needs-recovery sentinels.
const (
	needsRecoveryDir = "needs-recovery"
	sunsetMarkerFile = "sunset-running.marker"
)

// SentinelExists reports whether any `.json` or `.aborted.json` sentinel is
// present under <runs_base>/needs-recovery/. A single sentinel anywhere blocks
// every step70/sunset run.
func SentinelExists(runsBase string) (bool, error) {
	return sentinelExistsExceptRun(runsBase, "")
}

func SentinelExistsExceptRun(runsBase string, runID contracts.RunID) (bool, error) {
	return sentinelExistsExceptRun(runsBase, runID)
}

func sentinelExistsExceptRun(runsBase string, ignoreRunID contracts.RunID) (bool, error) {
	dir := filepath.Join(runsBase, needsRecoveryDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, err
		}
	} else {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if ignoreRunID != "" {
				if name == string(ignoreRunID)+".json" || name == string(ignoreRunID)+".aborted.json" {
					continue
				}
			}
			if strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".aborted.json") {
				return true, nil
			}
		}
	}
	if _, err := os.Stat(filepath.Join(runsBase, sunsetMarkerFile)); err == nil {
		return true, nil
	} else if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	return false, nil
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
	return internalio.WriteJSONAtomic(path, sentinel)
}

// FinalizeCleanup clears the durable manual-recovery block for a run after an
// operator has explicitly reconciled branch/registry state out of band.
func FinalizeCleanup(runCtx internalio.RunContext, store IntentionWriter) error {
	if store != nil {
		if err := store.Delete(); err != nil {
			return err
		}
	}
	for _, path := range []string{
		filepath.Join(runCtx.RunsBase, needsRecoveryDir, string(runCtx.RunID)+".json"),
		filepath.Join(runCtx.RunsBase, needsRecoveryDir, string(runCtx.RunID)+".aborted.json"),
	} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
