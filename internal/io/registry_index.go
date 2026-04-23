package io

import (
	"fmt"
	stdio "io"
	"os"
	"path/filepath"

	"github.com/nishimoto265/auto-improve/internal/contracts"
)

var idempotencyIndexBeforeAppendHook = func() error { return nil }
var idempotencyIndexBeforeRewriteHook = func() error { return nil }

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
	lock, err := AcquireFileLock(indexPath + ".lock")
	if err != nil {
		return err
	}
	defer func() {
		_ = lock.Unlock()
	}()

	registryLines, err := readRegistryLines(registryPath)
	if err != nil {
		return err
	}
	if err := ensureWritableParentDir(indexPath); err != nil {
		return err
	}
	indexFile, identity, err := openTrackedFileNoFollowRetry(indexPath, os.O_CREATE|os.O_RDWR|os.O_APPEND, defaultFilePerm)
	if err != nil {
		return err
	}
	defer indexFile.Close()

	existingEntries, rebuilt, err := ensureVerifiedIdempotencyIndexHandle(registryLines, indexPath, indexFile, identity)
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
	for _, existing := range existingEntries {
		if existing.RegistryOffset == indexEntry.RegistryOffset && existing.IdempotencyKey == indexEntry.IdempotencyKey {
			return nil
		}
	}
	if err := idempotencyIndexBeforeAppendHook(); err != nil {
		return err
	}
	if err := ensurePathMatchesIdentity(indexPath, identity); err != nil {
		return err
	}
	payload, err := marshalJSONLRecord(indexEntry)
	if err != nil {
		return err
	}
	if err := appendJSONLPayload(indexPath, indexFile, payload); err != nil {
		return err
	}
	return ensurePathMatchesIdentity(indexPath, identity)
}

func ensureVerifiedIdempotencyIndexHandle(
	registryLines []RegistryLine,
	indexPath string,
	indexFile *os.File,
	identity fileIdentity,
) ([]contracts.RuleIdempotencyIndexEntry, bool, error) {
	indexEntries, err := readJSONLHandle[contracts.RuleIdempotencyIndexEntry](indexFile)
	if err == nil {
		if err := verifyIdempotencyIndex(registryLines, indexEntries); err == nil {
			return indexEntries, false, nil
		}
	} else if !os.IsNotExist(err) {
		// Treat invalid or unreadable indexes the same as a missing cache.
	}

	rebuilt, err := rebuildIdempotencyIndexHandle(registryLines, indexPath, indexFile, identity)
	if err != nil {
		return nil, false, err
	}
	if err := verifyIdempotencyIndex(registryLines, rebuilt); err != nil {
		return nil, false, err
	}
	return rebuilt, true, nil
}

func rebuildIdempotencyIndexHandle(
	registryLines []RegistryLine,
	indexPath string,
	indexFile *os.File,
	identity fileIdentity,
) ([]contracts.RuleIdempotencyIndexEntry, error) {
	entries, buffer, err := buildIdempotencyIndexBuffer(registryLines)
	if err != nil {
		return nil, err
	}
	if err := idempotencyIndexBeforeRewriteHook(); err != nil {
		return nil, err
	}
	if err := writeJSONLBufferHandle(indexPath, indexFile, identity, buffer); err != nil {
		return nil, err
	}
	return entries, nil
}

func buildIdempotencyIndexBuffer(registryLines []RegistryLine) ([]contracts.RuleIdempotencyIndexEntry, []byte, error) {
	entries := make([]contracts.RuleIdempotencyIndexEntry, 0, len(registryLines))
	for _, line := range registryLines {
		index, err := BuildRuleIdempotencyIndexEntry(line.Entry, contracts.RegistryAppendResult{
			Offset: line.Offset,
			Sha256: line.Sha256,
		})
		if err != nil {
			return nil, nil, err
		}
		entries = append(entries, index)
	}
	buffer := make([]byte, 0, len(entries)*128)
	for _, entry := range entries {
		payload, err := marshalJSONLRecord(entry)
		if err != nil {
			return nil, nil, err
		}
		buffer = append(buffer, payload...)
		buffer = append(buffer, '\n')
	}
	return entries, buffer, nil
}

func writeJSONLBufferHandle(path string, file *os.File, identity fileIdentity, buffer []byte) error {
	if err := ensurePathMatchesIdentity(path, identity); err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, stdio.SeekStart); err != nil {
		return err
	}
	if _, err := file.Write(buffer); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := ensurePathMatchesIdentity(path, identity); err != nil {
		return err
	}
	return directorySync(filepath.Dir(path))
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
