package io

import (
	"os"

	"github.com/nishimoto265/auto-improve/internal/contracts"
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
	data, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	if err := contracts.DecodeStrictJSON(data, &out); err != nil {
		return out, err
	}
	return out, nil
}
