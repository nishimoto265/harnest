package state

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"unicode/utf8"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
)

func Append(ctx internalio.RunContext, entry contracts.StateEntry) error {
	return NewWriter(ctx).Append(entry)
}

func AppendStateEntry(ctx internalio.RunContext, entry contracts.StateEntry) error {
	return Append(ctx, entry)
}

func (w Writer) Append(entry contracts.StateEntry) error {
	runDir := w.runDir
	if _, ok := stateEntryRunID(entry); !ok {
		runDir = ""
	}
	normalized, err := normalizeDetailOverflow(runDir, entry)
	if err != nil {
		return err
	}
	lock, err := internalio.AcquireFileLock(stateLockPath(w.path))
	if err != nil {
		return err
	}
	defer func() {
		_ = lock.Unlock()
	}()
	return internalio.AppendJSONL(w.path, normalized)
}

func (w Writer) AppendStateEntry(entry contracts.StateEntry) error {
	return w.Append(entry)
}

func (w Writer) AppendAll(entries []contracts.StateEntry) error {
	if len(entries) == 0 {
		return nil
	}
	records := make([]any, 0, len(entries))
	for _, entry := range entries {
		runDir := w.runDir
		if _, ok := stateEntryRunID(entry); !ok {
			runDir = ""
		}
		normalized, err := normalizeDetailOverflow(runDir, entry)
		if err != nil {
			return err
		}
		records = append(records, normalized)
	}
	lock, err := internalio.AcquireFileLock(stateLockPath(w.path))
	if err != nil {
		return err
	}
	defer func() {
		_ = lock.Unlock()
	}()
	return internalio.AppendJSONLBatch(w.path, records)
}

func normalizeDetailOverflow(runDir string, entry contracts.StateEntry) (contracts.StateEntry, error) {
	switch value := entry.Value.(type) {
	case contracts.StateEntryInterrupted:
		normalized, err := normalizeDetailVariant(runDir, value.Detail, value.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		value.Detail = normalized.detail
		value.DetailOverflowRef = normalized.ref
		entry.Value = value
	case *contracts.StateEntryInterrupted:
		if value == nil {
			return contracts.StateEntry{}, contracts.ErrNilValidationValue
		}
		cloned := *value
		normalized, err := normalizeDetailVariant(runDir, cloned.Detail, cloned.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		cloned.Detail = normalized.detail
		cloned.DetailOverflowRef = normalized.ref
		entry.Value = cloned
	case contracts.StateEntryWarning:
		normalized, err := normalizeDetailVariant(runDir, value.Detail, value.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		value.Detail = normalized.detail
		value.DetailOverflowRef = normalized.ref
		entry.Value = value
	case *contracts.StateEntryWarning:
		if value == nil {
			return contracts.StateEntry{}, contracts.ErrNilValidationValue
		}
		cloned := *value
		normalized, err := normalizeDetailVariant(runDir, cloned.Detail, cloned.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		cloned.Detail = normalized.detail
		cloned.DetailOverflowRef = normalized.ref
		entry.Value = cloned
	case contracts.StateEntryCompleted:
		normalized, err := normalizeDetailVariant(runDir, value.Detail, value.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		value.Detail = normalized.detail
		value.DetailOverflowRef = normalized.ref
		entry.Value = value
	case *contracts.StateEntryCompleted:
		if value == nil {
			return contracts.StateEntry{}, contracts.ErrNilValidationValue
		}
		cloned := *value
		normalized, err := normalizeDetailVariant(runDir, cloned.Detail, cloned.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		cloned.Detail = normalized.detail
		cloned.DetailOverflowRef = normalized.ref
		entry.Value = cloned
	case contracts.StateEntryFailed:
		normalized, err := normalizeDetailVariant(runDir, value.Detail, value.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		value.Detail = normalized.detail
		value.DetailOverflowRef = normalized.ref
		entry.Value = value
	case *contracts.StateEntryFailed:
		if value == nil {
			return contracts.StateEntry{}, contracts.ErrNilValidationValue
		}
		cloned := *value
		normalized, err := normalizeDetailVariant(runDir, cloned.Detail, cloned.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		cloned.Detail = normalized.detail
		cloned.DetailOverflowRef = normalized.ref
		entry.Value = cloned
	case contracts.StateEntrySkipped:
		normalized, err := normalizeDetailVariant(runDir, value.Detail, value.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		value.Detail = normalized.detail
		value.DetailOverflowRef = normalized.ref
		entry.Value = value
	case *contracts.StateEntrySkipped:
		if value == nil {
			return contracts.StateEntry{}, contracts.ErrNilValidationValue
		}
		cloned := *value
		normalized, err := normalizeDetailVariant(runDir, cloned.Detail, cloned.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		cloned.Detail = normalized.detail
		cloned.DetailOverflowRef = normalized.ref
		entry.Value = cloned
	case contracts.StateEntryNeedsManualRecovery:
		normalized, err := normalizeDetailVariant(runDir, value.Detail, value.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		value.Detail = normalized.detail
		value.DetailOverflowRef = normalized.ref
		entry.Value = value
	case *contracts.StateEntryNeedsManualRecovery:
		if value == nil {
			return contracts.StateEntry{}, contracts.ErrNilValidationValue
		}
		cloned := *value
		normalized, err := normalizeDetailVariant(runDir, cloned.Detail, cloned.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		cloned.Detail = normalized.detail
		cloned.DetailOverflowRef = normalized.ref
		entry.Value = cloned
	}
	return entry, nil
}

type normalizedDetail struct {
	detail string
	ref    *contracts.OverflowRef
}

func normalizeDetailVariant(runDir, detail string, ref *contracts.OverflowRef) (normalizedDetail, error) {
	if utf8.RuneCountInString(detail) <= 300 {
		return normalizedDetail{detail: detail, ref: ref}, nil
	}
	if runDir == "" {
		return normalizedDetail{}, errors.New("state: detail overflow requires run directory")
	}
	sum := sha256.Sum256([]byte(detail))
	sha256Hex := hex.EncodeToString(sum[:])
	sidecarPath, err := internalio.WriteSidecar(filepath.Join(runDir, processedDetailsDir), sha256Hex, detail)
	if err != nil {
		return normalizedDetail{}, err
	}
	relPath, err := internalio.SidecarRefPath(runDir, sidecarPath)
	if err != nil {
		return normalizedDetail{}, err
	}
	return normalizedDetail{
		detail: truncateRunes(detail, 300),
		ref: &contracts.OverflowRef{
			Path:   relPath,
			Sha256: sha256Hex,
		},
	}, nil
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := make([]rune, 0, limit)
	for _, r := range value {
		if len(runes) == limit {
			break
		}
		runes = append(runes, r)
	}
	return string(runes)
}
