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
	return nil
}

// validateStruct runs the singleton validator on v. Separated so variant
// UnmarshalJSON can chain it cleanly.
func validateStruct(v any) error {
	return validation.Instance().Struct(v)
}
