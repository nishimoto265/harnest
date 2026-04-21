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
const needsRecoveryDir = "needs-recovery"

// SentinelExists reports whether any `.json` or `.aborted.json` sentinel is
// present under <runs_base>/needs-recovery/. A single sentinel anywhere blocks
// every step70/sunset run.
func SentinelExists(runsBase string) (bool, error) {
	dir := filepath.Join(runsBase, needsRecoveryDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".aborted.json") {
			return true, nil
		}
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
