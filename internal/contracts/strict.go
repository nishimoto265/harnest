package contracts

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
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

// DecodeStrictJSON exposes the strict single-value JSON decode used by the
// contracts package so step I/O envelopes can apply the same duplicate-key /
// unknown-field / trailing-token checks at their own top-level boundaries.
func DecodeStrictJSON(data []byte, v any) error {
	return decodeStrict(data, v)
}

func decodeStrictWithRequiredFields(data []byte, v any, requiredFields map[string]error) error {
	raw, err := decodeStrictJSONObjectFields(data)
	if err != nil {
		return err
	}
	for field, fieldErr := range requiredFields {
		if _, ok := raw[field]; !ok {
			return fieldErr
		}
	}
	return decodeStrict(data, v)
}

// RejectDuplicateJSONKeys exposes the duplicate-key scanner used by strict
// readers. Step I/O boundaries use it before decoding alias structs in custom
// UnmarshalJSON implementations.
func RejectDuplicateJSONKeys(data []byte) error {
	return rejectDuplicateKeys(data)
}

func decodeStrictJSONObjectFields(data []byte) (map[string]json.RawMessage, error) {
	if err := rejectDuplicateKeys(data); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	var raw map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	var rest any
	if err := dec.Decode(&rest); err != io.EOF {
		return nil, ErrTrailingJSON
	}
	return raw, nil
}

func rejectDuplicateKeys(data []byte) error {
	if len(trimStrictJSONPayload(data)) == 0 {
		return ErrEmptyJSON
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return ErrTrailingJSON
		}
		return err
	}
	return nil
}

func trimStrictJSONPayload(data []byte) []byte {
	trimmed := bytes.TrimSpace(data)
	trimmed = bytes.TrimPrefix(trimmed, []byte{0xEF, 0xBB, 0xBF})
	return bytes.TrimSpace(trimmed)
}

func scanJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyTok.(string)
			if !ok {
				return fmt.Errorf("contracts: expected object key token, got %T", keyTok)
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("%w: %s", ErrDuplicateJSONKey, key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return fmt.Errorf("contracts: expected object close delimiter, got %v", end)
		}
		return nil
	case '[':
		for dec.More() {
			if err := scanJSONValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return fmt.Errorf("contracts: expected array close delimiter, got %v", end)
		}
		return nil
	default:
		return fmt.Errorf("contracts: unexpected JSON delimiter %q", delim)
	}
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

// EncodeStrict writes v as JSON to w after running the same Validate() /
// validator.Struct chain that decodeStrict enforces (Phase 0-bootstrap-1 gate
// 3rd-round finding #1). Producers writing top-level persisted JSON
// (Manifest / TaskPackage / IntentionRecord / Decision / RuleRegistryEntry /
// StateEntry / Candidates / ChecklistResult etc.) must go through this helper
// so that transition checks / stage invariants / matrix invariants etc. are
// enforced on the write path too — decode-time auto-chain alone is not
// enough because a producer can hand-craft a struct and `json.Marshal` it
// without ever touching a reader.
//
// EncodeStrict uses `json.Encoder` which appends a single trailing newline;
// callers that need trailing-newline-free output should use MarshalStrict
// and strip / reuse the bytes as needed.
func EncodeStrict[T any](w io.Writer, v T) error {
	if err := runValidation(v); err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	return enc.Encode(v)
}

// MarshalStrict returns the JSON encoding of v after running the same
// Validate() / validator.Struct chain as EncodeStrict / decodeStrict.
//
// Unlike json.Marshal, MarshalStrict rejects structs that fail their
// Validate() method even when the producer constructed the struct directly
// and skipped the decode path (Phase 0-bootstrap-1 gate 3rd-round finding #1
// / #2). The returned bytes have no trailing newline.
func MarshalStrict[T any](v T) ([]byte, error) {
	if err := runValidation(v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
