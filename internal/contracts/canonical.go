package contracts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// CanonicalMarshal returns a deterministic JSON encoding for v.
//
// The canonical form sorts object keys lexicographically at every nesting
// level, preserves arrays in-order, disables HTML escaping, and normalizes
// numbers to the minimal JSON representation produced by Go's encoder.
func CanonicalMarshal(v any) ([]byte, error) {
	if err := rejectForbiddenCanonicalKinds(reflect.ValueOf(v), "$", make(map[uintptr]struct{})); err != nil {
		return nil, err
	}

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

func rejectForbiddenCanonicalKinds(v reflect.Value, path string, seen map[uintptr]struct{}) error {
	if !v.IsValid() {
		return nil
	}

	if shouldTrackCanonicalVisit(v) {
		ptr := v.Pointer()
		if ptr != 0 {
			if _, ok := seen[ptr]; ok {
				return &json.UnsupportedValueError{
					Value: v,
					Str:   fmt.Sprintf("encountered a cycle via %s", path),
				}
			}
			seen[ptr] = struct{}{}
			defer delete(seen, ptr)
		}
	}

	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return rejectForbiddenCanonicalKinds(v.Elem(), path, seen)
	case reflect.Pointer:
		if v.IsNil() {
			return nil
		}
		return rejectForbiddenCanonicalKinds(v.Elem(), path, seen)
	case reflect.Struct:
		t := v.Type()
		for _, field := range cachedCanonicalActiveFields(t) {
			fieldValue := canonicalValueByIndex(v, field.index)
			if !fieldValue.IsValid() {
				continue
			}
			if err := rejectForbiddenCanonicalKinds(fieldValue, path+"."+canonicalFieldPath(t, field.index), seen); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if err := rejectForbiddenCanonicalKinds(v.Index(i), fmt.Sprintf("%s[%d]", path, i), seen); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			if err := rejectForbiddenCanonicalKinds(iter.Value(), fmt.Sprintf("%s[%v]", path, iter.Key()), seen); err != nil {
				return err
			}
		}
		return nil
	case reflect.Float32, reflect.Float64, reflect.Uint, reflect.Uint32, reflect.Uint64, reflect.Uintptr, reflect.Complex64, reflect.Complex128:
		return fmt.Errorf("%w: kind=%s path=%s", ErrCanonicalForbiddenKind, v.Kind(), path)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint8, reflect.Uint16:
		return nil
	default:
		return nil
	}
}

func shouldTrackCanonicalVisit(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		return !v.IsNil()
	default:
		return false
	}
}

type canonicalActiveField struct {
	name  string
	index []int
	typ   reflect.Type
	tag   bool
}

var canonicalFieldCache sync.Map

func cachedCanonicalActiveFields(t reflect.Type) []canonicalActiveField {
	if fields, ok := canonicalFieldCache.Load(t); ok {
		return fields.([]canonicalActiveField)
	}
	fields, _ := canonicalFieldCache.LoadOrStore(t, canonicalActiveFields(t))
	return fields.([]canonicalActiveField)
}

func canonicalActiveFields(t reflect.Type) []canonicalActiveField {
	current := []canonicalActiveField{}
	next := []canonicalActiveField{{typ: t}}

	var count, nextCount map[reflect.Type]int
	visited := map[reflect.Type]bool{}
	var fields []canonicalActiveField

	for len(next) > 0 {
		current, next = next, current[:0]
		count, nextCount = nextCount, map[reflect.Type]int{}

		for _, field := range current {
			if visited[field.typ] {
				continue
			}
			visited[field.typ] = true

			for i := 0; i < field.typ.NumField(); i++ {
				sf := field.typ.Field(i)
				if sf.Anonymous {
					embeddedType := sf.Type
					if embeddedType.Kind() == reflect.Pointer {
						embeddedType = embeddedType.Elem()
					}
					if !sf.IsExported() && embeddedType.Kind() != reflect.Struct {
						continue
					}
				} else if !sf.IsExported() {
					continue
				}

				tag := sf.Tag.Get("json")
				if tag == "-" {
					continue
				}
				name, _ := parseCanonicalJSONTag(tag)
				if !isValidCanonicalJSONTag(name) {
					name = ""
				}

				index := make([]int, len(field.index)+1)
				copy(index, field.index)
				index[len(field.index)] = i

				fieldType := sf.Type
				if fieldType.Name() == "" && fieldType.Kind() == reflect.Pointer {
					fieldType = fieldType.Elem()
				}

				if name != "" || !sf.Anonymous || fieldType.Kind() != reflect.Struct {
					tagged := name != ""
					if name == "" {
						name = sf.Name
					}
					activeField := canonicalActiveField{
						name:  name,
						index: index,
						typ:   fieldType,
						tag:   tagged,
					}
					fields = append(fields, activeField)
					if count[field.typ] > 1 {
						fields = append(fields, activeField)
					}
					continue
				}

				nextCount[fieldType]++
				if nextCount[fieldType] == 1 {
					next = append(next, canonicalActiveField{
						name:  fieldType.Name(),
						index: index,
						typ:   fieldType,
					})
				}
			}
		}
	}

	sort.Slice(fields, func(i, j int) bool {
		left, right := fields[i], fields[j]
		if left.name != right.name {
			return left.name < right.name
		}
		if len(left.index) != len(right.index) {
			return len(left.index) < len(right.index)
		}
		if left.tag != right.tag {
			return left.tag
		}
		return compareCanonicalIndex(left.index, right.index) < 0
	})

	out := fields[:0]
	for advance, i := 0, 0; i < len(fields); i += advance {
		fi := fields[i]
		name := fi.name
		for advance = 1; i+advance < len(fields); advance++ {
			if fields[i+advance].name != name {
				break
			}
		}
		if advance == 1 {
			out = append(out, fi)
			continue
		}
		dominant, ok := dominantCanonicalField(fields[i : i+advance])
		if ok {
			out = append(out, dominant)
		}
	}

	fields = out
	sort.Slice(fields, func(i, j int) bool {
		return compareCanonicalIndex(fields[i].index, fields[j].index) < 0
	})

	return fields
}

func dominantCanonicalField(fields []canonicalActiveField) (canonicalActiveField, bool) {
	if len(fields) > 1 && len(fields[0].index) == len(fields[1].index) && fields[0].tag == fields[1].tag {
		return canonicalActiveField{}, false
	}
	return fields[0], true
}

func compareCanonicalIndex(left, right []int) int {
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] < right[i] {
			return -1
		}
		if left[i] > right[i] {
			return 1
		}
	}
	switch {
	case len(left) < len(right):
		return -1
	case len(left) > len(right):
		return 1
	default:
		return 0
	}
}

func canonicalValueByIndex(v reflect.Value, index []int) reflect.Value {
	for _, i := range index {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return reflect.Value{}
			}
			v = v.Elem()
		}
		v = v.Field(i)
	}
	return v
}

func canonicalFieldPath(t reflect.Type, index []int) string {
	parts := make([]string, 0, len(index))
	for _, i := range index {
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		field := t.Field(i)
		parts = append(parts, field.Name)
		t = field.Type
	}
	return strings.Join(parts, ".")
}

func parseCanonicalJSONTag(tag string) (string, string) {
	name, options, _ := strings.Cut(tag, ",")
	return name, options
}

func isValidCanonicalJSONTag(tag string) bool {
	if tag == "" {
		return false
	}
	for _, r := range tag {
		switch {
		case strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", r):
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			return false
		}
	}
	return true
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
