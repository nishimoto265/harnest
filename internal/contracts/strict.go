package contracts

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/nishimoto265/auto-improve/internal/validation"
)

// decodeStrict decodes a single JSON value into v using DisallowUnknownFields
// and then requires io.EOF on the next Decode call (= no trailing tokens).
//
// Used by the tagged-union UnmarshalJSON implementations (Manifest / Decision /
// RuleRegistryEntry / StateEntry). The public `ReadJSON` in `internal/io` uses
// the same pattern for top-level reads.
//
// After a successful decode decodeStrict automatically chains the following
// validations (Phase 0-bootstrap-1 gate 2nd-round finding #1-3):
//
//  1. If v (or the value it points to) implements `Validate() error`, that
//     method is invoked. Any type that defines Validate() is expected to run
//     tag-based struct validation itself (typically via the validateStruct
//     helper); decodeStrict therefore does NOT additionally call
//     validateStruct for such types.
//  2. Otherwise, decodeStrict falls back to running the singleton validator on
//     v (`validation.Instance().Struct`) so that decode paths without a
//     Validate() method still enforce struct tags.
//
// This guarantees every decode path is covered — producers cannot bypass
// Validate() by calling validator.Struct directly.
func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	var rest any
	if err := dec.Decode(&rest); err != io.EOF {
		return ErrTrailingJSON
	}
	return runValidation(v)
}

// runValidation invokes Validate() if v (or the value it points to) implements
// it, otherwise falls back to validateStruct.
func runValidation(v any) error {
	if vv, ok := v.(interface{ Validate() error }); ok {
		return vv.Validate()
	}
	return validateStruct(v)
}

// validateStruct runs the singleton validator on v. Separated so call sites
// (Validate() implementations, direct decode paths) can chain it cleanly.
func validateStruct(v any) error {
	return validation.Instance().Struct(v)
}
