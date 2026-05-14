package io

import (
	"io"
	"os"

	"github.com/nishimoto265/harnest/internal/contracts"
)

// WriteJSONAtomic validates v through contracts.MarshalStrict and atomically
// replaces path with the resulting strict JSON bytes.
func WriteJSONAtomic(path string, v any) error {
	data, err := contracts.MarshalStrict(v)
	if err != nil {
		return err
	}
	return WriteAtomic(path, data)
}

// ReadJSON reads one strict JSON document from path.
func ReadJSON[T any](path string) (T, error) {
	var out T
	if err := contracts.EnsureCleanAbsolutePath(path); err != nil {
		return out, err
	}
	file, err := openFileNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return out, err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return out, err
	}
	if err := contracts.DecodeStrictJSON(data, &out); err != nil {
		return out, err
	}
	return out, nil
}
