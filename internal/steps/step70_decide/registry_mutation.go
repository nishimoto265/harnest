package step70_decide

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
)

func appendRegistryEntries(ctx context.Context, runCtx internalio.RunContext, pkg *contracts.TaskPackage, intention *contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps, pr int) (contracts.RegistryAppendResult, error) {
	matches, err := findPlannedRegistryMatches(runCtx, *intention)
	if err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	if existing, ok := completePlannedRegistryMatches(matches); ok {
		if err := emitRegistrySizeWarnings(runCtx, writer, deps, pr); err != nil {
			return contracts.RegistryAppendResult{}, err
		}
		return existing, nil
	}

	currentHead, err := currentRegistryHead(runCtx.RulesRegistryPath())
	if err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	startIndex, err := resumeIndexForPlannedRegistryEntries(*intention, matches, currentHead)
	if err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	return appendPlannedRegistryEntries(ctx, runCtx, pkg, intention, store, writer, deps, pr, startIndex)
}

func appendRegistryRollbacks(runCtx internalio.RunContext, intention contracts.IntentionRecord, reason contracts.RollbackReason, at time.Time) (*contracts.RegistryAppendResult, error) {
	committed, err := committedPromotionEntries(runCtx, intention)
	if err != nil {
		return nil, err
	}
	if len(committed) == 0 {
		if len(intention.AppendedEntryOpIDs) > 0 || intention.RegistryAppendResult != nil {
			return nil, ErrRegistryDivergence
		}
		return nil, nil
	}

	var result *contracts.RegistryAppendResult
	for _, committedEntry := range committed {
		if existing, ok, err := findRollbackByTarget(runCtx, committedEntry.OpID, committedEntry.Result); err != nil {
			return nil, err
		} else if ok {
			existingCopy := existing
			result = &existingCopy
			continue
		}

		entry := contracts.RuleRegistryRolledBack{
			Kind:           contracts.RegistryKindRolledBack,
			SchemaVersion:  "1",
			TargetOpID:     committedEntry.OpID,
			TargetOffset:   committedEntry.Result.Offset,
			TargetSha256:   committedEntry.Result.Sha256,
			ByRunID:        intention.RunID,
			RollbackReason: reason,
			FailedStep:     contracts.FailedStep70,
			VersionSeq:     1,
			PrevHash:       "",
			At:             at,
		}
		wrapper, err := deriveRegistryChain(contracts.RuleRegistryEntry{Kind: entry.Kind, Value: entry}, runCtx.RulesRegistryPath())
		if err != nil {
			return nil, err
		}
		appended, err := appendRegistryEntry(runCtx.RulesRegistryPath(), wrapper)
		if err != nil {
			return nil, err
		}
		syncRegistryIndex(runCtx, wrapper, appended)
		appendedCopy := appended
		result = &appendedCopy
	}
	return result, nil
}

func appendPlannedRegistryEntries(ctx context.Context, runCtx internalio.RunContext, pkg *contracts.TaskPackage, intention *contracts.IntentionRecord, store IntentionWriter, writer state.Writer, deps Deps, pr int, startIndex int) (contracts.RegistryAppendResult, error) {
	rawEntries, err := registryEntriesFromPlannedAdoption(*intention, deps.Now())
	if err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	if startIndex < 0 || startIndex > len(rawEntries) {
		return contracts.RegistryAppendResult{}, fmt.Errorf("step70: invalid registry append start index=%d", startIndex)
	}

	var result contracts.RegistryAppendResult
	for idx := startIndex; idx < len(rawEntries); idx++ {
		if handled, err := rollbackOnOtherRunSentinel(ctx, pr, runCtx, pkg, *intention, store, writer, deps); err != nil {
			return contracts.RegistryAppendResult{}, err
		} else if handled {
			return contracts.RegistryAppendResult{}, errSentinelRollbackHandled
		}
		rawEntry := rawEntries[idx]
		entry, err := deriveRegistryChain(rawEntry, runCtx.RulesRegistryPath())
		if err != nil {
			return contracts.RegistryAppendResult{}, err
		}

		appended, err := appendRegistryEntry(runCtx.RulesRegistryPath(), entry)
		if err != nil {
			if !errors.Is(err, internalio.ErrRegistryCASMismatch) {
				return contracts.RegistryAppendResult{}, err
			}
			entry, err = deriveRegistryChain(rawEntry, runCtx.RulesRegistryPath())
			if err != nil {
				return contracts.RegistryAppendResult{}, err
			}
			appended, err = appendRegistryEntry(runCtx.RulesRegistryPath(), entry)
			if err != nil {
				return contracts.RegistryAppendResult{}, err
			}
		}

		syncRegistryIndex(runCtx, entry, appended)
		result = appended
		intention.AppendedEntryOpIDs = appendIfMissing(intention.AppendedEntryOpIDs, intention.PlannedAdoption.Entries[idx].OpID)
		if handled, err := rollbackOnOtherRunSentinel(ctx, pr, runCtx, pkg, *intention, store, writer, deps); err != nil {
			return contracts.RegistryAppendResult{}, err
		} else if handled {
			return contracts.RegistryAppendResult{}, errSentinelRollbackHandled
		}
		if err := store.Save(*intention); err != nil {
			return contracts.RegistryAppendResult{}, err
		}
	}

	if err := emitRegistrySizeWarnings(runCtx, writer, deps, pr); err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	return result, nil
}
