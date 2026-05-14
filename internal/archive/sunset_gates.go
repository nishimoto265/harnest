package archive

import (
	"os"
	"path/filepath"

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/nishimoto265/harnest/internal/state"
)

func sentinelExists(runsBase string) (bool, error) {
	if blocked, err := sunsetDivergedBlockExists(runsBase); err != nil {
		return false, err
	} else if blocked {
		return true, nil
	}
	processedPath := filepath.Join(runsBase, "processed.jsonl")
	latestRuns, err := state.NeedsManualRecoveryRunsPath(processedPath)
	if err != nil {
		return false, err
	}
	for _, latest := range latestRuns {
		if latest.RunID != "" {
			suppressed, err := needsRecoveryClearedMarkerExists(runsBase, latest.RunID)
			if err != nil {
				return false, err
			}
			if suppressed {
				continue
			}
		}
		return true, nil
	}
	dir := filepath.Join(runsBase, "needs-recovery")
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
		if contracts.IsNeedsRecoverySentinelFilename(name) {
			return true, nil
		}
	}
	return false, nil
}

func needsRecoveryClearedMarkerExists(runsBase string, runID contracts.RunID) (bool, error) {
	if runID == "" {
		return false, nil
	}
	path := filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelClearedFilename(runID))
	if _, err := os.Stat(path); err == nil {
		return true, nil
	} else if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	return false, nil
}

func sunsetDivergedBlockExists(runsBase string) (bool, error) {
	if _, err := os.Stat(filepath.Join(runsBase, divergedMarkerFile)); err == nil {
		return true, nil
	} else if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	return false, nil
}

func markStaleMarkerDiverged(runsBase string) error {
	from := filepath.Join(runsBase, markerFilename)
	to := filepath.Join(runsBase, divergedMarkerFile)
	if err := os.Rename(from, to); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return nil
}

func divergedMarkerExists(runsBase string) (bool, error) {
	if _, err := os.Stat(filepath.Join(runsBase, divergedMarkerFile)); err == nil {
		return true, nil
	} else if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	return false, nil
}

// ClearDivergedMarker removes the durable sunset divergence block after an
// operator has explicitly reconciled the stale sunset marker.
func ClearDivergedMarker(runsBase string) error {
	if err := os.Remove(filepath.Join(runsBase, divergedMarkerFile)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
