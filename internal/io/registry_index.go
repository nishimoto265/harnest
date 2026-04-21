package io

import (
	"fmt"
	"os"

	"github.com/nishimoto265/auto-improve/internal/contracts"
)

// EnsureVerifiedIdempotencyIndex loads rules-idempotency-index.jsonl, verifies
// every entry against the registry source of truth, and rebuilds the file when
// it is missing or inconsistent.
func EnsureVerifiedIdempotencyIndex(registryPath, indexPath string) ([]contracts.RuleIdempotencyIndexEntry, bool, error) {
	registryLines, err := readRegistryLines(registryPath)
	if err != nil {
		return nil, false, err
	}

	indexEntries, err := ReadJSONL[contracts.RuleIdempotencyIndexEntry](indexPath)
	if err == nil {
		if err := verifyIdempotencyIndex(registryLines, indexEntries); err == nil {
			return indexEntries, false, nil
		}
	} else if !os.IsNotExist(err) {
		// Treat invalid or unreadable indexes the same as a missing cache: rebuild
		// from registry, which remains the single source of truth.
	}

	rebuilt, err := RebuildIdempotencyIndex(registryPath, indexPath)
	if err != nil {
		return nil, false, err
	}
	if err := verifyIdempotencyIndex(registryLines, rebuilt); err != nil {
		return nil, false, err
	}
	return rebuilt, true, nil
}

// SyncIdempotencyIndex appends the freshly committed registry row to the index.
// If the index is missing or inconsistent it is rebuilt instead, which already
// captures the just-appended registry row.
func SyncIdempotencyIndex(registryPath, indexPath string, entry contracts.RuleRegistryEntry, result contracts.RegistryAppendResult) error {
	_, rebuilt, err := EnsureVerifiedIdempotencyIndex(registryPath, indexPath)
	if err != nil {
		return err
	}
	if rebuilt {
		return nil
	}

	indexEntry, err := BuildRuleIdempotencyIndexEntry(entry, result)
	if err != nil {
		return err
	}
	return AppendIdempotencyIndexEntry(indexPath, indexEntry)
}

func verifyIdempotencyIndex(registryLines []RegistryLine, indexEntries []contracts.RuleIdempotencyIndexEntry) error {
	expected := make(map[int64]contracts.RuleIdempotencyIndexEntry, len(registryLines))
	for _, line := range registryLines {
		indexEntry, err := BuildRuleIdempotencyIndexEntry(line.Entry, contracts.RegistryAppendResult{
			Offset: line.Offset,
			Sha256: line.Sha256,
		})
		if err != nil {
			return err
		}
		expected[line.Offset] = indexEntry
	}

	if len(indexEntries) != len(expected) {
		return fmt.Errorf("io: idempotency index count mismatch: got=%d want=%d", len(indexEntries), len(expected))
	}

	seen := make(map[int64]struct{}, len(indexEntries))
	for _, entry := range indexEntries {
		expectedEntry, ok := expected[entry.RegistryOffset]
		if !ok {
			return fmt.Errorf("io: idempotency index offset missing from registry: offset=%d", entry.RegistryOffset)
		}
		if entry != expectedEntry {
			return fmt.Errorf("io: idempotency index entry mismatch at offset=%d", entry.RegistryOffset)
		}
		if _, duplicated := seen[entry.RegistryOffset]; duplicated {
			return fmt.Errorf("io: duplicate idempotency index offset=%d", entry.RegistryOffset)
		}
		seen[entry.RegistryOffset] = struct{}{}
	}

	return nil
}
