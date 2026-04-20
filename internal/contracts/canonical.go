package contracts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
)

// CanonicalMarshal returns a deterministic JSON encoding for v.
//
// The canonical form sorts object keys lexicographically at every nesting
// level, preserves arrays in-order, disables HTML escaping, and normalizes
// numbers to the minimal JSON representation produced by Go's encoder.
func CanonicalMarshal(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	var rest any
	if err := dec.Decode(&rest); err != io.EOF {
		return nil, err
	}

	var buf bytes.Buffer
	if err := writeCanonicalJSON(&buf, tree); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCanonicalJSON(buf *bytes.Buffer, v any) error {
	switch vv := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if vv {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		return writeCanonicalScalar(buf, vv)
	case json.Number:
		return writeCanonicalNumber(buf, vv)
	case []any:
		buf.WriteByte('[')
		for i := range vv {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonicalJSON(buf, vv[i]); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(vv))
		for key := range vv {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonicalScalar(buf, key); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := writeCanonicalJSON(buf, vv[key]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("contracts: canonical marshal: unsupported type %T", v)
	}
	return nil
}

func writeCanonicalNumber(buf *bytes.Buffer, n json.Number) error {
	if i, err := n.Int64(); err == nil {
		buf.WriteString(strconv.FormatInt(i, 10))
		return nil
	}
	return fmt.Errorf("%w: %s", ErrCanonicalNonInteger, n.String())
}

func writeCanonicalScalar(buf *bytes.Buffer, v any) error {
	var scalar bytes.Buffer
	enc := json.NewEncoder(&scalar)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	data := bytes.TrimSuffix(scalar.Bytes(), []byte("\n"))
	buf.Write(data)
	return nil
}
