package io

import (
	"bufio"
	"errors"
	"fmt"
	stdio "io"
	"os"
	"path/filepath"

	"github.com/nishimoto265/auto-improve/internal/contracts"
)

type appendJSONLFile interface {
	Write([]byte) (int, error)
	Sync() error
	Truncate(size int64) error
	Seek(offset int64, whence int) (int64, error)
	Close() error
}

var appendJSONLOpenFile = func(path string) (appendJSONLFile, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, defaultFilePerm)
}

// AppendJSONL validates record, canonicalizes it, enforces the 4KB line limit,
// and appends exactly one newline-delimited JSONL row.
func AppendJSONL(path string, record any) error {
	if err := contracts.EnsureCleanAbsolutePath(path); err != nil {
		return err
	}
	payload, err := marshalJSONLRecord(record)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), defaultDirectoryPerm); err != nil {
		return err
	}
	f, err := appendJSONLOpenFile(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := appendJSONLPayload(f, payload); err != nil {
		return err
	}
	return directorySync(filepath.Dir(path))
}

// ReadJSONL strict-decodes each JSONL row via contracts.DecodeStrictJSON.
func ReadJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var records []T
	reader := bufio.NewReader(f)
	lineNo := 0
	var offset int64
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
		if len(line) > JSONLMaxLineBytes {
			return nil, fmt.Errorf("jsonl line %d at offset %d: %w", lineNo, offset, ErrEntryTooLarge)
		}
		var record T
		if decodeErr := contracts.DecodeStrictJSON(line, &record); decodeErr != nil {
			return nil, fmt.Errorf("jsonl line %d at offset %d: %w", lineNo, offset, decodeErr)
		}
		records = append(records, record)
		offset += int64(len(line) + 1)
		if err != nil {
			if isEOF(err) {
				break
			}
			return nil, err
		}
	}
	return records, nil
}

// CollapseByKey reduces append-only rows by keeping only the last record for
// each key while preserving the last-occurrence order.
func CollapseByKey[T any, K comparable](records []T, keyFn func(T) K) []T {
	if len(records) == 0 {
		return nil
	}
	seen := make(map[K]struct{}, len(records))
	collapsed := make([]T, 0, len(records))
	for i := len(records) - 1; i >= 0; i-- {
		key := keyFn(records[i])
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		collapsed = append(collapsed, records[i])
	}
	for i, j := 0, len(collapsed)-1; i < j; i, j = i+1, j-1 {
		collapsed[i], collapsed[j] = collapsed[j], collapsed[i]
	}
	return collapsed
}

func marshalJSONLRecord(record any) ([]byte, error) {
	if _, err := contracts.MarshalStrict(record); err != nil {
		return nil, err
	}
	payload, err := contracts.CanonicalMarshal(record)
	if err != nil {
		return nil, err
	}
	if len(payload)+1 > JSONLMaxLineBytes {
		return nil, ErrEntryTooLarge
	}
	return payload, nil
}

func appendJSONLPayload(f appendJSONLFile, payload []byte) error {
	originalSize, err := f.Seek(0, stdio.SeekEnd)
	if err != nil {
		return err
	}

	if err := writeAll(f, payload); err != nil {
		_ = f.Truncate(originalSize)
		return err
	}
	if err := writeAll(f, []byte{'\n'}); err != nil {
		_ = f.Truncate(originalSize)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Truncate(originalSize)
		return err
	}
	return nil
}

func writeAll(w stdio.Writer, payload []byte) error {
	for len(payload) > 0 {
		n, err := w.Write(payload)
		if err != nil {
			return err
		}
		if n <= 0 {
			return stdio.ErrShortWrite
		}
		payload = payload[n:]
	}
	return nil
}

func trimJSONLLine(line []byte) []byte {
	if n := len(line); n > 0 && line[n-1] == '\n' {
		line = line[:n-1]
	}
	return line
}

func isEOF(err error) bool {
	return errors.Is(err, stdio.EOF)
}
