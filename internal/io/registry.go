package io

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
)

type RegistryLine struct {
	Offset int64
	Sha256 string
	Entry  contracts.RuleRegistryEntry
}

func RegistryLines(path string) ([]RegistryLine, error) {
	return readRegistryLines(path)
}

func AppendRegistryEntry(path string, entry contracts.RuleRegistryEntry) (contracts.RegistryAppendResult, error) {
	if err := contracts.EnsureCleanAbsolutePath(path); err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	lock, err := AcquireFileLock(registryLockPath(path))
	if err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	defer func() {
		_ = lock.Unlock()
	}()
	lines, err := readRegistryLines(path)
	if err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	expectedPrevHash, err := registryPrevHash(entry)
	if err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	actualPrevHash := ""
	if len(lines) > 0 {
		actualPrevHash = lines[len(lines)-1].Sha256
	}
	if expectedPrevHash != actualPrevHash {
		return contracts.RegistryAppendResult{}, fmt.Errorf("%w: expected_prev_hash=%q actual_prev_hash=%q", ErrRegistryCASMismatch, expectedPrevHash, actualPrevHash)
	}

	payload, err := marshalJSONLRecord(entry)
	if err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), defaultDirectoryPerm); err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, defaultFilePerm)
	if err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	defer f.Close()

	offset, err := f.Seek(0, os.SEEK_END)
	if err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	if _, err := f.Write(payload); err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	if _, err := f.Write([]byte{'\n'}); err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	if err := f.Sync(); err != nil {
		return contracts.RegistryAppendResult{}, err
	}

	sum := sha256.Sum256(payload)
	return contracts.RegistryAppendResult{
		Offset: offset,
		Sha256: hex.EncodeToString(sum[:]),
	}, nil
}

func registryLockPath(path string) string {
	return path + ".lock"
}

func BuildRuleIdempotencyIndexEntry(entry contracts.RuleRegistryEntry, result contracts.RegistryAppendResult) (contracts.RuleIdempotencyIndexEntry, error) {
	key, at, err := registryIdempotencyFields(entry)
	if err != nil {
		return contracts.RuleIdempotencyIndexEntry{}, err
	}
	index := contracts.RuleIdempotencyIndexEntry{
		IdempotencyKey: key,
		RegistryOffset: result.Offset,
		RegistrySha256: result.Sha256,
		Kind:           entry.Kind,
		At:             at,
	}
	if err := index.Validate(); err != nil {
		return contracts.RuleIdempotencyIndexEntry{}, err
	}
	return index, nil
}

func AppendIdempotencyIndexEntry(path string, entry contracts.RuleIdempotencyIndexEntry) error {
	return AppendJSONL(path, entry)
}

func RebuildIdempotencyIndex(registryPath, indexPath string) ([]contracts.RuleIdempotencyIndexEntry, error) {
	lines, err := readRegistryLines(registryPath)
	if err != nil {
		return nil, err
	}
	entries := make([]contracts.RuleIdempotencyIndexEntry, 0, len(lines))
	for _, line := range lines {
		index, err := BuildRuleIdempotencyIndexEntry(line.Entry, contracts.RegistryAppendResult{
			Offset: line.Offset,
			Sha256: line.Sha256,
		})
		if err != nil {
			return nil, err
		}
		entries = append(entries, index)
	}

	buffer := make([]byte, 0, len(entries)*128)
	for _, entry := range entries {
		payload, err := marshalJSONLRecord(entry)
		if err != nil {
			return nil, err
		}
		buffer = append(buffer, payload...)
		buffer = append(buffer, '\n')
	}
	if err := WriteAtomic(indexPath, buffer); err != nil {
		return nil, err
	}
	return entries, nil
}

func readRegistryLines(path string) ([]RegistryLine, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	var (
		lines  []RegistryLine
		offset int64
		lineNo int
	)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			if isEOF(err) {
				break
			}
			return nil, err
		}
		lineNo++
		line = trimJSONLLine(line)
		if len(line) == 0 {
			return nil, fmt.Errorf("registry line %d at offset %d: %w", lineNo, offset, contracts.ErrEmptyJSON)
		}
		if len(line)+1 > JSONLMaxLineBytes {
			return nil, fmt.Errorf("registry line %d at offset %d: %w", lineNo, offset, ErrEntryTooLarge)
		}
		var entry contracts.RuleRegistryEntry
		if decodeErr := contracts.DecodeStrictJSON(line, &entry); decodeErr != nil {
			return nil, fmt.Errorf("registry line %d at offset %d: %w", lineNo, offset, decodeErr)
		}
		sum := sha256.Sum256(line)
		lines = append(lines, RegistryLine{
			Offset: offset,
			Sha256: hex.EncodeToString(sum[:]),
			Entry:  entry,
		})
		offset += int64(len(line) + 1)
		if err != nil {
			if isEOF(err) {
				break
			}
			return nil, err
		}
	}
	return lines, nil
}

func registryPrevHash(entry contracts.RuleRegistryEntry) (string, error) {
	switch v := entry.Value.(type) {
	case contracts.RuleRegistryAdded:
		return v.PrevHash, nil
	case *contracts.RuleRegistryAdded:
		return v.PrevHash, nil
	case contracts.RuleRegistryUpdated:
		return v.PrevHash, nil
	case *contracts.RuleRegistryUpdated:
		return v.PrevHash, nil
	case contracts.RuleRegistryRolledBack:
		return v.PrevHash, nil
	case *contracts.RuleRegistryRolledBack:
		return v.PrevHash, nil
	case contracts.RuleRegistryStatusChanged:
		return v.PrevHash, nil
	case *contracts.RuleRegistryStatusChanged:
		return v.PrevHash, nil
	case contracts.RuleRegistryArchived:
		return v.PrevHash, nil
	case *contracts.RuleRegistryArchived:
		return v.PrevHash, nil
	case contracts.RuleRegistryRestored:
		return v.PrevHash, nil
	case *contracts.RuleRegistryRestored:
		return v.PrevHash, nil
	default:
		return "", ErrRegistryUnsupportedKind
	}
}

func registryIdempotencyFields(entry contracts.RuleRegistryEntry) (string, time.Time, error) {
	switch v := entry.Value.(type) {
	case contracts.RuleRegistryAdded:
		return v.IdempotencyKey, v.At, nil
	case *contracts.RuleRegistryAdded:
		return v.IdempotencyKey, v.At, nil
	case contracts.RuleRegistryUpdated:
		return v.IdempotencyKey, v.At, nil
	case *contracts.RuleRegistryUpdated:
		return v.IdempotencyKey, v.At, nil
	case contracts.RuleRegistryRolledBack:
		return v.TargetOpID, v.At, nil
	case *contracts.RuleRegistryRolledBack:
		return v.TargetOpID, v.At, nil
	case contracts.RuleRegistryStatusChanged:
		return v.OpID, v.At, nil
	case *contracts.RuleRegistryStatusChanged:
		return v.OpID, v.At, nil
	case contracts.RuleRegistryArchived:
		return v.OpID, v.At, nil
	case *contracts.RuleRegistryArchived:
		return v.OpID, v.At, nil
	case contracts.RuleRegistryRestored:
		return v.OpID, v.At, nil
	case *contracts.RuleRegistryRestored:
		return v.OpID, v.At, nil
	default:
		return "", time.Time{}, ErrRegistryUnsupportedKind
	}
}
