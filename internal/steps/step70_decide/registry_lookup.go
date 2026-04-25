package step70_decide

import (
	"fmt"
	"log/slog"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
)

func findRegistryByIdempotencyKey(runCtx internalio.RunContext, key string) (contracts.RegistryAppendResult, bool, error) {
	if key == "" {
		return contracts.RegistryAppendResult{}, false, nil
	}
	lines, err := registryLookupLines(runCtx)
	if err != nil {
		return contracts.RegistryAppendResult{}, false, err
	}
	for i := len(lines) - 1; i >= 0; i-- {
		match, offset, sha := matchesIdempotency(lines[i], key)
		if match {
			return contracts.RegistryAppendResult{Offset: offset, Sha256: sha}, true, nil
		}
	}
	return contracts.RegistryAppendResult{}, false, nil
}

func registryPromotionAppendResultExists(runCtx internalio.RunContext, result contracts.RegistryAppendResult, runID contracts.RunID) (bool, error) {
	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	if err != nil {
		return false, err
	}
	for _, line := range lines {
		if line.Offset != result.Offset || line.Sha256 != result.Sha256 {
			continue
		}
		switch v := line.Entry.Value.(type) {
		case contracts.RuleRegistryAdded:
			return v.ByRunID == runID, nil
		case contracts.RuleRegistryUpdated:
			return v.ByRunID == runID, nil
		default:
			return false, nil
		}
	}
	return false, nil
}

func findRollbackByTarget(runCtx internalio.RunContext, targetOpID string, target contracts.RegistryAppendResult) (contracts.RegistryAppendResult, bool, error) {
	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	if err != nil {
		return contracts.RegistryAppendResult{}, false, err
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if v, ok := lines[i].Entry.Value.(contracts.RuleRegistryRolledBack); ok &&
			v.TargetOpID == targetOpID &&
			v.TargetOffset == target.Offset &&
			v.TargetSha256 == target.Sha256 {
			return contracts.RegistryAppendResult{Offset: lines[i].Offset, Sha256: lines[i].Sha256}, true, nil
		}
	}
	return contracts.RegistryAppendResult{}, false, nil
}

func matchesIdempotency(line registryLine, key string) (bool, int64, string) {
	switch v := line.Entry.Value.(type) {
	case contracts.RuleRegistryAdded:
		if v.IdempotencyKey == key {
			return true, line.Offset, line.Sha256
		}
	case contracts.RuleRegistryUpdated:
		if v.IdempotencyKey == key {
			return true, line.Offset, line.Sha256
		}
	case contracts.RuleRegistryRolledBack:
		if v.TargetOpID == key {
			return true, line.Offset, line.Sha256
		}
	case contracts.RuleRegistryStatusChanged:
		if v.OpID == key {
			return true, line.Offset, line.Sha256
		}
	case contracts.RuleRegistryArchived:
		if v.OpID == key {
			return true, line.Offset, line.Sha256
		}
	case contracts.RuleRegistryRestored:
		if v.OpID == key {
			return true, line.Offset, line.Sha256
		}
	}
	return false, 0, ""
}

func planningResumeNeedsRefresh(intention contracts.IntentionRecord, currentCandidatesHash string, fresh Target, hasTarget bool) (bool, error) {
	if !hasTarget {
		return true, nil
	}
	if currentCandidatesHash != intention.CandidatesHash {
		return true, nil
	}
	if fresh.TargetSHA != intention.TargetSha {
		return true, nil
	}
	if intention.PlannedAdoption == nil {
		return false, contracts.ErrIntentionMissingPlannedAdoption
	}
	if len(fresh.RulesToAppend) != len(intention.PlannedAdoption.Entries) {
		return true, nil
	}
	for idx, entry := range fresh.RulesToAppend {
		planned := intention.PlannedAdoption.Entries[idx]
		switch v := entry.Value.(type) {
		case contracts.RuleRegistryAdded:
			if planned.Kind != contracts.RegistryKindAdded || planned.RuleID != v.RuleID || planned.RulePath != v.RulePath || planned.Sha256 != v.Sha256 {
				return true, nil
			}
		case contracts.RuleRegistryUpdated:
			if planned.Kind != contracts.RegistryKindUpdated || planned.RuleID != v.RuleID || planned.RulePath != v.RulePath || planned.Sha256 != v.Sha256 || planned.PrevSha256 != v.PrevSha256 {
				return true, nil
			}
		default:
			return false, fmt.Errorf("step70: unsupported planned adoption registry kind=%q", entry.Kind)
		}
	}
	return false, nil
}

func registryLookupLines(runCtx internalio.RunContext) ([]registryLine, error) {
	return internalio.RegistryLookupLinesByIdempotencyIndex(runCtx.RulesRegistryPath(), runCtx.RulesIdempotencyIndexPath())
}

func syncRegistryIndex(runCtx internalio.RunContext, entry contracts.RuleRegistryEntry, result contracts.RegistryAppendResult) {
	count, err := internalio.RegistryLineCount(runCtx.RulesRegistryPath())
	if err != nil {
		slog.Warn("step70: failed to inspect registry size for index sync", slog.String("error", err.Error()))
		return
	}
	if count < internalio.RegistryIndexSyncAt {
		return
	}
	if err := internalio.SyncIdempotencyIndex(runCtx.RulesRegistryPath(), runCtx.RulesIdempotencyIndexPath(), entry, result); err != nil {
		slog.Warn("step70: idempotency index sync failed; registry append remains committed", slog.String("error", err.Error()))
	}
}

func readRegistryLines(path string) ([]registryLine, error) {
	return internalio.RegistryLines(path)
}

func currentRegistryHead(path string) (string, error) {
	lines, err := readRegistryLines(path)
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", nil
	}
	return lines[len(lines)-1].Sha256, nil
}

func emitRegistrySizeWarnings(runCtx internalio.RunContext, writer state.Writer, deps Deps, pr int) error {
	count, err := internalio.RegistryLineCount(runCtx.RulesRegistryPath())
	if err != nil {
		return err
	}
	source := contracts.WarningSourceStep70
	step := contracts.FailedStep70
	prVal := pr
	runID := runCtx.RunID
	cnt := int64(count)
	if count >= deps.RegistryCritAt {
		w := contracts.StateEntryWarning{
			Kind:   contracts.StateKindWarningRegistrySizeCritical,
			PR:     &prVal,
			RunID:  &runID,
			Source: &source,
			Step:   &step,
			Count:  &cnt,
			At:     deps.Now(),
		}
		return appendStateOnce(runCtx, writer, contracts.StateKindWarningRegistrySizeCritical, contracts.StateEntry{Kind: w.Kind, Value: w})
	}
	if count >= deps.RegistryHighAt {
		w := contracts.StateEntryWarning{
			Kind:   contracts.StateKindWarningRegistrySizeHigh,
			PR:     &prVal,
			RunID:  &runID,
			Source: &source,
			Step:   &step,
			Count:  &cnt,
			At:     deps.Now(),
		}
		return appendStateOnce(runCtx, writer, contracts.StateKindWarningRegistrySizeHigh, contracts.StateEntry{Kind: w.Kind, Value: w})
	}
	return nil
}
