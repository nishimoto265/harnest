package archive

import (
	"log/slog"
	"path/filepath"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/state"
)

func emitSizeWarnings(opts Opts) error {
	registryPath := filepath.Join(opts.RunsBase, "rules-registry.jsonl")
	count, err := registryLineCount(registryPath)
	if err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	writer, err := state.NewWriterPath(filepath.Join(opts.RunsBase, "processed.jsonl"))
	if err != nil {
		return err
	}
	source := contracts.WarningSourceSunsetTick
	cnt := int64(count)
	if count >= opts.RegistryCritAt {
		w := contracts.StateEntryWarning{
			Kind:   contracts.StateKindWarningRegistrySizeCritical,
			Source: &source,
			Count:  &cnt,
			At:     opts.Now(),
		}
		return writer.Append(contracts.StateEntry{Kind: w.Kind, Value: w})
	}
	if count >= opts.RegistryHighAt {
		w := contracts.StateEntryWarning{
			Kind:   contracts.StateKindWarningRegistrySizeHigh,
			Source: &source,
			Count:  &cnt,
			At:     opts.Now(),
		}
		return writer.Append(contracts.StateEntry{Kind: w.Kind, Value: w})
	}
	return nil
}

func syncRegistryIndex(runsBase, registryPath string, entry contracts.RuleRegistryEntry, result contracts.RegistryAppendResult) error {
	count, err := registryLineCount(registryPath)
	if err != nil {
		slog.Warn("archive: failed to inspect registry size for index sync", slog.String("error", err.Error()))
		return nil
	}
	if count < internalio.RegistryIndexSyncAt {
		return nil
	}
	indexPath := filepath.Join(runsBase, "rules-idempotency-index.jsonl")
	if err := internalio.SyncIdempotencyIndex(registryPath, indexPath, entry, result); err != nil {
		slog.Warn("archive: idempotency index sync failed; registry append remains committed", slog.String("error", err.Error()))
	}
	return nil
}
