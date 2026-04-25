package step70_decide

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

// promoteStagedRuleSidecars publishes the staged rule bodies to their
// canonical locations. It is idempotent per-entry: callers pass the intention
// so that each successful publish is persisted as PublishedRuleOpIDs, and a
// subsequent resume skips entries whose destination already holds the planned
// SHA even if the staged file is gone (F10).
//
// When store is non-nil, per-entry progress is saved after each successful
// publish so a crash-after-first-publish can resume without re-applying or
// escalating to needs_manual_recovery.
func promoteStagedRuleSidecars(runCtx internalio.RunContext, intention *contracts.IntentionRecord, store IntentionWriter) error {
	stagingDir, err := runCtx.ResolveRunRelative("staging")
	if err != nil {
		return err
	}
	if intention == nil || intention.PlannedAdoption == nil {
		if _, err := os.Stat(stagingDir); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		return errMissingPlannedAdoptionForStaging
	}
	if _, err := os.Stat(stagingDir); err != nil {
		if os.IsNotExist(err) {
			return verifyPlannedRuleSidecarDestinations(runCtx, intention.PlannedAdoption.Entries)
		}
		return err
	}
	published := make(map[string]struct{}, len(intention.PublishedRuleOpIDs))
	for _, opID := range intention.PublishedRuleOpIDs {
		published[opID] = struct{}{}
	}
	for _, entry := range intention.PlannedAdoption.Entries {
		if _, ok := published[entry.OpID]; ok {
			continue
		}
		stagedPath, err := stagedRuleSidecarPath(runCtx, entry.RulePath)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(runCtx.RunsBase, filepath.FromSlash(entry.RulePath))
		if err := promoteRuleSidecarFn(stagedPath, dstPath, entry.Sha256, entry.PrevSha256); err != nil {
			return err
		}
		intention.PublishedRuleOpIDs = appendIfMissing(intention.PublishedRuleOpIDs, entry.OpID)
		published[entry.OpID] = struct{}{}
		if store != nil {
			if err := store.Save(*intention); err != nil {
				return err
			}
		}
	}
	if err := verifyPlannedRuleSidecarDestinations(runCtx, intention.PlannedAdoption.Entries); err != nil {
		return err
	}
	return cleanupStagedRuleSidecars(runCtx)
}

func verifyPlannedRuleSidecarDestinations(runCtx internalio.RunContext, entries []contracts.PlannedAdoptionEntry) error {
	for _, entry := range entries {
		dstPath := filepath.Join(runCtx.RunsBase, filepath.FromSlash(entry.RulePath))
		data, err := internalio.ReadValidatedRegularFile(dstPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("%w: destination=%s", errRulePublishStagedMissing, dstPath)
			}
			return fmt.Errorf("%w: read destination=%s: %v", errRulePublishDestinationType, dstPath, err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != entry.Sha256 {
			return fmt.Errorf("%w: destination=%s", errRulePublishIntegrity, dstPath)
		}
	}
	return nil
}

func promoteRuleSidecar(stagedPath, dstPath, wantSHA, prevSHA string) error {
	data, err := internalio.ReadValidatedRegularFile(stagedPath)
	if err != nil {
		if os.IsNotExist(err) {
			// F10: per-entry idempotency for multi-rule adoptions. When the
			// staged file is missing but the destination already holds the
			// planned SHA as a regular file, a prior promote of this entry
			// landed and was removed before the crash. Re-treating that as
			// errRulePublishStagedMissing would escalate the whole batch to
			// needs_manual_recovery even though the canonical bytes are
			// correct. Fall through to an already-published success.
			if info, statErr := os.Lstat(dstPath); statErr == nil && info.Mode().IsRegular() {
				dstData, readErr := internalio.ReadValidatedRegularFile(dstPath)
				if readErr == nil {
					sum := sha256.Sum256(dstData)
					if hex.EncodeToString(sum[:]) == wantSHA {
						return nil
					}
				}
			}
			return fmt.Errorf("%w: path=%s", errRulePublishStagedMissing, stagedPath)
		}
		return fmt.Errorf("%w: read staged=%s: %v", errRulePublishIntegrity, stagedPath, err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != wantSHA {
		return fmt.Errorf("%w: path=%s", errRulePublishIntegrity, stagedPath)
	}

	if info, err := os.Lstat(dstPath); err == nil {
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("%w: path=%s", errRulePublishDestinationType, dstPath)
		case info.Mode().IsRegular():
			promoteRuleSidecarBeforeDestinationRead(dstPath)
			dstData, err := internalio.ReadValidatedRegularFile(dstPath)
			if err != nil {
				return fmt.Errorf("%w: read destination=%s: %v", errRulePublishDestinationType, dstPath, err)
			}
			sum := sha256.Sum256(dstData)
			dstSHA := hex.EncodeToString(sum[:])
			if dstSHA == wantSHA {
				if err := removePathAndSyncParent(stagedPath); err != nil && !os.IsNotExist(err) {
					return err
				}
				return nil
			}
			if prevSHA != "" && dstSHA == prevSHA {
				break
			}
			return fmt.Errorf("%w: path=%s", errRulePublishConflict, dstPath)
		default:
			return fmt.Errorf("%w: path=%s", errRulePublishDestinationType, dstPath)
		}
	} else if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if prevSHA != "" {
			return fmt.Errorf("%w: path=%s", errRulePublishConflict, dstPath)
		}
	}

	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}
	if err := internalio.WriteAtomic(dstPath, data); err != nil {
		return err
	}
	if err := removePathAndSyncParent(stagedPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func cleanupStagedRuleSidecars(runCtx internalio.RunContext) error {
	stagingDir, err := runCtx.ResolveRunRelative("staging")
	if err != nil {
		return err
	}
	return removeAllAndSyncParent(stagingDir)
}
